package verify

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// endpointsProxied: endpoints the frontend calls that this server does not serve.
//
// EMPTY, AND THAT IS THE POINT. It held the routes still delegated to Node during
// coexistence. There is no Node to delegate to now, so an entry here would mean a
// page calling something nothing answers -- a 404 the browser swallows into an
// empty card.
var endpointsProxied = map[string]string{}

// endpointsExtra: endpoints outside `/api` that the app legitimately calls.
// Named rather than pattern-matched, so adding one is deliberate.
var endpointsExtra = []string{"/healthz"}

// TestEveryCalledEndpointIsServed: every URL the frontend fetches is a route this
// server registers.
//
// A page calling a route nothing serves gets a 404, and almost every call site
// here handles failure by rendering nothing. So the symptom is an empty card and
// the cause is invisible -- no exception, no log, no red anywhere.
//
// This audit was RED for an unknown number of sessions before anyone noticed,
// which is why `verify.sh` stopped listing checks by hand and began discovering
// them.
func TestEveryCalledEndpointIsServed(t *testing.T) {
	root := repoRoot(t)

	// ── What the frontend calls ─────────────────────────────────────────────
	front := readFiles(t, root, "web/", func(r string) bool {
		if strings.HasPrefix(r, "web/dist/") || strings.HasPrefix(r, "web/node_modules/") ||
			strings.HasPrefix(r, "web/public/vendor/") {
			return false
		}
		// NOT THE TESTS. `web/test/` fetches invented URLs like `/api/anything`
		// as fixtures; reading them as if the app called them reported a route
		// the server rightly does not serve.
		if isTestSource(r) {
			return false
		}
		return hasExt(r, ".ts", ".html", ".js")
	})
	fetchCall := regexp.MustCompile("fetch\\(\\s*['\"`]([^'\"`]+)['\"`]")
	constURL := regexp.MustCompile(`(?:API|URL|ENDPOINT)\s*=\s*['"]([^'"]+)`)
	// ── AND A URL BUILT INTO AN href, WHICH IS HOW TWO ROUTES WENT MISSING ──
	//
	// The Backups page draws its .rsc and .backup download links as plain
	// `<a href>` — deliberately, so the browser saves the file rather than the
	// page holding several MB in memory — and builds the URL by concatenation:
	//
	//	'... href="/api/backups/' + r.id + '/rsc' + q + '">'
	//
	// That is neither a `fetch(` nor a NAME = '...' constant, so this scan did
	// not see it. Both routes were never ported, both links answered Go's own
	// "404 page not found" for the whole life of the port, and the check that
	// exists to catch exactly this looked straight past them. Issue #124.
	//
	// ── THE CONCATENATION HAS TO BE COLLAPSED FIRST ─────────────────────────
	//
	// Capturing up to the first quote yields `/api/backups/`, and `isServed`
	// treats a prefix of a registered route as covered — so the naive version of
	// this passed against the missing routes. The interpolations are folded into
	// `{}` so the whole path survives, and wildcards are compared shape-to-shape
	// against the `{id}` in the registration.
	called := map[string]bool{}

	for _, body := range front {
		for _, u := range hrefRoutes(body) {
			if inAPIScope(u) {
				called[u] = true
			}
		}
		for _, re := range []*regexp.Regexp{fetchCall, constURL} {
			for _, m := range re.FindAllStringSubmatch(body, -1) {
				u := normaliseRoute(m[1])
				// SCOPED TO THE API. A static asset -- the world atlas, a font --
				// is answered by the file server, not by a registered route, so
				// asking whether a route exists for it is the wrong question.
				if u == "" || !inAPIScope(u) {
					continue
				}
				called[u] = true
			}
		}
	}
	if len(called) < 15 {
		t.Fatalf("only %d called endpoints found — the scan broke, and this test would report "+
			"every route as served by looking at nothing", len(called))
	}

	// ── What Go serves ──────────────────────────────────────────────────────
	//
	// Routes are registered with string constants as often as literals
	// (`accountBase + "/password"`), so the constants are resolved first.
	goSrc := joined(readFiles(t, root, "internal/server/", func(r string) bool {
		return hasExt(r, ".go") && !strings.HasSuffix(r, "_test.go")
	}))
	consts := map[string]string{}
	for _, m := range regexp.MustCompile(`(\w+)\s*=\s*"(/[^"]*)"`).FindAllStringSubmatch(goSrc, -1) {
		consts[m[1]] = m[2]
	}
	names := make([]string, 0, len(consts))
	for k := range consts {
		names = append(names, k)
	}
	// Longest first, so a short name is not substituted inside a longer one.
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })

	served := map[string]bool{}
	for _, m := range regexp.MustCompile(`mux\.Handle(?:Func)?\(\s*([^)]+?)\s*,`).
		FindAllStringSubmatch(goSrc, -1) {
		expr := m[1]
		for _, k := range names {
			expr = regexp.MustCompile(`\b`+regexp.QuoteMeta(k)+`\b`).
				ReplaceAllString(expr, `"`+consts[k]+`"`)
		}
		var parts []string
		for _, q := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(expr, -1) {
			parts = append(parts, q[1])
		}
		if r := normaliseRoute(strings.Join(parts, "")); r != "" {
			served[collapseWildcards(r)] = true
		}
	}
	for _, e := range endpointsExtra {
		served[e] = true
	}
	if len(served) < 20 {
		t.Fatalf("only %d served routes read out of internal/server — the registration shape "+
			"changed", len(served))
	}

	// A ROUTE COVERS ITS SUBTREE, in both directions. Go registers
	// `/api/alerts/{id}/ack`, and the page calling `/api/alerts` is answered by
	// that family; a registration ending in `/` is a prefix handler and covers
	// everything beneath it. Matching only exact strings reported three working
	// endpoints as missing.
	servedList := make([]string, 0, len(served))
	for r := range served {
		servedList = append(servedList, r)
	}
	isServed := func(u string) bool {
		if served[u] {
			return true
		}
		for _, r := range servedList {
			if strings.HasSuffix(r, "/") && strings.HasPrefix(u, r) {
				return true
			}
			if strings.HasPrefix(r, u+"/") {
				return true
			}
		}
		return false
	}

	var unserved []string
	for c := range called {
		if !isServed(c) {
			unserved = append(unserved, c)
		}
	}
	sort.Strings(unserved)

	have := map[string]bool{}
	for _, u := range unserved {
		have[u] = true
		if _, ok := endpointsProxied[u]; !ok {
			t.Errorf("the frontend calls %s and this server does not serve it — a 404 the page "+
				"handles by rendering nothing", u)
		}
	}
	for u := range endpointsProxied {
		if !have[u] {
			t.Errorf("%s is recorded as proxied, but it is served now — delete the entry", u)
		}
	}
	t.Logf("%d endpoints called by the frontend; %d served, %d recorded",
		len(called), len(called)-len(unserved), len(unserved))
}

