package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikrodash/internal/db"
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

// The language on the account: `POST /api/lang` stores it and sets the cookie,
// and signing in on a browser with no choice brings it back. With nothing
// stored, the sign-in page's own choice is what gets kept.
func TestTheLanguageFollowsTheAccount(t *testing.T) {
	const pw = "an-invented-password-for-lang"
	web := t.TempDir()
	for _, f := range []string{"index.html", "login.html", "index.de.html", "login.de.html"} {
		if err := os.WriteFile(filepath.Join(web, f), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	server := func() (http.Handler, *db.DB) {
		d, err := db.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		srv, err := New(authFixture(t, pw), Options{WebDir: web, AuditDB: d})
		if err != nil {
			t.Fatal(err)
		}
		return srv.Handler(), d
	}
	login := func(h http.Handler, langCookie string) (sid, lang string) {
		req := httptest.NewRequest("POST", "/api/auth/login",
			strings.NewReader(`{"username":"someone","password":"`+pw+`"}`))
		if langCookie != "" {
			req.AddCookie(&http.Cookie{Name: "md_lang", Value: langCookie})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("sign-in answered %d", rec.Code)
		}
		for _, c := range rec.Result().Cookies() {
			switch c.Name {
			case "mikrodash_sid":
				sid = c.Value
			case "md_lang":
				lang = c.Value
			}
		}
		return sid, lang
	}
	choose := func(h http.Handler, sid, body string) (int, string) {
		req := httptest.NewRequest("POST", "/api/lang", strings.NewReader(body))
		if sid != "" {
			req.AddCookie(&http.Cookie{Name: "mikrodash_sid", Value: sid})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		lang := ""
		for _, c := range rec.Result().Cookies() {
			if c.Name == "md_lang" {
				lang = c.Value
			}
		}
		return rec.Code, lang
	}

	h, _ := server()
	if code, _ := choose(h, "", `{"lang":"de"}`); code != http.StatusUnauthorized {
		t.Errorf("a choice with no session answered %d, want 401", code)
	}
	// THE CONTROL: nothing stored, no cookie, so sign-in sets no language.
	sid, lang := login(h, "")
	if lang != "" {
		t.Fatalf("a first sign-in set md_lang=%q with nothing chosen", lang)
	}
	for _, bad := range []string{`{"lang":"fr"}`, `{"lang":"../x"}`, `{}`, `nope`} {
		if code, _ := choose(h, sid, bad); code != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400: a language this build lacks is not stored", bad, code)
		}
	}
	if code, set := choose(h, sid, `{"lang":"de"}`); code != http.StatusOK || set != "de" {
		t.Fatalf("choosing de answered %d with md_lang=%q", code, set)
	}
	// Another browser, no cookie: the account's choice comes with the sign-in.
	if _, lang := login(h, ""); lang != "de" {
		t.Errorf("signing in elsewhere set md_lang=%q, want de from the account", lang)
	}
	// English is a choice too, and the account keeps it over a German cookie.
	if code, _ := choose(h, sid, `{"lang":"en"}`); code != http.StatusOK {
		t.Fatalf("choosing en answered %d", code)
	}
	if _, lang := login(h, "de"); lang != "en" {
		t.Errorf("sign-in with a de cookie set md_lang=%q, want the account's en", lang)
	}

	// A NEW ACCOUNT takes the sign-in page's choice.
	h2, d2 := server()
	if _, lang := login(h2, "de"); lang != "" {
		t.Errorf("with nothing stored, sign-in rewrote the cookie to %q", lang)
	}
	if blob, _ := d2.Layout("u-1", "lang"); string(blob) != `{"lang":"de"}` {
		t.Errorf("the account holds %s after signing in with de, want {\"lang\":\"de\"}", blob)
	}
}
