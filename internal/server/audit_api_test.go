package server

import (
	"net/http/httptest"
	"testing"
)

// ── The narrowing rule ───────────────────────────────────────────────────────

// TestRouterIDFilterNarrowsAndNeverWidens is the security property in
// auditQueryFor: `?routerId=` may only ever SELECT WITHIN the permitted set.
//
// The failure it guards against is not subtle once seen — a filter honoured
// without checking scope hands any signed-in user any router's trail by editing
// a URL — but it is invisible in a read of the handler, because honouring a
// filter is what a filter is for.
func TestRouterIDFilterNarrowsAndNeverWidens(t *testing.T) {
	permitted := []string{"r-A", "r-B"}

	for _, tc := range []struct {
		name, query string
		want        []string
	}{
		{"no filter keeps the permitted set", "", []string{"r-A", "r-B"}},
		{"a permitted id narrows to it", "?routerId=r-B", []string{"r-B"}},
		{"an UNPERMITTED id is ignored", "?routerId=r-secret", []string{"r-A", "r-B"}},
		{"an unknown id is ignored", "?routerId=nonexistent", []string{"r-A", "r-B"}},
		{"an empty value is no filter", "?routerId=", []string{"r-A", "r-B"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/audit"+tc.query, nil)
			got := auditQueryFor(r, true, permitted).RouterIDs
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestRouterIDIsNeverPassedAsTheSeparateFilter pins the file header's argument.
// db.Query has a `RouterID` field that ANDs `router_id = ?` AFTER the visibility
// clause; `_auditQuery` never sets it, and wiring the query parameter there
// would return app-scoped rows alongside a router the session may not see.
func TestRouterIDIsNeverPassedAsTheSeparateFilter(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/audit?routerId=r-A", nil)
	if q := auditQueryFor(r, true, []string{"r-A"}); q.RouterID != "" {
		t.Errorf("RouterID = %q; the narrowing must go through RouterIDs only", q.RouterID)
	}
}

// ── The rest of the query ────────────────────────────────────────────────────

func TestOutcomeFilterIsAWhitelist(t *testing.T) {
	for _, ok := range []string{"ok", "denied", "failed"} {
		if outcomeFilter(ok) != ok {
			t.Errorf("outcomeFilter(%q) dropped a valid outcome", ok)
		}
	}
	// Anything else becomes NO filter rather than an error or a literal — a
	// value that reached SQL unchecked would be a filter nobody can satisfy.
	for _, bad := range []string{"OK", "pending", "'; DROP TABLE audit_events--", ""} {
		if got := outcomeFilter(bad); got != "" {
			t.Errorf("outcomeFilter(%q) = %q, want \"\"", bad, got)
		}
	}
}

func TestQueryParamsFollowJavaScriptParseInt(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/audit?from=1700000000000&to=1800000000000&limit=50&offset=10", nil)
	q := auditQueryFor(r, true, nil)
	if q.From != 1700000000000 || q.To != 1800000000000 {
		t.Errorf("range: got %d..%d", q.From, q.To)
	}
	if q.Limit != 50 || q.Offset != 10 {
		t.Errorf("paging: got limit=%d offset=%d", q.Limit, q.Offset)
	}

	// `parseInt` reads a leading integer and ignores the rest; garbage is 0.
	r = httptest.NewRequest("GET", "/api/audit?from=123abc&limit=nonsense", nil)
	q = auditQueryFor(r, true, nil)
	if q.From != 123 {
		t.Errorf("from=123abc: got %d, want 123", q.From)
	}
	if q.Limit != 0 {
		t.Errorf("limit=nonsense: got %d, want 0 (the db clamps it to 200)", q.Limit)
	}
}

// TestAbsentToBecomesNow pins `parseInt(q.to,10) || Date.now()`. A zero `to`
// would mean "everything before the epoch", i.e. an empty trail.
func TestAbsentToBecomesNow(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/audit", nil)
	if q := auditQueryFor(r, true, nil); q.To < 1700000000000 {
		t.Errorf("absent `to` = %d; want roughly now", q.To)
	}
}

func TestFilterValuesAreClipped(t *testing.T) {
	long := ""
	for i := 0; i < 300; i++ {
		long += "x"
	}
	r := httptest.NewRequest("GET", "/api/audit?actor="+long+"&action="+long+"&search="+long, nil)
	q := auditQueryFor(r, true, nil)
	if len(q.Actor) != 100 || len(q.Action) != 60 || len(q.Search) != 100 {
		t.Errorf("clipped to actor=%d action=%d search=%d; want 100/60/100",
			len(q.Actor), len(q.Action), len(q.Search))
	}
}
