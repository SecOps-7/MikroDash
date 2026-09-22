package server

// Setting the router's clock by hand: the AI Agent's `clock_set` action
// (MikroMCP's set_system_clock).
//
// ── THE TIME IS SENT ONCE, AT THE MOMENT IT IS ASKED FOR ────────────────────
//
// The Clock page shows the date and time and never sends them: as editable
// fields, every save of the time zone would re-send the time the form was
// opened with (resource.Clock). Setting the clock is a one-off command instead,
// sent when the operator approves it.
//
// ── ROUTEROS DOES NOT REFUSE A TIME OUT OF RANGE; IT WRAPS IT ───────────────
//
// Measured on the CHR, RouterOS 7.24.4: `/system/clock/set time=25:61:00` was
// accepted and moved the clock to 02:01 the NEXT day, fifteen hours out, with no
// error. An invalid date is refused ("invalid date"). So both are checked here,
// strictly, before anything is sent: a typo must not become a silent jump.

import (
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
)

// clockTarget reads "YYYY-MM-DD HH:MM[:SS]" (a T may stand for the space) into
// the date and time RouterOS takes, in the router's own time zone. Pure.
func clockTarget(raw string) (date, clock, problem string) {
	s := strings.Join(strings.Fields(strings.Replace(strings.TrimSpace(raw), "T", " ", 1)), " ")
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02"), t.Format("15:04:05"), ""
		}
	}
	return "", "", "give the date and time as YYYY-MM-DD HH:MM:SS, in the router's own time zone"
}

// runClockSet sets the router's date and time. `via` is "agent" for the
// assistant, recorded on the audit row.
func (cn *conn) runClockSet(raw, via string) writeOutcome {
	if cn.routerID == "" || cn.rsession == nil {
		return writeOutcome{Code: "unavailable"}
	}
	if !cn.canPage("clock", "write") {
		cn.recorder().Denied(audit.Event{Action: "clock.set", TargetType: "clock", RouterID: cn.routerID})
		return writeOutcome{Code: "denied"}
	}
	date, clock, problem := clockTarget(raw)
	if problem != "" {
		return writeOutcome{Code: "bad-request", Detail: map[string]any{"message": problem}}
	}
	name := date + " " + clock
	err := cn.inWriteQueue(func() error {
		// WHAT IT WAS, for the audit row: a clock set by hand is a thing someone
		// will later want to explain.
		was := ""
		if rows, err := cn.rsession.Exec(routeros.Cmd{Path: "/system/clock/print"}); err == nil && len(rows) > 0 {
			was = rows[0]["date"] + " " + rows[0]["time"]
		}
		extra := []audit.KV{{Key: "was", Value: was}}
		if via != "" {
			extra = append(extra, audit.KV{Key: "via", Value: via})
		}
		cn.recorder().Record(audit.Event{
			Action: "clock.set", TargetType: "clock", TargetName: name, RouterID: cn.routerID,
			Extra: extra, Note: "the router's clock was set by hand",
		})
		_, werr := cn.rsession.Exec(routeros.Cmd{Path: "/system/clock/set",
			Args: []string{"=date=" + date, "=time=" + clock}})
		return werr
	})
	if err != nil {
		return writeOutcome{Code: rosWriteFail(err), Name: name,
			Detail: map[string]any{"message": safe.Message(err.Error())}}
	}
	return writeOutcome{Action: "set", Name: name}
}
