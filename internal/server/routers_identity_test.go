package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
	"mikrodash/internal/session"
)

// A ROUTER'S IDENTITY IS A ROUTER WRITE, AND KEEPS THE #97 CONTRACT.
//
// The inner write is driven against a scripted router that holds one System
// Identity. `ignoreSet` makes it accept /system/identity/set and change nothing,
// which is the case the read-back exists for.

type identityRouter struct {
	mu        sync.Mutex
	name      string
	ignoreSet bool
	sets      []string
}

func (r *identityRouter) exec(cmd routeros.Cmd) ([]routeros.Reply, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch cmd.Path {
	case "/system/identity/print":
		return []routeros.Reply{{"name": r.name}}, nil
	case "/system/identity/set":
		for _, a := range cmd.Args {
			if v, ok := strings.CutPrefix(a, "=name="); ok {
				r.sets = append(r.sets, v)
				if !r.ignoreSet {
					r.name = v
				}
			}
		}
	}
	return nil, nil
}

func (r *identityRouter) written() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sets...)
}

func identityFixture(router *identityRouter) (*Server, *Session, *session.Session, *http.Request) {
	s := &Server{writeLimit: newWriteLimiter()}
	sess := &Session{Username: "someone", AuthMode: "modern"}
	sn := session.NewForTestWithExec(hub.New(), "r-A", router.exec)
	return s, sess, sn, httptest.NewRequest(http.MethodPut, "/api/routers/r-A/identity", nil)
}

func TestARouterIdentityIsSetAndReadBack(t *testing.T) {
	router := &identityRouter{name: "MikroTik"}
	s, sess, sn, r := identityFixture(router)
	got := s.setRouterIdentity(r, sess, sn, "r-A", "office-gw")
	if !got.OK || got.Code != "updated" || got.Name != "office-gw" {
		t.Errorf("a confirmed identity change answered %+v, want ok, updated, office-gw", got)
	}
	if w := router.written(); len(w) != 1 || w[0] != "office-gw" {
		t.Errorf("the router was sent %v, want one set to office-gw", w)
	}
}

func TestAnUnchangedRouterIdentityIsNotWritten(t *testing.T) {
	router := &identityRouter{name: "office-gw"}
	s, sess, sn, r := identityFixture(router)
	got := s.setRouterIdentity(r, sess, sn, "r-A", "office-gw")
	if !got.OK || got.Code != "unchanged" {
		t.Errorf("saving the name the router already has answered %+v, want ok, unchanged", got)
	}
	if w := router.written(); len(w) != 0 {
		t.Errorf("an unchanged name was still written to the router: %v", w)
	}
}

func TestAnUnconfirmedRouterIdentityIsAnUnknownOutcome(t *testing.T) {
	router := &identityRouter{name: "MikroTik", ignoreSet: true}
	s, sess, sn, r := identityFixture(router)
	got := s.setRouterIdentity(r, sess, sn, "r-A", "office-gw")
	if got.OK || got.Code != "outcome-unknown" {
		t.Errorf("a set the read-back could not confirm answered %+v, want outcome-unknown and not ok", got)
	}
}

func TestRouterIdentityWritesAreRateLimited(t *testing.T) {
	router := &identityRouter{name: "start"}
	s, sess, sn, r := identityFixture(router)
	limited := false
	for i := 0; i < 200 && !limited; i++ {
		if s.setRouterIdentity(r, sess, sn, "r-A", fmt.Sprintf("name-%d", i)).Code == "rate-limited" {
			limited = true
		}
	}
	if !limited {
		t.Fatal("200 identity writes in a row were never rate limited")
	}
	before := len(router.written())
	if got := s.setRouterIdentity(r, sess, sn, "r-A", "late"); got.Code != "rate-limited" {
		t.Errorf("a write past the allowance answered %+v, want rate-limited", got)
	}
	if len(router.written()) != before {
		t.Error("a rate-limited identity write still reached the router")
	}
}

func TestAnIdentityThatCanNeverBeANameIsRefused(t *testing.T) {
	for name, why := range map[string]string{
		"":                       "blank",
		"gw\nx":                  "control character",
		strings.Repeat("n", 256): "too long",
	} {
		if validIdentity(name) == "" {
			t.Errorf("a %s identity was accepted", why)
		}
	}
	if msg := validIdentity("office-gw"); msg != "" {
		t.Errorf("an ordinary name was refused: %s", msg)
	}
}

// WITH SIGN-IN OFF, `mayManageRouter` says yes, and a router write must still be
// refused, as it is on the socket.
func TestARouterIdentityWriteNeedsSignIn(t *testing.T) {
	s := alertSettingsServer(t, `{}`)
	auth := NewAuth("", time.Hour)
	auth.cache["tok"] = cached{session: &Session{Username: "someone", AuthMode: "none"},
		until: time.Now().Add(time.Minute)}
	s.auth = auth
	req := httptest.NewRequest(http.MethodPut, "/api/routers/r-A/identity", strings.NewReader(`{"name":"office-gw"}`))
	req.SetPathValue("id", "r-A")
	req.Header.Set("Cookie", authed)
	w := httptest.NewRecorder()
	s.routerIdentitySet(w, req)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "sign-in") {
		t.Errorf("with sign-in off the identity write answered %d %s, want 403 naming sign-in",
			w.Code, w.Body.String())
	}
}
