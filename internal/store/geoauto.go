package store

// The automatic location, learned from a router's public address.
//
// Its own entry point for the reason identity.go gives: a background refresh
// must not look like an edit. It takes a plain map rather than a geoplace type
// so this package stays free of that one; `geoplace.AutoGeoAction` builds it.

import (
	"encoding/json"
)

// UpdateGeoAuto stores `auto` as the router's `geo.auto`, or removes it when
// `auto` is nil. `place` is never touched: a town an operator picked outranks
// anything learned.
//
// Returns whether anything was written. A fix at the same address and the same
// coordinates is not news, so only its timestamp would change, and that is not
// worth rewriting routers.json or telling every viewer.
func (s *Store) UpdateGeoAuto(id string, auto map[string]any) (bool, error) {
	if id == "" {
		return false, nil
	}
	all, _ := s.Routers()
	var current *Router
	for i := range all {
		if all[i].ID == id {
			current = &all[i]
			break
		}
	}
	if current == nil {
		// Deleted while its session was mid-read: ordinary, not an error.
		return false, nil
	}
	var geo map[string]any
	if len(current.Geo) > 0 {
		_ = json.Unmarshal(current.Geo, &geo)
	}
	old, has := geo["auto"].(map[string]any)

	if auto == nil {
		if !has {
			return false, nil
		}
	} else if has && old["ip"] == auto["ip"] && old["lat"] == auto["lat"] && old["lon"] == auto["lon"] {
		return false, nil
	}

	// A nil `auto` inside the patch deletes the key; see applyPatch.
	var v any
	if auto != nil {
		v = auto
	}
	if err := s.UpdateRouter(id, map[string]any{"geo": map[string]any{"auto": v}}); err != nil {
		return false, err
	}
	return true, nil
}
