package store

// `PageSettings` against the LIVE `_pageSettings`, whose key list and projection
// are both lifted by the settings-pages corpus.

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type pagesCorpus struct {
	Keys  []string `json:"keys"`
	Cases map[string]struct {
		Src     map[string]any `json:"src"`
		Payload map[string]any `json:"payload"`
	} `json:"cases"`
}

func loadPagesCorpus(t *testing.T) pagesCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/settings-pages-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c pagesCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 || len(c.Keys) == 0 {
		t.Fatal("corpus is empty -- these tests would pass against nothing")
	}
	return c
}

func TestPageSettingsMatchesLive(t *testing.T) {
	c := loadPagesCorpus(t)

	for name, tc := range c.Cases {
		t.Run(name, func(t *testing.T) {
			// Through JSON, because that is what reaches the browser — and it is
			// the step that drops JavaScript's undefined values. Comparing the Go
			// map directly would compare something no client ever sees.
			b, err := json.Marshal(PageSettings(tc.Src))
			if err != nil {
				t.Fatal(err)
			}
			var mine map[string]any
			if err := json.Unmarshal(b, &mine); err != nil {
				t.Fatal(err)
			}
			want := tc.Payload
			if want == nil {
				want = map[string]any{}
			}
			if !reflect.DeepEqual(mine, want) {
				t.Errorf("payload differs:\n  got  %s\n  live %s\n  missing %v\n  extra %v",
					b, mustJSON(want), diffKeys(want, mine), diffKeys(mine, want))
			}
		})
	}
}

// TestTheEmbeddedKeyListIsTheLiveOne.
//
// The list grows from two different files upstream — the page table and the
// notification defaults — so an embed that has drifted is the whole risk here.
// The generator writes `pagekeys.json` itself and its `--check` compares it, but
// that only runs when someone runs the generator; this fails the Go suite.
func TestTheEmbeddedKeyListIsTheLiveOne(t *testing.T) {
	c := loadPagesCorpus(t)
	if !reflect.DeepEqual(PageSettingKeys(), c.Keys) {
		t.Errorf("the embedded key list differs from the lifted one:\n  missing %v\n  extra %v",
			diffSlice(c.Keys, PageSettingKeys()), diffSlice(PageSettingKeys(), c.Keys))
	}
}

// TestNoNotificationTextIsBroadcast.
//
// The `/^notif/` filter takes BOOLEANS ONLY, and that is a disclosure boundary
// rather than a tidiness rule: `notifTitle`, `notifBody` and `notifBodyUp` carry
// the operator's own message text, and this payload goes to every connected
// browser.
//
// Asserted here as well as in the generator because the two fail differently:
// The generator checks the LIVE list, and this checks what THIS port would send
// if the embed were ever replaced by hand.
func TestNoNotificationTextIsBroadcast(t *testing.T) {
	src := Settings{}
	for _, k := range []string{"notifTitle", "notifBody", "notifBodyUp", "notifCooldownSec"} {
		src[k] = "SECRET-" + k
	}
	// ...and one notif BOOLEAN, so a filter that dropped everything would not
	// pass this test by accident.
	src["notifCpu"] = true

	out := PageSettings(src)
	if _, ok := out["notifCpu"]; !ok {
		t.Fatal("notifCpu was dropped -- the filter excludes every notif key, so the " +
			"assertions below would hold for a projection that sends nothing")
	}
	for k, v := range out {
		if s, isStr := v.(string); isStr && strings.HasPrefix(s, "SECRET-") {
			t.Errorf("%s = %q reached the payload -- the filter is by NAME, not by type, "+
				"and this broadcasts the operator's message text", k, s)
		}
	}
}

// TestCredentialsAreNotProjected. The whitelist is what keeps them out; a port
// copying every key that happens to be in the file would send these.
func TestCredentialsAreNotProjected(t *testing.T) {
	src := Settings{
		"routerHost": "198.51.100.1", "smtpPass": "REDACTED",
		"telegramBotToken": "REDACTED", "pageWifi": true,
	}
	out := PageSettings(src)
	if _, ok := out["pageWifi"]; !ok {
		t.Fatal("pageWifi was dropped, so the assertions below prove nothing")
	}
	for _, k := range []string{"routerHost", "smtpPass", "telegramBotToken"} {
		if _, ok := out[k]; ok {
			t.Errorf("%s reached the browser", k)
		}
	}
}

// TestFalsyValuesSurvive.
//
// `false`, `0` and `""` are the OFF states. A projection using `||`, or skipping
// zero values, would drop exactly the settings an operator turned off — and the
// page would come back on after a reload.
func TestFalsyValuesSurvive(t *testing.T) {
	out := PageSettings(Settings{
		"pageWifi": false, "alertCpuThreshold": 0, "displayTimezone": "",
	})
	for k, want := range map[string]any{
		"pageWifi": false, "alertCpuThreshold": 0, "displayTimezone": "",
	} {
		got, ok := out[k]
		if !ok {
			t.Errorf("%s was dropped -- an operator's OFF state would revert on reload", k)
			continue
		}
		if got != want {
			t.Errorf("%s = %#v, want %#v", k, got, want)
		}
	}
}

// TestAbsentIsOmittedAndNullIsKept.
//
// Go's one-value map lookup yields the same nil for both, and JavaScript does
// not: an absent key is `undefined`, which `JSON.stringify` DROPS, while an
// explicit null is sent. Emitting null for an absent key would send something
// the live app never sends, and `pages[k] === undefined` on the client would
// stop being true.
//
// This is the second place in the port where that distinction matters; the first
// was `collection.PollRetunes`.
func TestAbsentIsOmittedAndNullIsKept(t *testing.T) {
	out := PageSettings(Settings{"pageWifi": nil})

	if _, ok := out["pageWifi"]; !ok {
		t.Error("a key holding null was omitted -- null is not undefined, and JSON keeps it")
	}
	if _, ok := out["displayTimezone"]; ok {
		t.Error("a key the source does not have appeared in the payload")
	}

	// And through JSON, which is where the difference is actually observable.
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"pageWifi":null`) {
		t.Errorf("the marshalled payload is %s; want an explicit null for pageWifi", b)
	}
	if strings.Contains(string(b), "displayTimezone") {
		t.Errorf("the marshalled payload is %s; an absent key must not appear", b)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func diffKeys(a, b map[string]any) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func diffSlice(a, b []string) []string {
	have := make(map[string]bool, len(b))
	for _, k := range b {
		have[k] = true
	}
	var out []string
	for _, k := range a {
		if !have[k] {
			out = append(out, k)
		}
	}
	return out
}
