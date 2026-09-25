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

// KeepLast is the last n rows: a continuous ping's trim.
func KeepLast(n int) func([]routeros.Reply) []routeros.Reply {
	return func(rows []routeros.Reply) []routeros.Reply {
		if len(rows) <= n {
			return rows
		}
		return rows[len(rows)-n:]
	}
}

// KeepLastSections is the rows of the last n `.section`s plus the one still
// arriving: a continuous torch's trim. Folded through CompleteSections, that is
// exactly its last n whole seconds.
func KeepLastSections(n int) func([]routeros.Reply) []routeros.Reply {
	return func(rows []routeros.Reply) []routeros.Reply {
		seen, i := 0, len(rows)
		for i > 0 {
			sec := rows[i-1][".section"]
			seen++
			if seen > n+1 {
				break
			}
			for i > 0 && rows[i-1][".section"] == sec {
				i--
			}
		}
		return rows[i:]
	}
}
