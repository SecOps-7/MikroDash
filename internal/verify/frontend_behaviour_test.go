package verify

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"mikrodash/internal/pages"
)

// ── Behaviour pinned by reading the source, not by driving a DOM ────────────
//
// Each of these asserts something about the frontend that is TRUE OR FALSE in the
// text: which page the app lands on, whether a fetch is at module scope, whether
// a handler re-asserts state. None needs a browser, which is why they are here
// rather than in `web/test/`.
//
// They all guard the same shape of defect: code that runs, returns normally, and
// does the wrong thing quietly.

// uncomment strips comments so a check cannot be satisfied by prose describing
// the very thing it looks for.
func uncomment(s string) string {
	s = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(s, " ")
	return regexp.MustCompile(`//[^\n]*`).ReplaceAllString(s, " ")
}

// TestAppLandsOnTheDashboard: the first page after a router is selected is the
// dashboard, and the dashboard is actually mounted.
//
// Landing anywhere else is the first-vertical-slice default outliving the slice.
// And if `dashboard` is missing from either list, navigating to it hands the
// browser to a Node that does not exist and the redirect lands back on the
// default page — the loop the operator originally hit.
func TestAppLandsOnTheDashboard(t *testing.T) {
	root := repoRoot(t)
	body := uncomment(mustRead(t, filepath.Join(root, "web", "src", "main.ts")))

	// The landing call now names the fallback inside `initialPage(...)`, because
	// the URL decides the first page and the fallback is only reached when it
	// names none. The pattern follows it rather than pinning the old shape.
	land := regexp.MustCompile(`initialPage\(PORTED, '([a-z-]+)'\)`).FindStringSubmatch(body)
	if land == nil {
		t.Fatal("the landing-page fallback could not be found — it is " +
			"`initialPage(PORTED, '<page>')` in select()")
	}
	if land[1] != "dashboard" {
		t.Errorf("the app falls back to %q. It is `dashboard` — the URL decides the page, and "+
			"this is only reached when the URL names none.", land[1])
	}
	// PORTED and webbuild's PAGES both read `internal/pages` now, so the two
	// regexes this used to run against their literals are gone. Asking the
	// package directly is the same question with no pattern to rot.
	if !pages.Has("dashboard") {
		t.Error("'dashboard' is not in internal/pages, so it has no markup composed, no URL " +
			"registered and no nav entry — the landing page would not exist.")
	}
	t.Log("the app lands on a mounted dashboard")
}

// TestReconnectDoesNotNavigateAway: the connect handler re-asserts the page the
// operator is on rather than forcing a fixed one.
//
// `select()` runs on EVERY connect, so a fixed page would yank the operator away
// from whatever they were reading whenever the socket blipped.
func TestReconnectDoesNotNavigateAway(t *testing.T) {
	root := repoRoot(t)
	src := mustRead(t, filepath.Join(root, "web", "src", "main.ts"))
	body := uncomment(sliceBetween(t, src, "const select = () => {", "\n  };"))

	if !strings.Contains(body, "showPage(") {
		t.Fatal("select() no longer calls showPage; if the page is asserted elsewhere on connect, " +
			"move this check rather than deleting it")
	}
	call := regexp.MustCompile(`showPage\(\s*socket\s*,\s*([^)]*)\)`).FindStringSubmatch(body)
	if call == nil {
		t.Fatal("showPage is called in a shape this check cannot read")
	}
	if !regexp.MustCompile(`\bcurrentPage\b`).MatchString(strings.TrimSpace(call[1])) {
		t.Errorf("select() calls showPage(socket, %s) — a fixed page. This runs on every connect, "+
			"so a reconnect would navigate the operator away from whatever they were reading.",
			strings.TrimSpace(call[1]))
	}
	t.Log("the connect handler re-asserts the current page rather than overriding it")
}

// TestLocalCCIsFetchedLazily: the arc-origin lookup happens when connection data
// arrives, not at module scope.
//
// Every page module initialises at BOOT, before the router session has settled.
// A fetch at that point answers an empty country and the map never draws an arc —
// and it does so silently, because an empty answer is a valid answer.
func TestLocalCCIsFetchedLazily(t *testing.T) {
	root := repoRoot(t)
	body := uncomment(mustRead(t, filepath.Join(root, "web", "src", "pages", "connections.ts")))

	if regexp.MustCompile(`\n {2}fetch\(\s*'/api/localcc'`).MatchString(body) {
		t.Error("fetch('/api/localcc') is called at the top level of initConnectionsPage. Page " +
			"modules initialise at boot, before the session has settled, so it answers an empty " +
			"country and the map never draws an arc.")
	}
	if !strings.Contains(body, "function fetchLocalCCOnce()") {
		t.Fatal("fetchLocalCCOnce is gone; if the fetch moved, move this check too")
	}
	at := strings.Index(body, "socket.on('conn:update'")
	if at < 0 {
		t.Fatal("no socket.on('conn:update') handler — the anchor is gone")
	}
	handler := body[at:]
	if next := strings.Index(body[at+10:], "socket.on("); next >= 0 {
		handler = body[at : at+10+next]
	}
	if !strings.Contains(handler, "fetchLocalCCOnce()") {
		t.Error("the conn:update handler does not call fetchLocalCCOnce. That call is what makes " +
			"the fetch happen when the session is UP — connection data arriving is the proof of it.")
	}
	t.Log("the arc origin is fetched lazily from conn:update")
}

