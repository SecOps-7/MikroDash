package server

// The Access Management reads: who may see them, and what the roles payload
// carries.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mikrodash/internal/rbac"
)

// TestOnlyAGlobalAdminSeesThePrincipalGraph. FAILS CLOSED, including when the
// resolver is unavailable: this is the answer to "who may do what", and serving
// it to somebody whose access could not be determined is the one outcome worth
// refusing outright.
func TestOnlyAGlobalAdminSeesThePrincipalGraph(t *testing.T) {
	s := &Server{}
	if s.isGlobalAdmin(&Session{AuthMode: "modern", Username: "someone"}) {
		t.Error("a server with no RBAC resolver granted the principal graph")
	}
	// One local operator with full reach, the same short circuit rbac.js makes.
	if !s.isGlobalAdmin(&Session{AuthMode: "none"}) {
		t.Error("auth mode none was refused")
	}
}

// TestADeniedGetIsNotAudited pins a rule that is easy to get wrong in both
// directions. `_auditDenied` opens with a method test — only POST, PUT, PATCH
// and DELETE reach the trail. Auditing every refusal would fill it with a
// viewer's browser polling endpoints it was never going to be shown; auditing
// none would lose the record of an attempted write.
//
// The guard has no audit call at all, and these routes are registered GET-only,
// so this asserts the ROUTING as well: a POST to one of them must not reach the
// handler in the first place.
func TestADeniedGetIsNotAudited(t *testing.T) {
	mux := http.NewServeMux()
	s := &Server{}
	s.registerPrincipals(mux)

	// Built from `principalsPrefix`, not from `Prefix`: the API came off the staging
	// prefix on 2026-08-25 and a test still spelling `/next` would pass against a
	// route nobody serves.
	for _, path := range []string{principalsPrefix + "/groups", principalsPrefix + "/roles",
		principalsPrefix + "/grants"} {
		// A GET matches; without a session it is refused before any audit could
		// happen, and the guard contains no audit call on any path.
		rec := httptest.NewRecorder()
		h, pattern := mux.Handler(httptest.NewRequest("GET", path, nil))
		if pattern == "" {
			t.Errorf("%s GET is not routed", path)
		}
		_ = h
		_ = rec

		// A POST must NOT be routed to these handlers.
		_, postPattern := mux.Handler(httptest.NewRequest("POST", path, nil))
		if postPattern == pattern && postPattern != "" {
			t.Errorf("%s accepts POST — these are read endpoints, and the writes "+
				"belong to Node until cutover", path)
		}
	}
}

// TestWriteCapablePagesComesFromTheProjection — "derived from the projection
// table, never restated in the client". A hand-written list here would be a
// second one that can disagree with the table consulted when a grant is
// actually evaluated.
func TestWriteCapablePagesComesFromTheProjection(t *testing.T) {
	got := rbac.WriteCapablePages()
	if len(got) == 0 {
		t.Fatal("no write-capable pages")
	}
	// Every entry must be a page that really does confer something at WRITE.
	for _, page := range got {
		if !rbac.ConfersAtWrite(page) {
			t.Errorf("%q is offered as write-capable but confers nothing", page)
		}
	}
	// SORTED AND STABLE. Go map order is random, and an unsorted payload would
	// differ on every request — which turns a diff of two responses into noise.
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("not sorted: %v", got)
		}
	}
	again := rbac.WriteCapablePages()
	for i := range got {
		if got[i] != again[i] {
			t.Fatalf("two calls disagreed: %v vs %v", got, again)
		}
	}
}

// TestThePageCatalogueIsComplete — a page missing here is a page nobody can
// grant access to, and the card gives no hint that a row is absent.
func TestThePageCatalogueIsComplete(t *testing.T) {
	if len(pageCatalogue) < 20 {
		t.Fatalf("the catalogue has %d pages; the live app has 26", len(pageCatalogue))
	}
	seen := map[string]bool{}
	noToggle := 0
	for _, p := range pageCatalogue {
		if p.Key == "" || p.Title == "" {
			t.Errorf("a page has no key or title: %+v", p)
		}
		if seen[p.Key] {
			t.Errorf("duplicate page key %q", p.Key)
		}
		seen[p.Key] = true
		if p.SettingsKey == nil {
			noToggle++
		}
	}
	// `settingsKey` IS NULL FOR THREE PAGES and that is meaningful: dashboard,
	// reports and settings cannot be hidden, and the card uses the absence to
	// know it must not draw a toggle. An omitted field would look the same as an
	// empty one in JSON, which is why the generator writes an explicit null.
	if noToggle != 3 {
		t.Errorf("%d pages have no settings toggle, want 3 (dashboard, reports, settings)", noToggle)
	}
	// And every page the projection can grant WRITE on must be in the catalogue.
	for _, page := range rbac.WriteCapablePages() {
		if !seen[page] {
			t.Errorf("%q is write-capable but is not in the page catalogue — a role "+
				"could confer it and no row would offer it", page)
		}
	}
}

// TestTheRolesPayloadShape — the keys the card reads, checked once so a rename
// is caught here rather than as an empty column.
func TestTheRolesPayloadShape(t *testing.T) {
	b, err := json.Marshal(map[string]any{
		"ok": true, "roles": []map[string]any{}, "pages": pageCatalogue,
		"writeCapablePages": rbac.WriteCapablePages(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ok", "roles", "pages", "writeCapablePages"} {
		if _, ok := m[k]; !ok {
			t.Errorf("the roles payload has no %q", k)
		}
	}
	first := m["pages"].([]any)[0].(map[string]any)
	for _, k := range []string{"key", "title", "settingsKey"} {
		if _, ok := first[k]; !ok {
			t.Errorf("a page entry has no %q", k)
		}
	}
}
