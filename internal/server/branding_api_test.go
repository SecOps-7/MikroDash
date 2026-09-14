package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mikrodash/internal/branding"
)

// brandingServer is settingsWriteServer with the branding routes on its mux.
func brandingServer(t *testing.T, sess *Session) (*Server, *http.ServeMux, string) {
	t.Helper()
	s, mux, dir := settingsWriteServer(t, sess, "")
	s.registerBranding(mux)
	return s, mux, dir
}

func brandingDo(mux *http.ServeMux, method, target, cookie string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.RemoteAddr = "10.0.0.9:1234"
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func brandingViewOf(t *testing.T, w *httptest.ResponseRecorder) brandingView {
	t.Helper()
	var v brandingView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("not a branding view (%d): %s", w.Code, w.Body.String())
	}
	return v
}

func squarePNG(t *testing.T, px int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, px, px))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// THE LOGIN PAGE READS THE BRANDING BEFORE ANYBODY SIGNS IN.
func TestTheBrandingIsReadableWithoutASession(t *testing.T) {
	_, mux, _ := brandingServer(t, &Session{AuthMode: "none", Username: "admin"})
	w := brandingDo(mux, "GET", "/api/branding", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/branding without a session answered %d", w.Code)
	}
	v := brandingViewOf(t, w)
	if v.DisplayName != branding.DefaultName || v.Icon != "/logo.png" || v.CustomIcon {
		t.Errorf("an install with no branding reads as %+v, want MikroDash and the default icon", v)
	}
}

// ONLY AN ADMINISTRATOR MAY CHANGE IT, AND A REFUSAL WRITES NOTHING.
func TestOnlyAnAdministratorMayChangeTheBranding(t *testing.T) {
	_, mux, dir := brandingServer(t, &Session{AuthMode: "modern", Username: "viewer"})
	body := []byte(`{"name":"Hijacked","font":""}`)
	if w := brandingDo(mux, "PUT", "/api/branding", "", body); w.Code != http.StatusUnauthorized {
		t.Errorf("an anonymous save answered %d, want 401", w.Code)
	}
	if w := brandingDo(mux, "PUT", "/api/branding", authed, body); w.Code != http.StatusForbidden {
		t.Errorf("a non-administrator's save answered %d, want 403", w.Code)
	}
	if w := brandingDo(mux, "POST", "/api/branding/icon", authed, squarePNG(t, 64)); w.Code != http.StatusForbidden {
		t.Errorf("a non-administrator's icon upload answered %d, want 403", w.Code)
	}
	if w := brandingDo(mux, "DELETE", "/api/branding/icon", authed, nil); w.Code != http.StatusForbidden {
		t.Errorf("a non-administrator's icon reset answered %d, want 403", w.Code)
	}
	if b, _ := branding.Load(dir); b != (branding.Branding{}) {
		t.Errorf("a refused request changed the branding: %+v", b)
	}
}

func TestAnAdministratorSavesANameAndFont(t *testing.T) {
	s, mux, dir := brandingServer(t, &Session{AuthMode: "none", Username: "admin"})
	w := brandingDo(mux, "PUT", "/api/branding", authed, []byte(`{"name":"  Acme Networks ","font":"inter"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("the save answered %d: %s", w.Code, w.Body.String())
	}
	if v := brandingViewOf(t, w); v.Name != "Acme Networks" || v.DisplayName != "Acme Networks" || v.Font != "inter" {
		t.Errorf("the save answered %+v", v)
	}
	if b, _ := branding.Load(dir); b.Name != "Acme Networks" || b.Font != "inter" {
		t.Errorf("the stored branding is %+v", b)
	}
	if _, ok := auditActions(t, s)["branding.update"]; !ok {
		t.Error("the save was not audited")
	}
	if got := s.appName(); got != "Acme Networks" {
		t.Errorf("report emails would be sent by %q", got)
	}
	if got := s.reportBrand(); got.Name != "Acme Networks" || got.Icon != nil {
		t.Errorf("the PDF header would draw %+v", got)
	}

	for body, why := range map[string]string{
		`{"name":"` + strings.Repeat("x", branding.MaxNameRunes+1) + `"}`: "an over-long name",
		`{"name":"Acme","font":"inter;color:red"}`:                        "a font that is not an id",
		`not json`: "a body that is not JSON",
	} {
		if w := brandingDo(mux, "PUT", "/api/branding", authed, []byte(body)); w.Code != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400", why, w.Code)
		}
	}
}

func TestAnIconIsStoredServedAndReset(t *testing.T) {
	s, mux, dir := brandingServer(t, &Session{AuthMode: "none", Username: "admin"})

	if w := brandingDo(mux, "POST", "/api/branding/icon", authed,
		[]byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>")); w.Code != http.StatusBadRequest {
		t.Errorf("an SVG upload answered %d, want 400", w.Code)
	}
	if w := brandingDo(mux, "POST", "/api/branding/icon", authed, squarePNG(t, 40)); w.Code != http.StatusBadRequest {
		t.Errorf("a 40px icon answered %d, want 400", w.Code)
	}

	w := brandingDo(mux, "POST", "/api/branding/icon", authed, squarePNG(t, 128))
	if w.Code != http.StatusOK {
		t.Fatalf("a valid icon answered %d: %s", w.Code, w.Body.String())
	}
	v := brandingViewOf(t, w)
	if !v.CustomIcon || !strings.HasPrefix(v.Icon, "/brand/icon.png?v=") {
		t.Errorf("after an upload the branding reads %+v, want a versioned custom icon URL", v)
	}
	if _, ok := auditActions(t, s)["branding.icon"]; !ok {
		t.Error("the upload was not audited")
	}

	got := brandingDo(mux, "GET", "/brand/icon.png", "", nil)
	if got.Code != http.StatusOK || got.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("the icon route answered %d %q", got.Code, got.Header().Get("Content-Type"))
	}
	if !bytes.Equal(got.Body.Bytes(), branding.ReadIcon(dir)) {
		t.Error("the icon route served something other than the stored icon")
	}
	if b := s.reportBrand(); b.Icon == nil {
		t.Error("the PDF header would draw no icon after one was uploaded")
	}

	w = brandingDo(mux, "DELETE", "/api/branding/icon", authed, nil)
	if w.Code != http.StatusOK || brandingViewOf(t, w).CustomIcon {
		t.Fatalf("the reset answered %d: %s", w.Code, w.Body.String())
	}
	if branding.ReadIcon(dir) != nil {
		t.Error("the reset left the icon file behind")
	}
	// This test server has no static directory, so the default icon is not there
	// to serve: the route must say so rather than serve the removed icon.
	if got := brandingDo(mux, "GET", "/brand/icon.png", "", nil); got.Code != http.StatusNotFound {
		t.Errorf("after a reset the icon route answered %d, want the default (absent here, so 404)", got.Code)
	}
}
