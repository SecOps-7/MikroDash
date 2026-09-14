package server

// A ROUTER WRITE IS REPORTED ONLY ONCE IT HAS BEEN READ BACK (#97).
//
// These drive the real res:save and res:remove handlers against a scripted
// router, for the DNS static resource the RBAC fixture grants write on. The
// router accepts every command; what varies is what the menu shows afterwards.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
	"mikrodash/internal/session"
)

// readbackConn is a writer on r-A whose router answers each print of the DNS
// static menu from `prints` in turn, and accepts every other command.
func readbackConn(t *testing.T, prints ...func() ([]routeros.Reply, error)) (*conn, *hub.Client) {
	t.Helper()
	cn := connFor(t, testResolver(t), "r-A")
	cn.srv.hub = hub.New()
	cn.srv.writeLimit = newWriteLimiter()
	me := hub.NewClient("me", 32)
	cn.srv.hub.Add(me)
	cn.c = me
	n := 0
	cn.rsession = session.NewForTestWithExec(cn.srv.hub, "r-A", func(cmd routeros.Cmd) ([]routeros.Reply, error) {
		if strings.HasSuffix(cmd.Path, "/print") && strings.HasPrefix(cmd.Path, "/ip/dns/static") {
			if n >= len(prints) {
				t.Fatalf("print %d of the menu was not scripted", n+1)
			}
			n++
			return prints[n-1]()
		}
		return nil, nil
	})
	return cn, me
}

func answer(rows ...routeros.Reply) func() ([]routeros.Reply, error) {
	return func() ([]routeros.Reply, error) { return rows, nil }
}

// sentEvents is every event the connection was sent, with its code if any.
func sentEvents(me *hub.Client) map[string]string {
	out := map[string]string{}
	for {
		select {
		case b := <-me.Send:
			var env struct {
				Event string `json:"event"`
				Data  struct {
					Code string `json:"code"`
				} `json:"data"`
			}
			if json.Unmarshal(b, &env) == nil {
				out[env.Event] = env.Data.Code
			}
		default:
			return out
		}
	}
}

var probe = routeros.Reply{".id": "*1", "name": "probe.lan", "type": "A", "address": "192.0.2.10"}

func TestAConfirmedCreateReportsOk(t *testing.T) {
	made := routeros.Reply{".id": "*2", "name": "new.lan", "type": "A", "address": "192.0.2.11"}
	cn, me := readbackConn(t, answer(probe), answer(probe, made))
	cn.resSave(json.RawMessage(`{"resource":"dnsStatic","values":{"name":"new.lan","type":"A","address":"192.0.2.11"}}`))
	got := sentEvents(me)
	if _, ok := got["res:ok"]; !ok || got["res:error"] != "" {
		t.Errorf("a create the read-back confirmed sent %v, want res:ok", got)
	}
}

func TestACreateThatLeavesNoRowIsAnUnknownOutcome(t *testing.T) {
	cn, me := readbackConn(t, answer(probe), answer(probe))
	cn.resSave(json.RawMessage(`{"resource":"dnsStatic","values":{"name":"new.lan","type":"A","address":"192.0.2.11"}}`))
	got := sentEvents(me)
	if _, ok := got["res:ok"]; ok {
		t.Errorf("a create with no new row on read-back reported success: %v", got)
	}
	if got["res:error"] != "outcome-unknown" {
		t.Errorf("a create with no new row sent %v, want res:error outcome-unknown", got)
	}
}

func TestAnUpdateWhoseReadBackFailsIsAnUnknownOutcome(t *testing.T) {
	cn, me := readbackConn(t, answer(probe), func() ([]routeros.Reply, error) {
		return nil, errors.New("routeros: read timed out")
	})
	cn.resSave(json.RawMessage(`{"resource":"dnsStatic","id":"*1","expectedIdentity":"probe.lan",` +
		`"values":{"name":"probe.lan","type":"A","address":"192.0.2.12"}}`))
	got := sentEvents(me)
	if _, ok := got["res:ok"]; ok || got["res:error"] != "outcome-unknown" {
		t.Errorf("an update whose read-back failed sent %v, want outcome-unknown and no res:ok", got)
	}
}

func TestADeleteWhoseRowRemainsIsAnUnknownOutcome(t *testing.T) {
	cn, me := readbackConn(t, answer(probe), answer(probe))
	cn.resRemove(json.RawMessage(`{"resource":"dnsStatic","id":"*1","expectedIdentity":"probe.lan"}`))
	got := sentEvents(me)
	if _, ok := got["res:ok"]; ok || got["res:error"] != "outcome-unknown" {
		t.Errorf("a delete whose row is still there sent %v, want outcome-unknown and no res:ok", got)
	}
}
