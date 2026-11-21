package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/pages"
	"mikrodash/internal/reports"
	"mikrodash/internal/store"
	"mikrodash/internal/ztp"
)

// framedHeadersOK reports what is wrong with a response's framing headers, or "".
func framedHeadersOK(h http.Header) string {
	if got := h.Values("X-Frame-Options"); len(got) != 1 || got[0] != "DENY" {
		return "X-Frame-Options = " + strings.Join(got, " | ")
	}
	csp := h.Values("Content-Security-Policy")
	if len(csp) == 0 {
		return "no Content-Security-Policy"
	}
	for _, v := range csp {
		if !strings.Contains(v, "frame-ancestors 'none'") {
			return "Content-Security-Policy without frame-ancestors 'none': " + v
		}
		if strings.Count(strings.ToLower(v), "frame-ancestors") != 1 {
			return "Content-Security-Policy with two frame-ancestors: " + v
		}
	}
	return ""
}

// registeredRoutes reads every route the server registers out of its source,
// with Go's own parser: each `mux.Handle(` and `mux.HandleFunc(` call in a
// non-test file of this package, its pattern evaluated from string literals,
// the package's string CONSTANTS (declared with `const`, nothing else) and
// `+`. A pattern this cannot evaluate FAILS the test, naming it, rather than
// being skipped: a route nobody checked would read as covered.
//
// Three registrations are built in other ways, and are expanded here from the
// same sources the server uses: the ZTP LAN path (a constant of internal/ztp),
// the page routes (a loop over pages.All) and the report exports (a loop over
// reports.ExportSpecs).
func registeredRoutes(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var parsed []*ast.File
	srcOf := map[*ast.File][]byte{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(fset, f, b, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, af)
		srcOf[af] = b
	}
	consts := map[string]string{}
	for _, af := range parsed {
		for _, d := range af.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							if v, err := strconv.Unquote(lit.Value); err == nil {
								consts[name.Name] = v
							}
						}
					}
				}
			}
		}
	}
	var eval func(e ast.Expr) (string, bool)
	eval = func(e ast.Expr) (string, bool) {
		switch x := e.(type) {
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return "", false
			}
			v, err := strconv.Unquote(x.Value)
			return v, err == nil
		case *ast.Ident:
			v, ok := consts[x.Name]
			return v, ok
		case *ast.BinaryExpr:
			if x.Op != token.ADD {
				return "", false
			}
			l, ok1 := eval(x.X)
			r, ok2 := eval(x.Y)
			return l + r, ok1 && ok2
		case *ast.ParenExpr:
			return eval(x.X)
		}
		return "", false
	}
	known := map[string][]string{`"POST "+ztp.LANPath`: {"POST " + ztp.LANPath}}
	for _, p := range pages.All {
		known[`"/"+p.URL()`] = append(known[`"/"+p.URL()`], "/"+p.URL())
	}
	for kind := range reports.ExportSpecs {
		known[`reportsPrefix+kind+"/export"`] = append(known[`reportsPrefix+kind+"/export"`],
			consts["reportsPrefix"]+kind+"/export")
	}
	norm := regexp.MustCompile(`\s*\+\s*`)
	var routes []string
	for _, af := range parsed {
		ast.Inspect(af, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "mux" {
				return true
			}
			if v, ok := eval(call.Args[0]); ok {
				routes = append(routes, v)
				return true
			}
			src := srcOf[af][fset.Position(call.Args[0].Pos()).Offset:fset.Position(call.Args[0].End()).Offset]
			if ps, ok := known[norm.ReplaceAllString(string(src), "+")]; ok {
				routes = append(routes, ps...)
				return true
			}
			t.Errorf("%s: cannot read the route `%s`; teach registeredRoutes it, "+
				"or this route goes unchecked", fset.Position(call.Pos()), src)
			return true
		})
	}
	return routes
}

// requestFor builds a request that the pattern itself matches: the method
// (GET when the pattern names none), and the path with each wildcard filled.
func requestFor(pattern string) *http.Request {
	method, path := http.MethodGet, pattern
	if i := strings.IndexByte(pattern, ' '); i > 0 {
		method, path = pattern[:i], strings.TrimSpace(pattern[i+1:])
	}
	path = strings.ReplaceAll(path, "{$}", "")
	// A subtree pattern ("/", "/css/") is reached by a path BELOW it; the
	// pattern's own path may belong to a more specific one ("/{$}").
	if strings.HasSuffix(path, "/") && !strings.HasSuffix(pattern, "{$}") {
		path += "x1"
	}
	path = regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(path, "x1")
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.RemoteAddr = "10.0.0.9:1234"
	return req
}

func frameGuardServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		".secret": "test-secret", "settings.json": `{}`, "routers.json": `[]`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(st, Options{WebDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// TestEveryRouteRefusesToBeFramed: every route the server registers, requested
// through the real handler, answers with X-Frame-Options DENY and
// frame-ancestors 'none' - and so do a 404, a redirect and the root.
//
// Each request is checked to have REACHED its own route (the mux records the
// pattern it matched), so a route the list names but the server does not
// register fails rather than passing on the 404 page's headers.
func TestEveryRouteRefusesToBeFramed(t *testing.T) {
	srv := frameGuardServer(t)
	h, mux := srv.Handler(), srv.routes()
	routes := registeredRoutes(t)
	if len(routes) < 100 {
		t.Fatalf("only %d routes read from the source; the registration shape changed "+
			"and this test would check almost nothing", len(routes))
	}

	serve := func(req *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { h.ServeHTTP(w, req); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s %s did not answer in 10s", req.Method, req.URL.Path)
		}
		return w
	}

	checked := 0
	for _, pattern := range routes {
		// The refused-write audit hands the mux a copy of each write request
		// (WithContext), so the pattern it matched is read from the mux itself.
		if _, got := mux.Handler(requestFor(pattern)); got != pattern {
			t.Errorf("%s: the request reaches %q, not this route; is it registered?",
				pattern, got)
			continue
		}
		req := requestFor(pattern)
		w := serve(req)
		if msg := framedHeadersOK(w.Header()); msg != "" {
			t.Errorf("%s answered %d with %s", pattern, w.Code, msg)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no route was checked")
	}
	t.Logf("%d registered routes checked", checked)

	// And the responses no route owns: a path nothing matches, and the root
	// redirecting a visitor with no session to the login page.
	for _, path := range []string{"/no-such-page", "/no/such/thing.js", "/"} {
		w := serve(httptest.NewRequest(http.MethodGet, path, nil))
		if msg := framedHeadersOK(w.Header()); msg != "" {
			t.Errorf("GET %s answered %d with %s", path, w.Code, msg)
		}
	}
	if w := serve(httptest.NewRequest(http.MethodGet, "/", nil)); w.Code != http.StatusFound {
		t.Errorf("control: GET / without a session answered %d, want the 302 to /login", w.Code)
	}
}

// TestAHandlersOwnPolicyIsMergedNotDoubled: a handler that sets a
// Content-Security-Policy keeps it, with frame-ancestors 'none' merged into
// the same header; a framing policy of its own is replaced; X-Frame-Options is
// DENY whatever the handler set; and a handler that writes without calling
// WriteHeader is covered too.
func TestAHandlersOwnPolicyIsMergedNotDoubled(t *testing.T) {
	cases := []struct {
		why     string
		set     func(h http.Header)
		wantCSP []string
	}{
		{"no policy of its own", func(http.Header) {}, []string{"frame-ancestors 'none'"}},
		{"a policy without frame-ancestors",
			func(h http.Header) { h.Set("Content-Security-Policy", "default-src 'self'") },
			[]string{"default-src 'self'; frame-ancestors 'none'"}},
		{"a policy that allows framing",
			func(h http.Header) {
				h.Set("Content-Security-Policy", "default-src 'self'; Frame-Ancestors *; img-src data:")
			},
			[]string{"default-src 'self'; img-src data:; frame-ancestors 'none'"}},
		{"two policies",
			func(h http.Header) {
				h.Add("Content-Security-Policy", "default-src 'self'")
				h.Add("Content-Security-Policy", "frame-ancestors https://a.example")
			},
			[]string{"default-src 'self'; frame-ancestors 'none'"}},
		{"a handler loosening X-Frame-Options",
			func(h http.Header) { h.Set("X-Frame-Options", "SAMEORIGIN") },
			[]string{"frame-ancestors 'none'"}},
		{"a handler deleting both",
			func(h http.Header) { h.Del("X-Frame-Options"); h.Del("Content-Security-Policy") },
			[]string{"frame-ancestors 'none'"}},
	}
	for _, c := range cases {
		for _, explicit := range []bool{true, false} {
			h := frameGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				c.set(w.Header())
				if explicit {
					w.WriteHeader(http.StatusTeapot)
				}
				_, _ = w.Write([]byte("x"))
			}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
			got := w.Header().Values("Content-Security-Policy")
			if strings.Join(got, "|") != strings.Join(c.wantCSP, "|") {
				t.Errorf("%s (WriteHeader %v): CSP %q, want %q", c.why, explicit, got, c.wantCSP)
			}
			if x := w.Header().Values("X-Frame-Options"); len(x) != 1 || x[0] != "DENY" {
				t.Errorf("%s (WriteHeader %v): X-Frame-Options %q", c.why, explicit, x)
			}
		}
	}
}

// TestTheGuardKeepsTheConnectionReachable: the socket upgrade and streamed
// responses reach the connection through the guard's writer.
func TestTheGuardKeepsTheConnectionReachable(t *testing.T) {
	var unwrapped, flushed bool
	h := frameGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, unwrapped = w.(interface{ Unwrap() http.ResponseWriter })
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
			flushed = true
		}
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if !unwrapped || !flushed || !w.Flushed {
		t.Errorf("unwrap %v, flush %v, reached the recorder %v", unwrapped, flushed, w.Flushed)
	}
	if msg := framedHeadersOK(w.Header()); msg != "" {
		t.Errorf("a flushed response: %s", msg)
	}
}
