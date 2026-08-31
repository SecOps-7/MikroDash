package guard

// Replay tools/fwguard-cases.js's recorded answers through the port.
//
// The cases are synthetic; the ANSWERS are the live fwGuard's. See the
// generator's header for why this guard in particular gets one: it warns and
// fails open, so a wrong answer is silent — and unlike a queue, what it fails to
// warn about is not recoverable from the page that caused it.

import (
	"encoding/json"
	"os"
	"testing"
)

type fwCtxJSON struct {
	Resolved   bool     `json:"resolved"`
	Addresses  []string `json:"addresses"`
	Interfaces []string `json:"interfaces"`
	APIPort    int      `json:"apiPort"`
}

func (c fwCtxJSON) ctx() FWContext {
	return FWContext{Resolved: c.Resolved, Addresses: c.Addresses,
		Interfaces: c.Interfaces, APIPort: c.APIPort}
}

type fwRuleJSON struct {
	Chain       string `json:"chain"`
	Action      string `json:"action"`
	SrcAddress  string `json:"srcAddress"`
	DstAddress  string `json:"dstAddress"`
	Protocol    string `json:"protocol"`
	DstPort     string `json:"dstPort"`
	InInterface string `json:"inInterface"`
	Disabled    bool   `json:"disabled"`
}

func (r fwRuleJSON) rule() FWRule {
	return FWRule{Chain: r.Chain, Action: r.Action, SrcAddress: r.SrcAddress,
		DstAddress: r.DstAddress, Protocol: r.Protocol, DstPort: r.DstPort,
		InInterface: r.InInterface, Disabled: r.Disabled}
}

type fwCases struct {
	Ctx       fwCtxJSON `json:"ctx"`
	CtxTLS    fwCtxJSON `json:"ctxTls"`
	CtxNoAddr fwCtxJSON `json:"ctxNoAddr"`

	AddressCovers []struct {
		Spec      string   `json:"spec"`
		Addresses []string `json:"addresses"`
		Ctx       string   `json:"ctx"`
		// true | false | null — a Go bool cannot hold the third, which is the
		// whole point of the function.
		Want *bool `json:"want"`
	} `json:"addressCovers"`

	PortCovers []struct {
		Spec string `json:"spec"`
		Port int    `json:"port"`
		Want bool   `json:"want"`
	} `json:"portCovers"`

	MatchesUs []struct {
		Name string     `json:"name"`
		Rule fwRuleJSON `json:"rule"`
		Ctx  string     `json:"ctx"`
		Want bool       `json:"want"`
	} `json:"matchesUs"`

	Verdicts []struct {
		Name      string     `json:"name"`
		What      string     `json:"what"`
		Menu      string     `json:"menu"`
		Ctx       string     `json:"ctx"`
		Rule      fwRuleJSON `json:"rule"`
		HasBefore bool       `json:"hasBefore"`
		Want      struct {
			Level  string `json:"level"`
			Code   string `json:"code"`
			Detail *struct {
				Kind    string `json:"kind"`
				Chain   string `json:"chain"`
				Action  string `json:"action"`
				What    string `json:"what"`
				Address string `json:"address"`
				Iface   string `json:"iface"`
				Port    int    `json:"port"`
			} `json:"detail"`
			Fingerprint string `json:"fingerprint"`
		} `json:"want"`
	} `json:"verdicts"`

	Toggles []struct {
		Action         string `json:"action"`
		What           string `json:"what"`
		BeforeDisabled bool   `json:"beforeDisabled"`
		Want           struct {
			Level string `json:"level"`
			Code  string `json:"code"`
			Kind  string `json:"kind"`
		} `json:"want"`
	} `json:"toggles"`
}

var fwMenus = map[string]string{
	"filter": "/ip/firewall/filter",
	"raw":    "/ip/firewall/raw",
	"nat":    "/ip/firewall/nat",
}

func loadFWCases(t *testing.T) fwCases {
	t.Helper()
	b, err := os.ReadFile("../../testdata/fwguard-cases.json")
	if err != nil {
		t.Fatalf("read cases: %v — run: node tools/fwguard-cases.js", err)
	}
	var c fwCases
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse cases: %v", err)
	}
	return c
}

