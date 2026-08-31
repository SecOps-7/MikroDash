package backups

// Where backups live on disk.
//
// ── A PAIR IS THE UNIT ──────────────────────────────────────────────────────
//
//	<data>/config-backups/<router-slug>/
//	    2026-08-19T203521.rsc.gz      gzipped export — diffable, no secrets
//	    2026-08-19T203521.backup      aes-sha256 binary — restorable
//
// The two share a stem and are always created, removed and counted together. A
// `.rsc` without its `.backup` is a diff you cannot act on; a `.backup` without
// its `.rsc` is a restore point you cannot inspect.
//
// ── THE DIRECTORY NAME IS A TRUST BOUNDARY ──────────────────────────────────
//
// It is derived from the router LABEL, which an operator types. `SlugFor` exists
// so that a label can never escape the base directory, and it is deliberately
// lossy rather than clever: lowercase, dashes, nothing else. Every traversal
// shape collapses — `../../etc/passwd` becomes `etc-passwd`, `..` becomes
// `router` — because the alternative is a sanitiser that has to be right about
// every encoding.
//
// The DATABASE does not trust the slug either: it records the directory that was
// used, and nothing resolves an old backup by re-deriving a slug from the current
// label. Renaming a router starts a new directory and leaves the old one
// findable.

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// unsafeInSlug is everything a directory name may not contain. Applied AFTER
// lower-casing, so an upper-case letter survives as its lower-case self.
var unsafeInSlug = regexp.MustCompile(`[^a-z0-9]+`)

var trimDashes = regexp.MustCompile(`^-+|-+$`)

// SlugFor is a directory name from a router label.
//
// A label of only punctuation slugs to nothing and falls back to a fixed name
// rather than producing "" and writing into the BASE directory itself.
//
// THE CUT TO 60 HAPPENS AFTER THE TRIM, NOT BEFORE, so a cut landing on a dash
// leaves a trailing dash and the original does not re-trim. Reproduced rather
// than tidied: a port that trimmed again would put the same router in a
// different directory from the one the database recorded.
func SlugFor(label string) string {
	s := strings.ToLower(label)
	s = unsafeInSlug.ReplaceAllString(s, "-")
	s = trimDashes.ReplaceAllString(s, "")
	// Bytes, not runes — but the string holds only [a-z0-9-] by now, so the two
	// are the same thing here and cannot cut a character in half.
	if len(s) > 60 {
		s = s[:60]
	}
	if s == "" {
		return "router"
	}
	return s
}

// BaseDir is resolved per call rather than frozen at package load.
//
// A constant meant the live test suite wrote real backup pairs into the real
// /data/config-backups and left a `test-router` directory on a production
// instance, which is how that was found. Taking the data directory as an
// argument lets a caller point it somewhere disposable.
func BaseDir(dataDir string) string {
	if dataDir == "" {
		dataDir = "/data"
	}
	return filepath.Join(dataDir, "config-backups")
}

// DirFor is a router's backup directory.
func DirFor(dataDir, slug string) string { return filepath.Join(BaseDir(dataDir), slug) }

// RscPath and BackupPath are the two halves of a pair.
func RscPath(dir, stem string) string    { return filepath.Join(dir, stem+".rsc.gz") }
func BackupPath(dir, stem string) string { return filepath.Join(dir, stem+".backup") }

// StemFor is the filename stem for a moment in time, IN UTC.
//
// UTC because a local-time stem repeats itself for an hour every autumn, and two
// backups that sort as equal are two backups that can overwrite each other.
// Seconds are included for the same reason at a smaller scale: a scheduled run
// and a manual one can land in the same minute.
//
// It must also produce a stem `stemToMs` can read back — retention parses these,
// and a stem this app writes but cannot re-read would never be aged out.
// `TestStemsRoundTrip` is what holds those two together.
func StemFor(ts int64) string {
	t := time.UnixMilli(ts).UTC()
	return t.Format("2006-01-02T150405")
}

