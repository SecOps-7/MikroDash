package wifiscan

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

type accumCorpus struct {
	MaxChannels int `json:"maxChannels"`
	Cases       []struct {
		Name        string `json:"name"`
		Sent        int    `json:"sent"`
		SampleCount int    `json:"sampleCount"`
		Truncated   bool   `json:"truncated"`
		Kept        int    `json:"kept"`
		First       *Row   `json:"first"`
		Last        *Row   `json:"last"`
		Channels    []int  `json:"channels"`
	} `json:"cases"`
}

func loadAccumCorpus(t *testing.T) accumCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/wifiscan-accum-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c accumCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("corpus is empty -- this test would pass against nothing")
	}
	if c.MaxChannels != MaxChannels {
		t.Fatalf("the live cap is %d, this port has %d", c.MaxChannels, MaxChannels)
	}
	return c
}

// rawsFor rebuilds each case's input from its name, the way the generator built
// it. The corpus records the OUTCOME; regenerating 250 identical rows into it
// would make it mostly padding.
func rawsFor(name string) []map[string]any {
	raw := func(freq int, over map[string]any) map[string]any {
		m := map[string]any{
			"channel": itoa2(freq) + "/20-Ce", "networks": "3", "load": "12",
			"nf": "-98", "max-signal": "-55", "min-signal": "-90",
		}
		for k, v := range over {
			m[k] = v
		}
		return m
	}
	seq := func(n int, over map[string]any) []map[string]any {
		out := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, raw(2412+i*5, over))
		}
		return out
	}
	switch name {
	case "one frame":
		return []map[string]any{raw(2412, nil), raw(2437, nil), raw(2462, nil)}
	case "nothing at all":
		return nil
	case "rows that do not parse":
		return []map[string]any{{}, {"channel": ""}, {"channel": "nonsense"}}
	case "two frames, latest wins":
		return []map[string]any{
			raw(2412, map[string]any{"load": "10"}), raw(2437, map[string]any{"load": "20"}),
			raw(2412, map[string]any{"load": "77"}), raw(2437, map[string]any{"load": "88"}),
		}
	case "out of order across bands":
		return []map[string]any{raw(5180, nil), raw(2412, nil), raw(5745, nil), raw(2437, nil), raw(5240, nil)}
	case "exactly at the cap":
		return seq(MaxChannels, nil)
	case "one channel over the cap":
		return seq(MaxChannels+1, nil)
	case "far over the cap":
		return seq(MaxChannels+50, nil)
	case "an existing channel updates after the cap":
		out := seq(MaxChannels, map[string]any{"load": "1"})
		out = append(out, raw(2412+999*5, nil), raw(2412, map[string]any{"load": "99"}))
		return out
	}
	return nil
}

func TestTableMatchesTheLiveAccumulator(t *testing.T) {
	c := loadAccumCorpus(t)
	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			raws := rawsFor(tc.Name)
			if raws == nil && tc.Sent != 0 {
				t.Fatalf("no input is reconstructed for %q, but the corpus sent %d rows", tc.Name, tc.Sent)
			}
			if len(raws) != tc.Sent {
				t.Fatalf("reconstructed %d rows, the corpus sent %d", len(raws), tc.Sent)
			}

			tab := NewTable()
			for _, r := range raws {
				if row, ok := ParseRow(r); ok {
					tab.Add(row)
				}
			}

			if tab.SampleCount != tc.SampleCount {
				t.Errorf("sampleCount %d, live %d", tab.SampleCount, tc.SampleCount)
			}
			if tab.Truncated != tc.Truncated {
				t.Errorf("truncated %v, live %v", tab.Truncated, tc.Truncated)
			}
			rows := tab.Rows()
			if len(rows) != tc.Kept {
				t.Fatalf("kept %d rows, live kept %d", len(rows), tc.Kept)
			}
			// BY VALUE. Row's optional fields are pointers, so `!=` compares
			// addresses and every comparison fails while printing two structs that
			// look identical apart from the hex.
			if tc.First != nil && len(rows) > 0 {
				if d := rowDiff(rows[0], *tc.First); d != "" {
					t.Errorf("first row: %s", d)
				}
			}
			if tc.Last != nil && len(rows) > 0 {
				if d := rowDiff(rows[len(rows)-1], *tc.Last); d != "" {
					t.Errorf("last row: %s", d)
				}
			}
			for i, ch := range tc.Channels {
				if rows[i].Ch != ch {
					t.Errorf("channel %d is %d, live %d", i, rows[i].Ch, ch)
				}
			}
			// Whatever the corpus says, the table must come out sorted.
			for i := 1; i < len(rows); i++ {
				if rows[i-1].Ch > rows[i].Ch {
					t.Fatalf("rows are not sorted by channel at index %d", i)
				}
			}
		})
	}
}

