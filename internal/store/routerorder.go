package store

// The order routers are listed in, which is the order routers.json holds them.
//
// ── THE ARRAY IS THE ORDER, AND IT ALWAYS WAS ──────────────────────────────
//
// `PublicRouters` reads the file and returns the records in file order, the
// picker renders them in that order, and the browser opened on `routers[0]`.
// So the order was already load-bearing and already persistent; the only thing
// missing was a way to CHOOSE it. There is no `order` field and there must not
// be: a number beside each record is a second statement of the same fact, and
// the two disagree the moment one write lands without the other.
//
// ── WHY THE RECORDS ARE NEVER DECODED ──────────────────────────────────────
//
// They are moved as `json.RawMessage`, the idiom `appendRouter` and
// `UpdateRouter` already use, so a field this port's struct does not model
// survives a reorder byte for byte. A move must not be able to lose a setting.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MoveRouter moves one router one place towards the front or the back, and
// reports where it ended up.
//
// AT THE END IT IS A NO-OP, not an error: the arrows are always drawn, and an
// operator clicking Up on the top row has made no mistake worth a message.
// The boolean says whether anything moved, so a caller can skip the write and
// the audit entry rather than recording a move that did not happen.
func (s *Store) MoveRouter(id string, up bool) (moved bool, err error) {
	if id == "" {
		return false, fmt.Errorf("store: no router id")
	}
	path := filepath.Join(s.Dir, "routers.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var records []json.RawMessage
	if err := json.Unmarshal(raw, &records); err != nil {
		return false, fmt.Errorf("store: routers.json: %w", err)
	}

	found := -1
	for i, rec := range records {
		var probe struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec, &probe); err != nil {
			// A record whose id cannot be read is left where it is, for the
			// reason `UpdateRouter` gives: it is somebody's router, and the
			// failure to parse it is ours.
			continue
		}
		if probe.ID == id {
			found = i
			break
		}
	}
	if found < 0 {
		return false, fmt.Errorf("store: no router %s", id)
	}

	swap := found + 1
	if up {
		swap = found - 1
	}
	if swap < 0 || swap >= len(records) {
		return false, nil
	}
	records[found], records[swap] = records[swap], records[found]
	if err := s.writeRouters(records); err != nil {
		return false, err
	}
	return true, nil
}

// AdoptPrimaryRouter names the first router as primary when nothing is, and
// reports whether it wrote.
//
// ── WHY AN INSTALL CAN HAVE NO PRIMARY ─────────────────────────────────────
//
// `activeRouterId` is written by the activate route, and an install where
// nobody ever pressed Activate has never had one. That was invisible while the
// browser opened on `routers[0]` regardless; now that the primary decides the
// landing router, an unset one would send every session to the fallback and
// make the new checkbox look like it does nothing.
//
// SO THE MIGRATION IS EXACTLY THE OLD BEHAVIOUR, WRITTEN DOWN: the router at
// the top of the list becomes the primary, which is the router those installs
// were already getting.
//
// It also repairs a primary naming a router that has since been DELETED, which
// is the same state by a different route and has the same remedy.
//
// IDEMPOTENT: it writes only when there is no usable primary, so a second start
// changes nothing. Run from `cmd/mikrodash` rather than `Open`, for the reason
// `MigrateReportingDefaults` records - `cmd/compat` opens a real /data
// read-only.
func (s *Store) AdoptPrimaryRouter() (id string, wrote bool, err error) {
	routers, _ := s.Routers()
	if len(routers) == 0 {
		return "", false, nil
	}
	current := s.ActiveRouterID()
	for _, r := range routers {
		if r.ID == current {
			return current, false, nil
		}
	}

	first := routers[0].ID
	cfg, err := s.Settings()
	if err != nil {
		return "", false, err
	}
	merged, kept := Merge(cfg, os.LookupEnv, s)
	merged["activeRouterId"] = first
	if err := SaveSettings(s.Dir, merged, Settings{"activeRouterId": first}, kept, s); err != nil {
		return "", false, err
	}
	return first, true, nil
}
