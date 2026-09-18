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
// static menu from `prints` in turn, names the row an add makes `*2`, and
// accepts every other command.
func readbackConn(t *testing.T, prints ...func() ([]routeros.Reply, error)) (*conn, *hub.Client) {
	t.Helper()
	cn, me, _ := readbackConnRet(t, "*2", prints...)
	return cn, me
}

// readbackConnRet is readbackConn with the add's `ret` chosen ("" for none),
// and every command the router was sent recorded.
func readbackConnRet(t *testing.T, ret string, prints ...func() ([]routeros.Reply, error)) (*conn, *hub.Client, *[]routeros.Cmd) {
	t.Helper()
	sent := &[]routeros.Cmd{}
	cn := connFor(t, testResolver(t), "r-A")
	cn.srv.hub = hub.New()
	cn.srv.writeLimit = newWriteLimiter()
	me := hub.NewClient("me", 32)
	cn.srv.hub.Add(me)
	cn.c = me
	n := 0
	cn.rsession = session.NewForTestWithExec(cn.srv.hub, "r-A", func(cmd routeros.Cmd) ([]routeros.Reply, error) {
		*sent = append(*sent, cmd)
		if strings.HasSuffix(cmd.Path, "/add") && ret != "" {
			return []routeros.Reply{{"ret": ret}}, nil
		}
		if strings.HasSuffix(cmd.Path, "/print") && strings.HasPrefix(cmd.Path, "/ip/dns/static") {
			if n >= len(prints) {
				t.Fatalf("print %d of the menu was not scripted", n+1)
			}
			n++
			return prints[n-1]()
		}
		return nil, nil
	})
	return cn, me, sent
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

// A CREATE READS NOTHING BEFORE ITS ADD, and reads back only the row the add
// named (`ret`), not the menu, re-aimed on 2026-09-18: every write read the
// whole menu before and after, 37,111 address-list entries and 6 s each way on
// the operator's router, to diff the ids for one new row.
func TestAConfirmedCreateReportsOk(t *testing.T) {
	made := routeros.Reply{".id": "*2", "name": "new.lan", "type": "A", "address": "192.0.2.11"}
	cn, me, sent := readbackConnRet(t, "*2", answer(made))
	cn.resSave(json.RawMessage(`{"resource":"dnsStatic","values":{"name":"new.lan","type":"A","address":"192.0.2.11"}}`))
	got := sentEvents(me)
	if _, ok := got["res:ok"]; !ok || got["res:error"] != "" {
		t.Errorf("a create the read-back confirmed sent %v, want res:ok", got)
	}
	var words []string
	for _, c := range *sent {
		words = append(words, c.Path+" "+strings.Join(c.Args, " "))
	}
	if len(*sent) != 2 || !strings.HasSuffix((*sent)[0].Path, "/add") ||
		strings.Join((*sent)[1].Args, " ") != "?.id=*2" {
		t.Errorf("the create sent %v; want the add, then a print of ?.id=*2 alone", words)
	}
}

// An add that names no row leaves nothing to read back: unknown, not guessed.
func TestACreateWhoseAddNamesNoRowIsAnUnknownOutcome(t *testing.T) {
	cn, me, _ := readbackConnRet(t, "")
	cn.resSave(json.RawMessage(`{"resource":"dnsStatic","values":{"name":"new.lan","type":"A","address":"192.0.2.11"}}`))
	if got := sentEvents(me); got["res:error"] != "outcome-unknown" {
		t.Errorf("an add with no ret sent %v, want outcome-unknown", got)
	}
}

// An edit reads its own row, by id, before and after: never the menu.
func TestAnEditReadsOnlyItsRow(t *testing.T) {
	edited := routeros.Reply{".id": "*1", "name": "probe.lan", "type": "A", "address": "192.0.2.12"}
	cn, me, sent := readbackConnRet(t, "*2", answer(probe), answer(edited))
	cn.resSave(json.RawMessage(`{"resource":"dnsStatic","id":"*1","expectedIdentity":"probe.lan",` +
		`"values":{"name":"probe.lan","type":"A","address":"192.0.2.12"}}`))
	if _, ok := sentEvents(me)["res:ok"]; !ok {
		t.Fatal("the edit was not confirmed")
	}
	prints := 0
	for _, c := range *sent {
		if strings.HasSuffix(c.Path, "/print") {
			prints++
			if strings.Join(c.Args, " ") != "?.id=*1" {
				t.Errorf("an edit printed %v; want ?.id=*1, its own row", c.Args)
			}
		}
	}
	if prints != 2 {
		t.Errorf("%d prints; want the row before and the row after", prints)
	}
}

func TestACreateThatLeavesNoRowIsAnUnknownOutcome(t *testing.T) {
	// The add named *2, and a print of *2 finds nothing.
	cn, me := readbackConn(t, answer())
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
