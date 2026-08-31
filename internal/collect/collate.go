package collect

// String ordering that matches JavaScript's String.prototype.localeCompare.
//
// WHY THIS EXISTS. Several collectors sort their rows before emitting, and the
// browser renders that order verbatim — the DNS static table is sorted by name
// in src/collectors/dns.js. Go's native string comparison is by byte, so
// "MikroTik" lands second in a nine-row table; V8 sorts it eighth, between
// "i-live-cache.akamaized.net" and "r-live-cache.akamaized.net". A page that
// lists the same rows in a different order has not been ported.
//
// It is a table, not an algorithm, and the table was MEASURED rather than
// reasoned about: every printable ASCII character was sorted through
// localeCompare in the same Node build the app runs on, and the resulting order
// is transcribed below. Two properties of that measurement are worth recording,
// because both contradict what one would assume from reading about the Unicode
// Collation Algorithm:
//
//   - Punctuation is NOT ignorable. "a-b" < "ab", because '-' sorts before 'b'
//     rather than being skipped. So each character contributes exactly one
//     primary weight and the comparison is element-wise.
//   - Case is a lower-strength difference, not a primary one. 'a' and 'A' share
//     a primary weight and lowercase wins the tiebreak, which is why case is a
//     second pass here rather than part of the first.
//
// The second pass is what a single remapped byte comparison would get wrong:
// "abd" vs "ABc" differ in case at the first character and in letter at the
// third, and only comparing all the letters first gets "ABc" in front.
//
// LIMIT, stated rather than hidden: this covers printable ASCII, which is what
// RouterOS identifiers, DNS names and interface names are. Anything else sorts
// after all of it, by code point. A collector whose rows carry non-ASCII names
// and are sorted for display needs this revisited — the golden corpus is what
// would catch it, since the goldens are produced by V8 itself.

// asciiOrder is printable ASCII in localeCompare order, case-folded: each
// letter appears once, at the position its lowercase form occupies.
const asciiOrder = " _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$0123456789abcdefghijklmnopqrstuvwxyz"

var primaryWeight [128]int

func init() {
	// Unlisted ASCII (the control characters) sorts before everything listed,
	// which is arbitrary but consistent — none of them can appear in a RouterOS
	// name, and leaving them at zero would make them collide with ' '.
	for i := range primaryWeight {
		primaryWeight[i] = -1
	}
	for i, r := range asciiOrder {
		primaryWeight[r] = i + 1
		if r >= 'a' && r <= 'z' {
			primaryWeight[r-32] = i + 1 // the uppercase form shares the weight
		}
	}
}

func primary(r rune) int {
	if r < 128 && primaryWeight[r] >= 0 {
		return primaryWeight[r]
	}
	// After every ASCII weight, ordered among themselves by code point.
	return len(asciiOrder) + 1 + int(r)
}

// upper reports the case tiebreak: lowercase and non-letters sort before
// uppercase, matching "a" < "A".
func upper(r rune) int {
	if r >= 'A' && r <= 'Z' {
		return 1
	}
	return 0
}

// Collate compares a and b the way localeCompare does, returning -1, 0 or 1.
func Collate(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	n := len(ra)
	if len(rb) < n {
		n = len(rb)
	}
	for i := 0; i < n; i++ {
		wa, wb := primary(ra[i]), primary(rb[i])
		if wa != wb {
			if wa < wb {
				return -1
			}
			return 1
		}
	}
	if len(ra) != len(rb) {
		if len(ra) < len(rb) {
			return -1
		}
		return 1
	}
	for i := 0; i < n; i++ {
		ca, cb := upper(ra[i]), upper(rb[i])
		if ca != cb {
			if ca < cb {
				return -1
			}
			return 1
		}
	}
	return 0
}
