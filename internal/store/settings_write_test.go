package store

// The write path's rules.
//
// UNLIKE THE REST OF THIS PORT these expectations are NOT generated from the
// live implementation — the handler is inline in index.js and cannot be loaded.
// They are read from the source and written by hand, which is a weaker gate, and
// the tables they range over ARE generated so the part most likely to drift is
// still pinned.

import (
	"strings"
	"testing"
)

// TestAMaskedCredentialIsNeverWritten is the one that protects a working
// install. The form renders a configured credential as the mask and submits it
// back unchanged; without this test, every save would replace the real token
// with eight bullets, the channel would stop working, and the page would still
// show it as configured.
func TestAMaskedCredentialIsNeverWritten(t *testing.T) {
	up, _ := SettingsUpdate(map[string]any{
		"telegramBotToken": Mask,
		"smtpPass":         Mask,
		"ntfyToken":        "NOT-A-REAL-NEW-TOKEN",
	})
	if _, present := up["telegramBotToken"]; present {
		t.Error("a masked telegramBotToken reached the updates — saving the form " +
			"would overwrite the real token with the mask")
	}
	if _, present := up["smtpPass"]; present {
		t.Error("a masked smtpPass reached the updates")
	}
	if up["ntfyToken"] != "NOT-A-REAL-NEW-TOKEN" {
		t.Errorf("a genuinely changed credential was dropped: %#v", up["ntfyToken"])
	}
	// AN EMPTY STRING IS A DELIBERATE CLEAR and must go through — it is how an
	// operator removes a credential.
	up, _ = SettingsUpdate(map[string]any{"ntfyToken": ""})
	if v, present := up["ntfyToken"]; !present || v != "" {
		t.Errorf("clearing a credential was refused: %#v present=%v", v, present)
	}
}

// TestTheRouterPasswordIsNotSettableHere — `credFields` in the live handler
// omits routerPass, so this endpoint cannot change it. Easy to "fix" by adding
// it to the list, which would give the settings form a second, unaudited path to
// the router credential.
func TestTheRouterPasswordIsNotSettableHere(t *testing.T) {
	for _, f := range wtables.CredFields {
		if f == "routerPass" {
			t.Fatal("routerPass is in credFields; the live handler omits it")
		}
	}
	up, _ := SettingsUpdate(map[string]any{"routerPass": "NOT-A-REAL-PASSWORD"})
	if _, present := up["routerPass"]; present {
		t.Error("routerPass reached the updates from POST /api/settings")
	}
}

// TestAnOutOfRangeIntegerIsIgnoredNotClamped — clamping would let a hand-crafted
// request move a setting to the edge of its range while looking refused.
func TestAnOutOfRangeIntegerIsIgnoredNotClamped(t *testing.T) {
	lo, hi := wtables.IntFields["pollConns"][0], wtables.IntFields["pollConns"][1]

	up, _ := SettingsUpdate(map[string]any{"pollConns": lo - 1})
	if v, present := up["pollConns"]; present {
		t.Errorf("a below-range value was accepted as %#v; it must be ignored", v)
	}
	up, _ = SettingsUpdate(map[string]any{"pollConns": hi + 1})
	if v, present := up["pollConns"]; present {
		t.Errorf("an above-range value was accepted as %#v", v)
	}
	up, _ = SettingsUpdate(map[string]any{"pollConns": lo})
	if up["pollConns"] != lo {
		t.Errorf("the floor itself was refused: %#v", up["pollConns"])
	}
	// THE REAL SHAPE: a body decoded by encoding/json holds float64, not int.
	// Testing only with Go ints would have left the production path unexercised.
	up, _ = SettingsUpdate(map[string]any{"pollConns": float64(lo)})
	if up["pollConns"] != lo {
		t.Errorf("a float64 floor (the shape json.Unmarshal produces) was refused: %#v",
			up["pollConns"])
	}
	up, _ = SettingsUpdate(map[string]any{"pollConns": float64(hi + 1)})
	if _, present := up["pollConns"]; present {
		t.Error("an above-range float64 was accepted")
	}
	up, _ = SettingsUpdate(map[string]any{"pollConns": "not a number"})
	if _, present := up["pollConns"]; present {
		t.Error("an unparseable integer was accepted")
	}
	// A NUMERIC STRING IS ACCEPTED, because the original runs everything through
	// parseInt and the form posts strings.
	up, _ = SettingsUpdate(map[string]any{"pollConns": "5000"})
	if up["pollConns"] != 5000 {
		t.Errorf("a numeric string was refused: %#v", up["pollConns"])
	}
}

