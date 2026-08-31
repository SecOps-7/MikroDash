// Package reports ports the scheduled-report subsystem. This file is its
// calendar vocabulary: which stretch of time a report covers, and whether one is
// due.
//
// ── CALENDAR PERIODS, NOT ROLLING INTERVALS ─────────────────────────────────
//
// The backup scheduler treats "monthly" as thirty days, which is right for a
// backup because a backup does not care WHICH thirty days. A report does.
// "August" is the deliverable a customer expects; "the last thirty days as of
// whenever the process happened to tick" is not, and it silently changes meaning
// every time the container restarts. So a period here is a real calendar period
// in the operator's timezone, and always the last COMPLETE one.
//
// ── THE OFFSET LOOP IS REPRODUCED, NOT REPLACED ─────────────────────────────
//
// Go has `time.Date`, which resolves a civil time in a location directly, and
// using it here would be wrong. On a spring-forward day the requested local time
// may not exist at all — 02:30 where the clocks jump from 02:00 to 03:00 — and
// Go's own documentation says that in such cases "the choice of time zone, and
// therefore the time, is not guaranteed". The original iterates:
//
//	guess = wall; three times: next = wall - offsetAt(guess); …
//
// which lands on a specific instant. Reproducing the ITERATION is what makes the
// two agree on the days that are hard; calling time.Date would agree on all the
// easy days and diverge exactly where a report would silently move.
package reports

import (
	"fmt"
	"time"

	// The timezone database is EMBEDDED. This runs in a scratch container with no
	// /usr/share/zoneinfo, where LoadLocation would fail for every zone and every
	// period would silently be computed in UTC — a report covering the wrong
	// days, with nothing on the page to indicate it. 450 KB is a fair price.
	_ "time/tzdata"
)

// Frequencies are the periods a schedule may use.
var Frequencies = []string{"daily", "weekly", "monthly"}

const (
	// MaxAttempts is how many times a failed period is retried before it is
	// abandoned until the next one.
	MaxAttempts = 3
	// RetryAfterMs is the minimum gap between those attempts.
	RetryAfterMs = 30 * 60 * 1000
)

// Period is a half-open range of instants, in epoch milliseconds: [From, To).
type Period struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// Civil is a wall-clock date and time, with MONTH 0-BASED as in JavaScript.
//
// Keeping the original's numbering rather than Go's is deliberate: every
// arithmetic expression in this file is copied from the source, `c.month - 1`
// among them, and translating the base at each use is how an off-by-one month
// gets introduced. It is converted once, at the single call to time.Date.
type Civil struct {
	Year, Month, Day int
	Hour, Minute     int
	// Weekday is 0 for Sunday, as getUTCDay() is.
	Weekday int
}

// loc resolves a zone name. An EMPTY name is UTC, which is what the rest of the
// app does when displayTimezone is unset.
//
// An UNKNOWN name is also UTC, and that is a deliberate difference from the
// original: `Intl.DateTimeFormat` throws a RangeError for one, so the live app
// fails the request. A scheduler that panics on a bad settings value stops every
// report rather than one, so this degrades instead. The zone is a settings field
// and is never attacker-supplied.
func loc(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	l, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return l
}