// TestTheBoundIsOnInsertionNotOnUpdates states the rule directly, because it is
// the one a reasonable implementation gets wrong and the one whose failure is
// invisible for the first 200 rows.
func TestTheBoundIsOnInsertionNotOnUpdates(t *testing.T) {
	tab := NewTable()
	for i := 0; i < MaxChannels; i++ {
		tab.Add(Row{Ch: i + 1, Load: intp(1)})
	}
	if tab.Truncated {
		t.Fatal("a table of exactly MaxChannels is already truncated")
	}

	tab.Add(Row{Ch: 9999, Load: intp(5)}) // new: refused
	if !tab.Truncated {
		t.Error("a new channel past the cap was not recorded as truncation")
	}
	if tab.Len() != MaxChannels {
		t.Errorf("the table grew to %d", tab.Len())
	}

	tab.Add(Row{Ch: 1, Load: intp(99)}) // existing: must still update
	rows := tab.Rows()
	if rows[0].Load == nil || *rows[0].Load != 99 {
		t.Error("an existing channel stopped updating once the table was full -- " +
			"every visible bar would freeze for the rest of the scan")
	}
	if tab.Len() != MaxChannels {
		t.Errorf("updating an existing channel changed the table size to %d", tab.Len())
	}
	// And every row seen is counted, dropped or not.
	if tab.SampleCount != MaxChannels+2 {
		t.Errorf("sampleCount %d, want %d -- it counts what the radio produced",
			tab.SampleCount, MaxChannels+2)
	}
}

func intp(v int) *int { return &v }

// rowDiff compares two rows by VALUE and names the first field that differs.
func rowDiff(got, want Row) string {
	if got.Ch != want.Ch {
		return fmt.Sprintf("ch %d, live %d", got.Ch, want.Ch)
	}
	if got.Primary != want.Primary || got.Secondary != want.Secondary {
		return fmt.Sprintf("primary/secondary %v/%v, live %v/%v",
			got.Primary, got.Secondary, want.Primary, want.Secondary)
	}
	for _, f := range []struct {
		name string
		a, b *int
	}{
		{"chNum", got.ChNum, want.ChNum}, {"nets", got.Nets, want.Nets},
		{"load", got.Load, want.Load}, {"nf", got.NF, want.NF},
		{"maxSig", got.MaxSig, want.MaxSig}, {"minSig", got.MinSig, want.MinSig},
	} {
		if (f.a == nil) != (f.b == nil) {
			return fmt.Sprintf("%s presence %v, live %v", f.name, f.a != nil, f.b != nil)
		}
		if f.a != nil && *f.a != *f.b {
			return fmt.Sprintf("%s %d, live %d", f.name, *f.a, *f.b)
		}
	}
	if (got.ChRaw == nil) != (want.ChRaw == nil) {
		return fmt.Sprintf("chRaw presence %v, live %v", got.ChRaw != nil, want.ChRaw != nil)
	}
	if got.ChRaw != nil && *got.ChRaw != *want.ChRaw {
		return fmt.Sprintf("chRaw %q, live %q", *got.ChRaw, *want.ChRaw)
	}
	return ""
}
