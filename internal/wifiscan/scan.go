package wifiscan

// The WiFi frequency scan's pure halves — the port of the three exported
// functions in `src/wifiScan.js` that need no router.
//
// ── WHAT IS DELIBERATELY NOT HERE ───────────────────────────────────────────
//
// The runner. That module is, in its own words, "the first deliberately
// DISRUPTIVE thing MikroDash does": a frequency scan "will disconnect all
// connected clients, or if the interface is in station mode, it will disconnect
// from the AP". The bounded duration, the wall-clock stop that does not trust
// the router to honour it, the one-scan-per-router rule and the fleet-wide cap
// all exist because of that sentence, and all of them need a live connection.
//
// What IS here is what turns a scan's rows into something a page can draw: the
// band arithmetic, the row parser and the trap classifier.

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// The limits the runner enforces. Carried here so a caller validating a request
// does not invent its own, and so a change upstream fails the corpus.
var Durations = []int{30, 60, 120}

const (
	MaxChannels = 200
	FleetCap    = 3
)

// FreqToChannel maps a frequency in MHz to the channel number operators talk in.
//
// Returns no channel outside the known bands rather than a plausible-looking
// wrong number — the live comment puts it well: "5905 MHz" is honest, "channel
// -9" is not.
func FreqToChannel(mhz float64) (int, bool) {
	if math.IsNaN(mhz) || math.IsInf(mhz, 0) || mhz != math.Trunc(mhz) {
		return 0, false
	}
	m := int(mhz)
	// Japan's channel 14, which is off the /5 grid and has to be named.
	if m == 2484 {
		return 14, true
	}
	if m >= 2412 && m <= 2472 && (m-2412)%5 == 0 {
		return (m - 2407) / 5, true
	}
	// 6GHz by its OWN BASE. The live comment says it is "checked first" because
	// its numbering overlaps 5GHz's — and the numbering does, but the FREQUENCY
	// RANGES do not (5885 < 5955), so the order is defensive rather than
	// load-bearing. Measured: swapping the two blocks kills nothing, while using
	// the 5GHz base for a 6GHz frequency kills a case immediately.
	//
	// The order is kept anyway — it costs nothing and it is how the original
	// reads — but the thing to preserve when editing is the BASE, not the
	// sequence.
	if m >= 5955 && m <= 7115 && (m-5955)%5 == 0 {
		return (m - 5950) / 5, true
	}
	if m >= 5160 && m <= 5885 && (m-5160)%5 == 0 {
		return (m - 5000) / 5, true
	}
	return 0, false
}

// Row is one parsed `!re` row.
//
// Every numeric is a POINTER because "not reported" and "zero" are different
// answers: a noise floor of 0 dBm is a reading, and an absent one is not.
type Row struct {
	Ch        int     `json:"ch"`
	ChNum     *int    `json:"chNum"`
	ChRaw     *string `json:"chRaw"`
	Nets      *int    `json:"nets"`
	Load      *int    `json:"load"`
	NF        *int    `json:"nf"`
	MaxSig    *int    `json:"maxSig"`
	MinSig    *int    `json:"minSig"`
	Primary   bool    `json:"primary"`
	Secondary bool    `json:"secondary"`
}

// ParseRow parses one row, or reports false when it cannot.
//
// A ROW WHOSE CHANNEL WILL NOT PARSE IS DROPPED, not returned with a zero: the
// live comment explains the cost of the alternative — "a bar at x=NaN silently
// disappears and looks like a channel that was never scanned".
func ParseRow(r map[string]any) (Row, bool) {
	if r == nil {
		return Row{}, false
	}
	raw, has := r["channel"]
	if !has {
		raw, has = r["freq"]
	}
	var rawStr string
	if has && raw != nil {
		rawStr = jsString(raw)
	}
	// `/interface/wifi/monitor` reports compound forms like "2427/ax/Ce", so the
	// leading integer is taken and the raw value kept.
	ch, ok := jsParseInt(strings.Split(rawStr, "/")[0])
	if !ok {
		return Row{}, false
	}

	out := Row{Ch: ch}
	if has && raw != nil {
		s := rawStr
		out.ChRaw = &s
	}
	if n, ok := FreqToChannel(float64(ch)); ok {
		out.ChNum = &n
	}
	out.Nets = intField(r["networks"])
	out.Load = intField(r["load"])
	out.NF = intField(r["nf"])
	out.MaxSig = intField(r["max-signal"])
	out.MinSig = intField(r["min-signal"])
	out.Primary = jsBool(r["primary"])
	out.Secondary = jsBool(r["secondary"])
	return out, true
}

// The trap patterns, IN ORDER. Order is the contract: "no such command prefix"
// contains "no such", so the unsupported-stack test must come before the
// no-such-interface one, or every unsupported stack reads as a missing
// interface and the operator goes looking for a typo in a name that is correct.
var trapRules = []struct {
	re   *regexp.Regexp
	code string
}{
	{regexp.MustCompile(`not enough privileges|permission denied|cannot run`), "permission-denied"},
	{regexp.MustCompile(`no such command prefix|unknown command`), "unsupported-stack"},
	{regexp.MustCompile(`no such item|not found`), "no-such-interface"},
	{regexp.MustCompile(`unknown parameter|no such argument|invalid value`), "bad-parameter"},
}

// ClassifyTrap turns RouterOS !trap text into a code the browser can phrase.
// Anything unmatched stays `router-error`.
func ClassifyTrap(msg string) string {
	m := strings.ToLower(msg)
	for _, r := range trapRules {
		if r.re.MatchString(m) {
			return r.code
		}
	}
	return "router-error"
}

// intField is `_int`: absent, null and blank are NO VALUE — not zero.
func intField(v any) *int {
	if v == nil {
		return nil
	}
	s := jsString(v)
	if s == "" {
		return nil
	}
	n, ok := jsParseInt(s)
	if !ok {
		return nil
	}
	return &n
}

// jsBool is `_bool`: true, "true" and "yes", and nothing else. "1" is NOT true
// here, which is worth knowing — RouterOS spells booleans several ways and this
// one accepts three of them.
func jsBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "yes"
	default:
		return false
	}
}

// jsString renders a value the way `String(x)` does for the shapes a reply row
// carries.
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case json.Number:
		return x.String()
	default:
		return ""
	}
}

// jsParseInt is `parseInt(s, 10)`: optional sign, then as many digits as it can
// take. "2427/ax" never reaches here with its tail, but "5180ac" would, and
// parseInt takes the 5180.
func jsParseInt(s string) (int, bool) {
	s = strings.TrimSpace(s)
	i, neg := 0, false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start, n := i, 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		n = n*10 + int(s[i]-'0')
		i++
	}
	if i == start {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}
