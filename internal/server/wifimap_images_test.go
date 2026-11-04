package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"mikrodash/internal/sitedoc"
)

// u-1 may draw the Wi-Fi map of r-A and only look at r-B's.
const mapGrantsDDL = `INSERT INTO role_pages (role_id, page, access)
  VALUES ('role-w','wifi-map','write'), ('role-r','wifi-map','read');
CREATE TABLE router_docs (router_id TEXT NOT NULL, kind TEXT NOT NULL, data TEXT NOT NULL,
  updated_at INTEGER NOT NULL, PRIMARY KEY (router_id, kind));`

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 40, 30))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mapImageCall(s *Server, method, url string, body []byte, signedIn bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, bytes.NewReader(body))
	if signedIn {
		req.Header.Set("Cookie", authed)
	}
	w := httptest.NewRecorder()
	if method == http.MethodPost {
		s.mapImageSave(w, req)
	} else {
		s.mapImageGet(w, req)
	}
	return w
}

// TestABackdropImageFollowsThePlansGrants. An upload needs write access to the
// Wi-Fi map on that router and a reading needs read access, as the plan does;
// what is stored is re-encoded and named so that no id can reach another path.
func TestABackdropImageFollowsThePlansGrants(t *testing.T) {
	s := gateServer(t, `{}`, mapGrantsDDL)
	for _, c := range []struct {
		name     string
		router   string
		body     []byte
		signedIn bool
		want     int
	}{
		{"signed out", "r-A", tinyPNG(t), false, 401},
		{"read only", "r-B", tinyPNG(t), true, 403},
		{"not an image", "r-A", []byte("<svg onload=alert(1)>"), true, 400},
		{"bad router id", "../x", tinyPNG(t), true, 400},
	} {
		if w := mapImageCall(s, http.MethodPost, "/api/router-doc/image?routerId="+c.router, c.body, c.signedIn); w.Code != c.want {
			t.Errorf("%s: answered %d, want %d", c.name, w.Code, c.want)
		}
	}

	w := mapImageCall(s, http.MethodPost, "/api/router-doc/image?routerId=r-A", tinyPNG(t), true)
	if w.Code != 200 {
		t.Fatalf("upload answered %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		ID   string `json:"id"`
		W, H int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !sitedoc.ImageIDRe.MatchString(got.ID) || got.W != 40 || got.H != 30 {
		t.Fatalf("upload answered %+v", got)
	}
	r := mapImageCall(s, http.MethodGet, "/api/router-doc/image?routerId=r-A&id="+got.ID, nil, true)
	if r.Code != 200 || r.Header().Get("Content-Type") != "image/png" || r.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("reading it answered %d %q", r.Code, r.Header().Get("Content-Type"))
	}
	if _, err := png.Decode(r.Body); err != nil {
		t.Errorf("what came back is not a PNG: %v", err)
	}
	for _, url := range []string{
		"/api/router-doc/image?routerId=r-B&id=" + got.ID,
		"/api/router-doc/image?routerId=r-A&id=../../settings.json",
		"/api/router-doc/image?routerId=r-A&id=" + got.ID + "/..",
	} {
		if r := mapImageCall(s, http.MethodGet, url, nil, true); r.Code != 404 {
			t.Errorf("%s answered %d, want 404", url, r.Code)
		}
	}
}

// TestAnUploadSaysWhyItFailed. Only a body over the limit is called too large:
// an upload cut off half way is a different thing to tell the operator.
func TestAnUploadSaysWhyItFailed(t *testing.T) {
	s := gateServer(t, `{}`, mapGrantsDDL)
	for _, c := range []struct {
		name string
		body io.Reader
		says string
	}{
		{"over the limit", bytes.NewReader(make([]byte, sitedoc.MaxImageBytes+1)), "larger than"},
		{"cut off", iotest.ErrReader(errors.New("connection reset")), "did not finish"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/router-doc/image?routerId=r-A", c.body)
		req.Header.Set("Cookie", authed)
		w := httptest.NewRecorder()
		s.mapImageSave(w, req)
		if w.Code != 400 || !strings.Contains(w.Body.String(), c.says) {
			t.Errorf("%s: answered %d %s, want 400 saying %q", c.name, w.Code, w.Body.String(), c.says)
		}
	}
}

// TestRemovingARouterRemovesItsImages, and leaves the other router's alone.
func TestRemovingARouterRemovesItsImages(t *testing.T) {
	s, mux, _ := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
	for _, rid := range []string{"r1", "r2"} {
		dir := s.mapImageDir(rid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "00000000000000aa.png"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if w := routerDelete(mux, "r1", authed); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(s.mapImageDir("r1")); !os.IsNotExist(err) {
		t.Errorf("the removed router's folder is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.mapImageDir("r2"), "00000000000000aa.png")); err != nil {
		t.Errorf("the other router lost its image: %v", err)
	}
}

// TestASavedPlanTakesItsUnusedImagesWithIt. Saving the plan removes what it does
// not name, once that is old enough not to be a second editor's fresh upload.
// Through the save handler, so the call to the clean-up is held too.
func TestASavedPlanTakesItsUnusedImagesWithIt(t *testing.T) {
	s := gateServer(t, `{}`, mapGrantsDDL)
	dir := s.mapImageDir("r-A")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	files := map[string]bool{
		"00000000000000aa.png": true, // named by the plan
		"00000000000000bb.jpg": false,
		"00000000000000cc.png": true, // not named, but fresh
	}
	for name := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if name != "00000000000000cc.png" {
			_ = os.Chtimes(path, old, old)
		}
	}
	body := `{"routerId":"r-A","kind":"wifi-map","doc":{"backdrops":[
	  {"floor":1,"kind":"image","image":"00000000000000aa.png","w":10,"h":10}]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/router-doc", strings.NewReader(body))
	req.Header.Set("Cookie", authed)
	w := httptest.NewRecorder()
	s.routerDocSave(w, req)
	if w.Code != 200 {
		t.Fatalf("the save answered %d: %s", w.Code, w.Body.String())
	}
	for name, kept := range files {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != kept {
			t.Errorf("%s: kept %v, want %v", name, err == nil, kept)
		}
	}
}