// TestZeroMeansNeverForTheSessionTimeout — the one range with a hole in it.
func TestZeroMeansNeverForTheSessionTimeout(t *testing.T) {
	// float64 throughout: this is what a decoded JSON body actually carries.
	cases := map[any]bool{
		float64(0): true, float64(3600000): true, float64(86400000): true,
		float64(1): false, float64(3599999): false, float64(86400001): false,
		float64(-1): false, "0": true, "3600000": true, "5": false,
	}
	for in, want := range cases {
		up, _ := SettingsUpdate(map[string]any{"sessionTimeoutMs": in})
		_, present := up["sessionTimeoutMs"]
		if present != want {
			t.Errorf("sessionTimeoutMs=%v accepted=%v, want %v — zero means NEVER "+
				"and must not be clamped to the one-hour minimum", in, present, want)
		}
	}
}

func TestOnlyTrueAndTheStringTrueAreTrue(t *testing.T) {
	cases := map[any]bool{true: true, "true": true, false: false, "false": false,
		"1": false, 1: false, "TRUE": false, "": false}
	for in, want := range cases {
		up, _ := SettingsUpdate(map[string]any{"pingEnabled": in})
		if up["pingEnabled"] != want {
			t.Errorf("pingEnabled=%#v gave %#v, want %v", in, up["pingEnabled"], want)
		}
	}
}

func TestAuthModeIsAWhitelist(t *testing.T) {
	for _, ok := range []string{"none", "modern"} {
		up, _ := SettingsUpdate(map[string]any{"authMode": ok})
		if up["authMode"] != ok {
			t.Errorf("%q was refused", ok)
		}
	}
	for _, bad := range []string{"", "None", "MODERN", "ldap", "true"} {
		up, _ := SettingsUpdate(map[string]any{"authMode": bad})
		if _, present := up["authMode"]; present {
			t.Errorf("%q was accepted as an auth mode", bad)
		}
	}
}

func TestDisplayTimezoneIsValidatedAndClearable(t *testing.T) {
	up, _ := SettingsUpdate(map[string]any{"displayTimezone": "Europe/Berlin"})
	if up["displayTimezone"] != "Europe/Berlin" {
		t.Errorf("a real zone was refused: %#v", up["displayTimezone"])
	}
	// EXPLICITLY CLEARABLE — an empty string means "use the browser's".
	up, _ = SettingsUpdate(map[string]any{"displayTimezone": "  "})
	if v, present := up["displayTimezone"]; !present || v != "" {
		t.Errorf("clearing the timezone was refused: %#v present=%v", v, present)
	}
	up, _ = SettingsUpdate(map[string]any{"displayTimezone": "Mars/Olympus_Mons"})
	if _, present := up["displayTimezone"]; present {
		t.Error("an unknown zone was accepted")
	}
}

func TestCustomPollProfileMustBeJSONOrEmpty(t *testing.T) {
	ok := []string{"", "{}", `{"pollConns":2000}`, "[]", "null"}
	for _, v := range ok {
		up, _ := SettingsUpdate(map[string]any{"customPollProfile": v})
		if _, present := up["customPollProfile"]; !present {
			t.Errorf("%q was refused; `typeof JSON.parse(v) === 'object'` accepts it "+
				"— arrays and null included, which is reproduced rather than tightened", v)
		}
	}
	for _, v := range []string{"{", "not json", "42", `"a string"`, "true"} {
		up, _ := SettingsUpdate(map[string]any{"customPollProfile": v})
		if _, present := up["customPollProfile"]; present {
			t.Errorf("%q was accepted", v)
		}
	}
}

