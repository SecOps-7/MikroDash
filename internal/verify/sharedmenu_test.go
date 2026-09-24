package verify

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEverySharedMenuIsRoutedOrExplained is the ledger for step 1.4 of the
// collector rewrite: every RouterOS menu read by more than one collector either
// goes through the coalescing cache, or says here why it cannot.
//
// ── WHY IT NEEDS A LEDGER ───────────────────────────────────────────────────
//
// Sharing is invisible at both ends. A collector that starts reading a menu
// another one already reads costs a router an extra API channel, which is the
// documented bottleneck, and NOTHING anywhere fails. The two files need not even
// mention each other. This is the only place the relation is written down.
//
// ── WHAT IT CAN AND CANNOT PROVE ────────────────────────────────────────────
//
// Honest about its own limits, because a check that overstates them is worse
// than none. The routing usually lives in a collector's `read` helper rather
// than at the call site -- that is where the availability latches are -- so this
// cannot tell WHICH menu a given readVia serves. What it does assert:
//
//	a menu read by two collectors is listed here, or the test fails
//	a listed menu that is no longer shared fails too, so an entry cannot
//	  outlive the situation it describes
//	a menu marked routed whose consumers do not call readVia at all fails
//	a menu marked unroutable with no reason fails
//
// The middle two are what caught nothing yet and are meant to. The first is what
// catches the next collector to reach for a menu somebody already reads.
func TestEverySharedMenuIsRoutedOrExplained(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "collect")

	// why is empty for a routed menu, and states the obstacle otherwise.
	ledger := map[string]string{
		"/interface/wifi/print":                        "",
		"/interface/wifi/registration-table/print":     "",
		"/interface/wireless/print":                    "",
		"/interface/wireless/registration-table/print": "",
		"/interface/wifi/capsman/remote-cap/print":     "",
		"/interface/vlan/print":                        "",
		"/ip/address/print":                            "",
		"/interface/bridge/port/print":                 "",
		"/interface/bridge/host/print":                 "",
		"/interface/detect-internet/state/print":       "",
		"/system/routerboard/print":                    "",
		"/system/package/update/print":                 "",
		"/ppp/active/print":                            "",
		"/ip/route/print":                              "",
		"/interface/print":                             "",
		// `/ip/firewall/connection/print` WAS HERE, routed since 3.2d: both
		// `connections.go` and `bandwidth.go` subscribed with byte-identical
		// proplists, one read with two deliveries. Since 2026-09-21 bandwidth
		// uses `connsCmd` itself (`bandwidthConnCmd = connsCmd`), so the menu has
		// one declaration: the two cannot diverge and stop coalescing, which is
		// what this entry watched for. The scan now sees one declaring file and
		// the ledger asked for the entry to go.

		// `/interface/monitor-traffic` WAS HERE, and the entry named the condition
		// under which it would leave: "SLATED FOR B.1: a stream can BACK an entry
		// when its rows are successive readings of a keyed value, which these are
		// -- at which point this becomes routed and the entry moves up".
		//
		// B.7's merge is that point. Both collectors hold ONE shared fill, the
		// command is built in `monitortraffic.go` so neither is the authority on
		// the other's needs, and the menu is declared once rather than twice - so
		// it is not a shared menu any more in the sense this ledger means.
		//
		// The `=once=` measurement in `ifstatus.go` survives as the FALLBACK for
		// an interface the channel has no rows for, which is what keeps that file
		// in the set B ledger next door - and it is why this menu is still read by
		// two files and still belongs here, now as ROUTED rather than exempt.
		//
		// "Routed" for a streamed menu means STREAM-FILLED: one channel backs the
		// entry both holders read, which is what `readVia` means for a polled one.
		"/interface/monitor-traffic": "",
		"/tool/ping": "ONE stream and one BOUNDED per-device read (topology sends =count=1), " +
			"not two streams. Parameterised by address, so the menu is not the question -- " +
			"and permanently unroutable for a second reason: each row is a DISTINCT " +
			"measurement the collector counts into min/max/avg/loss, not a fresh reading " +
			"of one value. A rolling entry would report 0% loss for ever and fail nothing",
	}

	consumers := sharedMenus(t, dir)
	subscribed := subscribedMenus(t, dir)

	for menu, files := range consumers {
		why, listed := ledger[menu]
		if !listed {
			t.Errorf("%s is read by %v and is not in the shared-menu ledger. Route it through "+
				"the cache (see internal/collect/cache.go) or record here why it cannot be.",
				menu, files)
			continue
		}
		if why != "" {
			// ── L.3: AN EXEMPTION IS RE-CHECKED, NOT TAKEN ON TRUST ────────
			//
			// This used to `continue` here, and that one line is how
			// `/ip/firewall/connection/print` spent a phase claiming it could
			// not be routed while both its consumers were subscribed to it. An
			// entry with a reason was never compared against reality again, so a
			// closed gap could sit here indefinitely saying the opposite -- the
			// exact failure "a recorded gap that has CLOSED is also a failure"
			// exists to prevent, missing from the one branch nobody applied it
			// to.
			//
			// It cost more than tidiness: a session on 2026-09-09 read this
			// entry, believed it, and argued a double-parse objection that had
			// been measured wrong months earlier.
			if f, ok := subscribed[menu]; ok {
				t.Errorf("%s is recorded as unroutable, but %s SUBSCRIBES to it. The "+
					"exemption has closed; move it to the routed list.\n(reason held: %q)",
					menu, f, why)
			}
			continue
		}
		if _, ok := subscribed[menu]; ok {
			continue // routed by subscription
		}
		// ── A STREAMED MENU IS ROUTED BY ITS FILL, NOT BY `readVia` ─────────
		//
		// `readVia` is the polled path: ask the cache, and it reads if it must.
		// A stream-filled menu is routed a different way - one channel keeps the
		// entry current and every holder reads that entry - and requiring
		// `readVia` of it would demand the one shape it cannot have.
		//
		// `JoinStream` is the marker because it is the SHARED form: a menu two
		// files read and one of them opens with `FillFromStream` would be a
		// single owner refusing the other, which is not routed at all.
		if streamJoined(t, dir, files) {
			continue
		}
		for _, f := range files {
			// `readShared` is the table core's readVia at the collector's interval
			// (internal/collect/table.go), so a table collector routes through it.
			src := mustRead(t, filepath.Join(dir, f))
			if !strings.Contains(src, "readVia(") && !strings.Contains(src, "readShared") {
				t.Errorf("%s is marked routed, but %s neither calls readVia, subscribes "+
					"to it, nor joins a shared fill. Either it stopped reading through the "+
					"cache or the entry is wrong.", menu, f)
			}
		}
	}

	for menu, why := range ledger {
		if len(consumers[menu]) > 1 {
			continue
		}
		t.Errorf("the ledger carries %s, which no longer has more than one consumer (%v). "+
			"A recorded gap that has closed is a failure here; drop the entry. (reason held: %q)",
			menu, consumers[menu], why)
	}
}

