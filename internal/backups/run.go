package backups

// The conversation that produces one backup.
//
// ── IT NEVER RETURNS AN ERROR ───────────────────────────────────────────────
//
// "The router was unreachable" is a RESULT worth recording, not an exception to
// lose. Every path produces a RunResult, and the caller writes it to the trail
// whatever it says — a fleet where one router has failed nightly for a month
// should be able to show that from its own history.
//
// ── THE SWEEP RUNS ON EVERY PATH, INCLUDING THE HAPPY-SHORT ONE ─────────────
//
// An UNCHANGED run still ran `/export` and still left a file on the router's
// flash. In the original that is a `return` from inside a `try`, so the
// `finally` sweeps anyway; here it is a `defer`. Getting this wrong leaks one
// file per poll on exactly the routers that are behaving — the quiet ones nobody
// looks at.
//
// ── FREE SPACE IS NOT A GATE ────────────────────────────────────────────────
//
// There was a threshold once — 8 MB, extrapolated from an AX3 whose export and
// binary came to 5.2 MB — and it refused a hAP ac2 that needed 45.7 KiB. Any
// constant is wrong for someone, because backup size tracks how much is
// configured rather than what the hardware is. So the router decides; it is the
// only thing that knows what it has room for. Free space is still READ, and
// carried on a failure, so "no space left" arrives with the number that explains
// it rather than making somebody go and look.

import (
	"errors"
	"io"
	"time"
)

// Outcomes.
//
// OutcomeSkipped IS NEVER PRODUCED HERE. It belongs to the concurrency guard a
// caller puts around a run — the live `runFor` returns it when a backup for this
// router is already in flight, without touching the router or writing a row. It
// lives with the other three so the audit trail and the page read one vocabulary
// rather than two.
const (
	OutcomeChanged   = "changed"
	OutcomeUnchanged = "unchanged"
	OutcomeFailed    = "failed"
	OutcomeSkipped   = "skipped"
)

// RunResult is what one run produces, whatever happened.
type RunResult struct {
	Outcome     string   `json:"outcome"`
	Stem        string   `json:"stem"`
	Fingerprint string   `json:"fingerprint"`
	Changed     bool     `json:"changed"`
	RscBytes    int64    `json:"rscBytes"`
	BackupBytes int64    `json:"backupBytes"`
	Identity    Identity `json:"identity"`
	// FreeBytes is kept OFF Identity deliberately: that is what gets recorded as
	// the device's identity, and free space is a fact about this moment.
	// SecretsBytes is the sealed migration export's size, 0 when this router
	// keeps none.
	SecretsBytes int64  `json:"secretsBytes"`
	FreeBytes    int64  `json:"freeBytes"`
	MS           int64  `json:"ms"`
	Error        string `json:"error"`
	Dir          string `json:"dir"`
}

// RunConfig is everything one run needs. Connect and WritePair are injected so
// the whole sequence is testable without a router or a filesystem.
type RunConfig struct {
	Label    string
	Password string
	// PrevFingerprint decides whether a pair is written at all — one restore
	// point per distinct configuration, not one per timer tick. That is what
	// makes a daily schedule cheap: an unchanged router costs one export read
	// and no disk.
	PrevFingerprint string
	DataDir         string

	// Connect returns a CONNECTED writer and a stop function. The caller owns
	// its lifetime, which is what keeps this testable with a fake.
	Connect   func() (Writer, func(), error)
	WritePair func(dir, stem, rsc string, binary []byte) (rscBytes, backupBytes int64, err error)

	// ── THE MIGRATION EXPORT, OFF UNLESS A ROUTER ASKS FOR IT ──────────────
	//
	// `/export` is either sensitive or it is not, so this cannot share the
	// diffable export's run: enabling it costs a SECOND export per changed
	// backup - one more command on a router whose concurrent channels are the
	// bottleneck this app exists to spend carefully. That cost is why it is a
	// per-router setting rather than always on.
	//
	// WriteSecrets receives the PLAIN text and is expected to seal it. Nil, or
	// MigrationExport false, and no second export is run at all.
	MigrationExport bool
	WriteSecrets    func(dir, stem, rsc string) (int64, error)

	Now   func() time.Time
	Sleep func(time.Duration)
	Log   func(string)
}

// settleTimeout is how long to wait for `/export` or `/system/backup/save` to
// finish writing before giving up on it.
const settleTimeout = 60 * time.Second

