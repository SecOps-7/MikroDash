package collection

// The `collection:config` payload.
//
// ── WHY THIS ARRIVED LATE ───────────────────────────────────────────────────
//
// The port resolved per-router collection config from the day #105 landed and
// never told the browser about it. The client-side consumer
// (`applyCollectionConfig` in `web/src/stale.ts`) was written, pinned against the
// live implementation by the stale check, and called by nothing — the
// event that would feed it was never emitted. Found 2026-08-28 by
// The live-socket-diff tool; the orphaned-consumer audit now watches
// that class so a gated-but-unreachable consumer fails a sweep.
//
// The visible consequence until now: a collector an operator turned off on a
// router showed a STALE dashboard card instead of `is-collector-off`, so it read
// as broken rather than as deliberately off.
//
// ── `off` IS ORDERED, AND NOT ALPHABETICALLY ────────────────────────────────
//
//	off: Object.keys(eff.enabled).filter(k => !eff.enabled[k])
//
// `Object.keys` is insertion order and `eff.enabled` is built by walking the
// collector registry, so `off` comes out in REGISTRY order. This side has the
// same ordered registry, so it reproduces that exactly instead of sorting and
// documenting a divergence the way `writeCapablePages` had to.
//
// The corpus discriminates: a cascade yields `["conns", "bandwidth"]` and two
// hand-picked collectors yield `["wifi", "logs"]`. Neither is alphabetical, so a
// sorted implementation fails rather than passing by luck.
//
// The three MAPS need no such care. A JSON object has no order a client can
// observe, and `applyCollectionConfig` indexes them by key.

// Payload is `_collectionPayload(routerId, session)`.
//
// A map rather than a struct, matching the rest of this port's socket payloads:
// the key names are the wire contract and a struct tag is one more place for
// them to drift.
func Payload(routerID string, eff Resolved) map[string]any {
	// REGISTRY ORDER, not map order. Ranging over eff.Enabled would give Go's
	// randomised map iteration, so `off` would differ between two emits of the
	// same configuration.
	off := []string{}
	for _, c := range loaded.Registry {
		if !eff.Enabled[c.Key] {
			off = append(off, c.Key)
		}
	}
	return map[string]any{
		"routerId": routerID,
		"mode":     eff.Mode,
		"enabled":  eff.Enabled,
		"stream":   eff.Stream,
		"poll":     eff.Poll,
		// NOT `omitempty` and never nil: an unconfigured router sends `[]`, and
		// the live payload always carries the key. `applyCollectionConfig`
		// reads `cfg.enabled` rather than this, but the contract is the contract.
		"off": off,
	}
}
