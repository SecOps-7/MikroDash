package collection

// THE #105 ONE-SHOT MIGRATION — `planMigration` in `src/collection.js`.
//
// ── WHAT IT IS FOR ────────────────────────────────────────────────────────
//
// Stream-vs-poll used to be one app-global setting. #105 made it a property of
// each router, and the five global keys were retired. This maps the old value
// onto per-router `collection` blocks so an install that upgrades across that
// change keeps what its operator chose.
//
// The live comment states the failure exactly: "Without this, anyone running
// global Poll would silently revert to Stream on upgrade." Silently is the
// operative word — every page still works, every collector still runs, and the
// router is simply asked for a stream the operator had turned off.
//
// After cutover the Node code holding this is gone, which is why the operator
// decided on 2026-08-30 that the merged app must carry it (LOOP.md 0g).
//
// ── PURE, AND THE CALLER OWNS PERSISTENCE ─────────────────────────────────
//
// Live's is pure for the reason this one is: "returns `[{ id, collection }]` for
// the routers that need writing, so the caller owns persistence and the mapping
// itself is testable". `internal/store/legacymigrate.go` is that caller.

// legacyStreamKeys is the retired app-global set, exactly as
// `LEGACY_STREAM_KEYS` freezes it. The VALUES are the collector keys; the plan
// itself does not use them, and they are kept because the live table keeps them,
// so a reader comparing the two sees one table rather than two half-tables.
//
// ORDER IS THE LIVE ORDER, so the `present` list — and anything derived from it
// — is built the same way on both sides.
var legacyStreamKeys = []struct{ key, collector string }{
	{"streamSystem", "system"},
	{"streamPing", "ping"},
	{"streamConns", "conns"},
	{"streamTalkers", "talkers"},
	{"streamIfrates", "ifStatus"},
}

// MigrationRouter is the subset of a router record the plan reads.
type MigrationRouter struct {
	ID string
	// Collection is the record's own block, already parsed. Nil when absent.
	Collection *Router
}

// MigrationPlan is one router's write.
type MigrationPlan struct {
	ID   string
	Mode string
	// Overrides is nil for the poll branch and carries the polled collectors for
	// the mixed one, matching live's `{ mode, overrides }` versus `{ mode }`.
	Overrides map[string]bool
}

// PlanMigration decides which routers need a `collection` block written.
//
// ── THE FOUR BRANCHES, AND WHY EACH RETURNS WHAT IT DOES ──────────────────
//
//	no legacy keys present  -> nothing. A modern install, or one already migrated.
//	all TRUE                -> nothing. That IS the new default, and writing a
//	                          block would pin every router to a value the operator
//	                          never chose — a retired global becoming permanent
//	                          per-router state.
//	all FALSE               -> mode "poll" for every unpinned router.
//	mixed                   -> mode "stream", recording only the collectors that
//	                          were polled, so the ones that were streaming stay on
//	                          the default rather than being frozen.
//
// A ROUTER THAT ALREADY CARRIES A MODE IS ALWAYS LEFT ALONE. An explicit
// per-router choice must never be overwritten by a global that has been retired.
// Note the test is `collection.mode`, not `collection`: a record with a block
// that has overrides but NO mode is still migrated, because it has expressed no
// opinion about delivery.
//
// ── ONLY BOOLEANS COUNT AS PRESENT ────────────────────────────────────────
//
// Live filters with `typeof cfg[k] === 'boolean'`, so a settings file holding the
// string "false" has no legacy keys at all and migrates NOTHING. That is the
// opposite of the `dccbf62` coercion class — where a stringly boolean must be
// honoured — and it is reproduced rather than harmonised: this decides whether a
// key was ever WRITTEN, not what its value means. Pinned by a corpus case.
func PlanMigration(settings map[string]any, routers []MigrationRouter) []MigrationPlan {
	present := make([]string, 0, len(legacyStreamKeys))
	vals := map[string]bool{}
	for _, k := range legacyStreamKeys {
		if b, ok := settings[k.key].(bool); ok {
			present = append(present, k.key)
			vals[k.key] = b
		}
	}
	if len(present) == 0 {
		return nil
	}

	allOff, allOn := true, true
	for _, k := range present {
		if vals[k] {
			allOff = false
		} else {
			allOn = false
		}
	}
	if allOn {
		return nil
	}

	var plan []MigrationPlan
	for _, r := range routers {
		if r.Collection != nil && r.Collection.Mode != "" {
			continue
		}
		if allOff {
			plan = append(plan, MigrationPlan{ID: r.ID, Mode: "poll"})
			continue
		}
		ovr := map[string]bool{}
		for _, k := range present {
			if !vals[k] {
				ovr[k] = false
			}
		}
		plan = append(plan, MigrationPlan{ID: r.ID, Mode: "stream", Overrides: ovr})
	}
	return plan
}
