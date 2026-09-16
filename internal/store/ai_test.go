package store

import "testing"

// TestAIReadyNeedsAllThree.
//
// The rule is not "enabled". A page drawn for an operator who switched the
// feature on and has not pasted an endpoint can reach no model, and a dashboard
// card beside it can generate nothing — both render empty with nothing on either
// to say why. Every arm below is a state a real install passes through while the
// tab is being filled in, and only the last two may draw the page.
func TestAIReadyNeedsAllThree(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Settings
		want bool
	}{
		{"nothing set", Settings{}, false},
		{"disabled, fully configured", Settings{
			"aiEnabled": false, "aiBaseUrl": "http://198.51.100.10:11434/v1", "aiModel": "a-model",
		}, false},
		{"enabled, no endpoint", Settings{
			"aiEnabled": true, "aiBaseUrl": "", "aiModel": "a-model",
		}, false},
		{"enabled, no model", Settings{
			"aiEnabled": true, "aiBaseUrl": "http://198.51.100.10:11434/v1", "aiModel": "",
		}, false},
		// THE KEY IS NOT PART OF THE RULE. A local endpoint usually needs none,
		// and requiring one would make the configuration that sends nothing to a
		// third party the awkward one.
		{"enabled, endpoint and model, no key", Settings{
			"aiEnabled": true, "aiBaseUrl": "http://198.51.100.10:11434/v1", "aiModel": "a-model",
		}, true},
		{"enabled, endpoint and model, with key", Settings{
			"aiEnabled": true, "aiBaseUrl": "https://api.example.invalid/v1",
			"aiModel": "a-model", "aiApiKey": "NOT-A-REAL-KEY",
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AIReady(tc.in); got != tc.want {
				t.Errorf("AIReady = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAIReadyToleratesTheWrongTypes.
//
// settings.json is a file on disk an operator can hand-edit, and `Merge` does
// not type-check it — a string "true" for `aiEnabled` survives the merge. A type
// assertion without the comma-ok form would panic the process on read, which is
// a crash on a corrupt file rather than a feature that stays off.
func TestAIReadyToleratesTheWrongTypes(t *testing.T) {
	for _, in := range []Settings{
		{"aiEnabled": "true", "aiBaseUrl": "http://198.51.100.10:11434/v1", "aiModel": "a-model"},
		{"aiEnabled": 1, "aiBaseUrl": "http://198.51.100.10:11434/v1", "aiModel": "a-model"},
		{"aiEnabled": true, "aiBaseUrl": 42, "aiModel": "a-model"},
		{"aiEnabled": true, "aiBaseUrl": "http://198.51.100.10:11434/v1", "aiModel": nil},
	} {
		if AIReady(in) {
			t.Errorf("AIReady(%#v) = true — a value of the wrong type read as configured", in)
		}
	}
}

// TestWithAIReadyDoesNotMutateItsInput.
//
// Callers hand this the live merged settings, and the save path writes that map.
// A derived key written into it would put a key with NO DEFAULT into
// settings.json, which `Merge` then drops on the next read — the file gains a
// key and loses it again, churn nobody could explain from the screen. Same rule
// as Public(), and pinned for the same reason.
func TestWithAIReadyDoesNotMutateItsInput(t *testing.T) {
	in := Settings{"aiEnabled": true, "aiBaseUrl": "http://198.51.100.10:11434/v1", "aiModel": "a-model"}
	out := WithAIReady(in)

	if _, ok := in[AIReadyKey]; ok {
		t.Errorf("WithAIReady wrote %q into its input — that map is what the save path writes", AIReadyKey)
	}
	if out[AIReadyKey] != true {
		t.Errorf("the copy carries %#v, want true", out[AIReadyKey])
	}
	for k, v := range in {
		if out[k] != v {
			t.Errorf("the copy lost %s: %#v, want %#v", k, out[k], v)
		}
	}
}

// TestTheDerivedKeyIsNotADefault.
//
// `Merge` keeps a stored key only when it is a default or an encrypted field, so
// a derived key must be NEITHER — otherwise a stale `aiReady` hand-written into
// settings.json would survive the merge and override the computed one, and the
// browser would be told the assistant is usable by a line in a file rather than
// by the settings that decide it.
func TestTheDerivedKeyIsNotADefault(t *testing.T) {
	if _, ok := Defaults()[AIReadyKey]; ok {
		t.Errorf("%q has a default — a stored copy would override the derivation", AIReadyKey)
	}
	for _, f := range tables.Encrypted {
		if f == AIReadyKey {
			t.Errorf("%q is an encrypted field — a stored copy would survive Merge", AIReadyKey)
		}
	}

	// AND IT SURVIVES THE PROJECTION. The whole point of the key is that
	// `PageSettings` carries it to the browser; a key missing from
	// `pagekeys.json` is dropped silently, and the page it gates stays hidden
	// with nothing logged.
	out := PageSettings(WithAIReady(Settings{"aiEnabled": true,
		"aiBaseUrl": "http://198.51.100.10:11434/v1", "aiModel": "a-model"}))
	if got, ok := out[AIReadyKey]; !ok || got != true {
		t.Errorf("PageSettings dropped %q (present=%v, value=%#v) — it is not in pagekeys.json",
			AIReadyKey, ok, got)
	}
}

// TestAISettingsRoundTripThroughTheWritePath.
//
// The seven keys are only useful if `POST /api/settings` accepts them, and the
// four tables that decide it are a JSON file — so this asks the validator, not
// the file. `aiApiKey` is the one that matters most: it must be accepted when
// typed and DROPPED when the browser hands back the mask, or every save would
// replace a working key with eight bullet characters.
func TestAISettingsRoundTripThroughTheWritePath(t *testing.T) {
	updates, reset := SettingsUpdate(map[string]any{
		"aiEnabled":     true,
		"aiBaseUrl":     "  http://198.51.100.10:11434/v1  ",
		"aiModel":       "a-model",
		"aiApiKey":      "NOT-A-REAL-KEY",
		"aiTimeoutMs":   float64(45000),
		"aiTlsInsecure": true,
		"aiHeaders":     "  X-Example: one  ",
	})
	if reset {
		t.Fatal("an ordinary body read as a reset")
	}
	for k, want := range map[string]any{
		"aiEnabled": true, "aiBaseUrl": "http://198.51.100.10:11434/v1",
		"aiModel": "a-model", "aiApiKey": "NOT-A-REAL-KEY",
		"aiTimeoutMs": 45000, "aiTlsInsecure": true, "aiHeaders": "X-Example: one",
	} {
		if updates[k] != want {
			t.Errorf("%s = %#v, want %#v", k, updates[k], want)
		}
	}

	// THE MASK IS NOT A VALUE. `IsMasked` drops it, so the stored key survives a
	// save the operator made for some unrelated setting.
	masked, _ := SettingsUpdate(map[string]any{"aiApiKey": Mask})
	if _, ok := masked["aiApiKey"]; ok {
		t.Errorf("the mask was accepted as a key — a save would overwrite the real one with %q", Mask)
	}

	// OUT OF RANGE IS IGNORED, NOT CLAMPED, like every other int field: a
	// hand-crafted request must not be able to move a setting to the edge of its
	// range while looking as though it were refused.
	tooBig, _ := SettingsUpdate(map[string]any{"aiTimeoutMs": float64(999999999)})
	if _, ok := tooBig["aiTimeoutMs"]; ok {
		t.Errorf("an out-of-range timeout was accepted as %#v", tooBig["aiTimeoutMs"])
	}
}
