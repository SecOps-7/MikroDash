package backups

// The Backups page's `backups:state` payload.
//
// ── DOWNLOADING IS A WRITE-LEVEL ACT, DELIBERATELY ──────────────────────────
//
// Read shows the history and the diffs. WRITE is required to take a backup,
// change the schedule, OR DOWNLOAD EITHER HALF OF A PAIR — because an export
// describes the whole network and the binary carries every key on the device.
// Handing either to a browser is closer to taking a copy of the router than to
// reading a page.
//
// That is the one permission decision here a reasonable person would get wrong
// by analogy: everything else that only READS is read-level, and these two only
// read. `PermittedFor` is where the distinction lives, and `MayDownload` is
// deliberately the same question as `MayWrite` rather than a separate one that
// could drift.
//
// ── THE SETTINGS DEFAULTS ARE THREE-WAY, NOT TWO-WAY ────────────────────────
//
// `time` distinguishes ABSENT (take the 08:00 default) from an explicit ""
// (any time, interval-only) — see due.go. `keepCount` and `keepDays` use
// `== null` rather than `||`, so a stored 0 survives as 0 rather than being
// replaced by the default: 0 means "no limit", which is a real choice.

import "mikrodash/internal/db"

// Settings is the schedule card's half of the payload.
type Settings struct {
	Enabled  bool   `json:"enabled"`
	Schedule string `json:"schedule"`
	Time     string `json:"time"`
	// Timezone is the server's display zone, so the card can say which clock
	// 02:00 means. Empty is the server's own.
	Timezone  string `json:"timezone"`
	KeepCount int    `json:"keepCount"`
	KeepDays  int    `json:"keepDays"`
}

// Row is one history row as the page renders it.
type Row struct {
	ID      int64   `json:"id"`
	TakenAt int64   `json:"takenAt"`
	Outcome string  `json:"outcome"`
	Source  string  `json:"source"`
	Actor   *string `json:"actor"`
	Stem    *string `json:"stem"`
	Pruned  bool    `json:"pruned"`
	// Bytes is the PAIR's size, both halves together — what the page's Size
	// column shows and what the disk total is built from.
	Bytes     int64   `json:"bytes"`
	OSVersion *string `json:"osVersion"`
	Model     *string `json:"model"`
	Serial    *string `json:"serial"`
	MS        int64   `json:"ms"`
	Error     *string `json:"error"`
}

// StatePayload is the whole `backups:state` body.
type StatePayload struct {
	RouterID string           `json:"routerId"`
	Label    string           `json:"label"`
	Settings Settings         `json:"settings"`
	Summary  db.BackupSummary `json:"summary"`
	Running  bool             `json:"running"`
	// Permitted is whether this session may WRITE — take a backup, change the
	// schedule, or download. The page uses it to decide which controls to draw.
	Permitted bool  `json:"permitted"`
	Rows      []Row `json:"rows"`
}

// DefaultKeepCount and DefaultKeepDays are `BACKUP_DEFAULTS`.
const (
	DefaultKeepCount = 30
	DefaultKeepDays  = 365
	DefaultSchedule  = "daily"
)

// SettingsFrom projects a router's stored backup block onto the card.
//
// KeepCount and KeepDays are POINTERS for the same reason Time is: a stored 0
// means "no limit" and must survive, where a `|| default` would replace it.
func SettingsFrom(b *Backup, keepCount, keepDays *int, timezone string) Settings {
	s := Settings{
		Schedule:  DefaultSchedule,
		Time:      DefaultTime,
		Timezone:  timezone,
		KeepCount: DefaultKeepCount,
		KeepDays:  DefaultKeepDays,
	}
	if b != nil {
		s.Enabled = b.Enabled
		if b.Schedule != "" {
			s.Schedule = b.Schedule
		}
		if b.Time != nil {
			s.Time = *b.Time
		}
	}
	if keepCount != nil {
		s.KeepCount = *keepCount
	}
	if keepDays != nil {
		s.KeepDays = *keepDays
	}
	return s
}

// RowsFrom flattens stored rows for the page.
func RowsFrom(rows []db.BackupRow) []Row {
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, Row{
			ID: r.ID, TakenAt: r.TakenAt, Outcome: r.Outcome, Source: r.Source,
			Actor: r.Actor, Stem: r.Stem, Pruned: r.PrunedAt != nil,
			Bytes:     r.RscBytes + r.BackupBytes,
			OSVersion: r.OSVersion, Model: r.Model, Serial: r.Serial,
			MS: r.MS, Error: r.Error,
		})
	}
	return out
}

// RowBelongsTo reports whether a row may be touched by a caller looking at this
// router.
//
// A ROW ON ANOTHER ROUTER IS "NOT FOUND", not "forbidden". The two are the same
// answer from outside, and distinguishing them would confirm the id exists.
func RowBelongsTo(row *db.BackupRow, routerID string) bool {
	return row != nil && row.RouterID == routerID
}
