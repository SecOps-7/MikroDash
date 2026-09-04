package store

// The one-time migration that gives an upgrading install the reporting it
// already had.
//
// ── WHY A MIGRATION AND NOT A DEFAULT ──────────────────────────────────────
//
// Before `reportingEnabled` existed, exactly ONE router recorded history: the
// active one, chosen by `SetHistoryRouter(activeRouterId)`. Nothing else did,
// whatever its settings.
//
// A nil-means-ON default would therefore start recording the WHOLE FLEET the
// moment this shipped — more connections, more command channels and more rows
// than the operator had before, with no action on their part. A nil-means-OFF
// default with no migration would do the opposite: silently stop the recording
// the active router is doing today, and existing reports would just stop
// updating.
//
// So the field defaults OFF and this writes the truth once: on for the router
// that is recording, off for the rest. After it runs, an upgrade has changed
// nothing.
//
// ── IT RUNS FROM cmd/mikrodash, NOT FROM Open ──────────────────────────────
//
// The precedent is `RenamePageGrants`, and the reason is the same: `cmd/compat`
// opens a real `/data` through a READ-ONLY mount, and a migration in `Open`
// would fail there — on a tool whose whole job is to read a production
// directory without touching it.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

// MigrateReportingDefaults writes `reportingEnabled` for every router that has
// no such key, reporting how many it changed.
//
// IDEMPOTENT BY CONSTRUCTION: a record that already carries the key is left
// exactly as it is, including one an operator has since turned off. So this can
// run on every start, and after the first it writes nothing at all.
//
// Records are carried as `json.RawMessage` and decoded ONE AT A TIME into a
// map, never through the `Router` struct — the same rule `appendRouter` and
// `UpdateRouter` follow, because the struct models 16 of the file's 24 fields
// and re-marshalling it would drop the rest.
func (s *Store) MigrateReportingDefaults(activeRouterID string) (int, error) {
	path := filepath.Join(s.Dir, "routers.json")
	raw, missing, err := readIfPresent(path)
	if err != nil {
		return 0, err
	}
	if missing {
		return 0, nil // no fleet yet; nothing to migrate
	}

	var records []json.RawMessage
	if err := json.Unmarshal(raw, &records); err != nil {
		return 0, fmt.Errorf("store: routers.json: %w", err)
	}

	changed := 0
	for i, rec := range records {
		var m map[string]any
		if err := json.Unmarshal(rec, &m); err != nil {
			// A record this cannot read is left alone rather than dropped — it
			// is somebody's router, and the failure is ours. Same posture as
			// `UpdateRouter`'s id probe.
			continue
		}
		if _, ok := m["reportingEnabled"]; ok {
			continue // already answered, by this migration or by an operator
		}
		id, _ := m["id"].(string)
		m["reportingEnabled"] = id != "" && id == activeRouterID
		encoded, err := encodeRecord(m)
		if err != nil {
			return changed, err
		}
		records[i] = encoded
		changed++
	}
	if changed == 0 {
		// NOT REWRITTEN when nothing changed. Rewriting would touch the file's
		// mtime on every start and, more to the point, would put a
		// re-encoded copy of every record on disk for no reason — which is how
		// a formatting difference becomes a diff nobody can explain.
		return 0, nil
	}
	if err := s.writeRouters(records); err != nil {
		return changed, err
	}
	return changed, nil
}

// ActiveRouterID is the router the install is pointed at, or "" when unset or
// unreadable. Read here so the migration's caller does not need the settings
// map's key spelling.
func (s *Store) ActiveRouterID() string {
	cfg, err := s.Settings()
	if err != nil {
		return ""
	}
	id, _ := cfg["activeRouterId"].(string)
	return id
}
