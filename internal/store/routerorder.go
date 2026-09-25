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
