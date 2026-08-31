package reports

import (
	"math"
	"math/big"

	"mikrodash/internal/jsval"
)

// ToFixed is JavaScript's `Number.prototype.toFixed`, which Go's `%.*f` is NOT.
//
// ECMA-262 negates first, then picks "an integer n for which n/10^f - x is as
// close to zero as possible; if there are two such n, pick the LARGER n". That
// is round-half-AWAY-FROM-ZERO on the value's exact binary expansion. Go's
// strconv rounds half to EVEN, so the two part company on every exact half:
//
//	(0.5).toFixed(0)  === "1"      fmt.Sprintf("%.0f", 0.5)  == "0"
//	(1.25).toFixed(1) === "1.3"    fmt.Sprintf("%.1f", 1.25) == "1.2"
//	(-1.25).toFixed(1) === "-1.3"  fmt.Sprintf("%.1f", -1.25) == "-1.2"
//
// None of those are contrived. `fmtDataMB` renders a sub-megabyte value as
// `(n*1000).toFixed(0)`, so a stored 0.0005 MB is exactly the first case; the
// report chart's y-axis labels hit the second whenever the range tops out at
// 5000; and the third turns up on any chart with negative values.
//
// big.Rat holds the float's exact value, so the comparison against a half is
// itself exact rather than one more rounding.
//
// Digits above 20 or below 0 are out of range for the live function (it throws),
// and no caller in this port passes one; they are clamped rather than panicking,
// because a report is not worth crashing a server for.
func ToFixed(x float64, digits int) string {
	switch {
	case math.IsNaN(x):
		return "NaN"
	case math.IsInf(x, 1):
		return "Infinity"
	case math.IsInf(x, -1):
		return "-Infinity"
	}
	if digits < 0 {
		digits = 0
	}
	if digits > 20 {
		digits = 20
	}
	// JS falls back to the general number format above 1e21, where toFixed stops
	// being a fixed-point rendering at all.
	if math.Abs(x) >= 1e21 {
		return JSNumber(x)
	}

	neg := math.Signbit(x)
	r := new(big.Rat).SetFloat64(math.Abs(x))
	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	r.Mul(r, new(big.Rat).SetInt(pow)) // x * 10^digits, exactly

	n := new(big.Int).Quo(r.Num(), r.Denom())
	frac := new(big.Rat).Sub(r, new(big.Rat).SetInt(n))
	if frac.Cmp(big.NewRat(1, 2)) >= 0 { // ">=" is the "pick the larger n" tie-break
		n.Add(n, big.NewInt(1))
	}

	s := n.String()
	if digits > 0 {
		for len(s) <= digits {
			s = "0" + s
		}
		s = s[:len(s)-digits] + "." + s[len(s)-digits:]
	}
	// "-0" and "-0.00" are what JS prints for a negative that rounds to nothing,
	// so the sign is kept rather than dropped on a zero result.
	if neg {
		s = "-" + s
	}
	return s
}

// JSNumber is JavaScript's `String(n)` for a finite number.
//
// Delegates to internal/jsval, which is where this port keeps the JavaScript
// coercions it has to reproduce. Kept exported here because the reports code
// reads better saying JSNumber than jsval.Number, and because folding it away
// entirely would touch every call site for no gain — but there is ONE
// implementation, which is the point.
func JSNumber(f float64) string { return jsval.Number(f) }

// MaxOf is `arr.reduce((m, v) => (v > m ? v : m), -Infinity)`.
//
// AN EMPTY SLICE GIVES -Inf, not zero. Every live caller guards with
// `arr.length ? … : '—'` so the value never reaches a page, but a port returning
// 0 would agree on every guarded call and diverge the instant a guard moved.
func MaxOf(vs []float64) float64 {
	m := math.Inf(-1)
	for _, v := range vs {
		if v > m {
			m = v
		}
	}
	return m
}

// BucketNoun names the bucket a volume peak was measured over. Without an
// aggregation the stored granularity is one minute.
func BucketNoun(agg string) string {
	switch agg {
	case "hour":
		return "Hour"
	case "day":
		return "Day"
	case "week":
		return "Week"
	case "month":
		return "Month"
	default:
		return "Minute"
	}
}

// FmtDataMB renders a stored `bandwidth_usage` MB value.
//
// The thresholds are DECIMAL on purpose, and the live comment says why: `rx_mb`
// is written as Mbps/8, i.e. 10^6-based, so 1024-based thresholds overstated
// every total by about 4.9% — and ISP quotas are quoted decimal anyway.
//
// NEGATIVES ARE NOT CLAMPED. `+mb || 0` turns null, NaN and "" into zero but
// leaves -5 alone, and -5 fails all three thresholds and comes out of the last
// line as "-5000 KB". Reproduced rather than tidied: a negative here means the
// counter went backwards, which is worth seeing rather than hiding as "0 KB".
func FmtDataMB(mb float64) string {
	n := mb
	// `+mb || 0`: NaN is falsy, and so is -0, which JS coerces to +0.
	if math.IsNaN(n) || n == 0 {
		n = 0
	}
	switch {
	case n >= 1e6:
		return ToFixed(n/1e6, 2) + " TB"
	case n >= 1000:
		return ToFixed(n/1000, 2) + " GB"
	case n >= 1:
		return ToFixed(n, 1) + " MB"
	default:
		return ToFixed(n*1000, 0) + " KB"
	}
}
