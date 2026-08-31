package reports

// Turning stored rows into export rows.
//
// ── THE EXPORT FORMATS TIMESTAMPS DIFFERENTLY FROM THE PAGE ─────────────────
//
// `TsFmt` here is `format.js`'s `tsFmt`, and it is NOT the page's `fmtTs`. Two
// differences, both deliberate on the live side and both easy to smooth away:
//
//   - an absent timestamp is "" here and "—" on the page. A CSV cell holding an
//     em dash is a string in a date column; a blank one is blank.
//   - with no displayTimezone this renders UTC WITH A SUFFIX
//     ("2026-01-01 00:00:00 UTC") while the page renders the browser's local time
//     with no suffix. A file that outlives the session it was downloaded in has
//     to say which zone it is in; a screen the operator is looking at does not.
//
// Reproducing both is the job. A port that shared one formatter between page and
// export would be tidier and would change what a downloaded file means.

import (
	"fmt"
	"time"
)

// TsFmt formats a timestamp for an exported row.
func TsFmt(ts int64, tz string) string {
	if ts == 0 {
		return ""
	}
	if tz != "" {
		t := time.UnixMilli(ts).In(loc(tz))
		return fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d",
			t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second())
	}
	// `toISOString().replace('T',' ').slice(0,19) + ' UTC'` — seconds, no
	// fraction, and the zone named because a file gets read somewhere else later.
	t := time.UnixMilli(ts).UTC()
	return fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d UTC",
		t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second())
}

// FmtDuration is format.js's, which is NOT the page's fmtDuration: an absent or
// negative duration is "" here and "—" there, for the same reason TsFmt differs.
func FmtDuration(ms int64) string {
	if ms <= 0 {
		return ""
	}
	s := ms / 1000
	h := s / 3600
	m := (s % 3600) / 60
	sec := s % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}

// ExportSpec is one report type's CSV shape: the column order and the filename.
type ExportSpec struct {
	Columns  []string
	Filename string
}

// ExportSpecs are the five report types.
//
// The column keys are the RAW ones — `ts`, `rtt_ms` — not the human headings the
// PDF uses. That asymmetry is the original's: a CSV is read by a program as often
// as by a person, and a stable machine-readable header is worth more there than a
// pretty one.
var ExportSpecs = map[string]ExportSpec{
	"ping": {
		Columns:  []string{"ts", "target", "rtt_ms", "loss_pct"},
		Filename: "ping-report.csv",
	},
	"traffic": {
		Columns:  []string{"ts", "interface", "rx_mbps", "tx_mbps"},
		Filename: "traffic-report.csv",
	},
	"bandwidth": {
		Columns:  []string{"ts", "interface", "rx_mb", "tx_mb"},
		Filename: "bandwidth-report.csv",
	},
	"alerts": {
		Columns:  []string{"fired_at", "alert_type", "subject", "detail", "resolved_at", "down_time"},
		Filename: "alerts-report.csv",
	},
	"connectivity": {
		Columns:  []string{"ts", "status", "down_duration"},
		Filename: "connectivity-report.csv",
	},
}

// LabelSamples formats a sample series for export: every field as stored, with
// `ts` rendered.
func LabelSamples(rows []map[string]any, tz string) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		c := make(map[string]any, len(r))
		for k, v := range r {
			c[k] = v
		}
		c["ts"] = TsFmt(asMillis(r["ts"]), tz)
		out = append(out, c)
	}
	return out
}

// LabelAlerts formats the alert series, deriving the down time from the two
// columns the row carries — the query returns no such column, which is the same
// gap the page's sort had.
func LabelAlerts(rows []map[string]any, tz string) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		fired := asMillis(r["fired_at"])
		resolved := asMillis(r["resolved_at"])
		down := ""
		if resolved != 0 {
			down = FmtDuration(resolved - fired)
		}
		c := make(map[string]any, len(r)+1)
		for k, v := range r {
			c[k] = v
		}
		c["fired_at"] = TsFmt(fired, tz)
		c["resolved_at"] = TsFmt(resolved, tz)
		c["down_time"] = down
		out = append(out, c)
	}
	return out
}

// LabelConnectivity formats the connectivity series.
//
// An offline row with no duration is "Ongoing" rather than blank: the outage has
// no end yet, and an empty cell would read as one that lasted no time.
func LabelConnectivity(rows []ConnRow, tz string) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		status := "Offline"
		if r.Connected != 0 {
			status = "Online"
		}
		down := ""
		if r.Connected == 0 {
			if r.DowntimeMs != nil {
				down = FmtDuration(*r.DowntimeMs)
			} else {
				down = "Ongoing"
			}
		}
		out = append(out, map[string]any{
			"ts": TsFmt(r.TS, tz), "status": status, "down_duration": down,
		})
	}
	return out
}

// asMillis reads a timestamp out of a decoded row, whatever numeric shape it
// arrived in. A JSON number is a float64; a value straight from the database is
// an int64.
func asMillis(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	case int:
		return int64(t)
	}
	return 0
}