// ── The pair on disk ────────────────────────────────────────────────────────

// WritePair writes both halves.
//
// THE BINARY LANDS FIRST, AND THE ORDER IS CRASH SAFETY RATHER THAN STYLE. If
// the process dies between the two writes, what survives is a `.backup` with no
// `.rsc` — and ListPairs ignores it, because a half-written pair is not a
// backup. The other order would leave a diffable export on disk claiming a
// restore point exists, and the failure would only surface when somebody tried
// to restore from it.
func WritePair(dir, stem, rscText string, binary []byte) (rscBytes, backupBytes int64, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, 0, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(rscText)); err != nil {
		return 0, 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, 0, err
	}
	if err := os.WriteFile(BackupPath(dir, stem), binary, 0o600); err != nil {
		return 0, 0, err
	}
	if err := os.WriteFile(RscPath(dir, stem), buf.Bytes(), 0o600); err != nil {
		return 0, 0, err
	}
	return int64(buf.Len()), int64(len(binary)), nil
}

// ReadRsc is the stored export, gunzipped.
func ReadRsc(dir, stem string) (string, error) {
	f, err := os.Open(RscPath(dir, stem))
	if err != nil {
		return "", err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	b, err := io.ReadAll(zr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ReadBackup is the stored binary, as it will be pushed back to the router.
//
// []byte, never a string routed through JSON — see rawbytes_test.go.
func ReadBackup(dir, stem string) ([]byte, error) {
	return os.ReadFile(BackupPath(dir, stem))
}

// HasPair reports whether BOTH halves are present. Neither is useful alone: a
// `.rsc` without its `.backup` is a diff you cannot act on, and a `.backup`
// without its `.rsc` is a restore point you cannot inspect.
func HasPair(dir, stem string) bool {
	if _, err := os.Stat(RscPath(dir, stem)); err != nil {
		return false
	}
	_, err := os.Stat(BackupPath(dir, stem))
	return err == nil
}

// RemovePair removes a pair and reports how many files went.
//
// MISSING HALVES ARE NOT AN ERROR. Pruning has to be idempotent, because it runs
// after a crash as readily as after a success — and a crash is exactly what
// leaves one half behind.
func RemovePair(dir, stem string) (int, error) {
	removed := 0
	for _, p := range []string{RscPath(dir, stem), BackupPath(dir, stem)} {
		err := os.Remove(p)
		switch {
		case err == nil:
			removed++
		case os.IsNotExist(err):
			// Fine.
		default:
			return removed, err
		}
	}
	return removed, nil
}

// ListPairs is every COMPLETE pair in a directory, newest first.
//
// Sorted by stem, which sorts chronologically because the stem is a fixed-width
// UTC timestamp — no parsing, and no dependence on filesystem mtime, which a
// copy or a restore from a host backup would rewrite.
//
// A missing directory is an empty list rather than an error: a router that has
// never been backed up has no directory, and that is not a fault.
func ListPairs(dir string) ([]Pair, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Pair{}, nil
		}
		return nil, err
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}

	out := []Pair{}
	for name := range names {
		stem, ok := strings.CutSuffix(name, ".rsc.gz")
		if !ok || !names[stem+".backup"] {
			continue
		}
		p := Pair{Stem: stem}
		if fi, err := os.Stat(RscPath(dir, stem)); err == nil {
			p.RscBytes = fi.Size()
		}
		if fi, err := os.Stat(BackupPath(dir, stem)); err == nil {
			p.BackupBytes = fi.Size()
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stem > out[j].Stem })
	return out, nil
}

// UsageOf is the total bytes a router's backups occupy.
func UsageOf(dir string) (int64, error) {
	pairs, err := ListPairs(dir)
	if err != nil {
		return 0, err
	}
	var sum int64
	for _, p := range pairs {
		sum += p.RscBytes + p.BackupBytes
	}
	return sum, nil
}
