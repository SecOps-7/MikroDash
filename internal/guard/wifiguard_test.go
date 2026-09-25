package guard

// Replay tools/wifiguard-cases.js's recorded answers through the port.
//
// The cases are synthetic; the ANSWERS are the live wifiGuard's. See the
// generator's header for why this guard needs one — the short version is that
// three of its details cannot survive a naive Go translation: presence versus
// emptiness, a write-only passphrase, and the ORDER of `detail.fields`.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"mikrodash/internal/routeros"
)

type wifiCases struct {
	Siblings []routeros.Reply `json:"siblings"`
	Order    []string         `json:"order"`
	Cases    []struct {
		Name      string            `json:"name"`
		Action    string            `json:"action"`
		Before    string            `json:"before"`
		Values    map[string]string `json:"values"`
		ValueKeys []string          `json:"valueKeys"`
		Want      struct {
			Level  string `json:"level"`
			Code   string `json:"code"`
			Detail *struct {
				Profile  string   `json:"profile"`
				SharedBy int      `json:"sharedBy"`
				Fields   []string `json:"fields"`
				Iface    string   `json:"iface"`
			} `json:"detail"`
			Fingerprint string `json:"fingerprint"`
		} `json:"want"`
	} `json:"cases"`
}

func loadWifiCases(t *testing.T) wifiCases {
	t.Helper()
	b, err := os.ReadFile("../../testdata/wifiguard-cases.json")
	if err != nil {
		t.Fatalf("read cases: %v — run: node tools/wifiguard-cases.js", err)
	}
	var c wifiCases
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse cases: %v", err)
	}
	return c
}

// TestInheritableOrderMatchesTheOriginal pins the ORDER, not just the set.
//
// `detail.fields` is built by walking the declaration, and the browser renders
// it into a sentence. A Go map would give a different order on different runs
// while every other assertion still passed.
func TestInheritableOrderMatchesTheOriginal(t *testing.T) {
	c := loadWifiCases(t)
	if len(c.Order) != len(Inheritable) {
		t.Fatalf("live declares %d inheritable fields, port has %d", len(c.Order), len(Inheritable))
	}
	for i, name := range c.Order {
		if Inheritable[i].Field != name {
			t.Errorf("inheritable[%d] = %q, live = %q", i, Inheritable[i].Field, name)
		}
	}
}

func TestCheckInheritMatchesTheOriginal(t *testing.T) {
	c := loadWifiCases(t)
	// The generator's `before` is one of the siblings, addressed by the case's
	// own name; "null" is the no-row case.
	byName := map[string]routeros.Reply{}
	for _, r := range c.Siblings {
		byName[r["name"]] = r
	}

	var warned int
	for _, tc := range c.Cases {
		// Which sibling the case used is implied by its expectations, so it is
		// resolved the same way the generator built it: the first sibling for
		// the shared-profile cases, and by profile for the others.
		var before routeros.Reply
		if tc.Before == "row" {
			before = wifiBeforeFor(tc.Name, c.Siblings)
		}
		set := map[string]bool{}
		for _, k := range tc.ValueKeys {
			set[k] = true
		}
		got := CheckInherit(WifiValues{Values: tc.Values, Set: set}, before, c.Siblings, tc.Action)
		label := tc.Name + " / " + tc.Action + " / before=" + tc.Before

		if got.Level != tc.Want.Level {
			t.Errorf("%s: level = %q, original = %q", label, got.Level, tc.Want.Level)
			continue
		}
		if got.Code != tc.Want.Code {
			t.Errorf("%s: code = %q, original = %q", label, got.Code, tc.Want.Code)
		}
		if got.Fingerprint != tc.Want.Fingerprint {
			t.Errorf("%s: fingerprint =\n  %s\noriginal =\n  %s",
				label, got.Fingerprint, tc.Want.Fingerprint)
		}
		if tc.Want.Detail == nil {
			if got.Detail != nil {
				t.Errorf("%s: detail = %v, original had none", label, got.Detail)
			}
			continue
		}
		warned++
		if got.Detail == nil {
			t.Errorf("%s: no detail, original had one", label)
			continue
		}
		if got.Detail["profile"] != tc.Want.Detail.Profile {
			t.Errorf("%s: profile = %v, original = %q", label, got.Detail["profile"], tc.Want.Detail.Profile)
		}
		if got.Detail["sharedBy"] != tc.Want.Detail.SharedBy {
			t.Errorf("%s: sharedBy = %v, original = %d", label, got.Detail["sharedBy"], tc.Want.Detail.SharedBy)
		}
		if got.Detail["interface"] != tc.Want.Detail.Iface {
			t.Errorf("%s: interface = %v, original = %q", label, got.Detail["interface"], tc.Want.Detail.Iface)
		}
		// ORDER-SENSITIVE on purpose. Comparing these as sets would pass for a
		// port that had lost the declaration order, which is the one thing this
		// field carries that the fingerprint does not.
		gotFields, _ := got.Detail["fields"].([]string)
		if len(gotFields) != len(tc.Want.Detail.Fields) {
			t.Errorf("%s: fields = %v, original = %v", label, gotFields, tc.Want.Detail.Fields)
			continue
		}
		for i := range gotFields {
			if gotFields[i] != tc.Want.Detail.Fields[i] {
				t.Errorf("%s: fields = %v, original = %v (order matters)",
					label, gotFields, tc.Want.Detail.Fields)
				break
			}
		}
	}
	// This guard's failure mode is silence. A corpus where nothing warns would
	// pass every assertion above and prove nothing.
	if warned < 5 {
		t.Errorf("only %d warning cases — the guard's whole purpose is barely covered", warned)
	}
}

// wifiBeforeFor picks the row a case was generated against.
//
// The generator names them: a case about a LONE profile uses wifi3, one about
// NO profile uses wifi4, and everything else uses wifi1 — the first of the two
// that share `home`.
func wifiBeforeFor(name string, siblings []routeros.Reply) routeros.Reply {
	want := "wifi1"
	switch {
	case strings.Contains(name, "LONE"):
		want = "wifi3"
	case strings.Contains(name, "NO profile"):
		want = "wifi4"
	case strings.Contains(name, "STORED"):
		want = "wifi5"
	}
	for _, r := range siblings {
		if r["name"] == want {
			return r
		}
	}
	return nil
}
