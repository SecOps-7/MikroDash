package server

// The Backups page's handlers: the state payload, the config diff, the schedule
// form's write, the operator delete, and the manual "Back Up Now".
//
// ── EVERY WRITE IS PORTED AND THE PAGE SHIPS ────────────────────────────────
//
// THIS HEADER HAS NOW BEEN WRONG TWICE, in the same way both times. It said "TWO
// WRITES REMAIN" while `backupsRun` sat a few hundred lines below it (corrected
// 2026-08-25), and then said "ONE WRITE REMAINS ... `backups:restore` ... that
// is why `backups` is still missing from `web/build.mjs`'s PAGES and `main.ts`'s
// PORTED set". All of that had expired too, and by then the page was shipping:
//
//   - The base-URL question was ANSWERED by the operator on 2026-08-25: Go owns
//     `/api/backups/:id/raw` outright. No new configuration, and `backupBaseUrl`
//     is not reinterpreted.
//   - `backups_raw.go` serves that route and `backups_restore.go` handles the
//     event; `ws.go` dispatches it.
//   - `backups` is in `build.mjs`'s PAGES and in `main.ts`'s PORTED, which says
//     so in its own comment.
//
// Corrected 2026-08-27 by measuring rather than reading. The recurring cause is
// that a note about something MISSING has no gate — nothing fails when the thing
// arrives, so only re-measuring finds it.
//
// ── DOWNLOADING IS A WRITE-LEVEL QUESTION ───────────────────────────────────
//
// `permitted` on the payload is `backups` at WRITE, not read — an export
// describes the whole network and the binary carries every key on the device.
// The page draws its download links from that flag, so getting it wrong here
// hands both to a viewer.

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/backups"
	"mikrodash/internal/db"
	"mikrodash/internal/routeros"
)

// bkErr answers the page in its own vocabulary. The page maps these codes to
// sentences; an unknown code falls through to its generic line.
func (cn *conn) bkErr(code string, extra map[string]any) {
	body := map[string]any{"code": code}
	for k, v := range extra {
		body[k] = v
	}
	cn.srv.hub.Send(cn.c, "backups:error", body)
}

// bkMayRead and bkMayWrite are the two gates. Kept as separate one-liners rather
// than folded into their call sites so the WRITE question has exactly one
// definition — see the header on why download is a write.
func (cn *conn) bkMayRead() bool  { return cn.canPage("backups", "read") }
func (cn *conn) bkMayWrite() bool { return cn.canPage("backups", "write") }

// backupsList answers `backups:list` with the whole page state.
//
// Settings, summary and history go together in ONE payload, so there is no
// order in which the parts can disagree with each other — a summary saying two
// stored pairs beside a table showing three is a bug report nobody can act on.
func (cn *conn) backupsList() {
	if cn.routerID == "" || cn.rsession == nil {
		cn.bkErr("denied", nil)
		return
	}
	if !cn.bkMayRead() {
		cn.bkErr("denied", nil)
		return
	}
	if cn.srv.auditDB == nil {
		// The history lives in the shared database. Without it an empty table
		// would read as "no backups have been taken" rather than "the record is
		// unavailable", and those are different enough to matter here.
		cn.bkErr("unavailable", nil)
		return
	}

	rows, err := cn.srv.auditDB.ListBackups(cn.routerID, 200)
	if err != nil {
		log.Printf("[backups] list %s: %v", cn.routerID, err)
		cn.bkErr("unavailable", nil)
		return
	}
	summary, err := cn.srv.auditDB.GetBackupSummary(cn.routerID)
	if err != nil {
		log.Printf("[backups] summary %s: %v", cn.routerID, err)
		cn.bkErr("unavailable", nil)
		return
	}

	rec := cn.backupRecordFor(cn.routerID)
	cn.srv.hub.Send(cn.c, "backups:state", backups.StatePayload{
		RouterID: cn.routerID,
		Label:    cn.rsession.Label,
		Settings: backups.SettingsFrom(rec.block, rec.keepCount, rec.keepDays, cn.srv.displayTimezone()),
		Summary:  summary,
		// The page uses this to disable its own buttons while a run is in flight,
		// and the set is on the SERVER so a second operator opening the page
		// mid-run sees it too — see (*Server).bkClaim.
		Running:   cn.srv.bkIsRunning(cn.routerID),
		Permitted: cn.bkMayWrite(),
		Rows:      backups.RowsFrom(rows),
	})
}

