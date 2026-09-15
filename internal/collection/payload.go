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
// ── NO `off` ────────────────────────────────────────────────────────────────
//
// The live payload also carried `off`, the collectors a router's config switched
// off, in registry order. Per-router switching was removed on 2026-09-15, and the
// browser reads `enabled`, which still says when the install-wide ping switch has
// turned ping off. The list went with the switch.

// Payload is `_collectionPayload(routerId, session)`, without `off`.
//
// A map rather than a struct, matching the rest of this port's socket payloads:
// the key names are the wire contract and a struct tag is one more place for
// them to drift.
func Payload(routerID string, eff Resolved) map[string]any {
	return map[string]any{
		"routerId": routerID,
		"mode":     eff.Mode,
		"enabled":  eff.Enabled,
		"stream":   eff.Stream,
		"poll":     eff.Poll,
	}
}
