package alert

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"

	"mikrodash/internal/db"
)

// The corpus the alert-row check writes, every expected value produced by
// the LIVE `_alertRow` lifted out of `src/index.js`.

type alertRowCorpus struct {
	Rows []struct {
		Name string `json:"name"`
		Row  struct {
			ID             int64   `json:"id"`
			RouterID       *string `json:"router_id"`
			AlertType      string  `json:"alert_type"`
			Subject        *string `json:"subject"`
			Detail         *string `json:"detail"`
			FiredAt        int64   `json:"fired_at"`
			ResolvedAt     *int64  `json:"resolved_at"`
			AcknowledgedAt *int64  `json:"acknowledged_at"`
			AcknowledgedBy *string `json:"acknowledged_by"`
		} `json:"row"`
	} `json:"rows"`
	Maps  map[string][][2]string     `json:"maps"`
	Cases map[string]json.RawMessage `json:"cases"`
}

func TestMakeRowMatchesLive(t *testing.T) {
	b, err := os.ReadFile("../../testdata/alert-row-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c alertRowCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Rows) == 0 || len(c.Cases) == 0 {
		t.Fatal("corpus is empty -- this test would pass against nothing")
	}

	// `none` decodes to a nil slice and must stay a NIL MAP, not an empty one:
	// `names && …` is false for a missing map, and an empty Go map would take the
	// same branch as a populated one and lose the distinction the live code makes.
	maps := map[string]map[string]string{}
	for name, pairs := range c.Maps {
		if pairs == nil {
			maps[name] = nil
			continue
		}
		m := map[string]string{}
		for _, kv := range pairs {
			m[kv[0]] = kv[1]
		}
		maps[name] = m
	}
	if _, ok := maps["none"]; !ok || maps["none"] != nil {
		t.Fatal("the `none` map is not nil, so the no-map branch is never taken")
	}

	seen := 0
	for _, r := range c.Rows {
		row := db.AlertRow{
			ID: r.Row.ID, AlertType: r.Row.AlertType,
			Subject: r.Row.Subject, Detail: r.Row.Detail, FiredAt: r.Row.FiredAt,
			ResolvedAt: r.Row.ResolvedAt, AcknowledgedAt: r.Row.AcknowledgedAt,
			AcknowledgedBy: r.Row.AcknowledgedBy,
		}
		if r.Row.RouterID != nil {
			row.RouterID = *r.Row.RouterID
		}
		for mapName, names := range maps {
			key := r.Name + "/" + mapName
			raw, ok := c.Cases[key]
			if !ok {
				t.Errorf("no live answer recorded for %s", key)
				continue
			}
			seen++

			got, err := json.Marshal(MakeRow(row, names))
			if err != nil {
				t.Fatal(err)
			}
			// Compared as decoded values, not as bytes: key ORDER differs between
			// a Go struct and a JavaScript object literal, and a byte comparison
			// would fail on that alone while missing a wrong value.
			var g, w any
			if err := json.Unmarshal(got, &g); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &w); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(g, w) {
				t.Errorf("%s:\n  got  %s\n  live %s", key, got, raw)
			}
		}
	}
	if seen != len(c.Cases) {
		t.Errorf("drove %d of the %d recorded cases -- the rest were never compared",
			seen, len(c.Cases))
	}
}

// TestTheKeySetIsExactlyLive.
//
// DeepEqual on decoded maps already catches an extra or missing key, but only on
// the rows that reach it. This states it once over the whole corpus, so a key
// the port invents is named rather than showing up as a diff in eight cases.
func TestTheKeySetIsExactlyLive(t *testing.T) {
	b, err := os.ReadFile("../../testdata/alert-row-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var c alertRowCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	var live map[string]any
	for _, raw := range c.Cases {
		if err := json.Unmarshal(raw, &live); err != nil {
			t.Fatal(err)
		}
		break
	}
	got, err := json.Marshal(MakeRow(db.AlertRow{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	var mine map[string]any
	if err := json.Unmarshal(got, &mine); err != nil {
		t.Fatal(err)
	}
	a, bb := keysOf(live), keysOf(mine)
	if !reflect.DeepEqual(a, bb) {
		t.Errorf("keys differ:\n  live %v\n  port %v", a, bb)
	}
}

// TestMakeRowsCarriesTheNameMap.
//
// `MakeRows` had NO test until a mutation dropping its `names` argument survived
// the suite — every case above drives `MakeRow` directly, so the list form was
// covered only by looking like it. It is the form both feeds inside
// `alerts:open` go through, which is the payload where a missing router name
// matters most: that emit carries alerts from one router, and the browser's
// panel shows several routers at once.
func TestMakeRowsCarriesTheNameMap(t *testing.T) {
	names := map[string]string{"r1": "Office", "r2": "Branch"}
	in := []db.AlertRow{
		{ID: 1, RouterID: "r1", AlertType: "cpu", FiredAt: 10},
		{ID: 2, RouterID: "r2", AlertType: "link", FiredAt: 20},
		{ID: 3, RouterID: "gone", AlertType: "ping", FiredAt: 30},
	}
	got := MakeRows(in, names)
	if len(got) != len(in) {
		t.Fatalf("%d rows out, %d in", len(got), len(in))
	}
	want := []*string{strPtr("Office"), strPtr("Branch"), nil}
	for i := range got {
		if got[i].ID != in[i].ID {
			t.Errorf("row %d is id %d, want %d -- the order moved", i, got[i].ID, in[i].ID)
		}
		if !sameStrPtr(got[i].RouterName, want[i]) {
			t.Errorf("row %d routerName = %s, want %s",
				i, showPtr(got[i].RouterName), showPtr(want[i]))
		}
	}
	// And the empty list is a list, not nil: it is marshalled straight into
	// `alerts:open`, where `[]` and `null` reach the browser differently and the
	// bell iterates it.
	if e := MakeRows(nil, names); e == nil || len(e) != 0 {
		t.Errorf("MakeRows(nil) = %v, want an empty non-nil slice", e)
	}
}

func strPtr(s string) *string { return &s }

func sameStrPtr(a, b *string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}

func showPtr(s *string) string {
	if s == nil {
		return "null"
	}
	return `"` + *s + `"`
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
