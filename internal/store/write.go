package store

// Writing routers.json the way Node reads it.
//
// ── THE TYPED STRUCT IS NOT WHAT GETS WRITTEN, AND THAT IS THE POINT ────────
//
// `Router` models 16 fields. The real routers.json carries 23, and the seven it
// does not model are not decoration:
//
//	collection             which collectors run on this router
//	alertsEnabled          whether it alerts at all
//	pingTarget             what its connectivity probe aims at
//	connDownThresholdSec   how long down means down
//	geo, serial, addedAt
//
// Marshalling the struct back would drop every one of them SILENTLY — a router
// that quietly stops collecting, or stops alerting, after somebody edits its
// label. So this reads the file as RAW JSON, changes only the keys it was asked
// to change, and writes the rest back untouched.
//
// Node has the same requirement and meets it differently: `_writeFile` spreads
// `...r` over an object that came from the file, so it round-trips whatever it
// does not know about. Same property, reached from the other side.
//
// ── ONLY THE EDITED RECORD IS RE-ENCODED ────────────────────────────────────
//
// Records are held as `json.RawMessage` and written back byte-for-byte unless
// they were the target. Go's map encoding sorts keys, so re-encoding everything
// would reorder every field of every router — a whole-file diff for a one-field
// edit, in a file an operator reads and backs up. This limits that churn to the
// one record that actually changed.
//
// ── ATOMIC, AND OWNER-ONLY ──────────────────────────────────────────────────
//
// Written to `.tmp` and renamed, at mode 0600, matching Node. The file holds
// encrypted credentials; a half-written routers.json is a fleet that will not
// load.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// RemoveRouter deletes one router record, reporting whether it existed.
//
// ── RAW RECORDS, FOR THE SAME REASON `AddRouter` USES THEM ──────────────────
//
// The survivors are carried as `json.RawMessage` and never decoded, so a field
// this port's `Router` struct does not model — `pingTarget`, `alertsEnabled`,
// `connDownThresholdSec`, `addedAt` — survives on every OTHER router. Decoding
// the fleet to filter one out and re-marshalling would strip those four from all
// of them, which is a data loss with no symptom until somebody looks.
//
// NOT AN ERROR WHEN ABSENT. The live `remove` returns false, and the route turns
// that into a 404 — the caller believed it had a router, and saying so is more
// useful than an error that reads like a failure to write.
func (s *Store) RemoveRouter(id string) (bool, error) {
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

	kept := make([]json.RawMessage, 0, len(records))
	found := false
	for _, rec := range records {
		var probe struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec, &probe); err != nil {
			// A record whose id cannot be read is KEPT, matching UpdateRouter: it
			// is somebody's router and the failure is ours. Dropping it would turn
			// one unreadable field into a deleted device.
			kept = append(kept, rec)
			continue
		}
		if probe.ID == id {
			found = true
			continue
		}
		kept = append(kept, rec)
	}
	if !found {
		return false, nil
	}
	if err := s.writeRouters(kept); err != nil {
		return false, err
	}
	return true, nil
}