// inAPIScope: this test is about registered API routes, not static files.

// collapseWildcards reduces every `{name}` to `{}`, so a route's SHAPE can be
// compared with a URL the frontend builds by interpolation. `/api/x/{id}/y` and
// `/api/x/{}/y` are the same route asked about from two directions.
func collapseWildcards(s string) string {
	return regexp.MustCompile(`\{[^}]*\}`).ReplaceAllString(s, "{}")
}

// jsConcat is `' + expr + '` inside a single-quoted JS string: the shape an
// interpolated path segment takes. Deliberately refuses a quote inside the
// expression, so it cannot run past the end of the string it is inside.
var jsConcat = regexp.MustCompile(`['"]\s*\+[^'"+]+\+\s*['"]`)

var hrefLiteral = regexp.MustCompile(`href=\\?["']([^"'>]*)`)

// hrefRoutes pulls API paths out of `<a href>` attributes built by string
// concatenation. See the note at the call site for why this is a separate pass.
func hrefRoutes(body string) []string {
	folded := jsConcat.ReplaceAllString(body, "{}")
	var out []string
	for _, m := range hrefLiteral.FindAllStringSubmatch(folded, -1) {
		u := m[1]
		// A TRAILING INTERPOLATION THAT IS NOT ITS OWN SEGMENT is a query string
		// or a fragment appended to the last segment — `'/rsc' + q` — not part of
		// the path. `/api/x/{}` keeps its wildcard; `/api/x/rsc{}` loses it.
		for strings.HasSuffix(u, "{}") && !strings.HasSuffix(u, "/{}") {
			u = strings.TrimSuffix(u, "{}")
		}
		if r := normaliseRoute(u); r != "" {
			out = append(out, collapseWildcards(r))
		}
	}
	return out
}

func inAPIScope(u string) bool {
	if strings.HasPrefix(u, "/api") {
		return true
	}
	for _, e := range endpointsExtra {
		if u == e {
			return true
		}
	}
	return false
}

// normaliseRoute strips a method prefix, a query string and a trailing slash, so
// `"POST /api/x/"` and `"/api/x?y=1"` are the same route.
func normaliseRoute(s string) string {
	s = regexp.MustCompile(`^(GET|POST|PUT|DELETE|PATCH)\s+`).ReplaceAllString(s, "")
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, "/")
	if !strings.HasPrefix(s, "/") {
		return ""
	}
	return s
}
