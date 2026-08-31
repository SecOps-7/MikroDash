package store

import (
	_ "embed"
	"encoding/json"
	"sync"
)

// The `settings:pages` payload: what every browser learns about which pages it
// may draw and which notification toggles are on.
//
// ── THE KEY LIST IS DERIVED UPSTREAM, SO IT IS NOT WRITTEN HERE ─────────────
//
// `_PAGE_SETTING_KEYS` is one key per page from the page table, five named keys,
// `userNotifyEnabled`, and every `notif*` DEFAULT that holds a BOOLEAN. It
// therefore grows from two different files, and a hand-copied list here would
// stop carrying a new key silently. That failure is not a crash: the browser
// gets the payload without the key, `applyPageVisibility` reads it as absent,
// and a page the operator can enable in the form stays hidden with nothing
// logged.
//
// So the list is EMBEDDED from `pagekeys.json`, which `tools/settings-pages-cases.js`
// lifts out of the live source.
//
// ── THE BOOLEAN FILTER IS A DISCLOSURE BOUNDARY, NOT A TIDINESS RULE ────────
//
// `notifTitle`, `notifBody` and `notifCooldownSec` all match `/^notif/` and are
// excluded because they are not booleans. Filtering by name would broadcast the
// operator's notification message text to every connected browser.

//go:embed pagekeys.json
var pageKeysJSON []byte

var (
	pageKeysOnce sync.Once
	pageKeys     []string
)

// PageSettingKeys is the projection's whitelist, in the live list's order.
func PageSettingKeys() []string {
	pageKeysOnce.Do(func() {
		if err := json.Unmarshal(pageKeysJSON, &pageKeys); err != nil {
			panic("store: pagekeys.json: " + err.Error())
		}
		// UNKILLABLE, and recorded rather than counted. `pagekeys.json` is
		// embedded at compile time, so no test can hand this an empty list —
		// a mutation deleting this guard survives the suite. It stays because
		// the failure it catches is the silent one: an empty list makes every
		// payload empty, and the browser then hides every page while the
		// settings form still shows them enabled.
		if len(pageKeys) == 0 {
			panic("store: the page settings key list is empty")
		}
	})
	return pageKeys
}

// PageSettings projects the settings onto what the browser is told.
//
// ── AN ABSENT KEY IS OMITTED; AN EXPLICIT NULL IS KEPT ──────────────────────
//
// The live projection is `out[k] = src[k]`, which writes the key whatever the
// source holds — so a key the file does not have is PRESENT in the object with
// the value `undefined`, and `JSON.stringify` then drops it. What reaches the
// browser omits it.
//
// A key holding null is different: JSON keeps that. So the two cases have to be
// distinguished, and in Go that means the two-value map lookup — `src[k]` alone
// yields the same nil for both, and emitting null for an absent key would send
// something JavaScript never sends, breaking `pages[k] === undefined` on the
// client.
//
// This is the SECOND place in the port where Go's one-value lookup conflates
// JavaScript's `undefined` and `null`; the first was `collection.PollRetunes`,
// where it silently sped up a collector whose interval had never been written.
//
// FALSY VALUES SURVIVE. `false`, `0` and `""` are the "off" states, and a
// projection using `||` or skipping empty values would drop exactly the settings
// an operator turned off.
func PageSettings(src Settings) Settings {
	out := make(Settings, len(PageSettingKeys()))
	for _, k := range PageSettingKeys() {
		if v, ok := src[k]; ok {
			out[k] = v
		}
	}
	return out
}