// TestLoginFadeIsRestored: preflight hides the page after a login, and main.ts
// puts it back.
//
// The failure is invisible in the worst way: every request returns 200, nothing
// is logged, the app renders correctly — at opacity zero. Each half is asserted
// separately so that removing one is a failure rather than quietly making the
// other dead code.
func TestLoginFadeIsRestored(t *testing.T) {
	root := repoRoot(t)
	entry := filepath.Join(root, "web", "src", "entry")

	preflight := mustRead(t, filepath.Join(entry, "preflight.ts"))
	if !regexp.MustCompile(`opacity\s*=\s*'0'`).MatchString(preflight) {
		t.Error("preflight no longer sets opacity to 0. If the hide is gone the restore is dead " +
			"code — delete both together, deliberately.")
	}
	if !strings.Contains(preflight, "justLoggedIn") {
		t.Error("preflight no longer keys the hide on justLoggedIn; the restore must follow it")
	}
	login := mustRead(t, filepath.Join(entry, "login.ts"))
	if !regexp.MustCompile(`sessionStorage\.setItem\(\s*'justLoggedIn'`).MatchString(login) {
		t.Error("login no longer sets justLoggedIn, so the hide never fires")
	}
	body := uncomment(mustRead(t, filepath.Join(root, "web", "src", "main.ts")))
	if !regexp.MustCompile(`sessionStorage\.getItem\(\s*'justLoggedIn'\s*\)`).MatchString(body) {
		t.Error("main.ts never reads justLoggedIn. After a login the app renders correctly and is " +
			"INVISIBLE — every request 200, nothing logged, nothing to see.")
	}
	if !regexp.MustCompile(`documentElement\.style\.opacity\s*=\s*'1'`).MatchString(body) {
		t.Error("main.ts never restores opacity to 1 — the page stays hidden after login")
	}
	t.Log("preflight hides on justLoggedIn, login sets it, main clears the flag and restores opacity")
}

// dashboardIDsUnwritten: ids in the dashboard markup that nothing references.
//
// Two kinds, and both are real reasons rather than excuses: an id BUILT at
// runtime, which no text search can resolve, and static SVG that no code ever
// touches on either side.
var dashboardIDsUnwritten = map[string]string{
	"trafficCardWarn": "built at runtime by dashboard-stream-health.ts as " +
		"`STREAM_WARN_CARDS[collector] + 'Warn'`, which a text search cannot resolve",
	"connCardWarn":    "built at runtime, as trafficCardWarn",
	"dc-dhcpGaugeSvg": "static SVG in the network diagram; nothing writes it",
	"dc-rtProtoGrid":  "static SVG, as above",
	"ndLineWired":     "static SVG in the network diagram",
	"ndLineWireless":  "static SVG in the network diagram",
	"ndLineWan":       "static SVG in the network diagram",
	"ndWiredGroup":    "static SVG in the network diagram",
	"ndWirelessGroup": "static SVG in the network diagram",
	"ndRouter":        "static SVG in the network diagram",
	"ndWanGroup":      "static SVG in the network diagram",
	"dc-worldMapWrap": "the map's wrapper. The card reaches it as `svg.parentElement` to position " +
		"the tooltip, which is traversal rather than a lookup, so it has no writer and needs none.",
}

