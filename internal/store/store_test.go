package store

import (
	"os"
	"path/filepath"
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

// ── FIRST RUN IS AN ABSENT FILE, NOT A FAILED READ (issue #124) ─────────────
//
// `GET /api/auth/status` computes `firstRun` from `len(users) == 0`, and
// `firstRun` is what puts the setup wizard on screen INSTEAD of the login form.
// A brand-new /data has no users.json, so returning the raw ENOENT made a fresh
// install answer 500, fall back to Sign In, and offer a username and password
// box for an account that could not exist and could not be created.
//
// The second half is the one that matters more, and it is why this is not simply
// "ignore the error": an empty user list means anyone can claim the first
// administrator account WITHOUT AUTHENTICATING. A users.json that exists and
// cannot be read must therefore stay loud, or a transient read failure on a
// populated system hands the next visitor an admin account.
func TestUsersTreatsOnlyAnAbsentFileAsFirstRun(t *testing.T) {
	t.Run("absent is first run", func(t *testing.T) {
		s := &Store{Dir: t.TempDir()}
		users, err := s.Users()
		if err != nil {
			t.Fatalf("a fresh /data reported %v; the setup wizard never appears "+
				"and the operator is shown a login form for an account that "+
				"cannot exist", err)
		}
		if len(users) != 0 {
			t.Errorf("got %d users from an empty directory", len(users))
		}
	})

	t.Run("empty array is first run too", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, dir, "users.json", "[]")
		s := &Store{Dir: dir}
		users, err := s.Users()
		if err != nil || len(users) != 0 {
			t.Errorf("users=%v err=%v; a bare empty array is a legitimate "+
				"no-accounts state", users, err)
		}
	})

	// ── THE BOUNDARY ───────────────────────────────────────────────────────
	t.Run("an unreadable path is NOT first run", func(t *testing.T) {
		// A DIRECTORY, not a chmod. The suite runs as root in the build
		// container, where a 0000 file is still readable — so a permission-based
		// case SKIPS, and a skipped security test is not a gate. Reading a
		// directory fails with EISDIR for every uid, which exercises the same
		// branch: the path exists and cannot be read.
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "users.json"), 0o700); err != nil {
			t.Fatal(err)
		}
		s := &Store{Dir: dir}
		if _, err := s.Users(); err == nil {
			t.Error("a users.json that EXISTS and could not be read reported no " +
				"error, so the install looks account-less: `firstRun` goes true " +
				"and the next visitor can create an administrator on a " +
				"populated system")
		}
	})

	t.Run("malformed content is NOT first run", func(t *testing.T) {
		dir := t.TempDir()
		// The object-instead-of-array shape the bare-array rule exists for.
		mustWrite(t, dir, "users.json", `{"users":[]}`)
		s := &Store{Dir: dir}
		if _, err := s.Users(); err == nil {
			t.Error("a users.json that is not a bare array reported no error; " +
				"silently seeing zero users is the exact failure that rule " +
				"exists to prevent")
		}
	})
}

func mustWrite(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
