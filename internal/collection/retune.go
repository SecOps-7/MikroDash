package collection

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"

	"mikrodash/internal/jsval"
)

// What a settings save applies to the RUNNING collectors.
//
// `POST /api/settings` does not only write the file: it re-tunes the live poll
// intervals, so moving a slider takes effect without a restart. Two rules decide
// that, and both are about what NOT to do — which is what makes them easy to
// port wrongly and invisible when they are.
//
// ── 1. A PER-ROUTER OVERRIDE OUTRANKS THE FLEET DEFAULT ─────────────────────
//
// The live comment (#105): "Without this the global save would silently un-pin
// whichever router the pool is currently serving, and the modal would then
// disagree with reality." A pinned key is still SAVED to the file; it is just
// not applied to the running collector. The two halves disagree on purpose.
//
// The test is PRESENCE, not truth — `overrides[key] !== undefined` — so an
// override of 0, of false or of "" still pins. A port checking truthiness
// un-pins exactly the values an operator sets to mean "off".
//
// ── 2. ONLY KEYS PRESENT IN THE UPDATES ─────────────────────────────────────
//
// The updates, not the merged file. Every poll key is in the file, so a port
// reading that would re-tune all twenty-three collectors on every save,
// restarting streams nobody touched.
//
// ── 3. THE VALUE IS RE-CLAMPED, TO A DIFFERENT RANGE ────────────────────────
//
// 500..600000, which is neither the validator's range nor a superset of it:
// `pollRouting` validates at 500..300000 and `pollWifi` at 10000..600000. The
// second clamp exists because the stored value may predate the current bounds.
//
// Pinned by `tools/settings-apply-cases.js`, whose table is LIFTED from the
// route. That table is the part with a history: the live source records that
// `pollTopology`, `pollVlans` and `pollPpp` were once missing from it, so "the
// sliders existed and the bounds existed, but with no entry here the value was
// dropped on save and never reached the collector".

const (
	retuneFloor   = 500
	retuneCeiling = 600000
)

// Retune is one collector's new interval. KeepCurrent means the stored value was
// not a finite number and the collector's existing interval stands — which is
// not the same as an interval of zero, and not the same as being absent.
type Retune struct {
	Collector   string
	PollMs      int
	KeepCurrent bool
}

var (
	pollMapOnce sync.Once
	pollMap     map[string]string
)

// PollMap is the settings key → collector name table, loaded from the generated
// file so a poll key added upstream cannot be silently missing here.
func PollMap() map[string]string {
	pollMapOnce.Do(func() {
		b, err := os.ReadFile(filepath.Join("testdata", "settings-apply-cases.json"))
		if err != nil {
			b, err = os.ReadFile(filepath.Join("..", "..", "testdata", "settings-apply-cases.json"))
		}
		if err != nil {
			panic("collection: settings-apply-cases.json is unreadable: " + err.Error())
		}
		var doc struct {
			PollMap map[string]string `json:"pollMap"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			panic("collection: settings-apply-cases.json: " + err.Error())
		}
		if len(doc.PollMap) == 0 {
			panic("collection: the poll map is empty")
		}
		pollMap = doc.PollMap
	})
	return pollMap
}

// PollRetunes is what a save should apply, given the keys it changed, the merged
// file it produced, and this router's overrides.
//
// `saved` is read for the VALUE and `updates` for the DECISION. They differ
// whenever the validator refused a key: the update is absent, so nothing is
// applied, and the stored value stays whatever it was.
func PollRetunes(updates, saved, overrides map[string]any) []Retune {
	out := []Retune{}
	for key, name := range PollMap() {
		if _, changed := updates[key]; !changed {
			continue
		}
		// PRESENCE, not truth.
		if _, pinned := overrides[key]; pinned {
			continue
		}
		// ── ABSENT IS NOT NULL, AND GO'S MAP LOOKUP HIDES THAT ──────────────
		//
		// `saved[key]` on a missing Go key yields a nil `any`, which is exactly
		// what a key holding JSON null yields. JavaScript tells them apart and
		// the answers differ: `Number(undefined)` is NaN — keep the collector's
		// current interval — while `Number(null)` is 0, which is finite and
		// clamps to the floor of 500.
		//
		// So a missing interval must leave the collector alone and a null one
		// must set it to the fastest allowed. Reading the one-value form conflates
		// them and silently speeds up a collector whose setting was never
		// written. The corpus carries both cases; this was found by the missing
		// one answering 500.
		raw, present := saved[key]
		if !present {
			out = append(out, Retune{Collector: name, KeepCurrent: true})
			continue
		}
		n, ok := jsval.ToNumber(raw)
		if !ok {
			out = append(out, Retune{Collector: name, KeepCurrent: true})
			continue
		}
		v := math.Trunc(n)
		if v < retuneFloor {
			v = retuneFloor
		}
		if v > retuneCeiling {
			v = retuneCeiling
		}
		out = append(out, Retune{Collector: name, PollMs: int(v)})
	}
	return out
}