// streamJoined reports whether one of these files joins a shared stream fill.
//
// ONE IS ENOUGH, and that is the shape rather than a concession: the join is
// written once, in the file that owns the merge rule, and the other holders call
// it. Demanding the call in every file would demand that each one build the
// command for itself, which is exactly what having a merge rule removed.
func streamJoined(t *testing.T, dir string, files []string) bool {
	t.Helper()
	for _, f := range files {
		if strings.Contains(mustRead(t, filepath.Join(dir, f)), "JoinStream(") {
			return true
		}
	}
	return false
}

// sharedMenus maps each RouterOS menu declared in more than one collector to the
// files that declare it.
//
// NON-TEST SOURCE ONLY. A scan that read this directory's tests would find the
// menus quoted in fixtures and stubs and call them consumers, and it would find
// this file's own ledger if it ever moved here.
func sharedMenus(t *testing.T, dir string) map[string][]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	// ── A PATH NAMED BY A CONSTANT IS STILL A DECLARATION ──────────────────
	//
	// This matched a string literal only. `monitortraffic.go` names its menu by
	// the const both collectors share, so without resolution a menu could move
	// behind a const and this ledger would read it as no longer shared - a gap
	// closing by going invisible, which is the direction that looks like
	// progress. The same miss hit `acquisition_test.go` on 2026-09-10 and it is
	// fixed there too.
	consts := menuConsts(t, dir)
	decl := regexp.MustCompile(`routeros\.Cmd\{\s*Path:\s*(?:"(/[^"]+)"|(\w+))`)
	byMenu := map[string]map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || isTestSource(filepath.Join(dir, n)) {
			continue
		}
		src := mustRead(t, filepath.Join(dir, n))
		for _, m := range decl.FindAllStringSubmatch(src, -1) {
			menu := m[1]
			if menu == "" {
				var ok bool
				if menu, ok = consts[m[2]]; !ok {
					continue
				}
			}
			if byMenu[menu] == nil {
				byMenu[menu] = map[string]bool{}
			}
			byMenu[menu][n] = true
		}
	}
	out := map[string][]string{}
	for menu, files := range byMenu {
		if len(files) < 2 {
			continue
		}
		list := make([]string, 0, len(files))
		for f := range files {
			list = append(list, f)
		}
		sort.Strings(list)
		out[menu] = list
	}
	if len(out) == 0 {
		t.Fatal("no shared menus found at all - the declaration pattern stopped matching")
	}
	return out
}