// TestStringsAreTrimmedAndCredentialsAreNot — a token with a trailing space is
// one the operator pasted, and trimming it produces an authentication failure
// nothing on the page explains.
func TestStringsAreTrimmedAndCredentialsAreNot(t *testing.T) {
	up, _ := SettingsUpdate(map[string]any{
		"pingTarget": "  198.51.100.9  ",
		"ntfyToken":  "  NOT-A-REAL-TOKEN  ",
	})
	if up["pingTarget"] != "198.51.100.9" {
		t.Errorf("a string field was not trimmed: %#v", up["pingTarget"])
	}
	if up["ntfyToken"] != "  NOT-A-REAL-TOKEN  " {
		t.Errorf("a credential was trimmed: %#v", up["ntfyToken"])
	}
}

// TestTheLengthLimitCountsUTF16Units — JavaScript's slice(0, n) counts UTF-16
// code units. Counting bytes would cut a template of accented text a third of
// the way short and could split a rune, writing invalid UTF-8 into the file.
func TestTheLengthLimitCountsUTF16Units(t *testing.T) {
	// 300 two-byte runes: 300 UTF-16 units, 600 bytes.
	in := strings.Repeat("é", 300)
	up, _ := SettingsUpdate(map[string]any{"pingTarget": in})
	got := up["pingTarget"].(string)
	if n := len([]rune(got)); n != 256 {
		t.Errorf("kept %d runes, want 256 — a byte limit would have kept 128", n)
	}
	// And an astral character is a SURROGATE PAIR: two units, not one.
	up, _ = SettingsUpdate(map[string]any{"pingTarget": strings.Repeat("😀", 200)})
	got = up["pingTarget"].(string)
	if n := len([]rune(got)); n != 128 {
		t.Errorf("kept %d emoji, want 128 — each is two UTF-16 units", n)
	}
}

func TestResetShortCircuits(t *testing.T) {
	up, reset := SettingsUpdate(map[string]any{"_reset": true, "pollConns": 5000})
	if !reset {
		t.Fatal("_reset was not reported")
	}
	if len(up) != 0 {
		t.Errorf("the reset branch also collected updates: %v — the live handler "+
			"returns before examining the rest of the body", up)
	}
}

// TestTheTablesCoverTheRealSurface — a floor, so a generator that captured
// almost nothing fails here rather than silently accepting almost nothing.
func TestTheTablesCoverTheRealSurface(t *testing.T) {
	if n := len(wtables.IntFields); n < 30 {
		t.Errorf("only %d integer ranges", n)
	}
	if n := len(wtables.BoolFields); n < 40 {
		t.Errorf("only %d boolean fields — the page toggles alone are ~24", n)
	}
	if len(wtables.CredFields) != 5 || len(wtables.StrFields) < 5 {
		t.Errorf("cred=%d str=%d", len(wtables.CredFields), len(wtables.StrFields))
	}
}

// TestEverySpecialCaseIsActuallyHandled closes the loop the generator opens.
//
// `tools/settings-write-tables.js` refuses to write its file if the handler
// names a setting that is in neither a table nor its SPECIAL_CASES list. That
// stops a new one being dropped silently — but it says nothing about whether
// THIS side implements the ones already listed. A key could sit in that list,
// satisfy the generator, and be ignored here.
//
// So: every listed special case must be accepted from a body that sets it to a
// value the live handler accepts. A case that reached the updates for none of
// its probes is not handled.
func TestEverySpecialCaseIsActuallyHandled(t *testing.T) {
	if len(wtables.SpecialCases) == 0 {
		t.Fatal("no special cases recorded; the generated table is not being read")
	}

	// One acceptable value per case, from the live handler's own rules.
	accepted := map[string][]any{
		"authMode":          {"modern", "none"},
		"sessionTimeoutMs":  {float64(0), float64(3600000)},
		"notifBody":         {"a body"},
		"notifBodyUp":       {"a recovery body"},
		"customPollProfile": {"", `{"pollConns":2000}`},
		"displayTimezone":   {"", "Europe/Berlin"},
	}

	for _, key := range wtables.SpecialCases {
		probes, ok := accepted[key]
		if !ok {
			t.Errorf("%q is a special case and this test has no probe for it — add one, "+
				"or the port could be ignoring it entirely", key)
			continue
		}
		handled := false
		for _, v := range probes {
			up, _ := SettingsUpdate(map[string]any{key: v})
			if _, present := up[key]; present {
				handled = true
			}
		}
		if !handled {
			t.Errorf("%q reached the updates for none of %v — the generator lists it as "+
				"handled outside the tables, and this side does not handle it", key, probes)
		}
	}
}
