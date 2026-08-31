package store

// `SettingsUpdate` against the LIVE validator, lifted and run by
// `tools/settings-validate-check.js`.
//
// ── THIS FILE EXISTS TO RETIRE A CAVEAT ─────────────────────────────────────
//
// `settings_write.go` opens by admitting that this one unit was read-ported:
// "Every other differential gate here RUNS the live implementation. This handler
// is inline in `src/index.js`, which calls `server.listen()` at require time and
// cannot be loaded by a test." True when written; not true since
// `tools/alert-row-check.js` showed that a block can be SLICED out of that file
// and evaluated without requiring it.
//
// A read-port is a rewrite that agrees with its author's reading. These 63 cases
// are what the live code DOES.

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

type settingsValidateCorpus struct {
	SettingKeys []string `json:"settingKeys"`
	Cases       map[string]struct {
		Body    map[string]any `json:"body"`
		Updates map[string]any `json:"updates"`
	} `json:"cases"`
}

func loadSettingsValidateCorpus(t *testing.T) settingsValidateCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/settings-validate-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c settingsValidateCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("corpus is empty -- this test would pass against nothing")
	}
	return c
}

func TestSettingsUpdateMatchesTheLiveValidator(t *testing.T) {
	c := loadSettingsValidateCorpus(t)

	for name, tc := range c.Cases {
		t.Run(name, func(t *testing.T) {
			got, reset := SettingsUpdate(tc.Body)
			if reset {
				t.Fatalf("%s reported a reset; no case carries _reset", name)
			}

			// Compared through JSON on both sides. The corpus holds what
			// JavaScript produced, where every number is a float64 once decoded;
			// comparing Go's int against that would fail on representation and
			// say nothing about the rule.
			gotJSON, err := json.Marshal(map[string]any(got))
			if err != nil {
				t.Fatal(err)
			}
			var mine map[string]any
			if err := json.Unmarshal(gotJSON, &mine); err != nil {
				t.Fatal(err)
			}
			if mine == nil {
				mine = map[string]any{}
			}
			want := tc.Updates
			if want == nil {
				want = map[string]any{}
			}

			if !reflect.DeepEqual(mine, want) {
				t.Errorf("body %v\n  got  %v\n  live %v", tc.Body, mine, want)
			}
		})
	}
}

// TestTheKeyIsABSENTWhenAValueIsRefused.
//
// Stated separately from the corpus comparison, because it is the property the
// whole validator turns on and a diff of two maps does not say it out loud: an
// invalid value means the key is MISSING, so `save` leaves the stored value
// alone. Clamping instead would let a hand-crafted request move a setting to the
// edge of its range while looking refused.
func TestTheKeyIsABSENTWhenAValueIsRefused(t *testing.T) {
	c := loadSettingsValidateCorpus(t)

	refused := map[string]string{
		"intBelowBound": "topN", "intAboveBound": "topN", "intUnparseable": "topN",
		"intNull": "topN", "intBoolean": "topN",
		"updateHoursLow": "updateCheckHours", "updateHoursHigh": "updateCheckHours",
		"authModeInvalid": "authMode", "authModeEmpty": "authMode",
		"sessionJustUnder": "sessionTimeoutMs", "sessionOne": "sessionTimeoutMs",
		"sessionOverMax": "sessionTimeoutMs", "sessionUnparseable": "sessionTimeoutMs",
		"credMasked":    "telegramBotToken",
		"tzInvalid":     "displayTimezone",
		"profileNumber": "customPollProfile", "profileString": "customPollProfile",
		"profileBroken": "customPollProfile",
	}
	for name, field := range refused {
		tc, ok := c.Cases[name]
		if !ok {
			t.Fatalf("the corpus has no case %q -- this list has drifted from it", name)
		}
		// The live side agrees it is refused. Without this the list could name a
		// case the validator actually accepts and the assertion would be about
		// nothing.
		if _, present := tc.Updates[field]; present {
			t.Fatalf("%s: the LIVE validator accepts %s, so this list is wrong", name, field)
		}
		got, _ := SettingsUpdate(tc.Body)
		if v, present := got[field]; present {
			t.Errorf("%s: %s = %v is present -- the value was clamped, not refused",
				name, field, v)
		}
	}
}

// TestEveryPageKeyIsAccepted.
//
// SETTING_KEYS is derived from the live page table, so a page added means a new
// `page*` boolean. A hand-copied list would quietly stop accepting the new one,
// and the page would then be unturnable-off with no error anywhere.
func TestEveryPageKeyIsAccepted(t *testing.T) {
	c := loadSettingsValidateCorpus(t)
	if len(c.SettingKeys) == 0 {
		t.Fatal("the corpus records no page keys")
	}
	var missing []string
	for _, k := range c.SettingKeys {
		got, _ := SettingsUpdate(map[string]any{k: true})
		if v, ok := got[k]; !ok || v != true {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("%d of %d page keys are not accepted: %v",
			len(missing), len(c.SettingKeys), missing)
	}
}
