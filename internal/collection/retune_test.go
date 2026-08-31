package collection

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

// `PollRetunes` against the LIVE rule, whose table is lifted from the route by
// `tools/settings-apply-cases.js`.

type retuneCorpus struct {
	PollMap map[string]string `json:"pollMap"`
	Cases   map[string]struct {
		Updates   map[string]any `json:"updates"`
		Saved     map[string]any `json:"saved"`
		Overrides map[string]any `json:"overrides"`
		// A collector name maps to its new interval, or to null meaning "keep
		// whatever the collector has".
		Retunes map[string]*float64 `json:"retunes"`
	} `json:"cases"`
}

func loadRetuneCorpus(t *testing.T) retuneCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/settings-apply-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c retuneCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 || len(c.PollMap) == 0 {
		t.Fatal("corpus is empty -- these tests would pass against nothing")
	}
	return c
}

func TestPollRetunesMatchesLive(t *testing.T) {
	c := loadRetuneCorpus(t)

	for name, tc := range c.Cases {
		t.Run(name, func(t *testing.T) {
			got := PollRetunes(tc.Updates, tc.Saved, tc.Overrides)

			mine := map[string]*float64{}
			for _, r := range got {
				if _, dup := mine[r.Collector]; dup {
					t.Errorf("%s was re-tuned twice", r.Collector)
				}
				if r.KeepCurrent {
					mine[r.Collector] = nil
					continue
				}
				v := float64(r.PollMs)
				mine[r.Collector] = &v
			}

			want := tc.Retunes
			if want == nil {
				want = map[string]*float64{}
			}
			if !sameRetunes(mine, want) {
				t.Errorf("updates %v overrides %v\n  got  %s\n  live %s",
					tc.Updates, tc.Overrides, showRetunes(mine), showRetunes(want))
			}
		})
	}
}

// TestThePollMapIsTheGeneratedOne.
//
// The table is the part with a history: the live source records that
// `pollTopology`, `pollVlans` and `pollPpp` were once missing from it, so "the
// sliders existed and the bounds existed, but with no entry here the value was
// dropped on save and never reached the collector". A hand-copied map here would
// reproduce exactly that, silently.
func TestThePollMapIsTheGeneratedOne(t *testing.T) {
	c := loadRetuneCorpus(t)
	if !reflect.DeepEqual(PollMap(), c.PollMap) {
		t.Errorf("the poll map differs from the lifted one:\n  got  %v\n  live %v",
			keysOfMap(PollMap()), keysOfMap(c.PollMap))
	}
	for _, k := range []string{"pollTopology", "pollVlans", "pollPpp"} {
		if _, ok := PollMap()[k]; !ok {
			t.Errorf("%s is missing -- that exact omission is recorded upstream as a "+
				"defect that dropped the value on save", k)
		}
	}
}

// TestAnOverridePinsByPRESENCE.
//
// `overrides[key] !== undefined`, not truthiness. An operator who pins an
// interval to 0 or to false means it, and a port testing truth un-pins exactly
// those — silently, while the modal still shows the override.
func TestAnOverridePinsByPRESENCE(t *testing.T) {
	updates := map[string]any{"pollSystem": 5000}
	saved := map[string]any{"pollSystem": 5000}

	// Believability: with no override it IS applied, or the refusals below are
	// indistinguishable from the function refusing everything.
	if got := PollRetunes(updates, saved, map[string]any{}); len(got) != 1 {
		t.Fatalf("an unpinned key produced %d re-tunes, want 1", len(got))
	}

	for _, v := range []any{0, 0.0, false, "", nil, "0"} {
		got := PollRetunes(updates, saved, map[string]any{"pollSystem": v})
		if len(got) != 0 {
			t.Errorf("an override of %#v did not pin -- the global save silently un-pins "+
				"the router the pool is serving", v)
		}
	}
}

// TestOnlyChangedKeysAreApplied.
//
// The decision reads `updates`; the value reads `saved`. `saved` is the whole
// merged file, so a port deciding from it would re-tune every collector on every
// save and restart streams nobody touched.
func TestOnlyChangedKeysAreApplied(t *testing.T) {
	saved := map[string]any{}
	for k := range PollMap() {
		saved[k] = 4000
	}
	got := PollRetunes(map[string]any{"pollSystem": 4000}, saved, map[string]any{})
	if len(got) != 1 {
		t.Errorf("one changed key re-tuned %d collectors -- the merged file drove the "+
			"decision", len(got))
	}
	if len(got) == 1 && got[0].Collector != "system" {
		t.Errorf("re-tuned %q", got[0].Collector)
	}
}

func sameRetunes(a, b map[string]*float64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || (av == nil) != (bv == nil) {
			return false
		}
		if av != nil && *av != *bv {
			return false
		}
	}
	return true
}

func showRetunes(m map[string]*float64) string {
	keys := keysOfMap2(m)
	out := "{"
	for i, k := range keys {
		if i > 0 {
			out += " "
		}
		if m[k] == nil {
			out += k + ":keep"
		} else {
			out += k + ":" + json.Number(jsonNum(*m[k])).String()
		}
	}
	return out + "}"
}

func jsonNum(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func keysOfMap(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func keysOfMap2(m map[string]*float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
