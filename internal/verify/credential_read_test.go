package verify

import (
	"regexp"
	"strings"
	"testing"

	"mikrodash/internal/audit"
)

// ── NO COLLECTOR ASKS A ROUTER FOR A CREDENTIAL ────────────────────────────
//
// The rule this holds up used to live only in prose. `internal/collect/ppp.go`
// said "/ppp/secret IS NEVER READ … a test enforces it across both", and that
// test was the NODE original's: it went at cutover with the rest of the parity
// harness, so the rule spent the entire port with nothing behind it. Nobody
// noticed, because nothing failed - which is the shape of every expired premise
// this repository has been bitten by.
//
// Issue #125 needed `/ppp/secret` read for subscriber management, so the rule
// was narrowed rather than dropped: the menu IS read, the password is NOT. That
// only means anything if something checks, and this is the something.
//
// A payload cannot leak a value the collector never asked the router for, so the
// proplist is the enforcement point and this reads the proplists.
var proplistRe = regexp.MustCompile(`=\.proplist=([^"]*)`)

// literalJoin collapses Go string concatenation so a proplist split across lines
// reads as one string.
//
// `"…a," +\n\t"b…"` is two literals to the compiler and one proplist to the
// router. Without this the scan would see the halves separately and a credential
// sitting just after a line break would be invisible - a checker that reads less
// than the code does is worse than none, because it reports success.
var literalJoin = regexp.MustCompile(`"\s*\+\s*"`)

func TestNoProplistNamesACredential(t *testing.T) {
	root := repoRoot(t)
	files := readFiles(t, root, "internal/collect", func(rel string) bool {
		return strings.HasSuffix(rel, ".go") && !isTestSource(rel)
	})

	found := 0
	for rel, src := range files {
		for _, m := range proplistRe.FindAllStringSubmatch(literalJoin.ReplaceAllString(src, ""), -1) {
			found++
			for _, f := range strings.Split(m[1], ",") {
				f = strings.TrimSpace(f)
				if f == "" {
					continue
				}
				// REUSING THE AUDIT TRAIL'S DEFINITION, so "credential-shaped
				// name" has one meaning in this codebase rather than two that
				// can drift apart. It deliberately leaves `public-key` alone -
				// a public key is public - so the WireGuard read stays green.
				if audit.IsCredentialField(f) {
					t.Errorf("%s asks the router for %q in a proplist.\n"+
						"A collector payload reaches every viewer of the page. "+
						"If a write needs this value, declare it as "+
						"resource.TypeSecret, which travels to the router and "+
						"never back.", rel, f)
				}
			}
		}
	}

	// A FLOOR, because the failure mode of a source scan is finding nothing and
	// passing. If the literals are ever reshaped so this regex stops matching,
	// that must read as a broken checker rather than as a clean bill of health.
	if found < 30 {
		t.Fatalf("only %d proplists parsed out of internal/collect - the scan "+
			"broke, and every credential would then look fine", found)
	}
	t.Logf("%d proplists checked across %d files", found, len(files))
}

// ── AND /ppp/secret IS NEVER READ WITHOUT ONE ──────────────────────────────
//
// The test above is satisfied by a proplist that omits `password`. It is also
// satisfied by NO PROPLIST AT ALL - and a bare `/ppp/secret/print` returns every
// property the menu has, password included. That is the likelier mistake by far:
// removing a proplist reads as simplification, and the payload grows a field
// nobody notices until it is in a browser.
//
// This is deliberately about ONE menu rather than a general rule, because a
// general "every command must carry a proplist" is false - most menus hold
// nothing sensitive and several collectors read them whole on purpose.
func TestTheSecretMenuIsOnlyReadThroughAProplist(t *testing.T) {
	root := repoRoot(t)
	files := readFiles(t, root, "internal/collect", func(rel string) bool {
		return strings.HasSuffix(rel, ".go") && !isTestSource(rel)
	})

	const menu = "/ppp/secret/print"
	seen := 0
	for rel, src := range files {
		joined := literalJoin.ReplaceAllString(src, "")
		// THE WHOLE Cmd LITERAL, NOT THE LINE. `Path:` and `Args:` sit on
		// separate lines in every command in this package, so a line-scoped
		// check reports "no proplist" for a command that has one - which this
		// test did on its first run, against code that was correct. The window
		// runs from the menu to the `}}` that closes the literal.
		for rest := joined; ; {
			i := strings.Index(rest, menu)
			if i < 0 {
				break
			}
			seen++
			window := rest[i:]
			if end := strings.Index(window, "}}"); end >= 0 {
				window = window[:end]
			}
			if !strings.Contains(window, "=.proplist=") {
				t.Errorf("%s reads %s with no proplist.\n"+
					"That returns `password` in clear text for every account on "+
					"the router. The proplist is not a bandwidth optimisation "+
					"here, it is the only thing keeping account passwords out of "+
					"the payload.", rel, menu)
			} else if strings.Contains(window, "password") {
				t.Errorf("%s names `password` in the %s proplist", rel, menu)
			}
			rest = rest[i+len(menu):]
		}
	}
	if seen == 0 {
		t.Fatalf("no read of %s found in internal/collect - either the PPP "+
			"secrets feature was removed, in which case delete this check, or "+
			"the command was reshaped and this check is now reading nothing", menu)
	}
}

// ── WHAT THE TWO ABOVE DO NOT COVER ─────────────────────────────────────────
//
// Written down because a gap written down is a gap somebody can close, and a gap
// left to be discovered is how the last one survived a whole port. A comment,
// not a test: it was `TestCredentialReadLedgerIsNotEmpty`, a single t.Log that
// could not fail and was counted as a check (review loop, 2026-09-19).
//
//  1. COMMANDS WITH NO PROPLIST ARE NOT CHECKED. Most reads in
//     `internal/collect` fetch a whole menu, which is correct - a menu holding
//     nothing sensitive is cheaper to read whole than to enumerate. But it means
//     the first test can only speak for the menus that DO carry a proplist.
//     `/ppp/secret` is covered by name in the second test precisely because it
//     is the one menu where that gap would cost something.
//
//  2. PAYLOAD STRUCT FIELDS ARE NOT CHECKED, and the reason is that
//     `audit.IsCredentialField` is too broad to be a gate on them. It is
//     deliberately broad for MASKING, where a false positive costs one field
//     name in an audit row. As a gate it fails on three innocent fields already
//     in the tree - `fasttrackBypassable`, `passwordPolicy` and this feature's
//     own `secrets` - so a struct-tag version would have to either weaken the
//     pattern or carry an exception list, and both are worse than saying plainly
//     that the proplist is where this is enforced.
//
//  3. THE WRITE PATH READS WHAT IT LIKES. `internal/server`'s `readMenu` prints
//     a whole menu with no proplist before every save, delete and action -
//     deliberately, because `ReadOnlyWhen` needs properties no page asks for.
//     So passwords DO enter server memory on a write. They stop there
//     (`RowValues` drops secret-typed fields, the audit masks them), but "no
//     password is ever read" would be false and this file does not claim it.
//     Narrowing that read is a change to shared machinery and is not this
//     feature's to make.