// backupRecord is the slice of a router's stored record this page needs.
type backupRecord struct {
	block     *backups.Backup
	keepCount *int
	keepDays  *int
	// password is the DECRYPTED backup credential. Carried here only so the
	// normaliser can tell "already has one" from "needs one minted"; it never
	// reaches a payload — see TestTheBackupPasswordCannotReachAPayload.
	password string
}

// backupRecordFor reads one router's backup block.
//
// The three-way defaults live in internal/backups; this only decides which
// values are PRESENT. A missing router yields a zero record, which
// `SettingsFrom` turns into the defaults — the same thing the live app shows for
// a router that has never been configured.
func (cn *conn) backupRecordFor(routerID string) backupRecord {
	list, _ := cn.srv.store.Routers()
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

// backupsSettings answers `backups:settings` — the schedule form's write.
//
// ── THE STORED BLOCK IS READ FIRST, AND THAT IS NOT AN OPTIMISATION ─────────
//
// The normaliser's contract is three-way: a field the browser OMITTED keeps what
// is stored. So the previous block has to be in hand before the patch is
// applied, or every omitted field silently reverts to a default and an unrelated
// edit changes a schedule nobody touched.
//
// ── ONLY THE BACKUP BLOCK IS WRITTEN ────────────────────────────────────────
//
// `UpdateRouter` patches the named keys and leaves the record's other 22 alone —
// including the seven the port does not model. See internal/store/write.go.
func (cn *conn) backupsSettings(raw json.RawMessage) {
	if cn.routerID == "" || cn.rsession == nil || !cn.bkMayWrite() {
		cn.recorder().Denied(audit.Event{
			Action: "backup.settings", TargetType: "router", RouterID: cn.routerID,
		})
		cn.bkErr("denied", nil)
		return
	}

	var in backups.BackupInput
	if err := json.Unmarshal(raw, &in); err != nil {
		cn.bkErr("failed", map[string]any{"message": "could not read the settings"})
		return
	}

	rec := cn.backupRecordFor(cn.routerID)
	var prev *backups.Prev
	if rec.block != nil {
		prev = &backups.Prev{
			Enabled: rec.block.Enabled, Schedule: rec.block.Schedule, Time: rec.block.Time,
			KeepCount: rec.keepCount, KeepDays: rec.keepDays, Password: rec.password,
		}
	}

	norm, err := backups.NormalizeBackup(in, prev, backups.GeneratePassword)
	if err != nil {
		// A mint failure, which must not be swallowed: a router enabled with no
		// password fails every backup, and it would fail silently.
		log.Printf("[backups] settings %s: %v", cn.routerID, err)
		cn.bkErr("failed", map[string]any{"message": "could not generate a backup password"})
		return
	}

	patch := map[string]any{
		"enabled":   norm.Enabled,
		"schedule":  norm.Schedule,
		"time":      norm.Time,
		"keepCount": norm.KeepCount,
		"keepDays":  norm.KeepDays,
	}
	// THE PASSWORD IS ONLY WRITTEN WHEN IT WAS JUST MINTED. Writing the carried
	// one back on every save would re-seal it with a fresh IV each time — churn
	// in a file an operator diffs, for a value that did not change. It is sealed
	// here rather than passed as plaintext, for the reason SetRouterPassword
	// exists.
	if norm.PasswordGenerated {
		sealed, err := cn.srv.store.Encrypt(norm.Password)
		if err != nil {
			log.Printf("[backups] seal %s: %v", cn.routerID, err)
			cn.bkErr("failed", map[string]any{"message": "could not store the backup password"})
			return
		}
		patch["password"] = sealed
	}
	if err := cn.srv.store.UpdateRouter(cn.routerID, map[string]any{"backup": patch}); err != nil {
		log.Printf("[backups] settings %s: %v", cn.routerID, err)
		cn.bkErr("failed", map[string]any{"message": "could not save the settings"})
		return
	}

	// The audit row carries what CHANGED, never the password — only that one
	// came into existence, which is worth a row even though its value is not.
	extra := []audit.KV{
		{Key: "enabled", Value: norm.Enabled},
		{Key: "schedule", Value: norm.Schedule},
		{Key: "time", Value: norm.Time},
	}
	if norm.PasswordGenerated {
		extra = append(extra, audit.KV{Key: "passwordGenerated", Value: true})
	}
	cn.recorder().Record(audit.Event{
		Action: "backup.settings", TargetType: "router", Scope: "router",
		RouterID: cn.routerID, TargetName: cn.rsession.Label, Extra: extra,
	})

	// Answer with the whole state, so the form and the summary cards cannot
	// disagree about what was just saved.
	cn.backupsList()
}

// bkRecorder and bkPruner adapt the database to what one run needs.
type bkRecorder struct {
	db       *db.DB
	routerID string
}

func (r bkRecorder) LatestFingerprint(routerID string) (string, error) {
	return r.db.LatestFingerprint(routerID)
}

func (r bkRecorder) Record(row backups.RunRow) (int64, error) {
	return r.db.RecordBackup(db.BackupRun{
		RouterID: row.RouterID, TakenAt: row.TakenAt, Outcome: row.Outcome,
		Source: row.Source, Actor: row.Actor, Stem: row.Stem, Dir: row.Dir,
		Fingerprint: row.Fingerprint, RscBytes: row.RscBytes, BackupBytes: row.BackupBytes,
		Model: row.Model, Serial: row.Serial, OSVersion: row.OSVersion,
		MS: row.MS, Error: row.Error,
	})
}

type bkPruner struct{ db *db.DB }

func (p bkPruner) StoredBackupsFor(routerID string) ([]backups.StoredPair, error) {
	rows, err := p.db.StoredBackups(routerID)
	if err != nil {
		return nil, err
	}
	out := make([]backups.StoredPair, 0, len(rows))
	for _, r := range rows {
		out = append(out, backups.StoredPair{
			ID: r.ID, Stem: deref(r.Stem), Dir: deref(r.Dir),
			Bytes: r.RscBytes + r.BackupBytes,
		})
	}
	return out, nil
}

func (p bkPruner) MarkPruned(id int64, ts int64) (bool, error) {
	return p.db.MarkBackupPruned(id, ts)
}

// backupsRun answers `backups:run` — the manual "Back Up Now".
//
// ── IT REUSES THE SESSION'S CONNECTION, AND THAT IS PROTOCOL REALITY 3 ──────
//
// The live app opens its OWN short-lived connection per backup, because
// `/file/read` returns raw bytes and "the decode is a property of the
// connection" — every collector on the shared one wants text. THIS SIDE HAS NO
// DECODE TO ISOLATE: go-routeros reads a word with io.ReadFull and converts with
// `string(b)`, which preserves bytes. So `Connect` hands back the session's own
// writer and `stop` is a no-op, proved by internal/backups/rawbytes_test.go.
//
// ── SERIALISED, BECAUSE A BACKUP HOLDS THE ROUTER ───────────────────────────
//
// Through `InWriteQueue`, the same chain a firewall edit goes through. A backup
// occupies flash and an API channel for as long as it takes, on hardware whose
// documented limit is concurrent channels.
func (cn *conn) backupsRun() {
	if cn.routerID == "" || cn.rsession == nil || !cn.bkMayWrite() {
		cn.recorder().Denied(audit.Event{
			Action: "backup.run", TargetType: "router", RouterID: cn.routerID,
		})
		cn.bkErr("denied", nil)
		return
	}
	if cn.srv.auditDB == nil {
		cn.bkErr("unavailable", nil)
		return
	}

	rec := cn.backupRecordFor(cn.routerID)
	if rec.password == "" {
		// Enabling generates the password; without one there is nothing to
		// encrypt the binary with, and an UNENCRYPTED backup is not an option —
		// it holds every key on the device in the clear.
		cn.bkErr("not-configured", nil)
		return
	}

	// EMITTED BEFORE THE CLAIM, as the original emits it before calling runFor.
	// So a click that turns out to be a duplicate still gets its `running` echo
	// and the button state follows the same path either way.
	cn.srv.hub.Send(cn.c, "backups:running", map[string]any{"routerId": cn.routerID})

	// A backup already in flight for this router is SKIPPED, not queued behind
	// the first: the second click wanted a restore point taken now, and the one
	// being written already is that. No row is recorded, because no run happened.
	if !cn.srv.bkClaim(cn.routerID) {
		cn.recorder().Record(audit.Event{
			Action: "backup.run", TargetType: "router", Scope: "router",
			RouterID: cn.routerID, TargetName: cn.rsession.Label, Outcome: "ok",
			Extra: []audit.KV{
				{Key: "outcome", Value: backups.OutcomeSkipped},
				{Key: "changed", Value: false},
			},
		})
		cn.backupsList()
		return
	}
	// RELEASED BEFORE THE STATE PAYLOAD IS BUILT, not after. `backupsList` reads
	// the same set to fill `running`, so a release deferred to the end of this
	// function would send a final payload still claiming a run is in flight and
	// leave the page's buttons disabled until something else refreshed it. The
	// original deletes in runFor's `finally`, which is likewise before its
	// `_bkPayload`. The defer stays as a panic net; Delete is idempotent.
	defer cn.srv.bkRelease(cn.routerID)

	actor := ""
	if cn.sess != nil {
		actor = cn.sess.Username
	}
	keep := backups.Retention{
		KeepCount: backups.DefaultKeepCount, KeepDays: backups.DefaultKeepDays,
	}
	if rec.keepCount != nil {
		keep.KeepCount = *rec.keepCount
	}
	if rec.keepDays != nil {
		keep.KeepDays = *rec.keepDays
	}

	var res backups.RunResult
	err := cn.rsession.InWriteQueue(func() error {
		var runErr error
		res, _, runErr = backups.RunFor(backups.RunForConfig{
			RouterID: cn.routerID, Label: cn.rsession.Label, Password: rec.password,
			DataDir: cn.srv.store.Dir, Source: "manual", Actor: actor,
			Recorder: bkRecorder{db: cn.srv.auditDB, routerID: cn.routerID},
			Pruner:   bkPruner{db: cn.srv.auditDB}, Retention: keep,
			Connect: func() (backups.Writer, func(), error) {
				return cn.bkWriter(), func() {}, nil
			},
			WritePair: backups.WritePair,
			Now:       func() int64 { return time.Now().UnixMilli() },
			Log:       func(m string) { log.Printf("[backup][%s] %s", cn.rsession.Label, m) },
		})
		return runErr
	})
	cn.srv.bkRelease(cn.routerID)
	if err != nil {
		// The RECORDING failed, not the backup — see RunFor. Worth saying,
		// because the next scheduled tick will take this backup again.
		log.Printf("[backups] run %s: %v", cn.routerID, err)
		cn.bkErr("failed", map[string]any{"message": "the run could not be recorded"})
		cn.backupsList()
		return
	}

	outcome := "ok"
	if res.Outcome == backups.OutcomeFailed {
		outcome = "error"
	}
	cn.recorder().Record(audit.Event{
		Action: "backup.run", TargetType: "router", Scope: "router",
		RouterID: cn.routerID, TargetName: cn.rsession.Label, Outcome: outcome,
		Extra: []audit.KV{
			{Key: "outcome", Value: res.Outcome},
			{Key: "changed", Value: res.Changed},
		},
	})
	cn.backupsList()
}

// bkWriter adapts the session to the one command shape a backup run needs.
func (cn *conn) bkWriter() backups.Writer {
	return func(cmd string, args ...string) ([]map[string]string, error) {
		replies, err := cn.rsession.Exec(routeros.Cmd{Path: cmd, Args: args})
		if err != nil {
			return nil, err
		}
		out := make([]map[string]string, 0, len(replies))
		for _, r := range replies {
			out = append(out, map[string]string(r))
		}
		return out, nil
	}
}

// bkDeleteStore adapts the database to what the delete sweep needs.
//
// `RowFor` is router-scoped HERE rather than in the sweep, so the sweep cannot
// be handed a row it should not see even by a caller that forgot — the same
// reason `bkStoredRow` exists for the diff.
type bkDeleteStore struct {
	db       *db.DB
	routerID string
}

func (s bkDeleteStore) RowFor(id int64, routerID string) *backups.DeletableRow {
	row, err := s.db.GetBackup(id)
	if err != nil || !backups.RowBelongsTo(row, routerID) {
		return nil
	}
	return &backups.DeletableRow{
		ID: row.ID, Stem: deref(row.Stem), Dir: deref(row.Dir),
		Pruned: row.PrunedAt != nil,
	}
}

func (s bkDeleteStore) DeleteRow(id int64) (bool, error) { return s.db.DeleteBackup(id) }

// backupsDelete answers `backups:delete`.
//
// ── THE AUDIT ROW IS THE SURVIVING RECORD ───────────────────────────────────
//
// This removes the history row as well as the files, so once it returns the only
// remaining trace is in `audit_events` — a table deliberately absent from
// PURGE_TABLES and deleteRouterData(). The note says so in those words, because
// somebody reading the trail later needs to know it is not one record among
// several.
func (cn *conn) backupsDelete(raw json.RawMessage) {
	if cn.routerID == "" || cn.rsession == nil || !cn.bkMayWrite() {
		cn.recorder().Denied(audit.Event{
			Action: "backup.delete", TargetType: "backup", RouterID: cn.routerID,
		})
		cn.bkErr("denied", nil)
		return
	}
	if cn.srv.auditDB == nil {
		cn.bkErr("unavailable", nil)
		return
	}

	var req struct {
		IDs []any `json:"ids"`
	}
	if json.Unmarshal(raw, &req) != nil {
		cn.bkErr("not-found", nil)
		return
	}
	ids := backups.NormalizeIDs(req.IDs)
	if len(ids) == 0 {
		cn.bkErr("not-found", nil)
		return
	}

	// The fallback directory, for a row written before `dir` was recorded. The
	// ROW's own dir wins where it has one — labels change, and re-slugging the
	// current label would aim the unlink at the wrong directory.
	fallback := backups.DirFor(cn.srv.store.Dir, backups.SlugFor(cn.rsession.Label))

	removed, failed := backups.DeleteFor(
		bkDeleteStore{db: cn.srv.auditDB, routerID: cn.routerID},
		cn.routerID, ids, fallback,
		func(msg string) { log.Printf("[backups] %s", msg) })

	if len(removed) > 0 {
		parts := make([]string, 0, len(removed))
		for _, id := range removed {
			parts = append(parts, strconv.FormatInt(id, 10))
		}
		cn.recorder().Record(audit.Event{
			Action: "backup.delete", TargetType: "backup", Scope: "router",
			RouterID: cn.routerID, TargetID: strings.Join(parts, ","),
			Note: strconv.Itoa(len(removed)) + " restore point(s) deleted, rows removed; " +
				"this audit entry is the surviving record",
		})
	}
	if failed > 0 {
		// Said rather than swallowed: the page's list will show the rows that
		// survived, and without this the operator would read that as their
		// selection having been partly ignored.
		cn.bkErr("failed", map[string]any{
			"message": strconv.Itoa(failed) + " could not be deleted"})
	}

	cn.backupsList()
	// Everyone ELSE on this router's Backups page re-requests their OWN payload.
	//
	// A nudge, not the data. `backupsList` builds a payload carrying `permitted`,
	// computed for the socket that asked — so broadcasting it here would tell a
	// viewer they may write. The live app splits it the same way and says so in
	// the same words.
	//
	// Without this, a second operator with the page open keeps seeing restore
	// points that no longer exist, and finds out by clicking one.
	cn.srv.hub.BroadcastExcept("router-"+cn.routerID+"-page-backups", cn.c,
		"backups:ran", map[string]any{"routerId": cn.routerID})
}

// backupsDiff answers `backups:diff` for one stored pair.
//
// THE ROW IS LOOKED UP AND THEN CHECKED AGAINST THIS SOCKET'S ROUTER. A row on
// another router is "not-found", not "denied": the two are the same answer from
// outside, and distinguishing them would confirm the id exists.
func (cn *conn) backupsDiff(raw json.RawMessage) {
	if cn.routerID == "" || !cn.bkMayRead() {
		cn.bkErr("denied", nil)
		return
	}
	if cn.srv.auditDB == nil {
		cn.bkErr("unavailable", nil)
		return
	}
	var req struct {
		ID int64 `json:"id"`
		// Against names an explicit comparison target. Without it the default is
		// the previous stored pair, which is what "what changed in this backup"
		// means — but the page is free to ask for any other stored row.
		Against int64 `json:"against"`
	}
	if json.Unmarshal(raw, &req) != nil {
		cn.bkErr("not-found", nil)
		return
	}

	newer := cn.bkStoredRow(req.ID)
	if newer == nil {
		cn.bkErr("not-found", nil)
		return
	}

	var older *db.BackupRow
	if req.Against != 0 {
		if older = cn.bkStoredRow(req.Against); older == nil {
			cn.bkErr("not-found", nil)
			return
		}
	} else {
		stored, err := cn.srv.auditDB.StoredBackups(cn.routerID)
		if err != nil {
			cn.bkErr("unavailable", nil)
			return
		}
		// STRICTLY EARLIER, not "the next one along". Two rows can share a
		// taken_at — a scheduled run and a manual one in the same millisecond —
		// and comparing a backup against its own timestamp twin would produce an
		// empty diff that reads as "nothing changed".
		for i := range stored {
			if stored[i].TakenAt < newer.TakenAt {
				older = &stored[i]
				break
			}
		}
	}

	// THE DIRECTORY COMES FROM THE ROW. Labels change; the row records where the
	// pair was actually written, and nothing resolves an old backup by slugging
	// the CURRENT label — otherwise renaming a router orphans every diff it had.
	read := func(r *db.BackupRow) (string, error) {
		return backups.ReadRsc(deref(r.Dir), deref(r.Stem))
	}

	newText, err := read(newer)
	if err != nil {
		log.Printf("[backups] read %d: %v", newer.ID, err)
		cn.bkErr("failed", map[string]any{"message": "could not read the stored export"})
		return
	}
	oldText := ""
	if older != nil {
		if oldText, err = read(older); err != nil {
			log.Printf("[backups] read %d: %v", older.ID, err)
			cn.bkErr("failed", map[string]any{"message": "could not read the stored export"})
			return
		}
	}

	// THE BASELINE PATH STILL RUNS A REAL DIFF, against "". The earliest stored
	// configuration has nothing before it, so every line counts as added — and
	// the counts are sent even though the page renders the baseline case as
	// "No earlier backup", because `baseline` is a flag on a real result rather
	// than a substitute for one.
	//
	// OLDER FIRST. The reversed call produces a diff of the same size with every
	// sign inverted, which reads as a plausible diff in the wrong direction
	// rather than as an error.
	result := backups.Diff(oldText, newText)

	var against any
	if older != nil {
		against = older.ID
	}
	cn.srv.hub.Send(cn.c, "backups:diff", map[string]any{
		"id": newer.ID, "against": against, "baseline": older == nil,
		"added": result.Added, "removed": result.Removed,
		"truncated": result.Truncated, "hunks": result.Hunks,
	})
}

// bkStoredRow reads a row that this socket's router owns AND that still has
// files.
//
// A row on another router is "not found" rather than "forbidden": the two are
// the same answer from outside, and distinguishing them would confirm the id
// exists.
func (cn *conn) bkStoredRow(id int64) *db.BackupRow {
	row, err := cn.srv.auditDB.GetBackup(id)
	if err != nil {
		log.Printf("[backups] row %d: %v", id, err)
		return nil
	}
	if !backups.RowBelongsTo(row, cn.routerID) || row.Stem == nil || row.PrunedAt != nil {
		return nil
	}
	return row
}

// displayTimezone is the zone the schedule card labels its time with.
//
// EMPTY IS THE SERVER'S OWN, and the card says "server time" for it — so an
// unreadable settings file degrades to a truthful label rather than to a wrong
// zone name. That is why the error is dropped rather than surfaced.
func (s *Server) displayTimezone() string {
	if s.store == nil {
		return ""
	}
	settings, err := s.store.Settings()
	if err != nil {
		return ""
	}
	tz, _ := settings["displayTimezone"].(string)
	return tz
}
