package store

// `hiddenAreas`: the generated pages an operator has switched off.
//
// ── ONE LIST, NOT A KEY PER AREA ────────────────────────────────────────────
//
// Every hand-built page has its own `pageX` boolean, and that is right for a
// fixed set of twenty-six. An area is a declaration, and there may be forty of
// them: forty keys, forty entries in the defaults, forty re-aims of the settings
// corpora and forty rows in the Visible Pages grid. The list says the same thing
// once, and an area added tomorrow needs no settings work at all.
//
// A MISSING KEY MEANS NOTHING IS HIDDEN, which is the right default: a page the
// operator has never heard of should appear, not be silently absent.

import (
	"sort"

	"mikrodash/internal/areas"
)

// CleanHiddenAreas normalises what a browser submitted: strings only, each one a
// declared area, deduplicated and sorted.
//
// Filtering against the declarations is what stops a stale key hiding nothing
// for ever, and what stops an unknown one reading as a setting that works.
func CleanHiddenAreas(raw any) []string {
	known := map[string]bool{}
	for _, k := range areas.Keys() {
		known[k] = true
	}
	seen := map[string]bool{}
	out := []string{}
	add := func(v any) {
		s, ok := v.(string)
		if !ok || !known[s] || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			add(item)
		}
	case []string:
		for _, item := range v {
			add(item)
		}
	}
	sort.Strings(out)
	return out
}

// HiddenAreas reads the stored list.
func HiddenAreas(s Settings) []string {
	if s == nil {
		return nil
	}
	return CleanHiddenAreas(s["hiddenAreas"])
}

// AreaVisible reports whether an area is offered at all on this install.
func AreaVisible(s Settings, key string) bool {
	for _, k := range HiddenAreas(s) {
		if k == key {
			return false
		}
	}
	return true
}
