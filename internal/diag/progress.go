package diag

import "mikrodash/internal/routeros"

// CompleteSections is the part of a run still in flight that can be folded:
// every row up to the `.section` still arriving.
//
// Traceroute and torch report a section at a time — a whole hop table, a whole
// second of flows — and a section's rows arrive one by one. Folded while its
// last section is half here, traceroute would draw a table missing its later
// hops and torch would average a second over half its flows. The section still
// arriving is complete when the next one begins, or when the run ends and the
// caller folds everything.
//
// Rows with no `.section` (ping) are each complete, and all of them are
// returned.
func CompleteSections(rows []routeros.Reply) []routeros.Reply {
	n := len(rows)
	if n == 0 {
		return rows
	}
	last, ok := rows[n-1][".section"]
	if !ok {
		return rows
	}
	for n > 0 && rows[n-1][".section"] == last {
		n--
	}
	return rows[:n]
}