// Run takes one backup.
// The return is NAMED so the deferred assignments below reach the caller. With
// an unnamed one the value is copied at `return` and `res.MS` — set in the defer
// — would always arrive as zero, which is the sort of thing a test that only
// checks Outcome never notices.
func Run(cfg RunConfig) (res RunResult) {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	say := cfg.Log
	if say == nil {
		say = func(string) {}
	}

	started := now()
	stem := StemFor(started.UnixMilli())
	res = RunResult{Outcome: OutcomeFailed, Stem: stem}

	var w Writer
	var stop func()
	defer func() {
		// The router does not keep our temp files, whatever happened above.
		if w != nil {
			Sweep(w, OwnFile, say)
		}
		if stop != nil {
			stop()
		}
		res.MS = now().Sub(started).Milliseconds()
	}()

	fail := func(err error) RunResult {
		res.Outcome = OutcomeFailed
		res.Error = err.Error()
		msg := "failed: " + res.Error
		if res.FreeBytes > 0 {
			msg += " (" + itoaKB(res.FreeBytes) + " KB free)"
		}
		say(msg)
		return res
	}

	var err error
	w, stop, err = cfg.Connect()
	if err != nil {
		return fail(err)
	}

	id, err := ReadIdentity(w)
	if err != nil {
		return fail(err)
	}
	res.Identity = Identity{Model: id.Model, Serial: id.Serial, OSVersion: id.OSVersion}
	res.FreeBytes = id.FreeBytes

	// Anything left by a run that died before its sweep.
	if swept := Sweep(w, OwnFile, say); swept > 0 {
		say("swept " + itoa(swept) + " file(s) left by an earlier run")
	}

	base := FilePrefix + stem

	// ── The export, for diffing ─────────────────────────────────────────────
	//
	// TERSE, ALWAYS. RouterOS's default export wraps a long command across
	// continuation lines, and where it wraps depends on the whole line's length:
	// change one property and the rest of the block re-wraps, so a one-rule edit
	// shows up as a dozen changed lines in the diff. Measured on a near-empty
	// 7.24.4 CHR, the default export had 34 continuations in 102 lines; `terse`
	// had none in 45, at 4% more bytes because each line repeats its menu path.
	//
	// This file exists to be diffed - `store.go` calls it the diffable half of
	// the pair - so one command per line is the format it wants. It is not a
	// setting: "wrapped, harder to read, noisier to diff" is not an option worth
	// offering, and two formats in one archive would mean every diff first
	// asking which era each side came from.
	//
	// `terse` does NOT reveal anything hidden. Sensitive values stay masked
	// exactly as before; that is `show-sensitive`, which this deliberately does
	// not pass. A terse export still imports - verified on the CHR by exporting
	// one menu, deleting its rows and importing the file back.
	//
	// THE PRICE IS ONE RE-BASELINE. Fingerprint is taken over this text, so the
	// first run after this change differs from the stored one for every router:
	// each writes a new pair and its next diff shows the whole configuration as
	// changed, once. That is the cost of the format moving, and it is paid once
	// rather than on every toggle.
	rscText, err := ExportText(w, "/export", base, now, sleep, "=terse=")
	if err != nil {
		return fail(err)
	}
	res.Fingerprint = Fingerprint(rscText)

	// Same configuration as last time: the run is worth recording, a second
	// identical restore point is not. The deferred sweep still runs.
	if cfg.PrevFingerprint != "" && cfg.PrevFingerprint == res.Fingerprint {
		res.Outcome = OutcomeUnchanged
		say("configuration unchanged")
		return res
	}

	// ── The binary, for restoring ───────────────────────────────────────────
	if cfg.Password == "" {
		return fail(errors.New("no backup password configured for this router"))
	}
	if _, err := w("/system/backup/save", "=name="+base,
		"=password="+cfg.Password, "=encryption=aes-sha256"); err != nil {
		return fail(err)
	}
	bakSize, err := Settled(w, base+".backup", settleTimeout, now, sleep)
	if err != nil {
		return fail(err)
	}
	bakBuf, err := ReadRouterFile(w, base+".backup", bakSize)
	if err != nil {
		return fail(err)
	}

	dir := DirFor(cfg.DataDir, SlugFor(cfg.Label))
	rscBytes, backupBytes, err := cfg.WritePair(dir, stem, Normalize(rscText), bakBuf)
	if err != nil {
		return fail(err)
	}
	res.RscBytes, res.BackupBytes, res.Dir = rscBytes, backupBytes, dir

	// ── The migration export, for rebuilding on a different device ──────────
	//
	// AFTER the pair, and after the unchanged check above, so an idle router
	// never pays for it. A SEPARATE BASE on the router: two exports to one
	// filename would have the second overwrite the first, and the sweep takes
	// both because it matches on FilePrefix rather than on the whole name.
	//
	// A FAILURE HERE DOES NOT FAIL THE RUN. The pair is already on disk and is
	// what restores and diffs; losing the migration export costs one download
	// that can be taken again next time, and failing the whole backup over it
	// would turn an optional extra into a way to lose restore points.
	if cfg.MigrationExport && cfg.WriteSecrets != nil {
		secText, serr := ExportText(w, "/export", base+"-s", now, sleep,
			"=terse=", "=show-sensitive=")
		if serr == nil {
			n, werr := cfg.WriteSecrets(dir, stem, Normalize(secText))
			if werr != nil {
				serr = werr
			} else {
				res.SecretsBytes = n
			}
		}
		if serr != nil {
			say("migration export skipped: " + serr.Error())
		}
	}
	res.Outcome, res.Changed = OutcomeChanged, true
	say("stored " + stem + " (" + itoaKB(rscBytes) + " KB export, " +
		itoaKB(backupBytes) + " KB binary)")
	return res
}

// ReadRouterFile reads a file of `size` bytes off the router whole, in
// /file/read chunks. Config Management reads its dry-run reports this way.
func ReadRouterFile(w Writer, name string, size int) ([]byte, error) {
	return ReadFile(chunkReaderOf(w), name, size)
}

// ReadRouterFileTo is ReadRouterFile streaming to `dst`. The capture export
// uses it so the browser sees the file arriving rather than waiting on a read
// that takes about a second per 800 KB.
func ReadRouterFileTo(w Writer, name string, size int, dst io.Writer) error {
	return ReadFileTo(chunkReaderOf(w), name, size, dst)
}

// chunkReaderOf adapts a Writer to the ChunkReader ReadFile wants.
func chunkReaderOf(w Writer) ChunkReader {
	return func(name string, off, size int) (string, bool, error) {
		rows, err := w("/file/read", "=file="+name, "=offset="+itoa(off),
			"=chunk-size="+itoa(size))
		if err != nil {
			return "", false, err
		}
		if len(rows) == 0 {
			return "", false, nil
		}
		data, ok := rows[0]["data"]
		return data, ok, nil
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// itoaKB rounds to the nearest KB, as `Math.round(n / 1024)` does.
func itoaKB(n int64) string { return itoa(int((n + 512) / 1024)) }