func TestAddressCoversMatchesTheOriginal(t *testing.T) {
	c := loadFWCases(t)
	var undecidable int
	for _, tc := range c.AddressCovers {
		covers, decided := AddressCovers(tc.Spec, tc.Addresses)
		if tc.Want == nil {
			undecidable++
			if decided {
				t.Errorf("AddressCovers(%q, %v) decided %t, original declined to answer",
					tc.Spec, tc.Addresses, covers)
			}
			continue
		}
		if !decided {
			t.Errorf("AddressCovers(%q, %v) declined, original said %t",
				tc.Spec, tc.Addresses, *tc.Want)
			continue
		}
		if covers != *tc.Want {
			t.Errorf("AddressCovers(%q, %v) = %t, original = %t",
				tc.Spec, tc.Addresses, covers, *tc.Want)
		}
	}
	// A corpus that stopped exercising the undecidable branch would still pass
	// every assertion above while testing two thirds of the function.
	if undecidable < 4 {
		t.Errorf("only %d undecidable cases — the third branch is barely covered", undecidable)
	}
}

func TestPortCoversMatchesTheOriginal(t *testing.T) {
	c := loadFWCases(t)
	for _, tc := range c.PortCovers {
		if got := PortCovers(tc.Spec, tc.Port); got != tc.Want {
			t.Errorf("PortCovers(%q, %d) = %t, original = %t", tc.Spec, tc.Port, got, tc.Want)
		}
	}
}

func TestMatchesUsMatchesTheOriginal(t *testing.T) {
	c := loadFWCases(t)
	for _, tc := range c.MatchesUs {
		ctx := c.Ctx.ctx()
		if tc.Ctx == "8729" {
			ctx = c.CtxTLS.ctx()
		}
		if got := MatchesUs(tc.Rule.rule(), ctx); got != tc.Want {
			t.Errorf("%s: MatchesUs = %t, original = %t", tc.Name, got, tc.Want)
		}
	}
}

func TestCheckRuleMatchesTheOriginal(t *testing.T) {
	c := loadFWCases(t)
	unresolvedCtx := FWContext{Resolved: false}
	var warned int
	for _, tc := range c.Verdicts {
		ctx := c.Ctx.ctx()
		if tc.Ctx == "unresolved" {
			ctx = unresolvedCtx
		}
		var before *FWRule
		if tc.HasBefore {
			b := tc.Rule.rule()
			before = &b
		}
		got := CheckRule(ctx, fwMenus[tc.Menu], tc.Rule.rule(), before, tc.What)
		label := tc.Name + " / " + tc.What + " / " + tc.Menu + " / " + tc.Ctx

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
		for _, f := range []struct {
			key  string
			want any
		}{
			{"kind", tc.Want.Detail.Kind}, {"chain", tc.Want.Detail.Chain},
			{"action", tc.Want.Detail.Action}, {"what", tc.Want.Detail.What},
			{"port", tc.Want.Detail.Port},
		} {
			if got.Detail[f.key] != f.want {
				t.Errorf("%s: detail.%s = %v, original = %v", label, f.key, got.Detail[f.key], f.want)
			}
		}
		// address and interface are `null` in the original when the context has
		// none, and the generator records that as "".
		gotAddr, _ := got.Detail["address"].(string)
		if gotAddr != tc.Want.Detail.Address {
			t.Errorf("%s: detail.address = %q, original = %q", label, gotAddr, tc.Want.Detail.Address)
		}
		gotIf, _ := got.Detail["interface"].(string)
		if gotIf != tc.Want.Detail.Iface {
			t.Errorf("%s: detail.interface = %q, original = %q", label, gotIf, tc.Want.Detail.Iface)
		}
	}
	// This guard's failure mode is silence. A corpus where nothing warns would
	// pass every assertion above and prove nothing.
	if warned < 20 {
		t.Errorf("only %d warning cases — the guard's whole purpose is barely covered", warned)
	}
}

// TestToggleClausesMatchTheOriginal exercises the two clauses that turn on the
// relationship between `values.disabled` and `before.disabled` — the shape
// enable and disable actually arrive in, and the only way each clause is tested
// against the other rather than in isolation.
func TestToggleClausesMatchTheOriginal(t *testing.T) {
	c := loadFWCases(t)
	for _, tc := range c.Toggles {
		values := FWRule{Chain: "input", Action: tc.Action, Disabled: tc.What == "disable"}
		before := FWRule{Chain: "input", Action: tc.Action, Disabled: tc.BeforeDisabled}
		got := CheckRule(c.Ctx.ctx(), "/ip/firewall/filter", values, &before, tc.What)
		kind := ""
		if got.Detail != nil {
			kind, _ = got.Detail["kind"].(string)
		}
		if got.Level != tc.Want.Level || got.Code != tc.Want.Code || kind != tc.Want.Kind {
			t.Errorf("%s/%s beforeDisabled=%t: got {%s %s %s}, original {%s %s %s}",
				tc.Action, tc.What, tc.BeforeDisabled,
				got.Level, got.Code, kind, tc.Want.Level, tc.Want.Code, tc.Want.Kind)
		}
	}
}
