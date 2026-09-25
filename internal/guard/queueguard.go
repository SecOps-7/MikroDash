package guard

// The self-throttle warning for the Queues page, and the rate parsing both the
// collector and the warning depend on.
//
// ── THIS IS NOT selfguard.go ────────────────────────────────────────────────
//
// It sits beside the lockout guard and inverts it in both directions that
// matter. Read this before assuming the sibling's rules apply:
//
//	selfguard REFUSES.       queueguard only ever WARNS.
//	selfguard FAILS CLOSED.  queueguard FAILS OPEN.
//
// Both inversions are deliberate, and the reasoning is the original's:
//
// WARN, NEVER REFUSE. selfguard refuses because breaking the login is
// unrecoverable from inside this app — the fix is WinBox. A queue that throttles
// the dashboard is recoverable from the very row that created it, seconds later,
// right here. And `target=10.0.0.0/24 max-limit=50M/50M` on the LAN that happens
// to contain the dashboard is the single most ordinary queue anyone writes.
// Refusing it would make the feature useless, which is a worse failure than the
// one being prevented.
//
// FAIL OPEN. If our own address on this router cannot be worked out, no warning
// is produced and the write proceeds. `/user/active` being denied to the API
// user is the COMMON case, not an edge one — the documented monitoring group
// denies `policy`. Failing closed would block queue creation on exactly those
// routers in order to prevent a slow dashboard.
//
// ── Deliberately blunt ──────────────────────────────────────────────────────
//
// The warning does not reason about queue ORDER (first match wins, and
// modelling that would be a second guard with its own bugs), nor `direction`,
// `time` windows, or `dst-address` narrowing. One honest warning beats a
// fake-precise one: every false alarm here trains the operator to click through
// the warning that mattered.
//
// Queue TREES are not checked at all. A tree has no `target` — it matches
// packet-marks under a parent — so it cannot be aimed at our address. That
// removes half the surface for free.

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SelfThrottleFloorBps is the cap below which a queue covering our own address
// is worth mentioning.
const SelfThrottleFloorBps int64 = 1000000

// Rate is a parsed bits-per-second value that can also be ABSENT.
//
// The distinction is load-bearing and is not the same as zero: RouterOS reads an
// unlimited queue back as "0/0" rather than omitting the field, so 0 means
// "explicitly unlimited" and absent means "no value at all". The page renders
// those differently, and the guard treats only absent as "nothing to compare".
type Rate struct {
	Bps int64
	Set bool
}

// MarshalJSON writes an absent rate as `null` and a present one as a bare
// number, which is what JSON.stringify does with the original's `null | number`
// — and therefore what the fingerprint below has to produce.
func (r Rate) MarshalJSON() ([]byte, error) {
	if !r.Set {
		return []byte("null"), nil
	}
	return []byte(strconv.FormatInt(r.Bps, 10)), nil
}

// Pair is a simple queue's `upload/download` pair.
type Pair struct {
	Up   Rate `json:"up"`
	Down Rate `json:"down"`
}

