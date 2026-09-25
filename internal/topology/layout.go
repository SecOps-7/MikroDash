// Package topology validates the Topology map's saved node positions.
//
// The port of `src/topologyLayout.js`, which exists for the reason its own
// header gives: "this is the one place where caller-supplied strings become
// OBJECT KEYS in a file written to disk, so it needs direct test coverage rather
// than only being exercised through a running server."
//
// ── REJECTION IS TOTAL, AND null MEANS 400 ──────────────────────────────────
//
// One banned key, one malformed key, one non-finite coordinate, and the WHOLE
// map is refused. The live header is explicit that callers "must treat null as a
// 400 rather than as 'no positions'" — a port that skipped the bad entry would
// persist a partial layout and report success, and the operator would find their
// map half-rearranged with nothing to explain it.
//
// ── PROTOTYPE POLLUTION HAS NO GO ANALOGUE; THE KEY RULES STILL MATTER ──────
//
// `__proto__` cannot poison a Go map. The other half of that rule can still bite:
// these keys are written into a JSON file, so `..`, `/` and unbounded length are
// refused here exactly as they are there. The banned names are kept because the
// FILE is read back by the Node app, where they do mean something.
package topology

import (
	"encoding/json"
	"math"
	"math/big"
	"regexp"
)

// The limits, taken from the live module rather than chosen here.
const (
	MaxNodes   = 200
	CoordLimit = 5000
)

var (
	ridRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// Node keys are MAC addresses ("48:A9:8A:E5:CE:34") or an "id:*3" fallback
	// when a neighbour advertises no MAC.
	keyRe = regexp.MustCompile(`^[A-Za-z0-9:._-]{1,64}$`)
)

var bannedKeys = map[string]bool{"__proto__": true, "constructor": true, "prototype": true}

// Point is one node's position, rounded to one decimal.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// IsValidRouterID reports whether a router id may be used as a filename part.
func IsValidRouterID(rid string) bool { return ridRe.MatchString(rid) }

// CleanPositions sanitises a positions map.
//
// `ok` is false when the input is unusable, which the caller must turn into a
// 400 — NOT into "no positions". An empty map with ok=true is a legitimate
// answer: it means the operator cleared every node.
func CleanPositions(raw json.RawMessage) (map[string]Point, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	// An ARRAY, a string or a number is not a positions map. Decoding into a map
	// rejects all three, which is what `typeof raw !== 'object' || Array.isArray`
	// does on the other side.
	var in map[string]json.RawMessage
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, false
	}
	if in == nil { // a literal `null` decodes without error
		return nil, false
	}
	if len(in) > MaxNodes {
		return nil, false
	}

	out := make(map[string]Point, len(in))
	for k, v := range in {
		if bannedKeys[k] || !keyRe.MatchString(k) {
			return nil, false
		}
		var p struct {
			X any `json:"x"`
			Y any `json:"y"`
		}
		if err := json.Unmarshal(v, &p); err != nil {
			return nil, false
		}
		x, okX := jsNumber(p.X)
		y, okY := jsNumber(p.Y)
		if !okX || !okY {
			return nil, false
		}
		out[k] = Point{X: round1(clamp(x)), Y: round1(clamp(y))}
	}
	return out, true
}

func clamp(v float64) float64 { return math.Max(-CoordLimit, math.Min(CoordLimit, v)) }

// ratRound1 rounds to one decimal on the EXACT value of the double, half away
// from zero — `Number(x.toFixed(1))`.
func ratRound1(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	exact := new(big.Rat).SetFloat64(v)
	if exact == nil {
		return v
	}
	scaled := exact.Mul(exact, big.NewRat(10, 1))
	neg := scaled.Sign() < 0
	if neg {
		scaled.Neg(scaled)
	}
	// floor(scaled) and the remainder, so the halfway test is exact.
	num, den := scaled.Num(), scaled.Denom()
	q := new(big.Int)
	rem := new(big.Int)
	q.QuoRem(num, den, rem)
	// rem/den >= 1/2  ->  2*rem >= den. HALF ROUNDS UP (away from zero).
	twice := new(big.Int).Lsh(rem, 1)
	if twice.Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	out, _ := new(big.Rat).SetFrac(q, big.NewInt(10)).Float64()
	if neg {
		out = -out
	}
	return out
}

// round1 is JavaScript's `+x.toFixed(1)`, which is NOT Go's `%.1f`.
//
// `toFixed` rounds the DECIMAL rendering of the nearest binary double, so it
// rounds half AWAY FROM ZERO on the value actually stored — while `%.1f` rounds
// half to EVEN. They disagree on 0.25: JavaScript gives 0.3, `%.1f` gives 0.2,
// and a coordinate differing in the first decimal moves a node on the map.
//
// SCALING BY TEN DOES NOT WORK, which the corpus proved: `0.15 * 10` rounds to
// exactly 1.5 in binary, so `math.Round` takes it UP to 0.2 — while JavaScript,
// reading the exact stored value (0.1499999…), gives 0.1. The multiplication
// destroys the very distinction the rounding depends on.
//
// So the exact value is taken with `big.Rat`, which `SetFloat64` fills without
// loss, and the halfway decision is made on it: half away from zero, matching
// `toFixed`. 0.15 is then below the halfway point and rounds down; 0.25 is
// exactly on it and rounds up; 0.05 is just above and rounds up.
func round1(v float64) float64 {
	r := ratRound1(v)
	// NEGATIVE ZERO IS NORMALISED. `-0.04` rounds to `-0` here, and Go marshals
	// that as `-0` where JavaScript's `JSON.stringify(-0)` is `0`. The value is
	// the same number; the bytes on disk would not be.
	if r == 0 {
		return 0
	}
	return r
}

// jsNumber is JavaScript's `Number(v)` for the shapes a JSON body carries.
//
// A NUMERIC STRING IS ACCEPTED — `Number("12")` is 12 — which a Go type switch
// would reject. `true` is not: the live code reaches `Number(true)` === 1, but
// only after `typeof v === 'object'` has already passed, and a boolean
// coordinate arrives as a JSON bool, which `Number` turns into 1. Reproduced by
// running the live module rather than reasoned about: the corpus says.
func jsNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return x, true
	case string:
		var f float64
		if err := json.Unmarshal([]byte(x), &f); err != nil {
			return 0, false
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}
