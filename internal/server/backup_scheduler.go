package server

import (
	"fmt"
	"log"
	"time"

	"mikrodash/internal/backups"
	"mikrodash/internal/routeros"
	"mikrodash/internal/session"
)

// The backup scheduler's construction — the last half of cutover step 0.
//
// ── OFF UNLESS SWITCHED ON ─────────────────────────────────────────────────
//
// Two schedulers against one fleet take two backups of every router on the same
// timetable, each holding a router channel while it runs. During coexistence
// Node owns that job, so `-backup-scheduler` defaults false and this returns nil.
//
// ── THE QUEUE IS `Manager.Acquire`, AND THAT IS THE WHOLE DESIGN DECISION ──
//
// `SchedDeps.Queue` exists because "a backup and a firewall edit on one router
// are not independent" — the live comment. The port already has that queue:
// `Session.InWriteQueue`. What it did not obviously have is a way to reach it
// for a router NOBODY IS WATCHING, which is exactly the case a scheduler serves.
//
// `Manager.Acquire` answers it, and answers it correctly rather than
// conveniently: it is REF-COUNTED, so a router someone has open returns THE SAME
// session and the backup serialises against that page's writes. A bespoke
// connection would have been cheaper and would have lost the property the queue
// exists for — two writers on one router, neither aware of the other.
//
// THE COST IS REAL AND BOUNDED: acquiring starts a full session for a router
// nobody is watching. That is the "wrong object" CLAUDE.md names for the
// background pool — but a backup is TRANSIENT (seconds, once per router per
// schedule) where the pool is continuous, so the same objection does not carry
// the same weight. Released in the same call, always.
func (s *Server) buildBackupScheduler(enabled bool) *backups.Scheduler {
	if !enabled {
		log.Printf("[backup] scheduler off; no scheduled backups will be taken " +
			"(pass -backup-scheduler to enable)")
		return nil
	}
	if s.store == nil || s.auditDB == nil {
		log.Printf("[backup] scheduler needs the store and the history database; not started")
		return nil
	}
	log.Printf("[backup] scheduler on — scheduled backups will be taken")

	return backups.NewScheduler(backups.SchedDeps{
		Routers:  s.schedRouters,
		LastRun:  s.lastBackupRun,
		Timezone: s.displayTimezone,
		Queue:    s.inRouterWriteQueue,
		RunFor:   s.runScheduledBackup,
		Log:      func(m string) { log.Printf("[backup] %s", m) },
		Now:      time.Now,
	})
}

// schedRouters is the fleet as the scheduler needs it.
//
// A DISABLED ROUTER IS STILL LISTED. The scheduler skips it itself, before
// asking `IsDue` — filtering here would move that decision into this file and
// leave the two able to disagree.
func (s *Server) schedRouters() []backups.SchedRouter {
	list, errs := s.store.Routers()
	for _, e := range errs {
		log.Printf("[backup] reading the fleet: %v", e)
	}
	out := make([]backups.SchedRouter, 0, len(list))
	for _, r := range list {
		out = append(out, backups.SchedRouter{
			ID: r.ID, Label: r.Label, Disabled: r.Disabled,
			Backup: &backups.Backup{
				Enabled: r.Backup.Enabled, Schedule: r.Backup.Schedule, Time: r.Backup.Time,
			},
		})
	}
	return out
}

// lastBackupRun is when this router was last backed up, or zero.
//
// ZERO ON ERROR, and that is the safe direction here rather than the cautious
// one: `IsDue` reads zero as "never run", so an unreadable history takes a
// backup that may be redundant. The alternative — reporting a recent run — would
// SKIP a backup, and a missing restore point is worse than a duplicate one.
func (s *Server) lastBackupRun(routerID string) int64 {
	ts, err := s.auditDB.LastBackupRun(routerID)
	if err != nil {
		log.Printf("[backup] last run for %s: %v", routerID, err)
		return 0
	}
	return ts
}

// inRouterWriteQueue runs `fn` inside the router's write queue.
//
// Acquire/Release around it, so a router nobody is watching gets a session for
// the duration and a router someone IS watching shares theirs — see the header.
func (s *Server) inRouterWriteQueue(routerID string, fn func() error) error {
	sess, err := s.sessions.Acquire(routerID)
	if err != nil {
		return err
	}
	defer s.sessions.Release(routerID)
	// ── AND THE ALERT POOL MUST LET GO WHILE WE HOLD IT ───────────────────
	//
	// `syncAlertPool` excludes routers with a live session, but nothing re-ran
	// it here, so a scheduled write on a router NOBODY is watching opened a
	// second connection alongside the pool's -- two API channels on one device
	// for the length of the backup, on exactly the routers the pool covers
	// because no browser does.
	//
	// The same call `router:select` makes, for the same reason. `onIdle` hands
	// the router back when this session finally goes.
	s.syncAlertPool()

	// ── AND WAIT FOR THE DIAL, WHICH Acquire DOES NOT ─────────────────────
	//
	// `Acquire` constructs the Session and launches `connectLoop` in a
	// GOROUTINE; it returns before the TCP/TLS dial has happened. Running the
	// backup immediately therefore reads through a nil client and fails
	// `routeros: not connected`.
	//
	// MEASURED, on the first real scheduled run (2026-08-29): hAP AX3 succeeded
	// because a session was already live for it, and hAP AC2 failed — the router
	// NOBODY WAS WATCHING, which is precisely the case a scheduler exists for.
	// Every unit test passed throughout, because a fake reader is always
	// connected. Only a live run could show it.
	//
	// The live app does not have this problem for a different reason: its pool
	// holds a connection per router, so its scheduler always finds one open.
	//
	// BEFORE the write queue rather than inside it. Waiting inside would hold the
	// queue — and therefore block a browser's writes to that router — for the
	// whole dial.
	if err := waitConnected(sess, backupDialWait); err != nil {
		return err
	}
	return sess.InWriteQueue(fn)
}

