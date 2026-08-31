package guard

// Replay tools/queueguard-cases.js's recorded answers through the port.
//
// The cases are synthetic; the ANSWERS are the live queueGuard's. Neither
// implementation is asked about itself — see the generator's header for why this
// guard in particular needs it.

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

type qgRate struct {
	set bool
	bps int64
}

// UnmarshalJSON accepts the `null | number` the original produces.
func (r *qgRate) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*r = qgRate{}
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*r = qgRate{set: true, bps: n}
	return nil
}

func (r qgRate) rate() Rate { return Rate{Bps: r.bps, Set: r.set} }

func (r qgRate) String() string {
	if !r.set {
		return "absent"
	}
	return strconv.FormatInt(r.bps, 10)
}

type qgValues struct {
	Target   string `json:"target"`
	MaxLimit string `json:"maxLimit"`
	Disabled bool   `json:"disabled"`
}

func (v qgValues) values() SimpleQueueValues {
	return SimpleQueueValues{Target: v.Target, MaxLimit: ParsePair(v.MaxLimit), Disabled: v.Disabled}
}

type qgCases struct {
	FloorBps int64 `json:"floorBps"`
	Rates    []struct {
		Raw  string `json:"raw"`
		Want qgRate `json:"want"`
	} `json:"rates"`
	Pairs []struct {
		Raw  string `json:"raw"`
		Up   qgRate `json:"up"`
		Down qgRate `json:"down"`
	} `json:"pairs"`
	Cidrs []struct {
		Cidr string `json:"cidr"`
		IP   string `json:"ip"`
		// `true | false | null` — a Go bool cannot hold the third, which is the
		// whole point of the function.
		Want *bool `json:"want"`
	} `json:"cidrs"`
	Checks []struct {
		Name          string    `json:"name"`
		SelfAddresses []string  `json:"selfAddresses"`
		Values        qgValues  `json:"values"`
		Before        *qgValues `json:"before"`
		Want          struct {
			Level  string `json:"level"`
			Code   string `json:"code"`
			Detail *struct {
				Address      string `json:"address"`
				Target       string `json:"target"`
				MaxLimitUp   qgRate `json:"maxLimitUp"`
				MaxLimitDown qgRate `json:"maxLimitDown"`
			} `json:"detail"`
			Fingerprint string `json:"fingerprint"`
		} `json:"want"`
	} `json:"checks"`
}

func loadQueueCases(t *testing.T) qgCases {
	t.Helper()
	b, err := os.ReadFile("../../testdata/queueguard-cases.json")
	if err != nil {
		t.Fatalf("read cases: %v — run: node tools/queueguard-cases.js", err)
	}
	var c qgCases
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse cases: %v", err)
	}
	return c
}

func TestQueueGuardFloorMatchesTheOriginal(t *testing.T) {
	c := loadQueueCases(t)
	if c.FloorBps != SelfThrottleFloorBps {
		t.Fatalf("floor drifted: original %d, port %d", c.FloorBps, SelfThrottleFloorBps)
	}
}

func TestParseRateMatchesTheOriginal(t *testing.T) {
	c := loadQueueCases(t)
	if len(c.Rates) == 0 {
		t.Fatal("no rate cases")
	}
	for _, tc := range c.Rates {
		got := ParseRate(tc.Raw)
		want := tc.Want.rate()
		if got != want {
			t.Errorf("ParseRate(%q) = {%d %t}, original = {%d %t}",
				tc.Raw, got.Bps, got.Set, want.Bps, want.Set)
		}
	}
}

func TestParsePairMatchesTheOriginal(t *testing.T) {
	c := loadQueueCases(t)
	for _, tc := range c.Pairs {
		got := ParsePair(tc.Raw)
		if got.Up != tc.Up.rate() || got.Down != tc.Down.rate() {
			t.Errorf("ParsePair(%q) = up{%d %t} down{%d %t}, original = up{%d %t} down{%d %t}",
				tc.Raw, got.Up.Bps, got.Up.Set, got.Down.Bps, got.Down.Set,
				tc.Up.bps, tc.Up.set, tc.Down.bps, tc.Down.set)
		}
	}
}