// OffsetAt is how many milliseconds this zone is ahead of UTC at ts.
//
// ── IT CARRIES ts's SUB-SECOND REMAINDER, AND THAT IS THE ORIGINAL'S ────────
//
// The original formats the instant with `Intl.DateTimeFormat('sv-SE')`, which
// has SECOND resolution, parses that back as UTC and returns `asUtc - ts`. When
// ts is not a whole second the milliseconds do not cancel, so `offsetAt(-1,
// 'UTC')` is -999 rather than 0. Returning the clean zone offset here looked
// obviously right and disagreed with the reference on every such instant.
//
// It is harmless where the module uses it — `civil` adds the result straight
// back to ts, which cancels into a truncation to the second, and every instant
// `instantOf` probes is already whole — but "harmless" is not the same as "the
// same", and a caller outside this file would see the difference.
//
// The truncation FLOORS rather than truncating toward zero: JavaScript's date
// formatting has no notion of a negative second, so an instant before the epoch
// rounds down. Go's integer division rounds toward zero, which is why this does
// not simply write ts/1000.
func OffsetAt(ts int64, tz string) int64 {
	// AN EMPTY ZONE IS NOT THE SAME AS "UTC" HERE. The original returns 0 before
	// it formats anything (`if (!tz) return 0`), so an unset displayTimezone
	// carries no sub-second remainder while an explicit "UTC" does. The two
	// differ only off a whole second, which is exactly the kind of asymmetry that
	// survives being tidied away and then shows up as a one-off somewhere else.
	if tz == "" {
		return 0
	}
	_, off := time.UnixMilli(ts).In(loc(tz)).Zone()
	sec := ts / 1000
	if ts%1000 != 0 && ts < 0 {
		sec--
	}
	return sec*1000 + int64(off)*1000 - ts
}

// CivilAt is the civil date and time in tz at ts.
//
// The original reads the fields off a Date shifted by the offset and then asks
// it for its UTC parts; doing the same here rather than asking Go for the local
// parts keeps the two implementations structurally identical.
func CivilAt(ts int64, tz string) Civil {
	d := time.UnixMilli(ts + OffsetAt(ts, tz)).UTC()
	return Civil{
		Year:  d.Year(),
		Month: int(d.Month()) - 1,
		Day:   d.Day(),
		// Hour and Minute exist for schedules that fire at a time of day rather
		// than on a period boundary — a backup picks an HH:MM. Reports read Hour
		// only, through FireAt.
		Hour:    d.Hour(),
		Minute:  d.Minute(),
		Weekday: int(d.Weekday()),
	}
}

// InstantOf is the instant at which the given civil time occurs in tz.
//
// Iterated because the offset to subtract depends on the instant being computed.
// See the package comment for why this is not time.Date.
func InstantOf(c Civil, tz string) int64 {
	// time.Date normalises out-of-range values exactly as Date.UTC does, which is
	// what makes `Day - 1` and `Month - 1` work across a year boundary.
	wall := time.Date(c.Year, time.Month(c.Month+1), c.Day, c.Hour, c.Minute, 0, 0, time.UTC).UnixMilli()
	guess := wall
	for i := 0; i < 3; i++ {
		next := wall - OffsetAt(guess, tz)
		if next == guess {
			break
		}
		guess = next
	}
	return guess
}

// PeriodFor is the last complete calendar period before now.
//
// The range is [From, To) and never includes today. The bool is false for a
// frequency this does not know, which is how a corrupted row stops rather than
// running constantly.
func PeriodFor(frequency string, now int64, tz string) (Period, bool) {
	c := CivilAt(now, tz)

	switch frequency {
	case "daily":
		return Period{
			From: InstantOf(Civil{Year: c.Year, Month: c.Month, Day: c.Day - 1}, tz),
			To:   InstantOf(Civil{Year: c.Year, Month: c.Month, Day: c.Day}, tz),
		}, true

	case "weekly":
		// Monday-start. Weekday is 0 for Sunday, so Sunday is 6 days into the week.
		back := (c.Weekday + 6) % 7
		return Period{
			From: InstantOf(Civil{Year: c.Year, Month: c.Month, Day: c.Day - back - 7}, tz),
			To:   InstantOf(Civil{Year: c.Year, Month: c.Month, Day: c.Day - back}, tz),
		}, true

	case "monthly":
		// Month -1 normalises into the previous year, so December works.
		return Period{
			From: InstantOf(Civil{Year: c.Year, Month: c.Month - 1, Day: 1}, tz),
			To:   InstantOf(Civil{Year: c.Year, Month: c.Month, Day: 1}, tz),
		}, true
	}
	return Period{}, false
}