// UpdateRouter applies a shallow patch to one router record.
//
// `backup` is merged ONE LEVEL DEEP, so patching `backup.enabled` does not drop
// `backup.password` — which would leave a router scheduled for backups it can no
// longer encrypt.
//
// A nil value in the patch DELETES the key, which is how a field is cleared. A
// patch naming no known router is an error rather than a silent no-op: the
// caller believed it had a router.
func (s *Store) UpdateRouter(id string, patch map[string]any) error {
	if id == "" {
		return fmt.Errorf("store: no router id")
	}
	path := filepath.Join(s.Dir, "routers.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var records []json.RawMessage
	if err := json.Unmarshal(raw, &records); err != nil {
		return fmt.Errorf("store: routers.json: %w", err)
	}

	found := -1
	for i, rec := range records {
		var probe struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec, &probe); err != nil {
			// A record this cannot even read the id of is left alone rather than
			// dropped — it is somebody's router, and the failure is ours.
			continue
		}
		if probe.ID == id {
			found = i
			break
		}
	}
	if found < 0 {
		return fmt.Errorf("store: no router %s", id)
	}

	var rec map[string]any
	if err := json.Unmarshal(records[found], &rec); err != nil {
		return fmt.Errorf("store: router %s: %w", id, err)
	}
	// ── TYPED HERE, NOT ONLY AT THE ROUTE ─────────────────────────────────
	//
	// `Routers()` decodes routers.json in ONE Unmarshal into `[]Router`, so a
	// single string where a bool belongs does not spoil one record — it returns
	// ZERO. Measured 2026-08-29: `{"disabled":"false"}` reached disk and the
	// whole fleet became unreadable.
	//
	// That was fixed at the HTTP route, which is where the untrusted value comes
	// from. This is the same call one layer down, and it is here because the
	// invariant "routers.json only ever holds the right types" was otherwise
	// enforced by convention across SIX call sites — the shape that fails. The
	// other five hand-construct their patches from Go values and are correct
	// today; nothing made them stay correct, or made a seventh caller safe.
	//
	// IDEMPOTENT, so the route coercing first costs nothing: a Go `true` through
	// `jsIsTrue` is `true`, an `int` through `jsInt` is itself. The route still
	// coerces because its active-router guard reads the value BEFORE this runs.
	patch = CoerceRouterPatch(patch)
	applyPatch(rec, patch)
	normalizeSiteMirror(rec, patch)

	encoded, err := encodeRecord(rec)
	if err != nil {
		return err
	}
	records[found] = encoded

	out, err := encodeDataFile(records)
	if err != nil {
		return err
	}
	// NO TRAILING NEWLINE: `JSON.stringify` does not write one and neither does
	// the file on disk. See internal/store/jsonwrite.go.
	return writeAtomic(path, out)
}

// applyPatch merges `patch` into `rec`, one level deep for `backup`.
func applyPatch(rec, patch map[string]any) {
	for k, v := range patch {
		if k == "backup" {
			if sub, ok := v.(map[string]any); ok {
				existing, _ := rec["backup"].(map[string]any)
				if existing == nil {
					existing = map[string]any{}
				}
				for sk, sv := range sub {
					if sv == nil {
						delete(existing, sk)
					} else {
						existing[sk] = sv
					}
				}
				rec["backup"] = existing
				continue
			}
		}
		if v == nil {
			delete(rec, k)
			continue
		}
		rec[k] = v
	}
}

// writeAtomic writes through a temp file in the SAME DIRECTORY, then renames.
//
// Same directory because rename is only atomic within one filesystem, and /data
// is a mount — a temp file in /tmp would make this a copy, which is exactly the
// non-atomic write it exists to avoid.
func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		// Leave nothing behind for the next run to trip over.
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// SetRouterPassword changes a router's credential, sealing it the way Node does.
//
// SEPARATE FROM UpdateRouter because the value must never arrive as plaintext in
// a patch: a caller assembling `map[string]any{"password": ...}` by hand would
// write the plaintext straight into routers.json, and it would look like it
// worked.
func (s *Store) SetRouterPassword(id, plaintext string) error {
	sealed, err := s.Encrypt(plaintext)
	if err != nil {
		return err
	}
	return s.UpdateRouter(id, map[string]any{"password": sealed})
}

// SetBackupPassword does the same for the backup block's credential, which
// encrypts the `.backup` binary and is a credential in its own right.
func (s *Store) SetBackupPassword(id, plaintext string) error {
	sealed, err := s.Encrypt(plaintext)
	if err != nil {
		return err
	}
	return s.UpdateRouter(id, map[string]any{"backup": map[string]any{"password": sealed}})
}