// rateRe is the original's, character for character. The optional whitespace
// before the suffix is deliberate: an operator types "15 M".
var rateRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([kKmMgG]?)$`)

var rateMult = map[string]float64{
	"": 1, "k": 1e3, "K": 1e3, "m": 1e6, "M": 1e6, "g": 1e9, "G": 1e9,
}

// ParseRate reads a RouterOS rate as bits per second.
//
// Over the API the router answers in raw bps ("15000000"), but it ACCEPTS the
// CLI's suffixed form ("15M"), and an operator typing into the form will use
// suffixes. Both are handled, so the same function reads a router response and
// validates a browser submission.
func ParseRate(raw string) Rate {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Rate{}
	}
	m := rateRe.FindStringSubmatch(s)
	if m == nil {
		return Rate{}
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return Rate{}
	}
	return Rate{Bps: int64(math.Round(n * rateMult[m[2]])), Set: true}
}

// ParsePair splits a simple queue's `upload/download` pair.
//
// Simple queues express every limit and counter as a pair; queue trees use a
// single value for the same fields. A single value yields the same number in
// both halves, which is why tree callers read Up only.
func ParsePair(raw string) Pair {
	// The ORIGINAL tests the raw string, not a trimmed one. "   " therefore
	// falls through and is split, and each half parses to absent anyway — the
	// same answer by a different route, and worth not "tidying".
	if raw == "" {
		return Pair{}
	}
	parts := strings.Split(raw, "/")
	up := ParseRate(parts[0])
	if len(parts) > 1 {
		return Pair{Up: up, Down: ParseRate(parts[1])}
	}
	return Pair{Up: up, Down: up}
}

// ── Address arithmetic ──────────────────────────────────────────────────────

var v4Re = regexp.MustCompile(`^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$`)

// v4ToInt converts a dotted quad to a 32-bit value. The second result is false
// if it is not one.
func v4ToInt(ip string) (uint32, bool) {
	m := v4Re.FindStringSubmatch(strings.TrimSpace(ip))
	if m == nil {
		return 0, false
	}
	var n uint32
	for i := 1; i <= 4; i++ {
		o, _ := strconv.Atoi(m[i])
		if o > 255 {
			return 0, false
		}
		n = n*256 + uint32(o)
	}
	return n, true
}

// CIDRContains answers whether `cidr` contains `ip`.
//
// THREE-VALUED ON PURPOSE, which is why it returns two bools. The second is
// `decided`; false means UNDECIDABLE — an interface-name target, an IPv6/IPv4
// mismatch, or anything unparseable. An undecidable answer must not be recorded
// as "no", because the two have different meanings to a reader even though both
// end in "no warning". Keeping them apart is what lets a test prove which branch
// it exercised.
func CIDRContains(cidr, ip string) (contains, decided bool) {
	raw := strings.TrimSpace(cidr)
	if raw == "" {
		return false, false
	}
	slash := strings.Split(raw, "/")
	netPart, bitsPart := slash[0], ""
	if len(slash) > 1 {
		bitsPart = slash[1]
	}

	// IPv6 on either side: not attempted. Simple queues can hold v6 targets, and
	// getting v6 containment subtly wrong is worse than declining to answer.
	if strings.Contains(netPart, ":") || strings.Contains(ip, ":") {
		return false, false
	}

	net, okNet := v4ToInt(netPart)
	addr, okAddr := v4ToInt(ip)
	// An interface name ("WAN1", "bridge") lands here and decides nothing.
	if !okNet || !okAddr {
		return false, false
	}

	bits := 32
	if len(slash) > 1 {
		b, err := strconv.Atoi(bitsPart)
		// Number("") is 0 in the original and Atoi("") is an error here, so the
		// empty suffix in "10.0.0.0/" is handled explicitly rather than falling
		// through to /32.
		if bitsPart == "" {
			b, err = 0, nil
		}
		if err != nil || b < 0 || b > 32 {
			return false, false
		}
		bits = b
	}
	if bits == 0 {
		return true, true // 0.0.0.0/0 contains everything
	}
	mask := ^uint32(0) << (32 - bits)
	return net&mask == addr&mask, true
}

// ── The warning ─────────────────────────────────────────────────────────────

// SimpleQueueValues is what a write would set, or what a freshly-read row
// currently holds.
type SimpleQueueValues struct {
	Target   string
	MaxLimit Pair
	Disabled bool
}

// CheckSimpleQueue answers: would this simple queue throttle MikroDash's own
// connection?
//
// `before` is the freshly-read row being edited, or nil on create. On an edit
// the answer is only "warn" when the change makes things WORSE — newly enabled,
// newly covering us, or a lower cap than before. Without that, a comment-only
// edit on a long-standing throttling queue prompts every single time, which is
// precisely how a warning becomes furniture.
//
// floorBps is explicit here where the original defaults it, because Go cannot
// tell an omitted argument from a zero one and a zero floor is a legal thing to
// ask for. Callers pass SelfThrottleFloorBps.
func CheckSimpleQueue(selfAddresses []string, values SimpleQueueValues,
	before *SimpleQueueValues, floorBps int64) Verdict {

	if len(selfAddresses) == 0 {
		return Verdict{Level: "none"} // fail open
	}
	if values.Disabled {
		return Verdict{Level: "none"} // not in force
	}

	// 0 means explicitly unlimited, which throttles nothing.
	capped := false
	for _, r := range []Rate{values.MaxLimit.Up, values.MaxLimit.Down} {
		if r.Set && r.Bps > 0 && r.Bps < floorBps {
			capped = true
			break
		}
	}
	if !capped {
		return Verdict{Level: "none"}
	}

	hit := ""
	for _, a := range selfAddresses {
		if c, decided := CIDRContains(values.Target, a); decided && c {
			hit = a
			break
		}
	}
	if hit == "" {
		return Verdict{Level: "none"}
	}

	if before != nil {
		wasCovering, decided := CIDRContains(before.Target, hit)
		wasCovering = decided && wasCovering
		wasEnabled := !before.Disabled
		gotWorse := !wasEnabled || !wasCovering ||
			lowerThan(values.MaxLimit.Up, before.MaxLimit.Up) ||
			lowerThan(values.MaxLimit.Down, before.MaxLimit.Down)
		if !gotWorse {
			return Verdict{Level: "none"}
		}
	}

	return Verdict{
		Level: "warn", Code: "self-throttle",
		Detail: map[string]any{
			"address":  hit,
			"target":   values.Target,
			"maxLimit": values.MaxLimit,
		},
		Fingerprint: queueFingerprint(values.Target, values.MaxLimit, selfAddresses),
	}
}

// lowerThan reports whether `a` is a real cap that is tighter than `b`.
//
// An ABSENT or unlimited `b` counts as looser than any real cap, which is how
// "was unlimited, now capped" comes out as worse.
func lowerThan(a, b Rate) bool {
	if !a.Set || a.Bps <= 0 {
		return false
	}
	return !b.Set || b.Bps <= 0 || a.Bps < b.Bps
}

// queueFingerprint is a stable identity for the exact inputs a verdict came
// from.
//
// The browser echoes it back to acknowledge the warning. Recomputing it from a
// fresh read means an acknowledgement cannot be carried from a mild queue to a
// harsher one, or replayed against a different write — the same idea as
// round-tripping a row's name, applied to a decision instead of a row.
func queueFingerprint(target string, maxLimit Pair, addresses []string) string {
	a := append([]string(nil), addresses...)
	sort.Strings(a)
	b, _ := json.Marshal([]any{target, maxLimit.Up, maxLimit.Down, a})
	return string(b)
}
