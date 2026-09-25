package collect

// Does a ported collector still ask the router for what the Node one asks for?
//
// WHY THIS EXISTS. The DNS collector gained four properties on the Node side —
// `text`, `mx-exchange`, `ns`, `srv-target` — and nothing here noticed. The
// golden did not move, because the fixture predated the change and the replay
// falls back to a same-command match when the parameters differ; the Go port
// went on sending the old proplist and quietly returned less than the page
// needed. It was caught by `api-surface.js --check` going stale, which is luck
// rather than a gate.
//
// PLAN.md names this risk directly: "Two codebases moving at once. Feature work
// on Node during the strangle widens the gap." A differential gate that only
// compares payloads cannot see it, because both sides are being fed the same
// recorded rows. This compares the QUESTION rather than the answer.
//
// It reads the live source, so it SKIPS rather than fails when that source is
// absent — `go test ./...` has to work in a checkout that has no sibling
// MikroDash. A skip says so out loud; it does not pass silently.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"mikrodash/internal/routeros"
)

// Each ported command, and where the Node side declares the same read.
var driftCases = []struct {
	name string
	cmd  routeros.Cmd
	file string // under src/collectors/
}{
	{"dns settings", dnsSettingsCmd, "dns.js"},
	{"dns static", dnsStaticCmd, "dns.js"},
	{"bridges", bridgeCmd, "bridges.js"},
	{"bridge ports", bridgePortCmd, "bridges.js"},
	{"bridge hosts", bridgeHostCmd, "bridges.js"},
	{"vlan interfaces", vlanCmd, "vlans.js"},
	{"bridge vlan table", bridgeVlanCmd, "vlans.js"},
	{"vlan bridge ports", vlanPortCmd, "vlans.js"},
	{"wan detect-internet", wanDetectCmd, "wan.js"},
	{"wan dhcp client", wanDhcpCmd, "wan.js"},
	{"wan routes", wanRouteCmd, "wan.js"},
	{"wan addresses", wanAddrCmd, "wan.js"},
	{"wan interfaces", wanIfaceCmd, "wan.js"},
	{"packages", packageCmd, "packages.js"},
	{"packages routerboard", routerboardCmd, "packages.js"},
	{"routes v4", routeV4Cmd, "routing.js"},
	{"routes v6", routeV6Cmd, "routing.js"},
	// system.js declares this proplist TWICE — once on the poll path and once on
	// the interval stream — with identical fields. This side polls, so it sends
	// one; the gate compares against every declaration the file makes, so a
	// change to either is a failure here.
	{"system resource", systemResourceCmd, "system.js"},
	{"log backlog", logPrintCmd, "logs.js"},
}

// liveSrc locates the reference, and says whether it is there.
//
// IT NO LONGER SKIPS. This gate used to `t.Skipf` without the reference, which
// CLAUDE.md already named as the hazard it is — "a gate that never runs". That
// was tolerable while the reference was merely sometimes absent. After cutover it
// is always absent, and a permanent skip is a deleted gate with extra steps.
//
// So the live proplists are RECORDED in testdata and the gate reads those. When a
// reference IS present the recording is re-derived and compared, so it cannot go
// stale unnoticed — the same shape as the JavaScript gates' frozen goldens.
func liveSrc(t *testing.T) (string, bool) {
	t.Helper()
	root := os.Getenv("MIKRODASH_SRC")
	if root == "" {
		root = filepath.Join("..", "..", "..", "MikroDash")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(abs, "src", "collectors")); err != nil {
		return "", false
	}
	return abs, true
}

// recordedProplistsFile holds what each live collector declared, keyed by case
// name. Regenerate with MIKRODASH_PROPLIST_FREEZE=1 and a reference present.
const recordedProplistsFile = "testdata/live-proplists.json"

func recordedProplists(t *testing.T) map[string][]string {
	t.Helper()
	b, err := os.ReadFile(recordedProplistsFile)
	if err != nil {
		t.Fatalf("no recorded proplists at %s (%v). Regenerate with a reference present: "+
			"MIKRODASH_PROPLIST_FREEZE=1 go test ./internal/collect/ -run Proplist",
			recordedProplistsFile, err)
	}
	var m map[string][]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s is not readable: %v", recordedProplistsFile, err)
	}
	// Not while FREEZING — the file is about to be rewritten, and refusing to read
	// a short one would make the recording impossible to create.
	if os.Getenv("MIKRODASH_PROPLIST_FREEZE") == "" && len(m) < len(driftCases) {
		t.Fatalf("%s records %d cases but there are %d — the recording is short, and the "+
			"missing ones would silently pass", recordedProplistsFile, len(m), len(driftCases))
	}
	return m
}

// A single-quoted JavaScript string. The collectors use no escaped quotes
// inside a proplist, and a proplist containing one would not be a proplist.
var jsString = regexp.MustCompile(`'([^']*)'`)

// resolveProplistVar follows a bare identifier argument to its declaration.
//
// Only the simple shape is followed — `const NAME = '…'`, possibly across
// concatenated literals — because that is the shape the collectors actually
// use. Anything computed resolves to "" and the caller reports a missing
// proplist, which is the safe direction: a gate that guessed would be worse
// than one that says it cannot tell.
func resolveProplistVar(src, arg string) string {
	// The argument text still carries the punctuation around it — the call site
	// reads `('/ip/route/print',   [proplist]` and the scan stops at the first
	// `]`, so `arg` is ",   [proplist". Take the identifier out of it rather
	// than trying to trim every bracket and comma that might be there.
	ident := regexp.MustCompile(`[A-Za-z_$][\w$]*`).FindString(arg)
	if ident == "" {
		return ""
	}
	name := ident
	decl := regexp.MustCompile(`(?:const|let|var)\s+` + regexp.QuoteMeta(name) + `\s*=\s*([^;]+);`)
	m := decl.FindStringSubmatch(src)
	if m == nil {
		return ""
	}
	joined := ""
	for _, lit := range jsString.FindAllStringSubmatch(m[1], -1) {
		joined += lit[1]
	}
	return joined
}

