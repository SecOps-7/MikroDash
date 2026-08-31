package server

// `POST /api/nav-prefs`, judged against what the LIVE filter answered.

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"

	"mikrodash/internal/db"

	"encoding/json"
	_ "modernc.org/sqlite"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

type navCorpus struct {
	CategoryKeys []string `json:"categoryKeys"`
	Cases        map[string]struct {
		Body     map[string]any `json:"body"`
		Accepted bool           `json:"accepted"`
		Stored   *struct {
			Grouped  bool     `json:"grouped"`
			Expanded []string `json:"expanded"`
		} `json:"stored"`
	} `json:"cases"`
}

func loadNavCorpus(t *testing.T) navCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/navprefs-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c navCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("the corpus is empty -- this test would pass against nothing")
	}
	return c
}

// TestTheCategoryAllowListIsTheLiveOne. The filter is a stored-XSS boundary, so
// the list it filters against is compared to the live registry rather than
// assumed to have been copied correctly.
func TestTheCategoryAllowListIsTheLiveOne(t *testing.T) {
	c := loadNavCorpus(t)
	if len(navCategoryKeys) != len(c.CategoryKeys) {
		t.Fatalf("%d category keys embedded, %d live", len(navCategoryKeys), len(c.CategoryKeys))
	}
	for _, k := range c.CategoryKeys {
		if !navCategoryKeys[k] {
			t.Errorf("%q is a live category and is not in the allow-list -- expanding it would "+
				"silently stop being remembered", k)
		}
	}
}

// TestNavPrefsMatchesLive drives every recorded body through the REAL mux and
// compares the stored blob by reading it back.
func TestNavPrefsMatchesLive(t *testing.T) {
	c := loadNavCorpus(t)
	const pw = "a-password-this-test-invents"

	for name, tc := range c.Cases {
		t.Run(name, func(t *testing.T) {
			h, token := signedInServer(t, pw)

			raw, _ := json.Marshal(tc.Body)
			req := httptest.NewRequest("POST", "/api/nav-prefs", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Cookie", "mikrodash_sid="+token)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			wantStatus := http.StatusOK
			if !tc.Accepted {
				wantStatus = http.StatusBadRequest
			}
			if rec.Code != wantStatus {
				t.Fatalf("status %d, live would answer %d: %s",
					rec.Code, wantStatus, rec.Body.String())
			}
			if !tc.Accepted {
				// A REFUSAL MUST NOT HAVE WRITTEN. Checked rather than assumed:
				// a handler that validated after saving would answer 400 and
				// still have stored the payload, which is the worst of both.
				if got := readNavPrefs(t, h, token); got != nil {
					t.Errorf("a refused body was stored anyway: %v", got)
				}
				return
			}

			got := readNavPrefs(t, h, token)
			if got == nil {
				t.Fatal("an accepted body stored nothing")
			}
			if got.Grouped != tc.Stored.Grouped {
				t.Errorf("grouped = %v, live %v", got.Grouped, tc.Stored.Grouped)
			}
			if len(got.Expanded) != len(tc.Stored.Expanded) {
				t.Fatalf("expanded %v, live %v", got.Expanded, tc.Stored.Expanded)
			}
			for i := range got.Expanded {
				if got.Expanded[i] != tc.Stored.Expanded[i] {
					t.Errorf("expanded %v, live %v -- the order is part of the answer, so two "+
						"clients expanding the same categories store the same blob",
						got.Expanded, tc.Stored.Expanded)
					break
				}
			}
		})
	}
}

type navBlob struct {
	Grouped  bool     `json:"grouped"`
	Expanded []string `json:"expanded"`
}

func readNavPrefs(t *testing.T, h http.Handler, token string) *navBlob {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/nav-prefs", nil)
	req.Header.Set("Cookie", "mikrodash_sid="+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/nav-prefs answered %d", rec.Code)
	}
	body := bytes.TrimSpace(rec.Body.Bytes())
	if string(body) == "null" {
		return nil
	}
	var out navBlob
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("the stored blob is not the expected shape: %s", body)
	}
	return &out
}

