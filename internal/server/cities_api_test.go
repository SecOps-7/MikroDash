package server

// `GET /api/cities` — the guard, and the unavailable answer.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func getCities(t *testing.T, h http.Handler, token, query string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/cities?"+query, nil)
	if token != "" {
		req.Header.Set("Cookie", "mikrodash_sid="+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// TestCitySearchNeedsSomethingWorthProtecting.
//
// The live guard's own comment says what it is for and what it is not: "This
// guard is about resources, not confidentiality: place names are public
// geographic data... What it protects is the *build* — the first search costs a
// few hundred milliseconds and tens of megabytes, so an arbitrary viewer should
// not be able to trigger it."
//
// So a principal with NOTHING is refused (they can edit no location, so the
// build would be pure cost), and the fixture here grants nothing.
func TestCitySearchNeedsSomethingWorthProtecting(t *testing.T) {
	h, token := signedInServer(t, "a-password-for-cities")

	if code, _ := getCities(t, h, "", "q=lon"); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated answered %d, want 401", code)
	}
	if code, _ := getCities(t, h, token, "q=lon"); code != http.StatusForbidden {
		t.Errorf("a principal with no grants answered %d, want 403", code)
	}
}

// TestEitherCapabilityOpensTheCitySearch.
//
// "Anyone who can edit something that carries a location may search: a site
// (system:principals) or at least one router." EITHER, never both — a router
// administrator with no site rights edits a router's location, and requiring
// both would lock each of them out of the form they actually fill in.
//
// The fixture grants `devices` at WRITE, which projects to `router:manage` and
// confers no `system:principals` — so it exercises the second arm alone.
func TestEitherCapabilityOpensTheCitySearch(t *testing.T) {
	h, token, _ := citySearchServer(t, "another-password-for-cities")

	code, body := getCities(t, h, token, "q=lon")
	if code != http.StatusOK {
		t.Fatalf("a router manager answered %d, want 200. The guard is EITHER capability, and "+
			"requiring system:principals as well locks out the administrator who edits a "+
			"router's location", code)
	}
	if body["ok"] != true {
		t.Errorf("ok is %v", body["ok"])
	}
}

// TestAnUnreadableGazetteerIsNotAnError.
//
// "An install whose geoip data cannot be read still works; it simply cannot
// offer the picker, and the widget renders that as a message rather than as a
// failure." So it is a 200 with a flag — and `cities` is an EMPTY ARRAY beside
// it, not omitted, because the widget reads its length before the flag.
//
// The fixture points at no geo directory at all, which is that state.
func TestAnUnreadableGazetteerIsNotAnError(t *testing.T) {
	h, token, _ := citySearchServer(t, "a-third-password-for-cities")

	code, body := getCities(t, h, token, "q=lon")
	if code != http.StatusOK {
		t.Fatalf("an unreadable gazetteer answered %d, want 200 -- it is a supported state, "+
			"not a failure", code)
	}
	if body["unavailable"] != true {
		t.Errorf("unavailable is %v, want true", body["unavailable"])
	}
	cities, ok := body["cities"].([]any)
	if !ok {
		t.Fatalf("cities is %T, want an array -- the widget reads its length before the flag",
			body["cities"])
	}
	if len(cities) != 0 {
		t.Errorf("%d cities from an unavailable index", len(cities))
	}
	if body["reason"] == nil || body["reason"] == "" {
		t.Error("no reason was given for the unavailability")
	}
}
