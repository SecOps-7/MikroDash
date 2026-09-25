// Package backups decides when a router's configuration backup should run.
//
// Only the SCHEDULING half lives here for now — pure, no router I/O, no
// filesystem — because it is the part that can be gated without hardware and the
// part that failed silently for longest. A backup that does not happen produces
// no error, no row and no log line, only an absence somebody notices much later.
// One router's daily schedule had never fired once.
package backups

import (
	"regexp"
	"strconv"
	"strings"

	"mikrodash/internal/reports"
)

// Schedules is `BACKUP_SCHEDULES`, in milliseconds.
var Schedules = map[string]int64{
	"hourly":  3600000,
	"daily":   86400000,
	"weekly":  604800000,
	"monthly": 2592000000,
}

// DefaultTime is what a router that has never had a time chosen backs up at.
//
// The distinction the scheduler rests on: an ABSENT time takes this default,
// while an explicitly stored "" means "any time" and keeps the interval-only
// behaviour. Collapsing the two would make CLEARING the field impossible — it
// would read back as unset and the default would reappear on the next tick.
const DefaultTime = "08:00"

// Backup is the per-router backup block.
//
// Time is a POINTER for exactly the reason above: nil is "absent, take the
// default" and a pointer to "" is "any time". A plain string cannot tell those
// apart, and the difference is a feature the operator can reach.
type Backup struct {
	Enabled  bool
	Schedule string
	Time     *string
}

// backupTime is `/^([01]\d|2[0-3]):([0-5]\d)$/`, and it is STRICTER than the
// normaliser that writes the field: `_normalizeTime` accepts a single-digit hour
// and pads it, but a value stored raw as "8:00" never went through that and is
// read here as no time at all. Reproduced rather than widened — a port that
// accepted "8:00" would schedule a backup the live app does not.
var backupTime = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)

// TimeMinutes is minutes since local midnight, or -1 when no time is set.
//
// -1 rather than a (int, bool) pair because the caller's only question is
// "anchored or not", and the live version's `null` collapses the same way.
func TimeMinutes(t string) int {
	m := backupTime.FindStringSubmatch(strings.TrimSpace(t))
	if m == nil {
		return -1
	}
	h, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	return h*60 + min
}

// IsDue reports whether a router should be backed up now.
//
// ── THE ELAPSED-INTERVAL GATE IS SKIPPED FOR AN ANCHORED DAILY SCHEDULE ─────
//
// This is the whole of drift §1 and it is easy to get subtly wrong. A daily
// backup at a chosen time is anchored by the WALL CLOCK alone: the
// `now >= target && lastRun < target` pair below already permits exactly one run
// per day, so gating on the elapsed interval as well only holds a run back past
// its own target. A backup taken at 11:45 left 08:00 undue the next morning —
// under 24 hours had passed — and once it finally fired at 11:45 it stayed
// there for good. That is the very drift the anchor exists to remove.
//
// Weekly and monthly KEEP the interval gate, because `todayAt` knows an hour and
// a minute but not a weekday or a date, and so cannot tell one week or month
// from the next on its own.
//
// `hourly` is never anchored at all: an hourly backup that waits for 08:00 is a
// daily backup.
func IsDue(b *Backup, lastRun, now int64, tz string) bool {
	if b == nil || !b.Enabled {
		return false
	}
	interval := Schedules[b.Schedule]
	if interval == 0 {
		return false
	}
	// A router that has never run is due immediately: the first backup should
	// not wait a day to prove the feature works.
	if lastRun == 0 {
		return true
	}

	chosen := DefaultTime
	if b.Time != nil {
		chosen = *b.Time
	}
	at := TimeMinutes(chosen)
	anchored := at >= 0 && b.Schedule != "hourly"

	if !(anchored && b.Schedule == "daily") {
		if now-lastRun < interval {
			return false
		}
	}
	if !anchored {
		return true
	}

	// Anchored to the wall clock rather than to the last run, so a daily backup
	// set for 02:00 stays at 02:00 instead of drifting by however long each run
	// took. `lastRun < target` holds it to one run per day; `now >= target` lets
	// a router switched off at 02:00 still catch up when it comes back, rather
	// than skipping the day altogether.
	target := todayAt(at, now, tz)
	return now >= target && lastRun < target
}

// todayAt is the instant of `minutes` past local midnight on the day `now` falls
// in.
//
// It borrows internal/reports' period arithmetic rather than reimplementing it —
// as the live version borrows `reports/period` — because a backup at 02:00 has
// exactly the same spring-forward problem a report at 02:00 does, and one
// DST-correct implementation that is already gated beats a second one that is
// not.
func todayAt(minutes int, now int64, tz string) int64 {
	c := reports.CivilAt(now, tz)
	return reports.InstantOf(reports.Civil{
		Year: c.Year, Month: c.Month, Day: c.Day,
		Hour: minutes / 60, Minute: minutes % 60,
	}, tz)
}
