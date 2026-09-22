package server

// The interface language, kept on the account (#94).
//
// The browser remembers a choice in the `md_lang` cookie, which is what picks
// the translated document (`servedDoc`). A cookie is one browser's, so the
// choice is also stored on the account, as a `user_layouts` row of kind
// 'lang', and signing in elsewhere brings it along:
//
//   - `POST /api/lang` stores the choice and sets the cookie, from Settings.
//   - At sign-in, a stored choice sets the cookie. With none stored, the
//     cookie's own choice (made on the sign-in page, say) is stored, so the
//     first choice a user makes is the one that follows them.

import (
	"encoding/json"
	"net/http"

	"mikrodash/internal/i18n"
)

func (s *Server) registerLang(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/lang", s.langSave)
}

// offersLang is whether this build can show a language: English always, and
// any language whose documents were built.
func (s *Server) offersLang(code string) bool {
	if code == "en" {
		return true
	}
	for _, l := range s.langs {
		if l == code {
			return true
		}
	}
	return false
}

// langCookie is the cookie that picks the document, as the browser's own
// `setLang` writes it: readable by the page, for a year, and Secure when the
// session cookie is.
func (s *Server) langCookie(code string) string {
	c := "md_lang=" + code + "; Path=/; Max-Age=31536000; SameSite=Lax"
	if s.forceHTTPS {
		c += "; Secure"
	}
	return c
}

func (s *Server) langSave(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	var body struct {
		Lang string `json:"lang"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil ||
		!i18n.ValidLang(body.Lang) || !s.offersLang(body.Lang) {
		writeJSONErr(w, http.StatusBadRequest, "unknown language")
		return
	}
	user := s.layoutUser(sess)
	if user == "" {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if err := s.auditDB.SetLayout(user, "lang", map[string]string{"lang": body.Lang}); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not save")
		return
	}
	w.Header().Add("Set-Cookie", s.langCookie(body.Lang))
	writeJSON(w, map[string]any{"ok": true})
}

// syncAccountLang runs at sign-in: the account's language wins and sets the
// cookie, and an account with none takes the cookie's. A language this build
// no longer offers is left alone, so a catalog removed and restored brings the
// choice back rather than losing it.
func (s *Server) syncAccountLang(w http.ResponseWriter, r *http.Request, userID string) {
	if userID == "" || s.auditDB == nil {
		return
	}
	if blob, err := s.auditDB.Layout(userID, "lang"); err == nil && blob != nil {
		var stored struct {
			Lang string `json:"lang"`
		}
		if json.Unmarshal(blob, &stored) == nil && i18n.ValidLang(stored.Lang) {
			if s.offersLang(stored.Lang) {
				w.Header().Add("Set-Cookie", s.langCookie(stored.Lang))
			}
			return
		}
	}
	if c, err := r.Cookie("md_lang"); err == nil && i18n.ValidLang(c.Value) && s.offersLang(c.Value) {
		_ = s.auditDB.SetLayout(userID, "lang", map[string]string{"lang": c.Value})
	}
}
