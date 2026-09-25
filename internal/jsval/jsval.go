// Package jsval holds JavaScript value semantics that this port has to
// reproduce exactly.
//
// It exists because the same three rules were wanted in three places, and this
// repo has already been bitten by a rounding rule kept in two copies: the
// renderer's `toFixed1` and `reports.ToFixed` were the same rule written twice
// until they were folded together, and a corpus was needed to prove they still
// agreed. A JavaScript coercion is exactly that kind of rule — small, obvious to
// get subtly wrong, and invisible when it drifts.
//
// Nothing here is a general JS interpreter. Each function covers the values a
// JSON body or a JSON-decoded payload can actually hold, and says so.
package jsval

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Truthy is JavaScript's `if (x)`.
//
// The cases that matter and are easy to miss: the empty string is FALSE, 0 is
// FALSE, and NaN is FALSE — while an object or an array is TRUE however empty
// it is.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	case int:
		return x != 0
	case int64:
		return x != 0
	}
	return true
}

// String is JavaScript's `String(x)` for those same values.
func String(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return Number(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return fmt.Sprintf("%v", v)
}

// Number is `String(n)` for a finite float.
//
// Go's `%v` prints 1e+06 where JavaScript prints 1000000, so the integral case
// is handled first — which is the only one the values here realistically take.
func Number(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return new(big.Float).SetFloat64(f).Text('f', -1)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// ToNumber is JavaScript's `Number(v)` for the shapes a decoded JSON value can
// hold, reporting whether the result is FINITE — i.e. `Number.isFinite(Number(v))`.
//
// ── IT IS NOT parseIntLike, AND THE DIFFERENCE IS LOAD-BEARING ──────────────
//
// `internal/store`'s `parseIntLike` is `parseInt`, which takes a LEADING number
// and ignores the rest: `parseInt("25abc")` is 25. `Number("25abc")` is NaN. The
// settings validator uses the first and the poll re-tune uses the second, so a
// single shared helper would make one of them wrong.
//
// The cases that matter here, all of which the corpus carries:
//
//	nil    → 0, finite. `Number(null) === 0`. So a null poll interval takes the
//	         NUMBER path and clamps to the floor, rather than the keep-current
//	         path. 500 and "leave it alone" are different outcomes.
//
//	         CALLERS MUST NOT REACH HERE FOR A MISSING KEY. `m[k]` on an absent
//	         Go map key yields the same nil this maps to 0, but JavaScript's
//	         `Number(undefined)` is NaN. There is no value this function can be
//	         handed that means "absent", so the two-value map lookup has to do it
//	         — see `collection.PollRetunes`, where reading the one-value form made
//	         a never-written interval clamp to 500 instead of being left alone.
//	""     → 0, finite. `Number("")` is 0, not NaN — whitespace-only too.
//	bool   → 1 / 0, both finite.
//	string → trimmed, then parsed as a JS numeric literal.
//	other  → not finite. `Number({})` and `Number([1,2])` are NaN.
//
// A one-element array is the one shape deliberately NOT reproduced: JavaScript
// makes `Number([5])` 5, via ToPrimitive on the array. Nothing in this port
// stores an interval as a single-element array, and reproducing that coercion
// would be a rule with no caller — which is the kind of code the port's own
// "a read nothing calls is a read nothing gates" rule warns about.
func ToNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return x, true
	case float32:
		return ToNumber(float64(x))
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return 0, false
		}
		return ToNumber(f)
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, true // Number("") === 0, and Number("   ") too
		}
		// `Number()` accepts the JS numeric literal forms, which is a wider set
		// than ParseFloat: 0x, 0b and 0o all parse, and `Infinity` is a value.
		switch s {
		case "Infinity", "+Infinity", "-Infinity":
			return 0, false // a value, but not FINITE, which is the question asked
		}
		if len(s) > 2 && (s[0] == '0') {
			switch s[1] {
			case 'x', 'X', 'b', 'B', 'o', 'O':
				n, err := strconv.ParseInt(s[2:], baseOf(s[1]), 64)
				if err != nil {
					return 0, false
				}
				return float64(n), true
			}
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func baseOf(c byte) int {
	switch c {
	case 'x', 'X':
		return 16
	case 'o', 'O':
		return 8
	default:
		return 2
	}
}
