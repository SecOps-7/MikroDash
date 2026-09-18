package server

import (
	"strings"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
	"mikrodash/internal/session"
)

// THE ASSISTANT'S TOOLS ACT ON THE ROUTER THE QUESTION WAS ASKED ABOUT (review
// 2026-09-19).
//
// The exchange runs on its own goroutine and read cn.rsession live, so a
// router:select mid-answer sent list_* and change_row to the new router while
// the context and the saved thread described the old one.

func recordingSession(h *hub.Hub, id string, sent *[]string) *session.Session {
	return session.NewForTestWithExec(h, id, func(cmd routeros.Cmd) ([]routeros.Reply, error) {
		*sent = append(*sent, id+" "+cmd.Path)
		return []routeros.Reply{{".id": "*1", "name": "lan"}}, nil
	})
}

func TestAToolReadsThePinnedRouterNotTheCurrentOne(t *testing.T) {
	h := hub.New()
	var sent []string
	a, b := recordingSession(h, "r-A", &sent), recordingSession(h, "r-B", &sent)
	cn := &conn{srv: &Server{hub: h}, sess: &Session{AuthMode: "none"}, routerID: "r-A", rsession: a}
	sc := cn.scope()
	cn.setRouter("r-B", b) // the operator switched mid-answer

	cn.runAITool(sc, callTool("list_ipPool", `{}`))
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "r-A ") {
		t.Errorf("the list tool sent %v; want one read on r-A, the router the question was about", sent)
	}
}

func TestAWriteToolRefusesAfterTheRouterMoved(t *testing.T) {
	h := hub.New()
	me := hub.NewClient("me", 8)
	h.Add(me)
	var sent []string
	a, b := recordingSession(h, "r-A", &sent), recordingSession(h, "r-B", &sent)
	cn := &conn{srv: &Server{hub: h}, c: me, sess: &Session{AuthMode: "none"}, routerID: "r-A", rsession: a}
	sc := cn.scope()
	cn.setRouter("r-B", b)

	out := cn.runAITool(sc, callTool("change_row", `{"resource":"ipPool","values":{"name":"x","ranges":"198.51.100.10-198.51.100.20"}}`))
	if !strings.Contains(out, "switched to another router") {
		t.Errorf("a write after the router moved answered %q; want the switched refusal", out)
	}
	if len(sent) != 0 {
		t.Errorf("a write after the router moved reached a router: %v", sent)
	}

	// THE CONTROL: on the router the question was about, change_row reaches the
	// write path (which, with sign-in off, refuses a router write itself: that
	// answer is not the switched one).
	cn.setRouter("r-A", a)
	if out := cn.runAITool(sc, callTool("change_row", `{"resource":"ipPool","values":{"name":"x","ranges":"198.51.100.10-198.51.100.20"}}`)); strings.Contains(out, "switched to another router") {
		t.Errorf("on the pinned router the write was refused as switched: %q", out)
	}
}