// FireAt is when a report for this period should go out: sendHour local time on
// the day the period closed.
//
// Computed from the civil date rather than by adding hours to To, so a 23-hour or
// 25-hour DST day still fires at the requested wall-clock hour.
func FireAt(p Period, sendHour int, tz string) int64 {
	c := CivilAt(p.To, tz)
	h := sendHour
	if h < 0 {
		h = 0
	}
	if h > 23 {
		h = 23
	}
	return InstantOf(Civil{Year: c.Year, Month: c.Month, Day: c.Day, Hour: h}, tz)
}

// Schedule is the half of a schedule row this file reads.
type Schedule struct {
	Enabled   bool   `json:"enabled"`
	Frequency string `json:"frequency"`
	SendHour  int    `json:"send_hour"`
	CreatedAt int64  `json:"created_at"`

	// Name is the operator's label for the schedule, and it is the ONLY
	// operator-supplied string that reaches a report email — it appears in the
	// subject and in the body's closing line. mailer.go treats it accordingly.
	Name string `json:"name"`
}

// History is what the database knows about a schedule's previous runs.
type History struct {
	// LastRun is the epoch ms of the most recent attempt, 0 if never.
	LastRun int64 `json:"lastRun"`
	// LastOutcome is "sent", "failed", "skipped" or empty.
	LastOutcome string `json:"lastOutcome"`
	// RunsInPeriod is how many attempts are already recorded inside the current
	// period.
	RunsInPeriod int `json:"runsInPeriod"`
}

// DueWindow is the window a schedule should report on right now, or false if it
// is not due.
//
// A schedule is due once now passes the period's fire time, provided nothing has
// already run for it since. CreatedAt is the floor when there is no history, so a
// schedule created at 10:00 with a 07:00 send hour does not immediately dump
// yesterday's report — "Send now" exists for proving it works.
//
// ── WHY FAILURES ARE RETRIED, AND ONLY A FEW TIMES ──────────────────────────
//
// Recording a failed attempt moves LastRun past the fire time, which would
// silently swallow an entire month's report because one SMTP connection timed
// out. Not recording it means retrying every tick forever against a dead host.
// Neither is acceptable, so a failed period is retried up to MaxAttempts with
// RetryAfterMs between attempts and then abandoned until the next period. Every
// attempt is recorded either way, so the history stays honest.
func DueWindow(s Schedule, h History, now int64, tz string) (Period, bool) {
	if !s.Enabled {
		return Period{}, false
	}
	period, ok := PeriodFor(s.Frequency, now, tz)
	if !ok {
		return Period{}, false
	}

	fire := FireAt(period, s.SendHour, tz)
	if now < fire {
		return Period{}, false
	}

	floor := h.LastRun
	if floor == 0 {
		floor = s.CreatedAt
	}
	if floor < fire {
		return period, true // nothing has run for this period yet
	}

	// Something has. Only a failure earns another go.
	if h.LastOutcome != "failed" {
		return Period{}, false
	}
	if h.RunsInPeriod >= MaxAttempts {
		return Period{}, false
	}
	if now-h.LastRun < RetryAfterMs {
		return Period{}, false
	}
	return period, true
}

// Label is a human label for the period, for the email subject.
func Label(frequency string, p Period, tz string) string {
	c := CivilAt(p.From, tz)
	if frequency == "monthly" {
		// `Intl.DateTimeFormat('en-GB', { year: 'numeric', month: 'long' })`
		// renders "August 2026", which is what this layout gives.
		return time.UnixMilli(p.From).In(loc(tz)).Format("January 2006")
	}
	start := fmt.Sprintf("%d-%02d-%02d", c.Year, c.Month+1, c.Day)
	if frequency == "daily" {
		return start
	}
	// `To - 1` is the last instant INSIDE the period, so a weekly label reads
	// Monday to Sunday rather than Monday to the following Monday.
	e := CivilAt(p.To-1, tz)
	return start + " to " + fmt.Sprintf("%d-%02d-%02d", e.Year, e.Month+1, e.Day)
}
