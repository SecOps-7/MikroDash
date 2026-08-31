package store

import (
	"testing"
	"time"
)

// #117: an explicit EMPTY `siteIds` means "no sites", not "no answer".
//
// The live `_rtrSiteIds` tests `Array.isArray` before falling back to the
// scalar, so an empty array wins over a non-empty `siteId`. Falling through
// there — which `len(r.SiteIDs) > 0` does, and it is the natural Go spelling —
// resurrects a membership the operator had just cleared, and on this path that
// means restoring access a site grant confers.
func TestRouterSiteIDsNormalisation(t *testing.T) {
	cases := []struct {
		name string
		rec  Router
		want []string
	}{
		{"neither", Router{}, nil},
		{"only the mirror", Router{SiteID: "s1"}, []string{"s1"}},
		{"an array", Router{SiteIDs: []string{"s1", "s2"}}, []string{"s1", "s2"}},
		{"an EMPTY array beats the mirror", Router{SiteIDs: []string{}, SiteID: "s1"}, []string{}},
		{"an array beats the mirror", Router{SiteIDs: []string{"s2"}, SiteID: "s1"}, []string{"s2"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RouterSiteIDs(c.rec)
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("got %v, want %v", got, c.want)
				}
			}
		})
	}
}

// TestAMissingUserCostsTheSameAsARealOne.
//
// ── A TIMING TEST, WHICH IS UNUSUAL HERE AND IS THE ONLY WAY TO SEE THIS ────
//
// `verifyPassword` in users.js hashes against `_DUMMY_SALT` and DISCARDS the
// result before answering false for a user that does not exist. The comment
// says why: "login timing does not reveal whether a username exists (username
// enumeration oracle)". This port returned false immediately until 2026-08-27,
// which is the obvious reading of the code and is wrong.
//
// Nothing else could have caught it. The function's ANSWER is identical either
// way — false is false — so a corpus comparing verdicts passes against both, and
// mutation testing on the return value finds nothing. The only observable is how
// long it takes, so that is what this asserts.
//
// THE RATIO IS DELIBERATELY LOOSE. scrypt at N=16384 takes tens of milliseconds
// and the point is to separate "did the work" from "returned in microseconds",
// a difference of three orders of magnitude. A tight bound would be flaky on a
// loaded machine and would be testing the scheduler rather than the code. Ten
// times is far below the gap being defended and far above ordinary jitter.
func TestAMissingUserCostsTheSameAsARealOne(t *testing.T) {
	real := User{
		Username: "someone",
		// A real 64-hex salt and a hash that will not match, so the comparison
		// fails on the VALUE rather than short-circuiting on a missing field.
		Salt:         "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		PasswordHash: "00",
	}
	missing := User{Username: "nobody"}

	timeIt := func(u User) time.Duration {
		// Best of three: this measures a floor, and a floor is what matters —
		// the leak is "one path can be fast", not "one path is slow on average".
		best := time.Duration(1<<62 - 1)
		for i := 0; i < 3; i++ {
			start := time.Now()
			if VerifyPassword(u, "a-candidate-password") {
				t.Fatal("a deliberately wrong password verified")
			}
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}

	realD, missD := timeIt(real), timeIt(missing)
	if realD <= 0 {
		t.Fatalf("the real path took no measurable time (%v) -- the clock or the test is wrong, "+
			"and the comparison below would prove nothing", realD)
	}
	if missD*10 < realD {
		t.Errorf("a MISSING user answered in %v where a real one took %v -- more than 10x "+
			"faster, so login timing reveals whether a username exists. The live "+
			"verifyPassword hashes against _DUMMY_SALT and discards it for exactly this reason",
			missD, realD)
	}
}