// subscribedMenus maps each menu a collector routes by SUBSCRIPTION to the file
// that does it.
//
// ── L.4: "ROUTED" MEANT `readVia` AND THAT PREDATES THE SCHEDULER ───────────
//
// Phase 1 routed a menu by calling `readVia` on the cache, so looking for that
// call was the whole test. Phase 3 gave collectors a second way in: a
// `scheduled` literal naming a menu, which subscribes and is delivered to. That
// is routing by any measure -- one read, many deliveries -- and neither
// `connections.go` nor `bandwidth.go` contains `readVia(` at all.
//
// So marking `/ip/firewall/connection/print` routed would have failed this test
// for entirely the wrong reason, and the fix for L.2 needed this one first.
//
// ── AND IT ANSWERS A QUESTION `readVia` CANNOT ─────────────────────────────
//
// This file's header records a limit: routing usually lives in a collector's
// `read` helper, so the scan "cannot tell WHICH menu a given readVia serves".
// A subscription does not have that problem -- `scheduled{menu: xxxCmd.Path}`
// names its menu, and `xxxCmd` is declared with a literal path in the same file.
// Resolving one to the other gives an exact menu-to-file mapping, which is what
// makes the L.3 inverse check possible at all: "this menu is not routed" is only
// assertable when routing can be located precisely.
//
// The limit therefore still stands for the `readVia` half and no longer stands
// for the subscription half, which is now most of them.
func subscribedMenus(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	// `xxxCmd = routeros.Cmd{Path: "/menu"` - the declaration, per file.
	decl := regexp.MustCompile(`(\w+)\s*=\s*routeros\.Cmd\{\s*Path:\s*"(/[^"]+)"`)
	// `menu: xxxCmd.Path` inside a scheduled literal, or `cmd: xxxCmd,` inside a
	// table collector's tableSpec (internal/collect/table.go), which subscribes to
	// that command's menu.
	sub := regexp.MustCompile(`menu:\s*(\w+)\.Path|\bcmd:\s*(\w+),`)

	out := map[string]string{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || isTestSource(filepath.Join(dir, n)) {
			continue
		}
		src := mustRead(t, filepath.Join(dir, n))
		paths := map[string]string{}
		for _, m := range decl.FindAllStringSubmatch(src, -1) {
			paths[m[1]] = m[2]
		}
		for _, m := range sub.FindAllStringSubmatch(src, -1) {
			if p, ok := paths[m[1]+m[2]]; ok {
				out[p] = n
			}
		}
	}
	// A BELIEVABILITY FLOOR, like sharedMenus'. Phase 3 subscribed 22 of 25
	// collectors, so finding a handful means the pattern stopped matching and
	// every check built on this map silently passes.
	if len(out) < 10 {
		t.Fatalf("found only %d subscribed menu(s); the `menu: xxxCmd.Path` pattern "+
			"has stopped matching and both checks built on it are measuring nothing", len(out))
	}
	return out
}