// normalizeSiteMirror keeps `siteId` equal to `siteIds[0]`, or null.
//
// ── THE MIRROR IS FOR A DOWNGRADE, WHICH IS WHY IT LOOKS DEAD ───────────────
//
// Since #117 a device holds a LIST and `siteId` survives as a scalar mirror of
// its first entry. Every reader on both sides prefers the list — `RouterSiteIDs`
// here, `_normalizeSites` there — so a stale scalar changes nothing anyone can
// see today. It matters on a DOWNGRADE: a pre-#117 build reads the scalar and
// nothing else, so a device whose membership changed under the new build would
// show up in a site it had left.
//
// The live `Routers.update` recomputes it on every write:
//
//	siteIds: _updSiteIds(data, existing),
//	siteId:  _updSiteIds(data, existing)[0] || null,
//
// This port merged the patch and left the mirror alone, so
// `PUT /api/sites/:id/routers` — which writes `siteIds` and nothing else —
// produced a file Node would never have written.
//
// ── AND IT ONLY RUNS WHEN THE PATCH MENTIONS MEMBERSHIP ─────────────────────
//
// `_updSiteIds` falls back to the existing record, so the live function does
// recompute on every update. Reproducing that literally would make a RENAME add
// a `siteIds: []` and a `siteId: null` to a device that has never had either —
// fields Node's own writer only adds because its record is rebuilt from a
// spread. Here the record is the decoded JSON, so writing them would be a
// visible change to a file this port is only meant to patch. The recompute is
// therefore gated on the patch actually carrying one of the two.
func normalizeSiteMirror(rec, patch map[string]any) {
	_, hasList := patch["siteIds"]
	_, hasScalar := patch["siteId"]
	if !hasList && !hasScalar {
		return
	}
	// `_updSiteIds`: the LIST wins when sent, then the scalar, then whatever the
	// record already had.
	var raw any
	switch {
	case hasList:
		raw = patch["siteIds"]
	case hasScalar:
		raw = patch["siteId"]
	default:
		raw = rec["siteIds"]
	}
	ids := cleanSiteIDs(raw)
	rec["siteIds"] = ids
	if len(ids) > 0 {
		rec["siteId"] = ids[0]
	} else {
		// NULL, not absent and not "": a device detached from every site must
		// not read as belonging to one, and removing the key would make an
		// older reader fall back to whatever it last cached.
		rec["siteId"] = nil
	}
}

// ClearSite detaches every device from one site, and reports how many changed.
//
// ── ONE PASS AND ONE WRITE, NOT N CALLS TO UpdateRouter ─────────────────────
//
// Sites live in SQLite and devices in `routers.json`, so there is no foreign key
// to cascade through and something has to walk the file. Doing it through
// `UpdateRouter` would re-read, re-decode and re-write the whole file once per
// device, and a failure halfway would leave the fleet half-detached with the
// site already gone.
//
// ── THIS SITE IS REMOVED, NOT THE FIELD NULLED ──────────────────────────────
//
// Since #117 a device can hold several memberships, and detaching it from a
// deleted site must not take its others with it. That is the same rule as
// `routers.SiteMembership`'s removal branch, and it is not shared with it on
// purpose: that package is pure and decides what a SAVE changes, where this is a
// cascade with no decision in it — every device carrying the id loses it, and
// there is no "wanted" set to consult.
func (s *Store) ClearSite(siteID string) (int, error) {
	if siteID == "" {
		// The live `clearSite` returns 0 rather than walking the fleet. An empty
		// id matches no membership, so the only thing a walk could do is cost a
		// file read -- or, if the filter were ever loosened, detach everything.
		return 0, nil
	}
	path := filepath.Join(s.Dir, "routers.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var records []json.RawMessage
	if err := json.Unmarshal(raw, &records); err != nil {
		return 0, fmt.Errorf("store: routers.json: %w", err)
	}

	changed := 0
	for i, rec := range records {
		var probe map[string]any
		if err := json.Unmarshal(rec, &probe); err != nil {
			// KEPT AS IT WAS, matching RemoveRouter and UpdateRouter: it is
			// somebody's device and the failure is ours.
			continue
		}
		// The array wins outright when present, even when empty -- the same rule
		// as `RouterSiteIDs`, reached here through the raw record because this
		// walks decoded JSON rather than typed values.
		var ids []string
		if list, ok := probe["siteIds"]; ok && list != nil {
			ids = cleanSiteIDs(list)
		} else {
			ids = cleanSiteIDs(probe["siteId"])
		}
		kept := make([]string, 0, len(ids))
		for _, id := range ids {
			if id != siteID {
				kept = append(kept, id)
			}
		}
		if len(kept) == len(ids) {
			continue
		}
		probe["siteIds"] = kept
		if len(kept) > 0 {
			probe["siteId"] = kept[0]
		} else {
			probe["siteId"] = nil
		}
		encoded, err := json.Marshal(probe)
		if err != nil {
			return 0, err
		}
		records[i] = encoded
		changed++
	}
	if changed == 0 {
		// NOT WRITTEN AT ALL when nothing moved, matching the live guard
		// (`if (changed) { _cache = routers; _writeFile(routers); }`). Deleting a
		// site nobody is in leaves routers.json untouched -- which matters during
		// coexistence, where the Node process holds the same file and every
		// rewrite is a chance for the two to interleave for no gain. Nothing
		// watches the file for changes; checked rather than assumed.
		return 0, nil
	}
	if err := s.writeRouters(records); err != nil {
		return 0, err
	}
	return changed, nil
}
