package server

// `GET /api/collectors` — the per-router collector toggles the Add/Edit Device
// dialog offers.
//
// ── WHY THIS ONE CAN BE SERVED NOW ──────────────────────────────────────────
//
// It was on `endpoint-audit`'s proxied list with a precise cost: without it "the
// dialog offers an EMPTY list rather than an error, so the operator sees a router
// with no collectors to choose from and no way to tell that is wrong."
//
// It became servable when #105 landed. The live route is three lines over the
// SAME registry this port now embeds (`_COLLECTOR_DEFS` is `collection.js`'s
// `COLLECTORS` under an alias), so answering it here is a projection of generated
// data rather than a second source of truth.
//
// ── ONLY THE DISABLEABLE ONES, AND THAT IS THE CONTRACT ─────────────────────
//
// The dialog draws a checkbox per entry, so a protected collector appearing here
// would offer an operator a switch that does nothing — `resolveCollection` forces
// `enabled` true for anything `disableable: false` regardless of the off list.
// The live route filters, and so does this.
//
// `requires` rides along because the dialog uses it to cascade: unticking a
// collector others depend on must visibly untick them too.

import (
	"encoding/json"
	"net/http"

	"mikrodash/internal/collection"
)

const collectorsPath = "/api/collectors"

func (s *Server) registerCollectors(mux *http.ServeMux) {
	mux.HandleFunc("GET "+collectorsPath, s.collectorsGet)
}

// collectorEntry is the shape the dialog reads. Three fields, matching the live
// route's `.map` exactly — not the whole registry row, which carries poll keys
// and stream keys the browser has no use for.
type collectorEntry struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Requires []string `json:"requires"`
}

func (s *Server) collectorsGet(w http.ResponseWriter, r *http.Request) {
	out := []collectorEntry{}
	for _, c := range collection.Collectors() {
		if !c.Disableable {
			continue
		}
		// `Requires` IS NEVER NIL, and no guard is written here for it.
		//
		// The live route says `c.requires || []` because a registry row may omit
		// the field. The generated tables cannot: `tools/collection-cases.js`
		// emits `(c.requires || []).slice()`, so every row carries an array and a
		// JSON `[]` decodes to an empty non-nil slice. A `if req == nil` here was
		// unreachable — the mutation that removed it killed nothing, which is how
		// it was found — and unreachable defensive code reads as a hazard that
		// exists.
		//
		// The invariant is asserted where it can actually fail: in the generator.
		out = append(out, collectorEntry{Key: c.Key, Label: c.Label, Requires: c.Requires})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"collectors": out})
}
