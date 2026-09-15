package server

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/rbac"
	"mikrodash/internal/routeros"
	"mikrodash/internal/session"
)

// AN INTERFACE IS EDITED, NEVER CREATED, AND ITS NAME NEVER REACHES THE ROUTER (#97).
//
// These drive the real res:save handler against a scripted router. The
// resource-level rules are pinned in internal/resource; what these add is that
// the HANDLER honours them, because a rule the handler skips is a rule a
// hand-built request walks straight past.

// ifaceConn is a writer on r-A holding interfaces:write. Each print of
// /interface answers from `prints` in turn, holding on the last, and every other
// command is recorded and accepted.
func ifaceConn(t *testing.T, prints ...[]routeros.Reply) (*conn, *hub.Client, func() []routeros.Cmd) {
	t.Helper()
	dir := t.TempDir()
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(rbacDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`INSERT INTO role_pages (role_id, page, access) VALUES ('role-w','interfaces','write')`); err != nil {
		t.Fatal(err)
	}
	h.Close()
	database, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	resolver := rbac.New(database, func() []rbac.Router { return []rbac.Router{{ID: "r-A"}, {ID: "r-B"}} })

	cn := connFor(t, resolver, "r-A")
	cn.sess.Pages = map[string]string{"interfaces": "write"}
	cn.srv.hub = hub.New()
	cn.srv.writeLimit = newWriteLimiter()
	me := hub.NewClient("me", 32)
	cn.srv.hub.Add(me)
	cn.c = me

	var mu sync.Mutex
	var sent []routeros.Cmd
	n := 0
	cn.rsession = session.NewForTestWithExec(cn.srv.hub, "r-A", func(cmd routeros.Cmd) ([]routeros.Reply, error) {
		if cmd.Path == "/interface/print" {
			if len(prints) == 0 {
				t.Fatal("the handler read /interface, which this case does not expect")
			}
			i := n
			if i >= len(prints) {
				i = len(prints) - 1
			}
			n++
			return prints[i], nil
		}
		mu.Lock()
		sent = append(sent, cmd)
		mu.Unlock()
		return nil, nil
	})
	return cn, me, func() []routeros.Cmd {
		mu.Lock()
		defer mu.Unlock()
		return append([]routeros.Cmd(nil), sent...)
	}
}

func TestAnInterfaceCannotBeCreated(t *testing.T) {
	cn, me, sent := ifaceConn(t)
	cn.resSave(json.RawMessage(`{"resource":"iface","values":{"comment":"x","disabled":"false"}}`))

	if got := sentEvents(me); got["res:error"] != "not-creatable" {
		t.Errorf("a create on the interface resource sent %v, want res:error not-creatable", got)
	}
	if cmds := sent(); len(cmds) != 0 {
		t.Errorf("a refused create still sent %d command(s) to the router: %+v", len(cmds), cmds)
	}
}

func TestAnInterfaceCommentEditSendsTheCommentAndNeverTheName(t *testing.T) {
	was := routeros.Reply{".id": "*1", "name": "ether1", "type": "ether", "disabled": "false", "comment": ""}
	now := routeros.Reply{".id": "*1", "name": "ether1", "type": "ether", "disabled": "false", "comment": "uplink"}
	cn, me, sent := ifaceConn(t, []routeros.Reply{was}, []routeros.Reply{now})

	// A hand-built request carrying a changed name anyway.
	cn.resSave(json.RawMessage(`{"resource":"iface","id":"*1","expectedIdentity":"ether1",` +
		`"values":{"name":"renamed","comment":"uplink","disabled":"false"}}`))

	got := sentEvents(me)
	if _, ok := got["res:ok"]; !ok || got["res:error"] != "" {
		t.Errorf("a confirmed comment edit sent %v, want res:ok", got)
	}
	var set []string
	for _, c := range sent() {
		if c.Path == "/interface/set" {
			set = c.Args
		}
		if strings.HasSuffix(c.Path, "/add") || strings.HasSuffix(c.Path, "/remove") {
			t.Errorf("a comment edit sent %s", c.Path)
		}
	}
	if set == nil {
		t.Fatalf("no /interface/set was sent; commands: %+v", sent())
	}
	args := strings.Join(set, " ")
	for _, want := range []string{"=.id=*1", "=comment=uplink"} {
		if !strings.Contains(args, want) {
			t.Errorf("/interface/set %q lacks %s", args, want)
		}
	}
	if strings.Contains(args, "=name=") {
		t.Errorf("the name reached the router: /interface/set %s", args)
	}
}
