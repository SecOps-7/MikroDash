package wifiscan

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type corpus struct {
	Durations   []int `json:"durations"`
	MaxChannels int   `json:"maxChannels"`
	FleetCap    int   `json:"fleetCap"`
	Freqs       []struct {
		MHz any  `json:"mhz"`
		Ch  *int `json:"ch"`
	} `json:"freqs"`
	Rows []struct {
		Name string          `json:"name"`
		Row  json.RawMessage `json:"row"`
		Want json.RawMessage `json:"want"`
	} `json:"rows"`
	Traps []struct {
		Msg  *string `json:"msg"`
		Code string  `json:"code"`
	} `json:"traps"`
}

func load(t *testing.T) corpus {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "wifiscan-cases.json"))
	if err != nil {
		t.Fatalf("corpus: %v (regenerate with tools/wifiscan-cases.js)", err)
	}
	var c corpus
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Freqs) < 20 || len(c.Rows) < 10 || len(c.Traps) < 10 {
		t.Fatal("the corpus is not the generated one")
	}
	return c
}

func TestFreqToChannelMatchesTheLiveModule(t *testing.T) {
	for _, f := range load(t).Freqs {
		// A non-finite input is carried as a STRING ("NaN", "Infinity"), because
		// JSON has no way to spell one. Those exercise the guard.
		var mhz float64
		switch x := f.MHz.(type) {
		case float64:
			mhz = x
		case string:
			switch x {
			case "NaN":
				mhz = nan()
			case "Infinity":
				mhz = inf()
			default:
				t.Fatalf("unexpected frequency %q", x)
			}
		}
		got, ok := FreqToChannel(mhz)
		if f.Ch == nil {
			if ok {
				t.Errorf("FreqToChannel(%v) = %d; live says no channel", f.MHz, got)
			}
			continue
		}
		if !ok || got != *f.Ch {
			t.Errorf("FreqToChannel(%v) = %d/%v, live says %d", f.MHz, got, ok, *f.Ch)
		}
	}
}

func TestParseRowMatchesTheLiveModule(t *testing.T) {
	for _, c := range load(t).Rows {
		t.Run(c.Name, func(t *testing.T) {
			var in map[string]any
			// A row that is not an object at all ("nope", null) decodes to nil,
			// which is the case the parser must refuse.
			_ = json.Unmarshal(c.Row, &in)

			got, ok := ParseRow(in)
			if string(c.Want) == "null" {
				if ok {
					t.Fatalf("parsed %s as %+v; the live module drops it", c.Row, got)
				}
				return
			}
			if !ok {
				t.Fatalf("dropped %s; the live module parses it", c.Row)
			}
			// Compared through a DECODED map, not as raw text: the corpus is
			// pretty-printed and a Go marshal is compact, so a byte comparison
			// fails on whitespace alone. Decoding keeps what matters — a JSON
			// null stays nil and a zero stays 0, so "not reported" and "zero" are
			// still distinguishable, which is the whole reason those fields are
			// pointers.
			a, _ := json.Marshal(got)
			var mine, live map[string]any
			if err := json.Unmarshal(a, &mine); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(c.Want, &live); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(mine, live) {
				t.Errorf("row\n  got  %v\n  live %v", mine, live)
			}
		})
	}
}

func TestClassifyTrapMatchesTheLiveModule(t *testing.T) {
	for _, c := range load(t).Traps {
		msg := ""
		if c.Msg != nil {
			msg = *c.Msg
		}
		if got := ClassifyTrap(msg); got != c.Code {
			t.Errorf("ClassifyTrap(%q) = %q, live says %q", msg, got, c.Code)
		}
	}
}

// THE TRAP ORDER IS THE CONTRACT. "no such command prefix" contains "no such",
// so a classifier that tested the no-such-interface rule first would call every
// unsupported stack a missing interface — sending the operator to look for a
// typo in a name that is correct.
func TestTrapOrderPrefersTheMoreSpecificRule(t *testing.T) {
	if got := ClassifyTrap("no such command prefix (not found)"); got != "unsupported-stack" {
		t.Errorf("got %q; the more specific rule must win", got)
	}
}

// The limits are the live module's, not numbers chosen here.
func TestLimitsMatchTheLiveModule(t *testing.T) {
	c := load(t)
	if len(Durations) != len(c.Durations) {
		t.Fatalf("Durations = %v, live says %v", Durations, c.Durations)
	}
	for i := range Durations {
		if Durations[i] != c.Durations[i] {
			t.Errorf("Durations = %v, live says %v", Durations, c.Durations)
		}
	}
	if MaxChannels != c.MaxChannels {
		t.Errorf("MaxChannels = %d, live says %d", MaxChannels, c.MaxChannels)
	}
	if FleetCap != c.FleetCap {
		t.Errorf("FleetCap = %d, live says %d", FleetCap, c.FleetCap)
	}
}

func nan() float64 { return math.NaN() }
func inf() float64 { return math.Inf(1) }
