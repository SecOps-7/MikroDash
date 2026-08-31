package collect

// The cases below are not invented. Each pair was run through
// String.prototype.localeCompare in the Node build the app ships on, and the
// expectation recorded here is what V8 answered — including the ones that
// contradict a byte comparison, which are the only reason this code exists.

import (
	"sort"
	"testing"
)

func TestCollateMatchesLocaleCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// Case is a tiebreak, not a primary difference: 'a' beats 'B' even
		// though 'B' is the smaller byte. This is the one that reorders the DNS
		// table.
		{"a", "B", -1},
		{"A", "a", 1},
		{"z", "Z", -1},
		// Punctuation participates rather than being ignored.
		{"a-b", "ab", -1},
		{"a.b", "ab", -1},
		{".", "-", 1},
		{"-", "0", -1},
		{"-", "a", -1},
		{"_", "a", -1},
		// Digits sort before letters of either case.
		{"a1", "aA", -1},
		// Letters are compared to exhaustion before case is consulted at all,
		// which a single remapped byte comparison gets backwards.
		{"abd", "ABc", 1},
		{"ABc", "abd", -1},
		// A prefix is shorter, so it comes first, whatever the case.
		{"MikroTik", "mikrotikx", -1},
		{"", "a", -1},
		{"a", "a", 0},
	}
	for _, c := range cases {
		if got := Collate(c.a, c.b); got != c.want {
			t.Errorf("Collate(%q, %q) = %d, localeCompare says %d", c.a, c.b, got, c.want)
		}
	}
}

// The nine names from the AX3's static DNS table, in the order V8 sorted them.
// A byte sort puts MikroTik second; the router's own page and this one put it
// eighth.
func TestCollateOrdersTheCapturedTable(t *testing.T) {
	want := []string{
		"3b1ccdb7.d.adguard-dns.com",
		"d.adguard-dns.com",
		"dstv.com",
		"dstv.stream",
		"host-name-47e5",
		"host-name-a7ba",
		"i-live-cache.akamaized.net",
		"MikroTik",
		"r-live-cache.akamaized.net",
	}
	got := []string{
		"MikroTik", "r-live-cache.akamaized.net", "host-name-a7ba", "dstv.stream",
		"d.adguard-dns.com", "i-live-cache.akamaized.net", "3b1ccdb7.d.adguard-dns.com",
		"dstv.com", "host-name-47e5",
	}
	sort.SliceStable(got, func(i, j int) bool { return Collate(got[i], got[j]) < 0 })
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: %q, V8 puts %q there\nfull order: %v", i, got[i], want[i], got)
		}
	}
}

func TestCollateIsAntisymmetric(t *testing.T) {
	// A comparator that is not consistent produces an order that depends on the
	// input permutation, which would show up as a flaky golden rather than as a
	// clear failure.
	s := []string{"", "a", "A", "ab", "aB", "Ab", "AB", "a-b", "a.b", "a1", "1a", "_", "MikroTik", "mikrotik"}
	for _, x := range s {
		for _, y := range s {
			if Collate(x, y) != -Collate(y, x) {
				t.Errorf("Collate(%q,%q)=%d but Collate(%q,%q)=%d",
					x, y, Collate(x, y), y, x, Collate(y, x))
			}
		}
	}
}
