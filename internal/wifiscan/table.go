package wifiscan

import "sort"

// Table accumulates a frequency scan's freeze-frames.
//
// RouterOS does not send each row once: it re-sends the WHOLE table every
// freeze-frame interval. So rows are keyed on channel and the latest value wins
// — without that "the graph would grow a duplicate bar every second", in the
// live comment's words.
type Table struct {
	rows map[int]Row
	// Order is insertion order, kept only so a full table can be rendered without
	// re-sorting on every flush. Rows() sorts by channel.
	order []int

	// SampleCount is EVERY parseable row seen, including ones the bound dropped.
	// It is how many samples the radio produced, not how many survived.
	SampleCount int

	// Truncated is set once a NEW channel has been refused for want of room.
	Truncated bool
}

func NewTable() *Table { return &Table{rows: make(map[int]Row, 64)} }

// Add takes one parsed row.
//
// THE BOUND IS ON INSERTION, NOT ON UPDATES, and the distinction is the whole
// point of it. Once the table holds MaxChannels channels a NEW one is dropped
// and the scan is marked truncated — but a channel already in the table keeps
// updating.
//
// The bound exists to "bound memory against a device reporting per-sample". A
// device doing that would fill the table in seconds; a cap that also froze
// updates would then leave every one of the 200 channels the operator can see
// stuck at its first reading for the rest of the scan, while the real
// measurements were discarded. It would look identical for the first 200 rows
// and be wrong for everything after.
func (t *Table) Add(r Row) {
	t.SampleCount++
	if _, known := t.rows[r.Ch]; !known {
		if len(t.rows) >= MaxChannels {
			t.Truncated = true
			return
		}
		t.order = append(t.order, r.Ch)
	}
	t.rows[r.Ch] = r
}

// Rows is the table sorted by channel ascending.
//
// Sorted, not insertion-ordered: a real dual-band sweep arrives in whatever
// order the radio swept, which is not monotonic across bands.
func (t *Table) Rows() []Row {
	out := make([]Row, 0, len(t.rows))
	for _, ch := range t.order {
		if r, ok := t.rows[ch]; ok {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ch < out[j].Ch })
	return out
}

// Len is how many channels the table holds.
func (t *Table) Len() int { return len(t.rows) }
