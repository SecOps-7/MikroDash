package reports

import (
	"strconv"
	"strings"
)

// The two limits `src/reports/build.js` applies to every report it builds.
const (
	// MaxPDFRows is where the PDF table stops. The live comment gives the
	// reasoning and it is worth keeping: samples are 1-minute bucketed, so an
	// unaggregated month is ~43,200 rows per series -- at roughly 43 rows to a
	// page, a thousand-page document rendered on the event loop that serves live
	// dashboards. The CSV is deliberately NOT capped: it is plain text, and a
	// spreadsheet user asking for a month wants the month.
	MaxPDFRows = 5000

	// ChartPoints is how many points a series is thinned to before drawing.
	ChartPoints = 150
)

// Thin keeps every step'th row so a long range still draws a readable line.
//
// `step` is `ceil(len/ChartPoints)`, so the result is at most ChartPoints long
// but usually shorter -- 43,200 rows thin to 150 exactly only when the length
// divides evenly, and the port must reproduce the uneven case too rather than
// truncating to a round number.
func Thin[T any](rows []T) []T {
	step := 1
	// `>` rather than `>=` mirrors the live source. The two are EQUIVALENT here --
	// at exactly ChartPoints the other branch computes ceil(150/150) = 1, the same
	// step -- which mutation testing confirmed by leaving `>=` alive. Recorded so
	// the next reader knows the gate is not blind to it; there is simply nothing
	// for it to see.
	if len(rows) > ChartPoints {
		// ceil, in integers: the live side is `Math.ceil(rows.length / CHART_POINTS)`.
		step = (len(rows) + ChartPoints - 1) / ChartPoints
	}
	out := make([]T, 0, (len(rows)+step-1)/step)
	for i, r := range rows {
		if i%step == 0 {
			out = append(out, r)
		}
	}
	return out
}

// CapRows caps the PDF table and says so IN THE TABLE.
//
// A note row rather than a seventh stat box, which is the live reasoning and a
// real constraint: `_render` lays the boxes out with `lineBreak: false`, so a
// seventh starts truncating values instead of wrapping, and traffic and
// bandwidth already use all six.
//
// The note goes in the FIRST column and every other column is set to the empty
// string -- not left absent. `_render` reads `row[col] != null ? String(...) : ”`
// so the two render identically, but the row's key set is what a differential
// gate compares, and an absent key is a different object.
func CapRows(rows []map[string]any, columns []string) ([]map[string]any, bool) {
	if len(rows) <= MaxPDFRows {
		return rows, false
	}
	kept := make([]map[string]any, 0, MaxPDFRows+1)
	kept = append(kept, rows[:MaxPDFRows]...)

	note := make(map[string]any, len(columns))
	for i, c := range columns {
		if i == 0 {
			note[c] = "… showing the first " + groupDigits(MaxPDFRows) + " of " +
				groupDigits(len(rows)) + " rows — narrow the range or aggregate for the rest"
		} else {
			note[c] = ""
		}
	}
	return append(kept, note), true
}

// groupDigits is JavaScript's `Number.prototype.toLocaleString()` with no
// argument, for the non-negative integers this note contains.
//
// THE LOCALE IS NOT A DETAIL. `toLocaleString()` uses the runtime's default, and
// the app container's is en-US -- so the live note reads "5,000 of 43,200" and a
// port using strconv would write "5000 of 43200". It appears only on the largest
// exports, which are the ones nobody re-reads, so it would have been a long time
// before anyone noticed.
//
// Pinned by build_test.go against a corpus that records the container's locale
// alongside the expected strings, because if that locale ever changes it is the
// EXPECTATIONS that are wrong, not this function.
func groupDigits(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