// TestDashboardMarkupIsDriven: every id in the dashboard's markup is referenced by
// TypeScript or styled by CSS.
//
// The dashboard is the densest markup in the app. An id nothing references is a
// card, a stat or a badge that renders its empty initial state forever — and it
// looks completely normal, because an empty dashboard tile is a plausible state.
func TestDashboardMarkupIsDriven(t *testing.T) {
	root := repoRoot(t)

	html := mustRead(t, filepath.Join(root, "web", "src", "ui", "page-dashboard.html"))
	css := mustRead(t, filepath.Join(root, "web", "public", "app.css"))
	ts := joined(readFiles(t, root, "web/src/", func(r string) bool { return hasExt(r, ".ts") }))

	seen := map[string]bool{}
	var ids []string
	for _, m := range regexp.MustCompile(`id="([A-Za-z0-9_-]+)"`).FindAllStringSubmatch(html, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			ids = append(ids, m[1])
		}
	}
	if len(ids) < 100 {
		t.Fatalf("only %d ids found in the dashboard markup — the scan broke", len(ids))
	}

	referenced := func(id string) bool {
		return strings.Contains(ts, "'"+id+"'") ||
			strings.Contains(ts, `"`+id+`"`) ||
			strings.Contains(css, "#"+id)
	}

	have := map[string]bool{}
	unwritten := 0
	for _, id := range ids {
		if referenced(id) {
			continue
		}
		unwritten++
		have[id] = true
		if _, ok := dashboardIDsUnwritten[id]; !ok {
			t.Errorf("#%s is in the dashboard markup and nothing references it — the element "+
				"renders its empty initial state forever, which looks like a plausible dashboard", id)
		}
	}
	for id := range dashboardIDsUnwritten {
		if !have[id] {
			t.Errorf("#%s is recorded as unwritten, but something references it now — delete the "+
				"entry rather than leaving a note that has stopped being true", id)
		}
	}
	t.Logf("%d dashboard ids, %d driven, %d recorded as unwritten", len(ids), len(ids)-unwritten, unwritten)
}

// TestTrafficPickSurvivesReconnect: the operator's interface choice is cleared on
// a router SWITCH and never on a reconnect.
//
// Clearing it on connect would silently reset the chart every time the socket
// blipped; not clearing it on a switch would show one router's pick against
// another router's data. The original's own rule was the same, and the guarded
// half of the JavaScript check that asserted it against the deleted source is
// gone — the port half below is the whole check now.
func TestTrafficPickSurvivesReconnect(t *testing.T) {
	root := repoRoot(t)
	traffic := uncomment(mustRead(t, filepath.Join(root, "web", "src", "pages", "dashboard-traffic.ts")))

	// THE ASYMMETRY IS EXPRESSED AS TWO FUNCTIONS, and that is the whole design:
	// `resetTrafficOnReconnect` forgets the samples, `resetTraffic` forgets the
	// samples AND the pick. A single reset wired to both events could not tell
	// the two situations apart, which is how this was wrong before.
	onReconnect := sliceBetween(t, traffic, "func"+"tion resetTrafficOnReconnect(): void {", "\n}")
	full := sliceBetween(t, traffic, "func"+"tion resetTraffic(): void {", "\n}")

	clears := regexp.MustCompile(`userPickedIf\s*=\s*''`)
	if clears.MatchString(onReconnect) {
		t.Error("resetTrafficOnReconnect clears the operator's interface pick. A reconnect is the " +
			"same operator looking at the same router, so their choice must survive it.")
	}
	if !clears.MatchString(full) {
		t.Error("resetTraffic no longer clears the pick. A router switch is a different fleet of " +
			"interfaces, and carrying a name across would either miss or, worse, match something " +
			"unrelated that happens to share it.")
	}

	// AND THE WIRING, which is the half that actually decides behaviour: the
	// connect handler must call the narrow reset, not the full one.
	dash := uncomment(mustRead(t, filepath.Join(root, "web", "src", "pages", "dashboard.ts")))
	// SEVERAL connect handlers are registered here, one per card, so this asks
	// about all of them rather than the first: exactly one must reset traffic,
	// and it must be the narrow reset.
	handlers := regexp.MustCompile(`socket\.on\('connect',[^\n]*`).FindAllString(dash, -1)
	if len(handlers) == 0 {
		t.Fatal("no socket.on('connect') in dashboard.ts — the anchor is gone")
	}
	narrow, wide := 0, 0
	for _, h := range handlers {
		if strings.Contains(h, "resetTrafficOnReconnect") {
			narrow++
		} else if regexp.MustCompile(`resetTraffic\s*\(`).MatchString(h) {
			wide++
		}
	}
	if wide > 0 {
		t.Errorf("%d connect handler(s) call the FULL resetTraffic — every socket blip would "+
			"silently reset the operator's chart selection", wide)
	}
	if narrow != 1 {
		t.Errorf("%d of %d connect handlers call resetTrafficOnReconnect; exactly one should — "+
			"without it a reconnect appends new history to samples from before the gap, drawing "+
			"the chart straight across the outage", narrow, len(handlers))
	}
	t.Log("the pick survives a reconnect and is cleared only on a router switch")
}
