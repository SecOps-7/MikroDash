package guard

// Replay tools/capsmanguard-cases.js's recorded answers through the port.
//
// This guard has the largest blast radius in the registry and the least visible
// one: a profile save reaches every CAP that follows it, and the only sign is
// this warning. See the generator's header for the two details that make running
// both implementations worth it.

import (
	"encoding/json"
	"os"
	"testing"

	"mikrodash/internal/routeros"
)

type capsCases struct {
	ConfigRows []routeros.Reply `json:"configRows"`
	ProvRows   []routeros.Reply `json:"provRows"`
	Cases      []struct {
		Name      string            `json:"name"`
		Key       string            `json:"key"`
		Action    string            `json:"action"`
		CapCount  *int              `json:"capCount"`
		Values    map[string]string `json:"values"`
		ValueKeys []string          `json:"valueKeys"`
		Before    routeros.Reply    `json:"before"`
		Want      struct {
			Level  string `json:"level"`
			Code   string `json:"code"`
			Detail *struct {
				Profile   string   `json:"profile"`
				Rules     []string `json:"rules"`
				RuleCount int      `json:"ruleCount"`
				Caps      int      `json:"caps"`
				Action    string   `json:"action"`
			} `json:"detail"`
			Fingerprint string `json:"fingerprint"`
		} `json:"want"`
	} `json:"cases"`
}

func loadCapsCases(t *testing.T) capsCases {
	t.Helper()
	b, err := os.ReadFile("../../testdata/capsmanguard-cases.json")
	if err != nil {
		t.Fatalf("read cases: %v — run: node tools/capsmanguard-cases.js", err)
	}
	var c capsCases
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse cases: %v", err)
	}
	return c
}

func TestCheckPushMatchesTheOriginal(t *testing.T) {
	c := loadCapsCases(t)
	var warned, silent int
	for _, tc := range c.Cases {
		caps := -1
		if tc.CapCount != nil {
			caps = *tc.CapCount
		}
		got := CheckPush(CapsPushInput{
			ResourceKey: tc.Key, Action: tc.Action, Values: tc.Values,
			Before: tc.Before, ConfigRows: c.ConfigRows, ProvRows: c.ProvRows,
			CapCount: caps,
		})
		label := tc.Name + " / " + tc.Action

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
			silent++
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
		if got.Detail["ruleCount"] != tc.Want.Detail.RuleCount {
			t.Errorf("%s: ruleCount = %v, original = %d", label, got.Detail["ruleCount"], tc.Want.Detail.RuleCount)
		}
		if got.Detail["action"] != tc.Want.Detail.Action {
			t.Errorf("%s: action = %v, original = %q", label, got.Detail["action"], tc.Want.Detail.Action)
		}
		// `caps` is null when unknown; the corpus records that as -1.
		wantCaps := any(tc.Want.Detail.Caps)
		if tc.Want.Detail.Caps < 0 {
			wantCaps = nil
		}
		if got.Detail["caps"] != wantCaps {
			t.Errorf("%s: caps = %v, original = %v", label, got.Detail["caps"], wantCaps)
		}
		gotRules, _ := got.Detail["rules"].([]string)
		if len(gotRules) != len(tc.Want.Detail.Rules) {
			t.Errorf("%s: rules = %v, original = %v", label, gotRules, tc.Want.Detail.Rules)
			continue
		}
		for i := range gotRules {
			if gotRules[i] != tc.Want.Detail.Rules[i] {
				t.Errorf("%s: rules = %v, original = %v", label, gotRules, tc.Want.Detail.Rules)
				break
			}
		}
	}
	// BOTH floors. A corpus that only warned would prove the guard fires but not
	// that it stays quiet, and the silence is half of what this guard is for.
	if warned < 20 {
		t.Errorf("only %d warning cases — the guard's purpose is barely covered", warned)
	}
	if silent < 20 {
		t.Errorf("only %d silent cases — over-warning would go unnoticed", silent)
	}
}