// proplistsFor returns every proplist the Node file declares for a menu.
//
// The declaration is an array literal — `['<menu>', '<proplist>']` — and the
// proplist may be SPLIT ACROSS CONCATENATED LITERALS, which dns.js now does to
// fit the MX/NS/SRV properties. Reading only the first literal is exactly the
// bug that made tools/api-surface.js under-report the surface while claiming to
// be current, so every literal up to the closing bracket is joined.
func proplistsFor(src, menu string) []string {
	var out []string
	for _, idx := range regexp.MustCompile(regexp.QuoteMeta("'"+menu+"'")).FindAllStringIndex(src, -1) {
		end := strings.Index(src[idx[1]:], "]")
		if end < 0 {
			continue
		}
		arg := src[idx[1] : idx[1]+end]

		joined := ""
		for _, m := range jsString.FindAllStringSubmatch(arg, -1) {
			joined += m[1]
		}
		// A PROPLIST HELD IN A VARIABLE IS STILL A PROPLIST. routing.js writes
		// `const proplist = '=.proplist=…'` once and passes `[proplist]` to both
		// route menus, so there is no literal at the call site at all. Reading
		// only literals made this gate report a false failure for /ip/route and,
		// worse, a false PASS for /ipv6/route — which matched a literal belonging
		// to a different call site in the same file. A gate that can be satisfied
		// by the wrong evidence is not a gate.
		if joined == "" {
			joined = resolveProplistVar(src, arg)
		}
		if strings.HasPrefix(joined, "=.proplist=") {
			out = append(out, joined)
		}
	}
	return out
}

func TestProplistsMatchTheLiveCollectors(t *testing.T) {
	root, haveRef := liveSrc(t)
	cache := map[string]string{}
	recorded := recordedProplists(t)

	// WITH A REFERENCE, THE RECORDING IS RE-DERIVED AND CHECKED. Without this the
	// frozen copy could drift from the source it claims to describe and nothing
	// would say so — which is the failure the recording exists to avoid, moved one
	// step along.
	if haveRef {
		fresh := map[string][]string{}
		for _, c := range driftCases {
			src, ok := cache[c.file]
			if !ok {
				b, err := os.ReadFile(filepath.Join(root, "src", "collectors", c.file))
				if err != nil {
					t.Fatalf("cannot read %s: %v", c.file, err)
				}
				src = string(b)
				cache[c.file] = src
			}
			fresh[c.name] = proplistsFor(src, c.cmd.Path)
		}
		if os.Getenv("MIKRODASH_PROPLIST_FREEZE") != "" {
			b, _ := json.MarshalIndent(fresh, "", "  ")
			if err := os.WriteFile(recordedProplistsFile, append(b, '\n'), 0o644); err != nil {
				t.Fatalf("writing %s: %v", recordedProplistsFile, err)
			}
			t.Logf("froze %d live proplist case(s) -> %s", len(fresh), recordedProplistsFile)
			recorded = fresh
		} else {
			for _, c := range driftCases {
				if !slices.Equal(fresh[c.name], recorded[c.name]) {
					t.Errorf("the recording for %q no longer matches %s:\n  recorded: %v\n  live    : %v\n"+
						"  Regenerate with MIKRODASH_PROPLIST_FREEZE=1.", c.name, c.file,
						recorded[c.name], fresh[c.name])
				}
			}
		}
	}

	for _, c := range driftCases {
		t.Run(c.name, func(t *testing.T) {

			// What the Go side sends, if anything.
			ours := ""
			for _, a := range c.cmd.Args {
				if strings.HasPrefix(a, "=.proplist=") {
					ours = a
				}
			}

			theirs := recorded[c.name]
			if len(theirs) == 0 {
				if ours == "" {
					return // neither side uses a proplist for this menu
				}
				t.Errorf("%s sends a proplist for %s and %s declares none —\n  ours: %s",
					c.name, c.cmd.Path, c.file, ours)
				return
			}

			for _, want := range theirs {
				if want == ours {
					return
				}
			}
			// Report the FIELD difference, not two long strings: the useful
			// question is always which properties were gained or lost.
			t.Errorf("proplist drift on %s (%s)\n  ours  : %s\n  theirs: %s\n  %s",
				c.cmd.Path, c.file, ours, theirs[0], describeDiff(ours, theirs[0]))
		})
	}
}

func describeDiff(ours, theirs string) string {
	set := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, f := range strings.Split(strings.TrimPrefix(s, "=.proplist="), ",") {
			if f = strings.TrimSpace(f); f != "" {
				m[f] = true
			}
		}
		return m
	}
	a, b := set(ours), set(theirs)
	var missing, extra []string
	for f := range b {
		if !a[f] {
			missing = append(missing, f)
		}
	}
	for f := range a {
		if !b[f] {
			extra = append(extra, f)
		}
	}
	parts := []string{}
	if len(missing) > 0 {
		parts = append(parts, "the live collector now asks for: "+strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		parts = append(parts, "we ask for what it no longer does: "+strings.Join(extra, ", "))
	}
	if len(parts) == 0 {
		return "the same fields in a different order"
	}
	return strings.Join(parts, "; ")
}
