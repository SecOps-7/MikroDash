package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The document served for "/" and "/login", by the md_lang cookie and the
// browser's language (#94). With no translations built, nothing changes: the
// control is the first case, and it checks the header too.
func TestTheServedDocumentFollowsTheLanguage(t *testing.T) {
	write := func(dir string, files ...string) {
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte(f), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	get := func(s *Server, h http.Handler, path, cookie, accept string) (string, string) {
		r := httptest.NewRequest("GET", path, nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "md_lang", Value: cookie})
		}
		if accept != "" {
			r.Header.Set("Accept-Language", accept)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		b, _ := io.ReadAll(w.Result().Body)
		return string(b), w.Header().Get("Vary")
	}

	// NO TRANSLATIONS: exactly as before, whatever the browser asks for.
	plain := t.TempDir()
	write(plain, "index.html", "login.html")
	s := &Server{web: http.FileServer(http.Dir(plain)), langs: builtLangs(plain)}
	if body, vary := get(s, s.spa(), "/home", "zh-CN", "zh-CN"); body != "index.html" || vary != "" {
		t.Errorf("no translations: served %q with Vary %q", body, vary)
	}

	dir := t.TempDir()
	// de has no login page: a half-built language is not offered at all.
	write(dir, "index.html", "login.html", "index.zh-CN.html", "login.zh-CN.html", "index.de.html")
	s = &Server{web: http.FileServer(http.Dir(dir)), langs: builtLangs(dir)}
	if len(s.langs) != 1 || s.langs[0] != "zh-CN" {
		t.Fatalf("langs = %v, want [zh-CN]", s.langs)
	}
	cases := []struct{ path, cookie, accept, want string }{
		{"/home", "", "", "index.html"},
		{"/home", "zh-CN", "", "index.zh-CN.html"},
		{"/logs", "", "zh-TW,zh;q=0.9", "index.zh-CN.html"},
		{"/home", "en", "zh-CN", "index.html"}, // English, chosen, beats the browser
		{"/home", "de", "", "index.html"},      // not offered
		{"/home", "../../x", "", "index.html"}, // not a language
		{"/login", "zh-CN", "", "login.zh-CN.html"},
		{"/login", "", "", "login.html"},
	}
	for _, c := range cases {
		h := s.spa()
		if c.path == "/login" {
			h = s.distFile("/login.html")
		}
		body, vary := get(s, h, c.path, c.cookie, c.accept)
		if body != c.want {
			t.Errorf("%s cookie=%q accept=%q: served %q, want %q", c.path, c.cookie, c.accept, body, c.want)
		}
		if vary == "" {
			t.Errorf("%s: no Vary header, so a cache could serve one language to everyone", c.path)
		}
	}
}
