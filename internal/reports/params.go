package reports

// The small pure pieces the report endpoints share: how a query string becomes a
// range, how a run of connectivity events becomes downtime, how an alert key
// becomes a human label, and how a rate becomes a percentage of a link.
//
// They live here rather than beside the handlers for the reason the live app
// moved them out of index.js: the scheduled-report path needs the same answers,
// and two implementations of "which window is this" would diverge the first time
// one of them was fixed.

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// aggValid is the allow-list from index.js. Anything else becomes the empty
// string, which means "do not aggregate" — NOT an error and not a default
// interval. A caller asking for `?aggregate=quarter` gets raw samples, which is
// the original's answer and what the page is built to render.
var aggValid = map[string]bool{"hour": true, "day": true, "week": true, "month": true}

// Params is one report request's range.
type Params struct {
	RouterID  string
	From      int64
	To        int64
	Aggregate string
}

// ParseParams reproduces `_parseReportParams`.
//
// ── parseInt's LENIENCE IS PART OF THE CONTRACT ─────────────────────────────
//
// `parseInt('123abc', 10)` is 123, and `parseInt('abc', 10) || 0` is 0. Go's
// ParseInt refuses both, so using it directly would turn a request the live app
// answers into one this app rejects. LeadingInt below is JavaScript's rule:
// optional sign, then digits, stopping at the first character that is not one.
//
// The `|| 0` and `|| Date.now()` fallbacks also fire for a parsed ZERO, not only
// for a failure — `?to=0` is indistinguishable from `?to=` here, and both mean
// "up to now".
func ParseParams(routerID, from, to, aggregate string, now time.Time) Params {
	p := Params{RouterID: routerID}
	p.From = LeadingInt(from)
	p.To = LeadingInt(to)
	if p.To == 0 {
		p.To = now.UnixMilli()
	}
	if aggValid[aggregate] {
		p.Aggregate = aggregate
	}
	return p
}

// LeadingInt is `parseInt(s, 10) || 0` — JavaScript's parse, which reads a
// leading integer and ignores whatever follows it.
//
// EXPORTED because it is not a reports detail: every ported endpoint that takes
// a numeric query parameter needs this exact rule, and the audit trail's
// from/to/limit/offset are the second set. Duplicating it would mean two places
// to get the overflow saturation below right, and the second one would be
// written by someone who had not read this comment.
func LeadingInt(s string) int64 {
	s = strings.TrimLeft(s, " \t\n\r\f\v")
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == start {
		return 0
	}
	n, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil {
		// OUT OF int64 RANGE, and the choice here is not free. JavaScript has no
		// integer overflow: `parseInt('99999999999999999999')` is 1e20, a float,
		// and the live app then asks for `ts >= 1e20` and gets NOTHING.
		//
		// Returning 0 — "same as unparseable" — was the first version of this and
		// it is the worst answer available: a bound so large it excludes every
		// sample would have become a bound that includes every one of them. All
		// rows and no rows are not close together.
		//
		// Saturating keeps the OBSERVABLE behaviour: an absurd lower bound still
		// matches nothing, an absurd upper bound still matches everything.
		if strings.HasPrefix(s, "-") {
			return math.MinInt64
		}
		return math.MaxInt64
	}
	return n
}

// ── Connectivity downtime ───────────────────────────────────────────────────

// ConnRow is a connectivity event as the endpoint sends it: the stored columns
// plus the derived duration.
type ConnRow struct {
	TS        int64 `json:"ts"`
	Connected int   `json:"connected"`
	// DowntimeMs is null for an online row and for an outage still running.
	DowntimeMs *int64 `json:"downtime_ms"`
}

// AnnotateDowntime fills in how long each offline event lasted.
//
// A SINGLE BACKWARDS PASS, which is the only way to do this in one sweep: an
// outage's duration is not known until the row that ends it is seen, and that
// row is later in the list. Walking forwards would need a second pass to fill
// the gaps in.
//
// A trailing outage — offline events with no online event after them — keeps a
// NIL duration rather than being measured against "now". The router may still be
// down, and reporting a number would freeze an ongoing outage at whatever moment
// the page happened to be loaded.
func AnnotateDowntime(rows []ConnRow) []ConnRow {
	// A NIL SLICE MARSHALS TO `null`, and the live endpoint sends `[]`. The page
	// iterates what it is given, so null is a different payload and not merely a
	// different spelling of empty.
	if rows == nil {
		return []ConnRow{}
	}
	var nextOnline *int64
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Connected != 0 {
			ts := rows[i].TS
			nextOnline = &ts
			rows[i].DowntimeMs = nil
			continue
		}
		if nextOnline != nil {
			d := *nextOnline - rows[i].TS
			rows[i].DowntimeMs = &d
		} else {
			rows[i].DowntimeMs = nil
		}
	}
	return rows
}

// ── Alert labels ────────────────────────────────────────────────────────────

// alertLabels are the keys whose mechanical title-casing would read wrong.
var alertLabels = map[string]string{
	"routeros_update":  "Update Available",
	"routeros_updated": "Up To Date",
	"connectivity":     "Router Connectivity",
}

