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
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/pages"
	"mikrodash/internal/reports"
	"mikrodash/internal/store"
	"mikrodash/internal/ztp"
)

// routesFromSource reads every route this package registers, with Go's
// parser: each `mux.Handle(` / `mux.HandleFunc(` in a non-test file, its
// pattern evaluated from string literals, `const` strings and `+`. The three
// built another way are expanded from the sources the server uses (the ZTP LAN
// path, the page loop, the report export loop). A pattern it cannot evaluate
// fails the test, naming it.
func routesFromSource(t *testing.T) []string {
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
			t.Errorf("%s: cannot read the route `%s`", fset.Position(call.Pos()), src)
			return true
		})
	}
	return routes
}

// writeRoutes is the routes whose pattern names a write method.
func writeRoutes(t *testing.T) []string {
	var out []string
	for _, r := range routesFromSource(t) {
		if i := strings.IndexByte(r, ' '); i > 0 && isWriteMethod(r[:i]) {
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

func requestForRoute(pattern, addr string) *http.Request {
	method, path, _ := strings.Cut(pattern, " ")
	path = strings.ReplaceAll(path, "{$}", "")
	path = regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(path, "x1")
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = addr + ":1234"
	return req
}

// deniedServer is the real server with a real audit database.
func deniedServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		".secret": "test-secret", "settings.json": `{}`, "routers.json": `[]`,
		"users.json": seedUsersJSON,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	srv, err := New(st, Options{WebDir: t.TempDir(), AuditDB: d})
	if err != nil {
		t.Fatal(err)
	}
	return srv, srv.Handler()
}

func deniedRowsFrom(t *testing.T, srv *Server, ip string) []db.Row {
	t.Helper()
	page, err := srv.auditDB.QueryAuditEvents(db.Query{IncludeApp: true, Outcome: "denied", Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	var out []db.Row
	for _, r := range page.Rows {
		if r.ActorIP != nil && *r.ActorIP == ip {
			out = append(out, r)
		}
	}
	return out
}

func serveWithin(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
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

// TestEveryRefusedWriteLeavesOneRow: every write route, called with no
// session and then as a signed-in user with no grants, each request from its
// own address. Whatever answers 401 or 403 leaves exactly one denied row,
// whether its handler wrote it or the middleware did.
func TestEveryRefusedWriteLeavesOneRow(t *testing.T) {
	srv, h := deniedServer(t)
	routes := writeRoutes(t)
	if len(routes) < 60 {
		t.Fatalf("only %d write routes read; the registration shape changed", len(routes))
	}
	refused := 0
	for pass, cookie := range []string{"", "mikrodash_sid=tok"} {
		if cookie != "" {
			srv.auth = authFor("tok", &Session{Username: "bob"})
		}
		for i, pattern := range routes {
			if _, exempt := deniedAuditExempt[pattern]; exempt {
				continue
			}
			ip := "10.7." + strconv.Itoa(pass) + "." + strconv.Itoa(i%250+1)
			if i >= 250 {
				ip = "10.8." + strconv.Itoa(pass) + "." + strconv.Itoa(i%250+1)
			}
			req := requestForRoute(pattern, ip)
			if cookie != "" {
				req.Header.Set("Cookie", cookie)
			}
			w := serveWithin(t, h, req)
			if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
				continue
			}
			refused++
			if rows := deniedRowsFrom(t, srv, ip); len(rows) != 1 {
				t.Errorf("%s (pass %d) answered %d and left %d denied rows, want 1",
					pattern, pass, w.Code, len(rows))
			}
		}
	}
	if refused < 60 {
		t.Fatalf("only %d refusals seen; the passes are not exercising the gates", refused)
	}
}

// TestAnAnonymousWriteIsRefusedOrPublic: a write route that does not refuse a
// caller with no session must be on this list, with its reason, and every
// entry must still be a route that answers anonymously. Both ways.
func TestAnAnonymousWriteIsRefusedOrPublic(t *testing.T) {
	public := map[string]string{
		"POST /api/auth/login":  "signing in",
		"POST /api/users/setup": "first-run setup, guarded by the setup code",
	}
	_, h := deniedServer(t)
	answered := map[string]int{}
	for i, pattern := range writeRoutes(t) {
		w := serveWithin(t, h, requestForRoute(pattern, "10.9.0."+strconv.Itoa(i%250+1)))
		if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
			answered[pattern] = w.Code
		}
	}
	for p, code := range answered {
		if _, ok := public[p]; !ok {
			t.Errorf("%s answered %d to a caller with no session; refuse it, or list it as public with a reason", p, code)
		}
	}
	for p := range public {
		if _, ok := answered[p]; !ok {
			t.Errorf("%s is listed as public but refused a caller with no session; drop it from the list", p)
		}
	}
}

func TestTheExemptionsNameRealRoutes(t *testing.T) {
	have := map[string]bool{}
	for _, r := range routesFromSource(t) {
		have[r] = true
	}
	for p := range deniedAuditExempt {
		if !have[p] {
			t.Errorf("deniedAuditExempt names %q, which is not a registered route", p)
		}
	}
}

// TestARefusedReadIsNotRecorded: only writes are recorded.
func TestARefusedReadIsNotRecorded(t *testing.T) {
	srv, h := deniedServer(t)
	w := serveWithin(t, h, requestForRoute("GET /api/settings", "10.9.9.9"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("control: an anonymous settings read answered %d, want 401", w.Code)
	}
	if rows := deniedRowsFrom(t, srv, "10.9.9.9"); len(rows) != 0 {
		t.Errorf("a refused GET left %d rows", len(rows))
	}
}

// TestAFloodIsCountedNotWritten: a flood of refusals from one actor is one
// row per action and target, and the rest are reported in one summary row for
// that actor when the minute closes. Another actor is unaffected.
func TestAFloodIsCountedNotWritten(t *testing.T) {
	srv, h := deniedServer(t)
	clock := time.Unix(1_800_000_000, 0)
	var limit *audit.DeniedLimiter
	limit = audit.NewDeniedLimiter(func() time.Time { return clock }, func(sum audit.DeniedSummary) {
		audit.WriteDeniedSummary(auditSink{srv.auditDB}, sum, nowMillis)
	})
	srv.deniedLimit = limit
	// The router routes have their own request limiter (60 a minute, on the
	// real clock): past it they answer 429, which is not a refusal and is not
	// counted. So the refusals actually made are counted here.
	refused := 0
	for i := 0; i < 500; i++ {
		w := serveWithin(t, h, requestForRoute("DELETE /api/routers/{id}", "10.6.6.6"))
		if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
			refused++
		}
	}
	if refused < 30 {
		t.Fatalf("control: only %d of 500 requests were refusals", refused)
	}
	if rows := deniedRowsFrom(t, srv, "10.6.6.6"); len(rows) != 1 {
		t.Fatalf("%d refusals in a minute left %d rows, want 1", refused, len(rows))
	}
	clock = clock.Add(time.Minute)
	limit.Flush(false)
	rows := deniedRowsFrom(t, srv, "10.6.6.6")
	if len(rows) != 2 {
		t.Fatalf("after the minute: %d rows, want the refusal and its summary", len(rows))
	}
	want := strconv.Itoa(refused-1) + " refused attempts not recorded one by one: DELETE /api/routers/{id} x" + strconv.Itoa(refused-1)
	found := false
	for _, r := range rows {
		if r.Action == audit.DeniedSummaryAction && r.ActorName == "anonymous" && r.Detail != nil && strings.Contains(*r.Detail, want) {
			found = true
		}
	}
	if !found {
		t.Errorf("no summary row for the anonymous actor reports %q", want)
	}
}

// TestOneActorHasOneBudgetAcrossTheSocketAndHTTP: refusals one actor makes on
// the socket and over HTTP count against the same budget.
func TestOneActorHasOneBudgetAcrossTheSocketAndHTTP(t *testing.T) {
	srv, h := deniedServer(t)
	srv.auth = authFor("tok", &Session{Username: "bob"})
	cn := &conn{srv: srv, clientIP: "10.5.5.5", sess: &Session{Username: "bob"}, userID: srv.userIDFor("bob")}
	if cn.userID == "" {
		t.Fatal("control: bob has no user id in the fixture")
	}
	for i := 0; i < 30; i++ {
		cn.recorder().Denied(audit.Event{Action: "socket-action-" + strconv.Itoa(i)})
	}
	if rows := deniedRowsFrom(t, srv, "10.5.5.5"); len(rows) != 30 {
		t.Fatalf("control: 30 distinct socket refusals left %d rows", len(rows))
	}
	req := requestForRoute("POST /api/users", "10.5.5.5")
	req.Header.Set("Cookie", "mikrodash_sid=tok")
	if w := serveWithin(t, h, req); w.Code != http.StatusForbidden {
		t.Fatalf("control: bob's POST /api/users answered %d, want 403", w.Code)
	}
	if rows := deniedRowsFrom(t, srv, "10.5.5.5"); len(rows) != 30 {
		t.Errorf("bob's HTTP refusal past his shared cap of 30 was written (%d rows)", len(rows))
	}
}

// TestAnAnonymousFloodCannotHideASignedInUsersRefusal: behind one address
// (a proxy MikroDash does not trust), an anonymous flood uses up only the
// anonymous budget; a signed-in user's refusal from the same address is still
// written.
func TestAnAnonymousFloodCannotHideASignedInUsersRefusal(t *testing.T) {
	srv, h := deniedServer(t)
	for i := 0; i < 50; i++ {
		serveWithin(t, h, requestForRoute("POST /api/users", "10.4.4.4"))
	}
	before := len(deniedRowsFrom(t, srv, "10.4.4.4"))
	srv.auth = authFor("tok", &Session{Username: "bob"})
	req := requestForRoute("POST /api/users", "10.4.4.4")
	req.Header.Set("Cookie", "mikrodash_sid=tok")
	if w := serveWithin(t, h, req); w.Code != http.StatusForbidden {
		t.Fatalf("control: bob's POST answered %d, want 403", w.Code)
	}
	rows := deniedRowsFrom(t, srv, "10.4.4.4")
	if len(rows) != before+1 {
		t.Fatalf("bob's refusal after an anonymous flood: %d rows, want %d", len(rows), before+1)
	}
	if rows[0].ActorName != "bob" {
		t.Errorf("the newest row is %q's, want bob's", rows[0].ActorName)
	}
}
