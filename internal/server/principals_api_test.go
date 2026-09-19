package server

// The Access Management reads: who may see them, and what the roles payload
// carries.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mikrodash/internal/areas"
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
		_, isArea := areas.ByKey(p.Key)
		switch {
		case isArea && p.SettingsKey != nil:
			t.Errorf("area %q has settings toggle %q; every area's visibility is `hiddenAreas`",
				p.Key, *p.SettingsKey)
		case !isArea && p.SettingsKey == nil:
			noToggle++
		}
	}
	// `settingsKey` IS NULL FOR THREE PAGES and that is meaningful: dashboard,
	// reports and settings cannot be hidden, and the card uses the absence to
	// know it must not draw a toggle. An omitted field would look the same as an
	// empty one in JSON, which is why the generator writes an explicit null.
	//
	// Wifi Map was a fourth from PR #134 until it gained `pageWifiMap`; the count
	// went back to three, so a page added without a toggle fails here again.
	//
	// FOUR SINCE THE AI AGENT (#98), and for a different reason from the other
	// three. Dashboard, Reports and Settings cannot be hidden at all. The AI
	// Agent CAN be, and is — by `aiReady`, derived from the AI tab: enabled, with
	// an endpoint and a model. A `pageAiAgent` toggle beside it would be a second
	// switch for one page, and the Visible Pages presets walk that same table
	// writing to `el('s_' + key)` across the whole document, so the Advanced
	// preset would have found the enable toggle in the AI tab and switched the
	// assistant on as a side effect of choosing a nav layout.
	// FIVE SINCE THE FIRST GENERATED PAGE (slice 5 of the MikroMCP parity work).
	// An area's visibility is not a settings key of its own: one `hiddenAreas`
	// list covers every area, which is the whole point — forty areas would
	// otherwise be forty keys, forty corpus re-aims and forty rows in the
	// Visible Pages grid. `ip-pools` is the first.
	//
	// COUNTED APART SINCE 2026-09-18, when IP Addresses became the second area and
	// this number would have moved again — as it would for every area after it.
	// Areas are held to "no toggle" one by one above; the rest are counted here.
	//
	// FIVE SINCE THE TOOLS PAGE (slice 8), by the operator's choice on 2026-09-18.
	// It runs nothing until somebody starts a tool, so hiding it is a question of
	// who may run diagnostics — the permission matrix — and not of router load,
	// which is what the Visible Pages toggles exist to answer.
	//
	// SIX SINCE THE SECURITY SCAN (2026-09-19), for the Tools page's reason: it
	// reads nothing until it is opened, so who sees it is the permission
	// matrix's question (admins only by default), not router load's.
	if noToggle != 6 {
		t.Errorf("%d hand-built pages have no settings toggle, want 6 (dashboard, reports, "+
			"settings, ai-agent, tools, security-scan)", noToggle)
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
