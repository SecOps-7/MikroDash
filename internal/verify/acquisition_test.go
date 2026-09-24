package verify

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestSetBIsDeclared is the ledger for the two ways this app gets data out of a
// router: table reads on one side, measurements and streams on the other.
//
// ── WHY THE SECOND SET NEEDS A LEDGER AND THE FIRST DOES NOT ────────────────
//
// A table read is the default and the safe case. It caches, it dedupes, it
// tolerates being a second old, and everything phase 1 built is about it. About
// sixty of the sixty-four commands in `internal/collect` are one, and a
// sixty-first arriving needs no announcement.
//
// The other set is five call sites across two menus, and between them they carry
// the most expensive acquisition in the app. They obey none of phase 1's rules:
// no cache can serve them, no union of field lists applies, and the cadence work
// that took ifStatus from 204 commands a minute to 58 does not reach the one
// read still costing 52. They are going to need their own model, and the first
// requirement of designing one is knowing exactly what is in the set.
//
// ── IT FAILS IN BOTH DIRECTIONS ─────────────────────────────────────────────
//
// A new measurement or stream that nobody recorded is a failure: it has joined
// the expensive set silently, which is how the ifStatus rates read went unnoticed
// as 76% of an idle router's load. A recorded one that has gone is also a
// failure, because the second track is meant to shrink this list and a list that
// does not shrink stops describing anything.
func TestSetBIsDeclared(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "collect")

	// key is "<file>: <menu>", so the same menu on both sides of the line -- which
	// both of these menus are -- reads as the two different questions it is.
	declared := map[string]string{
		"ifstatus.go: /interface/monitor-traffic": "measurement: interface rates, taken at an instant on a " +
			"named set of interfaces. THE FALLBACK since B.7 -- it runs only when the shared " +
			"channel has no rows for an interface, which is a fresh session or a router that " +
			"refused the stream. It was ~52 commands a minute and is the largest thing B.7 removed.",
		"monitortraffic.go: /interface/monitor-traffic": "stream: ONE channel, two holders. The traffic " +
			"chart wants every row; ifStatus wants a snapshot of the rates. It was two channels " +
			"and one command per holder until B.7's merge, and the command is built here now " +
			"precisely so neither collector is the authority on the other's needs.",
		"ping.go: /tool/ping":     "stream: the configured ping target.",
		"topology.go: /tool/ping": "measurement: one bounded ping per discovered node.",
		"logs.go: /log/listen":    "stream: the only one identified by its path rather than an argument.",
	}

	found := map[string]bool{}
	queries := 0

	// The classification is KindOf's, restated here against source text because a
	// gate that imported the collector package would be asserting that a function
	// agrees with itself.
	// ── A PATH MAY BE A CONSTANT, AND IT WAS INVISIBLE UNTIL 2026-09-10 ─────
	//
	// This matched a STRING LITERAL after `Path:` and nothing else. When the
	// monitor-traffic command moved into its own file and named its menu by the
	// const the two collectors share, the ledger simply stopped seeing it - the
	// entry read as a gap that had CLOSED, which is the one failure direction
	// that looks like progress. The menu had not gone anywhere.
	//
	// So package-level `const NAME = "/menu"` is resolved first. Same fix as
	// L.4's in `sharedmenu_test.go`, which resolves `scheduled{menu: xxxCmd.Path}`
	// for the same reason and after the same kind of miss.
	consts := menuConsts(t, dir)
	cmd := regexp.MustCompile(`(?s)routeros\.Cmd\{\s*Path:\s*(?:"(/[^"]+)"|(\w+))(.*?)\}\}?`)
	arg := regexp.MustCompile(`"(=[^"]*)"`)

	for _, name := range collectGoFiles(t, dir) {
		src := mustRead(t, filepath.Join(dir, name))
		for _, m := range cmd.FindAllStringSubmatch(src, -1) {
			menu, rest := m[1], m[3]
			if menu == "" {
				var ok bool
				if menu, ok = consts[m[2]]; !ok {
					continue // a Path built from something this cannot resolve
				}
			}
			kind := "query"
			if strings.HasSuffix(menu, "/listen") {
				kind = "stream"
			} else {
				interval := false
				for _, a := range arg.FindAllStringSubmatch(rest, -1) {
					switch {
					case strings.HasPrefix(a[1], "=once="), strings.HasPrefix(a[1], "=count="):
						kind = "measurement"
					case strings.HasPrefix(a[1], "=interval="):
						interval = true
					}
				}
				if kind == "query" && interval {
					kind = "stream"
				}
			}
			if kind == "query" {
				queries++
				continue
			}
			key := name + ": " + menu
			found[key] = true
			if declared[key] == "" {
				t.Errorf("%s is a %s and is not declared in the set B ledger. It obeys none of "+
					"phase 1's rules -- no cache can serve it and no field-list union applies -- "+
					"so it has joined the expensive set silently.", key, kind)
			}
		}
	}

	for key, why := range declared {
		if !found[key] {
			t.Errorf("the ledger carries %q, which is no longer a measurement or a stream. "+
				"A recorded gap that has closed is a failure here; drop the entry. (held: %s)",
				key, why)
		}
	}

	// ── THE LEDGER MUST PROVE ITS OWN CLASSIFIER IS ALIVE ───────────────────
	//
	// Every entry matching is the passing state, so a classifier that returned
	// "query" for everything would produce an empty set B and pass every
	// assertion above by looking at nothing.
	if queries < 40 {
		t.Fatalf("only %d table reads were classified; there are around sixty, so the command "+
			"scan has stopped matching and this ledger checks nothing", queries)
	}
	if len(found) != len(declared) {
		t.Errorf("set B has %d members and the ledger declares %d: %v",
			len(found), len(declared), sortedStrings(found))
	}
	t.Logf("%d table reads, %d measurements and streams", queries, len(found))
}

// menuConsts maps a package-level constant to the menu path it holds.
//
// Only paths: a const whose value does not start with `/` is not a menu, and
// including it would let an unrelated string be matched as one.
func menuConsts(t *testing.T, dir string) map[string]string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^(?:const\s+)?\s*(\w+)\s*=\s*"(/[^"]+)"`)
	out := map[string]string{}
	for _, name := range collectGoFiles(t, dir) {
		for _, m := range re.FindAllStringSubmatch(mustRead(t, filepath.Join(dir, name)), -1) {
			out[m[1]] = m[2]
		}
	}
	if len(out) == 0 {
		t.Fatal("no menu constant was resolved; a Path named by a const would be " +
			"invisible to this ledger, which is how the monitor-traffic entry " +
			"silently read as closed on 2026-09-10")
	}
	return out
}

func collectGoFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".go") && !isTestSource(filepath.Join(dir, n)) {
			out = append(out, n)
		}
	}
	return out
}

func sortedStrings(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