// The three-valued answer is compared as three values. Collapsing undecidable
// into false here would hide exactly the confusion the function exists to avoid.
func TestCIDRContainsMatchesTheOriginal(t *testing.T) {
	c := loadQueueCases(t)
	var undecidable int
	for _, tc := range c.Cidrs {
		contains, decided := CIDRContains(tc.Cidr, tc.IP)
		if tc.Want == nil {
			undecidable++
			if decided {
				t.Errorf("CIDRContains(%q, %q) decided %t, original declined to answer",
					tc.Cidr, tc.IP, contains)
			}
			continue
		}
		if !decided {
			t.Errorf("CIDRContains(%q, %q) declined to answer, original said %t",
				tc.Cidr, tc.IP, *tc.Want)
			continue
		}
		if contains != *tc.Want {
			t.Errorf("CIDRContains(%q, %q) = %t, original = %t",
				tc.Cidr, tc.IP, contains, *tc.Want)
		}
	}
	// A corpus that stopped exercising the undecidable branch would still pass
	// every assertion above while testing two thirds of the function.
	if undecidable < 5 {
		t.Errorf("only %d undecidable cases — the third branch is barely covered", undecidable)
	}
}

func TestCheckSimpleQueueMatchesTheOriginal(t *testing.T) {
	c := loadQueueCases(t)
	var warned int
	for _, tc := range c.Checks {
		var before *SimpleQueueValues
		if tc.Before != nil {
			b := tc.Before.values()
			before = &b
		}
		got := CheckSimpleQueue(tc.SelfAddresses, tc.Values.values(), before, c.FloorBps)

		if got.Level != tc.Want.Level {
			t.Errorf("%s: level = %q, original = %q", tc.Name, got.Level, tc.Want.Level)
			continue
		}
		if got.Code != tc.Want.Code {
			t.Errorf("%s: code = %q, original = %q", tc.Name, got.Code, tc.Want.Code)
		}
		if got.Fingerprint != tc.Want.Fingerprint {
			t.Errorf("%s: fingerprint =\n  %s\noriginal =\n  %s",
				tc.Name, got.Fingerprint, tc.Want.Fingerprint)
		}
		if tc.Want.Detail == nil {
			if got.Detail != nil {
				t.Errorf("%s: detail = %v, original had none", tc.Name, got.Detail)
			}
			continue
		}
		warned++
		if got.Detail == nil {
			t.Errorf("%s: no detail, original had one", tc.Name)
			continue
		}
		if got.Detail["address"] != tc.Want.Detail.Address {
			t.Errorf("%s: detail.address = %v, original = %q",
				tc.Name, got.Detail["address"], tc.Want.Detail.Address)
		}
		if got.Detail["target"] != tc.Want.Detail.Target {
			t.Errorf("%s: detail.target = %v, original = %q",
				tc.Name, got.Detail["target"], tc.Want.Detail.Target)
		}
		ml, ok := got.Detail["maxLimit"].(Pair)
		if !ok {
			t.Errorf("%s: detail.maxLimit is %T, want Pair", tc.Name, got.Detail["maxLimit"])
			continue
		}
		if ml.Up != tc.Want.Detail.MaxLimitUp.rate() || ml.Down != tc.Want.Detail.MaxLimitDown.rate() {
			t.Errorf("%s: detail.maxLimit = %v/%v, original = %s/%s",
				tc.Name, ml.Up, ml.Down, tc.Want.Detail.MaxLimitUp, tc.Want.Detail.MaxLimitDown)
		}
	}
	// This guard's failure mode is silence. A corpus where nothing warns would
	// pass every assertion above and prove nothing.
	if warned < 5 {
		t.Errorf("only %d warning cases — the guard's whole purpose is barely covered", warned)
	}
}
