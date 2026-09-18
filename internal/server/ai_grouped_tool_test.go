package server

import (
	"strings"
	"testing"

	"mikrodash/internal/aiprovider"
	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
	"mikrodash/internal/session"
)

// groupedToolConn is a viewer with sign-in off (so every page reads) on a router
// whose every command is recorded and answered by `answer`.
func groupedToolConn(t *testing.T, answer func(routeros.Cmd) []routeros.Reply) (*conn, *[]routeros.Cmd) {
	t.Helper()
	sent := &[]routeros.Cmd{}
	cn := &conn{srv: &Server{hub: hub.New()}, sess: &Session{AuthMode: "none"}, routerID: "r-A"}
	cn.rsession = session.NewForTestWithExec(cn.srv.hub, "r-A", func(cmd routeros.Cmd) ([]routeros.Reply, error) {
		*sent = append(*sent, cmd)
		return answer(cmd), nil
	})
	return cn, sent
}

func callTool(name, args string) aiprovider.ToolCall {
	tc := toolCall("c1", name)
	tc.Function.Arguments = args
	return tc
}

// WITHOUT `list`, THE TOOL ANSWERS EACH LIST, from one read of three fields.
func TestTheAddressListToolAnswersEachListWithoutReadingEveryField(t *testing.T) {
	cn, sent := groupedToolConn(t, func(routeros.Cmd) []routeros.Reply {
		return []routeros.Reply{{"list": "prod_blocklist", "dynamic": "false", "disabled": "false"},
			{"list": "prod_blocklist", "dynamic": "false", "disabled": "false"},
			{"list": "Trusted IPs", "dynamic": "false", "disabled": "true"}}
	})
	out := cn.runAITool(callTool("list_addressList", `{}`))
	if len(*sent) != 1 || strings.Join((*sent)[0].Args, " ") != "=.proplist=list,dynamic,disabled" {
		t.Fatalf("sent %v; want one print of list, dynamic and disabled only", *sent)
	}
	for _, want := range []string{`"name":"prod_blocklist","count":2`, `"name":"Trusted IPs","count":1`, `"totalRows":3`, "list_addressList again"} {
		if !strings.Contains(out, want) {
			t.Errorf("the answer lacks %s: %s", want, out)
		}
	}
}

// WITH `list`, ONLY THAT LIST IS READ, filtered on the router.
func TestTheAddressListToolReadsOneListOnTheRouter(t *testing.T) {
	cn, sent := groupedToolConn(t, func(routeros.Cmd) []routeros.Reply {
		return []routeros.Reply{{".id": "*A", "list": "Trusted IPs", "address": "198.51.100.7"}}
	})
	out := cn.runAITool(callTool("list_addressList", `{"list":"Trusted IPs"}`))
	if len(*sent) != 1 {
		t.Fatalf("%d commands; want one read", len(*sent))
	}
	if a := (*sent)[0].Args; len(a) == 0 || a[len(a)-1] != "?list=Trusted IPs" {
		t.Errorf("the read was %v; want it filtered by ?list=Trusted IPs", a)
	}
	if !strings.Contains(out, `"id":"*A"`) || !strings.Contains(out, "198.51.100.7") {
		t.Errorf("the list's row is not in the answer: %s", out)
	}
}

// A name longer than any list is refused without reading anything.
func TestTheAddressListToolRefusesAnAbsurdName(t *testing.T) {
	cn, sent := groupedToolConn(t, func(routeros.Cmd) []routeros.Reply { return nil })
	out := cn.runAITool(callTool("list_addressList", `{"list":"`+strings.Repeat("x", aiGroupArgMax+1)+`"}`))
	if len(*sent) != 0 || !strings.Contains(out, "too long") {
		t.Errorf("sent %d commands, answered %q; want a refusal and no read", len(*sent), out)
	}
}

// The control: a list tool whose menu is not grouped still reads its menu.
func TestAnUngroupedListToolStillReadsItsMenu(t *testing.T) {
	cn, sent := groupedToolConn(t, func(routeros.Cmd) []routeros.Reply {
		return []routeros.Reply{{".id": "*1", "name": "lan", "ranges": "198.51.100.10-198.51.100.20"}}
	})
	out := cn.runAITool(callTool("list_ipPool", `{}`))
	if len(*sent) != 1 || len((*sent)[0].Args) != 0 || (*sent)[0].Path != "/ip/pool/print" {
		t.Errorf("sent %v; want one unfiltered print of /ip/pool", *sent)
	}
	if !strings.Contains(out, `"id":"*1"`) {
		t.Errorf("the pool is not in the answer: %s", out)
	}
}