// labelAcronyms are the words the title-caser would otherwise mangle into "Bgp"
// and "Ok".
var labelAcronyms = map[string]string{
	"bgp": "BGP", "vpn": "VPN", "cpu": "CPU", "ok": "OK", "os": "OS",
}

// LabelFor is the human name for an alert type.
//
// It rides ALONGSIDE the raw key in the payload rather than replacing it:
// sorting, filtering and the CSV export all key off alert_type, and only the
// display wants a name. Derived server-side so the report and the notification
// bell cannot end up calling the same alert two different things.
//
// An unmapped key is title-cased word by word, so a future alert type reads as
// words rather than leaking a database column.
func LabelFor(alertType string) string {
	if alertType == "" {
		return "Alert"
	}
	key := strings.ToLower(alertType)
	if l, ok := alertLabels[key]; ok {
		return l
	}
	// `.split('_').filter(Boolean)` — empty segments from a leading, trailing or
	// doubled underscore are dropped rather than becoming empty words.
	parts := []string{}
	for _, w := range strings.Split(key, "_") {
		if w == "" {
			continue
		}
		if a, ok := labelAcronyms[w]; ok {
			parts = append(parts, a)
			continue
		}
		// `charAt(0).toUpperCase() + slice(1)` operates on UTF-16 code units, so
		// only the first unit is upper-cased. For every alert type this app emits
		// that is an ASCII letter; using the first RUNE keeps a multi-byte key
		// intact instead of splitting one, which is the safer disagreement.
		r := []rune(w)
		parts = append(parts, strings.ToUpper(string(r[0]))+string(r[1:]))
	}
	return strings.Join(parts, " ")
}

// ── Link utilisation ────────────────────────────────────────────────────────

// CapacityOr is `Math.max(1, parseInt(v, 10) || 1000)`.
//
// The floor of 1 is not cosmetic: capacity is a divisor, and a router record
// carrying 0 would otherwise produce an infinite utilisation.
// ClampInt narrows an int64 to an int WITHOUT WRAPPING.
//
// On a 64-bit build this is the identity and none of this matters. On 32-bit it
// does: `int` is 32 bits there, and a plain conversion of a query parameter like
// ?limit=4294967296 wraps, sometimes to a negative. This project publishes
// linux/arm/v7, so that is a real target rather than a theoretical one.
//
// Every current caller happens to be saved by a clamp further down -- Limit is
// bounded to 1..1000 in the db layer, CapacityOr floors at 1, and SQLite reads a
// negative OFFSET as zero. That makes this a PARITY fix rather than a security
// one: the same request should produce the same answer on every architecture,
// and relying on a downstream clamp to absorb a wrap is the kind of accident
// that stops being true when somebody adds a caller.
func ClampInt(n int64) int {
	if n > int64(math.MaxInt) {
		return math.MaxInt
	}
	if n < int64(math.MinInt) {
		return math.MinInt
	}
	return int(n)
}

func CapacityOr(v string) int {
	n := ClampInt(LeadingInt(v))
	if n == 0 {
		n = 1000
	}
	if n < 1 {
		n = 1
	}
	return n
}

// UtilPct is a rate as a percentage of a capacity, to one decimal.
//
// NOT CLAMPED TO 100, deliberately. The live dashboard card does clamp, which is
// what hides a misconfigured capacity — a link reporting 151% is telling you the
// configured figure is wrong, and that is worth seeing.
//
// Nil in, nil out: a percentage of a missing rate is not zero.
func UtilPct(v *float64, capacity int) *float64 {
	if v == nil {
		return nil
	}
	// `+((v / cap) * 100).toFixed(1)`, and it took three attempts to get right.
	// Both wrong versions are recorded because each is the obvious one:
	//
	//  1. `strconv.FormatFloat(x, 'f', 1, 64)` — Go rounds a tie to EVEN, so
	//     12.25 formats as "12.2" where toFixed gives "12.3". A 12.25 Mbps peak
	//     on a 100 Mbps link is not a contrived number.
	//  2. `math.Floor(x*10 + 0.5) / 10` — the right RULE on the wrong value.
	//     Multiplying by ten is itself a rounding step: 940.5/1000*100 is the
	//     double just below 94.05, but ×10 lands exactly on 940.5, so the
	//     half-up then rounds a number that was never a tie. 94.0 became 94.1.
	//
	// toFixed rounds the EXACT value of the double — the spec picks the n where
	// n/10 is nearest, ties going to the LARGER n (toward +∞, not away from
	// zero). big.Rat holds that exact value, so the arithmetic below is the spec
	// rather than an approximation of it.
	x := (*v / float64(capacity)) * 100
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return nil
	}
	r := new(big.Rat).SetFloat64(x)
	r.Mul(r, big.NewRat(10, 1))
	r.Add(r, big.NewRat(1, 2))
	// Div is Euclidean, which for a positive denominator is the floor — and a
	// Rat's denominator is always positive.
	n := new(big.Int).Div(r.Num(), r.Denom())
	f, _ := new(big.Rat).SetFrac(n, big.NewInt(10)).Float64()
	return &f
}
