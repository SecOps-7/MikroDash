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
	FreeBytes int64  `json:"freeBytes"`
	MS        int64  `json:"ms"`
	Error     string `json:"error"`
	Dir       string `json:"dir"`
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
			Sweep(w, say)
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
	if swept := Sweep(w, say); swept > 0 {
		say("swept " + itoa(swept) + " file(s) left by an earlier run")
	}

	base := FilePrefix + stem

	// ── The export, for diffing ─────────────────────────────────────────────
	if _, err := w("/export", "=file="+base); err != nil {
		return fail(err)
	}
	rscSize, err := Settled(w, base+".rsc", settleTimeout, now, sleep)
	if err != nil {
		return fail(err)
	}
	rscBuf, err := ReadFile(chunkReaderOf(w), base+".rsc", rscSize)
	if err != nil {
		return fail(err)
	}
	rscText := string(rscBuf)
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
	bakBuf, err := ReadFile(chunkReaderOf(w), base+".backup", bakSize)
	if err != nil {
		return fail(err)
	}

	dir := DirFor(cfg.DataDir, SlugFor(cfg.Label))
	rscBytes, backupBytes, err := cfg.WritePair(dir, stem, Normalize(rscText), bakBuf)
	if err != nil {
		return fail(err)
	}
	res.RscBytes, res.BackupBytes, res.Dir = rscBytes, backupBytes, dir
	res.Outcome, res.Changed = OutcomeChanged, true
	say("stored " + stem + " (" + itoaKB(rscBytes) + " KB export, " +
		itoaKB(backupBytes) + " KB binary)")
	return res
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