// TestThePreferenceIsKeyedOnTheUser.
//
// `_layoutUser(req)` is `authSession?.userId || SHARED_LAYOUT_USER`, so the
// shared identity is the FALLBACK for authMode 'none' and never the answer for a
// signed-in user. Keying everyone on `_shared` still passes every other test
// here — one user writes and reads back their own blob either way — and would
// silently give a whole install one shared sidebar, where the last save wins.
// That mutation survived until this existed.
func TestThePreferenceIsKeyedOnTheUser(t *testing.T) {
	h, token := signedInServer(t, "yet-another-invented-password")

	req := httptest.NewRequest("POST", "/api/nav-prefs",
		bytes.NewReader([]byte(`{"grouped":true,"expanded":[]}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "mikrodash_sid="+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save answered %d", rec.Code)
	}

	// Read the KEY out of the database rather than inferring it from a
	// round-trip, which is what a shared key would also satisfy.
	rows := navLayoutUsers(t)
	if len(rows) != 1 {
		t.Fatalf("%d layout rows, want 1: %v", len(rows), rows)
	}
	if rows[0] == db.SharedLayoutUser {
		t.Errorf("the preference was stored under %q, the SHARED identity, for a signed-in "+
			"user -- every account on the install would share one sidebar and the last save "+
			"would win", rows[0])
	}
	// ── THE USER ID, NOT THE USERNAME ───────────────────────────────────
	//
	// `user_layouts` is a table BOTH PROCESSES READ, and Node writes
	// `authSession.userId`. This port keyed on the username first, and the
	// consequence was only visible in the real database after a standalone run:
	// one account with TWO nav rows, one written by each half, neither able to
	// see the other. A round trip through one implementation agrees with itself
	// whatever the key, which is why no unit test caught it — so this asserts
	// the KEY rather than the round trip.
	if rows[0] == "someone" {
		t.Error("the preference was stored under the USERNAME. Node writes the user id, so a " +
			"sidebar saved here would be invisible there and the state would appear to " +
			"forget itself depending on which half served the request")
	}
	if rows[0] != "u-1" {
		t.Errorf("stored under %q, want the signed-in user's ID", rows[0])
	}
}

// TestNavPrefsNeedsASession. A preference keyed on the user must not be
// readable or writable without one, or every anonymous request shares the
// `_shared` identity and can overwrite it.
func TestNavPrefsNeedsASession(t *testing.T) {
	h, _ := signedInServer(t, "another-invented-password")
	for _, m := range []string{"GET", "POST"} {
		req := httptest.NewRequest(m, "/api/nav-prefs", bytes.NewReader([]byte(`{}`)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with no cookie answered %d, want 401", m, rec.Code)
		}
	}
}

// navTestDDL is the layout table plus the version row `db.Open` insists on.
//
// The CHECK is the live one: three kinds and no more. A test schema without it
// would accept a `kind` the real one refuses, so a port writing the wrong kind
// would pass here and fail on a real /data.
// (No backticks in this comment: it sits inside a Go raw string.)
const navTestDDL = `
CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
INSERT INTO schema_version (version, applied_at) VALUES (14, 0);
CREATE TABLE user_layouts (
  user_id    TEXT NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('dashboard','topology','nav')),
  data       TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, kind)
);
`

// signedInServer stands a STANDALONE server up with a real layout database and
// signs a user in through the real login route, returning its session token.
//
// Through the route rather than by inserting a session: the point is that the
// blob is keyed on whoever the server thinks is asking, and a hand-made session
// would let a mistake in that keying pass unnoticed.
func signedInServer(t *testing.T, password string) (http.Handler, string) {
	t.Helper()
	st := authFixture(t, password)

	dbDir := t.TempDir()
	h, err := sql.Open("sqlite", filepath.Join(dbDir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(navTestDDL); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	navDBPath = filepath.Join(dbDir, "mikrodash.db")

	srv, err := New(st, Options{NodeURL: "", WebDir: t.TempDir(), AuditDB: d})
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()

	rec := postLogin(handler, "someone", password)
	if rec.Code != http.StatusOK {
		t.Fatalf("the fixture could not sign in: %d %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Header().Get("Set-Cookie")
	token := strings.SplitN(strings.TrimPrefix(cookie, "mikrodash_sid="), ";", 2)[0]
	if token == "" {
		t.Fatalf("no session token in %q", cookie)
	}
	return handler, token
}

// navLayoutUsers reads the user_id of every stored layout. The path is captured
// by signedInServer, so this reads the same database the handler wrote to.
func navLayoutUsers(t *testing.T) []string {
	t.Helper()
	h, err := sql.Open("sqlite", navDBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	rows, err := h.Query(`SELECT user_id FROM user_layouts ORDER BY user_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			t.Fatal(err)
		}
		out = append(out, u)
	}
	return out
}

// navDBPath is set by signedInServer so the assertion above can reach the file.
var navDBPath string
