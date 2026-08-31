package backups

// One backup, recorded — the composition both the scheduler and the manual
// button go through.
//
// `Run` (run.go) has the conversation with the router. `PruneFor` (prunefor.go)
// applies retention. This is what joins them to the database, and it is the live
// `runFor` from src/backups/index.js.
//
// ── EVERY RUN GETS A ROW, WHATEVER HAPPENED ─────────────────────────────────
//
// Including the failures. A router that has failed nightly for a month should be
// able to show that from its own history, and a backup that silently does not
// happen is the failure mode this whole feature exists to prevent.
//
// ── ONLY A CHANGED RUN RECORDS A STEM ───────────────────────────────────────
//
// `stem` and `dir` are the location of a stored pair. An unchanged run wrote no
// pair, so it has a FINGERPRINT and no stem — which is exactly what
// `StoredBackups` filters on and what `LatestFingerprint` reads. Recording a
// stem for a run that stored nothing would make the disk figure and the restore
// list both wrong.
//
// ── RETENTION RUNS ONLY AFTER A CHANGE ──────────────────────────────────────
//
// An unchanged run added nothing, so there is nothing new to age out. Sweeping
// anyway would be a directory walk and a query per poll on a fleet that is
// behaving.

import "fmt"

// RunRecorder is the database half.
type RunRecorder interface {
	LatestFingerprint(routerID string) (string, error)
	Record(r RunRow) (int64, error)
}

// RunRow is one run as the database stores it. Pointers where absent is an
// ordinary state — see the header.
type RunRow struct {
	RouterID    string
	TakenAt     int64
	Outcome     string
	Source      string
	Actor       *string
	Stem        *string
	Dir         *string
	Fingerprint *string
	RscBytes    int64
	BackupBytes int64
	Model       *string
	Serial      *string
	OSVersion   *string
	MS          int64
	Error       *string
}

// RunForConfig is one recorded run.
type RunForConfig struct {
	RouterID string
	Label    string
	Password string
	DataDir  string
	// Source is "schedule" or "manual"; Actor names the human for a manual run,
	// so the history can answer WHO took a restore point as well as when.
	Source string
	Actor  string

	Recorder  RunRecorder
	Pruner    PruneStore
	Retention Retention

	Connect   func() (Writer, func(), error)
	WritePair func(dir, stem, rsc string, binary []byte) (rscBytes, backupBytes int64, err error)
	Now       func() int64
	Log       func(string)
}

// RunFor takes one backup and records it.
//
// NEVER RETURNS AN ERROR FOR A FAILED BACKUP — `Run` does not either, and the
// outcome is the answer. An error here means the RECORDING failed, which is a
// different thing and worth surfacing: a run whose row was not written is one
// the next tick will take again.
func RunFor(cfg RunForConfig) (RunResult, int64, error) {
	log := cfg.Log
	if log == nil {
		log = func(string) {}
	}
	now := cfg.Now
	if now == nil {
		now = func() int64 { return 0 }
	}

	previous, err := cfg.Recorder.LatestFingerprint(cfg.RouterID)
	if err != nil {
		return RunResult{}, 0, fmt.Errorf("reading the last fingerprint: %w", err)
	}

	res := Run(RunConfig{
		Label: cfg.Label, Password: cfg.Password, PrevFingerprint: previous,
		DataDir: cfg.DataDir, Connect: cfg.Connect, WritePair: cfg.WritePair,
		Log: log,
	})

	row := RunRow{
		RouterID: cfg.RouterID, TakenAt: now(), Outcome: res.Outcome,
		Source: cfg.Source, MS: res.MS,
		RscBytes: res.RscBytes, BackupBytes: res.BackupBytes,
	}
	if cfg.Actor != "" {
		row.Actor = &cfg.Actor
	}
	// Only a CHANGED run stored a pair.
	if res.Changed {
		stem, dir := res.Stem, res.Dir
		row.Stem, row.Dir = &stem, &dir
	}
	if res.Fingerprint != "" {
		fp := res.Fingerprint
		row.Fingerprint = &fp
	}
	if res.Identity.Model != "" {
		m := res.Identity.Model
		row.Model = &m
	}
	if res.Identity.Serial != "" {
		s := res.Identity.Serial
		row.Serial = &s
	}
	if res.Identity.OSVersion != "" {
		v := res.Identity.OSVersion
		row.OSVersion = &v
	}
	if res.Error != "" {
		e := res.Error
		row.Error = &e
	}

	id, err := cfg.Recorder.Record(row)
	if err != nil {
		return res, 0, fmt.Errorf("recording the run: %w", err)
	}

	// Retention only after a change — see the header.
	if res.Changed && cfg.Pruner != nil {
		if _, err := PruneFor(cfg.Pruner, cfg.RouterID, cfg.Retention, row.TakenAt, log); err != nil {
			// A sweep that failed does not undo a backup that succeeded. The pair
			// is stored and recorded; the next run sweeps again.
			log(fmt.Sprintf("retention sweep failed: %v", err))
		}
	}
	return res, id, nil
}