// backupDialWait bounds the wait above. `connectLoop` retries every 5s, so this
// allows for a first dial plus a couple of retries; a router that is genuinely
// down should fail the run and record it, not stall the scheduler's tick.
const backupDialWait = 30 * time.Second

// waitConnected polls until the session has a live client or the wait expires.
//
// POLLED rather than signalled, deliberately: a condition variable on `Session`
// would be shared state on the hot path for the benefit of one caller. 250ms is
// far below the 5s retry cadence, so nothing is lost by not being event-driven.
func waitConnected(sess *session.Session, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		if sess.Connected() {
			return nil
		}
		if time.Now().After(deadline) {
			// The session's own reason if it has one — "connection refused",
			// "certificate error" — rather than a bare timeout, which would send
			// an operator looking in the wrong place.
			if why := sess.LastError(); why != "" {
				return fmt.Errorf("router did not connect within %s: %s", within, why)
			}
			return fmt.Errorf("router did not connect within %s", within)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// runScheduledBackup takes one backup.
//
// It is the manual path's `RunForConfig` with two differences and no third:
// `Source` is "schedule" rather than "manual", and `Actor` is empty because
// nobody pressed anything. Everything else — the recorder, the pruner, the
// retention defaults, `WritePair` — is the same code the Backups page uses, so a
// scheduled restore point and a manual one are the same artefact.
func (s *Server) runScheduledBackup(r backups.SchedRouter) error {
	rec := s.backupRecord(r.ID)
	keep := retentionFor(rec)

	sess, err := s.sessions.Acquire(r.ID)
	if err != nil {
		return err
	}
	defer s.sessions.Release(r.ID)
	// The pool lets go while this session holds the router — see
	// inRouterWriteQueue for why, and note this is the commoner path: a
	// scheduled backup runs against routers nobody is watching by definition.
	s.syncAlertPool()

	_, _, runErr := backups.RunFor(backups.RunForConfig{
		RouterID: r.ID, Label: r.Label, Password: rec.password,
		DataDir: s.store.Dir, Source: "schedule",
		Recorder: bkRecorder{db: s.auditDB, routerID: r.ID},
		Pruner:   bkPruner{db: s.auditDB}, Retention: keep,
		// DRIFT AND FAILURE REACH THE OPERATOR AGAIN. The live app notified on
		// both and the port carried neither, so a scheduled backup has been
		// silent since cutover -- including when it FAILED, which is the one a
		// dashboard cannot show you because nobody is looking at it.
		Notify: func(kind, title, body string) { s.dispatchBackup(r.ID, kind, title, body) },
		Connect: func() (backups.Writer, func(), error) {
			return func(cmd string, args ...string) ([]map[string]string, error) {
				replies, err := sess.Exec(routeros.Cmd{Path: cmd, Args: args})
				if err != nil {
					return nil, err
				}
				out := make([]map[string]string, 0, len(replies))
				for _, rep := range replies {
					out = append(out, map[string]string(rep))
				}
				return out, nil
			}, func() {}, nil
		},
		WritePair: backups.WritePair,
		Now:       func() int64 { return time.Now().UnixMilli() },
		Log:       func(m string) { log.Printf("[backup][%s] %s", r.Label, m) },
	})
	return runErr
}

// backupRecord is `conn.backupRecordFor` without the connection.
//
// The page's version hangs off a browser `conn`; the scheduler has none. Same
// read, same three-way defaults left to `internal/backups`.
func (s *Server) backupRecord(routerID string) backupRecord {
	list, _ := s.store.Routers()
	for _, r := range list {
		if r.ID != routerID {
			continue
		}
		return backupRecord{
			block: &backups.Backup{
				Enabled: r.Backup.Enabled, Schedule: r.Backup.Schedule, Time: r.Backup.Time,
			},
			keepCount: r.Backup.KeepCount,
			keepDays:  r.Backup.KeepDays,
			password:  r.Backup.Password,
		}
	}
	return backupRecord{}
}

// retentionFor is the three-way read: absent takes the DEFAULT, present takes
// the value, and ZERO IS A VALUE.
//
// ── THE ZERO IS THE WHOLE REASON THIS IS A POINTER ─────────────────────────
//
// `keepCount: 0` means "keep no restore points by count", and a nil-coalescing
// read that treated it as absent would substitute 30 — quietly keeping backups
// an operator asked to stop keeping. The mirror is worse: a nil that read as 0
// would DELETE every restore point on a router that never configured retention.
// The backup-normalize corpus pins the same distinction on the write side.
func retentionFor(rec backupRecord) backups.Retention {
	keep := backups.Retention{
		KeepCount: backups.DefaultKeepCount, KeepDays: backups.DefaultKeepDays,
	}
	if rec.keepCount != nil {
		keep.KeepCount = *rec.keepCount
	}
	if rec.keepDays != nil {
		keep.KeepDays = *rec.keepDays
	}
	return keep
}
