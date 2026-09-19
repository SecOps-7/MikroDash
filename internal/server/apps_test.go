package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/rbac"
	"mikrodash/internal/routeros"
	"mikrodash/internal/store"
)

// appFake answers each command from a table keyed by path, and records what was
// sent, in order.
type appFake struct {
	rows  map[string][]routeros.Reply
	trap  map[string]bool
	sent  []routeros.Cmd
	queue []routeros.Reply // successive answers to /app/print ?name= (followAppRow)
}

func (f *appFake) Exec(cmd routeros.Cmd) ([]routeros.Reply, error) {
	f.sent = append(f.sent, cmd)
	if f.trap[cmd.Path] {
		return nil, &routeros.Trap{Message: "no such command or directory"}
	}
	if cmd.Path == "/app/print" && len(f.queue) > 0 && hasArg(cmd, "?name=") {
		r := f.queue[0]
		if len(f.queue) > 1 {
			f.queue = f.queue[1:]
		}
		return []routeros.Reply{r}, nil
	}
	return f.rows[cmd.Path], nil
}

func hasArg(cmd routeros.Cmd, prefix string) bool {
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

func appFixture(t *testing.T) *appFake {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "CHR Test", "appStore.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Exchanges []struct {
			Cmd  string              `json:"cmd"`
			Rows []map[string]string `json:"rows"`
		} `json:"exchanges"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	fake := &appFake{rows: map[string][]routeros.Reply{}, trap: map[string]bool{}}
	for _, e := range f.Exchanges {
		for _, r := range e.Rows {
			fake.rows[e.Cmd] = append(fake.rows[e.Cmd], routeros.Reply(r))
		}
	}
	return fake
}

// THE STORE IS READ WITHOUT ITS SECRETS: an /app row carries `secrets` and its
// compose `yaml`, and neither is ever asked for.
func TestTheAppStoreReadsNoSecret(t *testing.T) {
	for _, p := range appProps {
		// default-credentials is the catalog's published first login ("admin:admin"),
		// the same for every router, which the card shows so it can be changed.
		if p == "default-credentials" {
			continue
		}
		if audit.IsCredentialField(p) || p == "yaml" || p == "secrets" {
			t.Errorf("the store asks the router for %q", p)
		}
	}
	f := appFixture(t)
	readAppStore(f)
	for _, c := range f.sent {
		if !hasArg(c, "=.proplist=") {
			t.Errorf("%s is read without a proplist", c.Path)
		}
	}
}

// THE CHR TEST STORE, AS CAPTURED: set up on pcie1, librespeed running, the
// other 107 apps available; only the mounted ext4 disk is offered; no IP Cloud
// name, so plain HTTP links.
func TestTheCHRStoreReplays(t *testing.T) {
	p := readAppStore(appFixture(t))
	if !p.Supported || !p.Ready || p.Disk != "pcie1" || p.HTTPSLinks {
		t.Fatalf("supported %v ready %v disk %q https %v", p.Supported, p.Ready, p.Disk, p.HTTPSLinks)
	}
	if len(p.Apps) != 108 {
		t.Errorf("%d apps, want 108", len(p.Apps))
	}
	states := map[string]int{}
	for _, a := range p.Apps {
		states[a.State]++
		if a.Name == "librespeed" && (a.State != "running" || a.UIURL == "") {
			t.Errorf("librespeed is %s with ui %q, want running with its address", a.State, a.UIURL)
		}
	}
	if states["running"] != 1 || states["available"] != 107 {
		t.Errorf("states %v, want 1 running and 107 available", states)
	}
	if len(p.Disks) != 1 || p.Disks[0].Slot != "pcie1" {
		t.Errorf("disks %v: only the mounted ext4 disk is a place for apps, not the swap file", p.Disks)
	}
	if len(p.Bridges) != 1 || p.Bridges[0] != "internal" {
		t.Errorf("bridges %v", p.Bridges)
	}
}

// NO /app IS "UNSUPPORTED", not a failure: RouterOS before 7.21, or no
// container package.
func TestNoAppMenuIsUnsupported(t *testing.T) {
	f := &appFake{rows: map[string][]routeros.Reply{}, trap: map[string]bool{"/app/print": true}}
	p := readAppStore(f)
	if p.Supported || p.Code != "" || p.Reason == "" {
		t.Errorf("supported %v code %q reason %q", p.Supported, p.Code, p.Reason)
	}
}

// A ROW'S STATE. Found live on 7.24.3: RouterOS reports progress steps its docs
// do not list ("wait for reverse proxy", "initalizing network: adding
// bridge"), which a list of progress words read as errors. Anything that is not
// a failure is progress.
func TestAnAppsState(t *testing.T) {
	for _, c := range []struct {
		row  routeros.Reply
		want string
	}{
		{routeros.Reply{"disabled": "true"}, "available"},
		{routeros.Reply{"disabled": "true", "app-size": "10"}, "stopped"},
		{routeros.Reply{"disabled": "false", "running": "true"}, "running"},
		{routeros.Reply{"disabled": "false", "status": "downloading/extracting"}, "installing"},
		{routeros.Reply{"disabled": "false", "status": "wait for reverse proxy"}, "installing"},
		{routeros.Reply{"disabled": "false", "status": "initalizing network: adding bridge"}, "installing"},
		{routeros.Reply{"disabled": "false", "status": ""}, "installing"},
		{routeros.Reply{"disabled": "false", "status": "error: not enough space"}, "error"},
	} {
		if got := appState(c.row); got != c.want {
			t.Errorf("%v is %s, want %s", c.row, got, c.want)
		}
	}
}

// A CHANGE: the app is found by name in a fresh read, recorded before anything
// is sent, and the command goes to its id. An install first sets use-https from
// IP Cloud; an app not in the store is refused with nothing sent.
func TestAnAppChangeIsFoundRecordedThenSent(t *testing.T) {
	f := &appFake{rows: map[string][]routeros.Reply{
		"/app/print":      {{".id": "*2A", "name": "librespeed"}},
		"/ip/cloud/print": {{}},
	}}
	recordedAt := -1
	err := runAppVerb(f, "librespeed", "enable", true, func() { recordedAt = len(f.sent) })
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, c := range f.sent {
		paths = append(paths, c.Path+" "+strings.Join(c.Args, " "))
	}
	want := []string{"/app/print =.proplist=.id,name ?name=librespeed", "/ip/cloud/print =.proplist=dns-name",
		"/app/set =numbers=*2A =use-https=no", "/app/enable =numbers=*2A"}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Errorf("sent %q\nwant %q", paths, want)
	}
	if recordedAt != 1 {
		t.Errorf("the audit row was written after %d commands, want 1 (after the read, before any change)", recordedAt)
	}
	// With an IP Cloud name, HTTPS.
	f = &appFake{rows: map[string][]routeros.Reply{
		"/app/print": {{".id": "*2A", "name": "librespeed"}}, "/ip/cloud/print": {{"dns-name": "x.sn.mynetname.net"}}}}
	if err := runAppVerb(f, "librespeed", "enable", true, func() {}); err != nil || !strings.Contains(f.sent[2].Args[1], "use-https=yes") {
		t.Errorf("with a cloud name the install sent %v (%v)", f.sent, err)
	}
	// A stop sets nothing but the stop.
	f = &appFake{rows: map[string][]routeros.Reply{"/app/print": {{".id": "*2A", "name": "librespeed"}}}}
	if err := runAppVerb(f, "librespeed", "disable", false, func() {}); err != nil || len(f.sent) != 2 || f.sent[1].Path != "/app/disable" {
		t.Errorf("a stop sent %v (%v)", f.sent, err)
	}
	// Not in the store: refused, nothing recorded, nothing changed.
	f = &appFake{rows: map[string][]routeros.Reply{}}
	recorded := false
	if err := runAppVerb(f, "nope", "cleanup", false, func() { recorded = true }); !errors.Is(err, errAppNotFound) || recorded || len(f.sent) != 1 {
		t.Errorf("an unknown app: %v, recorded %v, sent %v", err, recorded, f.sent)
	}
}

// THE VERBS ARE AN ALLOW-LIST: exactly the five the page offers, each to the
// /app command the docs name. A verb the browser invents has no command.
func TestTheAppVerbsAreAnAllowList(t *testing.T) {
	want := map[string]string{"install": "enable", "start": "enable", "stop": "disable", "restart": "restart", "remove": "cleanup"}
	if len(appVerbs) != len(want) {
		t.Errorf("%d verbs, want %d", len(appVerbs), len(want))
	}
	for k, v := range want {
		if appVerbs[k] != v {
			t.Errorf("%s runs %q, want %q", k, appVerbs[k], v)
		}
	}
	if _, ok := appVerbs["set"]; ok {
		t.Error("the page can send /app/set")
	}
}

// REMOVE NEEDS THE NAME TYPED, exactly; nothing else asks for it.
func TestARemoveNeedsTheNameTyped(t *testing.T) {
	for _, c := range []struct {
		req  appsDoReq
		want bool
	}{
		{appsDoReq{Name: "librespeed", Verb: "remove", Confirm: "librespeed"}, true},
		{appsDoReq{Name: "librespeed", Verb: "remove"}, false},
		{appsDoReq{Name: "librespeed", Verb: "remove", Confirm: "Librespeed"}, false},
		{appsDoReq{Name: "librespeed", Verb: "remove", Confirm: "librespeed "}, false},
		{appsDoReq{Name: "librespeed", Verb: "stop"}, true},
	} {
		if got := c.req.confirmed(); got != c.want {
			t.Errorf("%+v confirmed %v, want %v", c.req, got, c.want)
		}
	}
}

// FOLLOWING AN INSTALL: progress until it runs; a failure, a stop, or quit end it.
func TestAnInstallIsFollowedToItsEnd(t *testing.T) {
	old := appPollEvery
	appPollEvery = time.Millisecond
	t.Cleanup(func() { appPollEvery = old })
	follow := func(rows ...routeros.Reply) (followResult, int) {
		f := &appFake{queue: rows}
		n := 0
		return followAppRow(f, "librespeed", nil, func(routeros.Reply) { n++ }), n
	}
	res, n := follow(routeros.Reply{"disabled": "false", "status": "downloading/extracting"},
		routeros.Reply{"disabled": "false", "status": "wait for reverse proxy"},
		routeros.Reply{"disabled": "false", "running": "true", "ui-url": "http://198.51.100.15:3004"})
	if res.code != "" || !res.running || res.uiURL == "" || n != 3 {
		t.Errorf("a clean install ended %+v after %d frames", res, n)
	}
	if res, _ := follow(routeros.Reply{"disabled": "false", "status": "error: not enough space"}); res.code != "failed" {
		t.Errorf("a failure ended %+v", res)
	}
	if res, _ := follow(routeros.Reply{"disabled": "true", "app-size": "10"}); res.code != "failed" {
		t.Errorf("an app that stopped ended %+v", res)
	}
	quit := make(chan struct{})
	close(quit)
	f := &appFake{queue: []routeros.Reply{{"disabled": "false", "status": "starting"}}}
	if res := followAppRow(f, "librespeed", quit, func(routeros.Reply) {}); res.code != codeScanStopped {
		t.Errorf("a quit follow ended %+v", res)
	}
}

// SETUP TAKES ONLY WHAT THE ROUTER OFFERED: a mounted disk, and a bridge it has
// or none.
func TestSetupTakesOnlyWhatTheRouterOffered(t *testing.T) {
	store := readAppStore(appFixture(t))
	for _, c := range []struct {
		req appsSetupReq
		ok  bool
	}{
		{appsSetupReq{Disk: "pcie1"}, true},
		{appsSetupReq{Disk: "pcie1", LanBridge: "internal"}, true},
		{appsSetupReq{Disk: "file-pcie1-swap"}, false},
		{appsSetupReq{Disk: "usb9"}, false},
		{appsSetupReq{Disk: "pcie1", LanBridge: "bridge-nope"}, false},
	} {
		if got := validateAppSetup(store, c.req) == ""; got != c.ok {
			t.Errorf("%+v accepted %v, want %v", c.req, got, c.ok)
		}
	}
}

// ONLY A GLOBAL ADMINISTRATOR MANAGES APPS: an app is new code on the router.
// A session with sign-in off is not one (the control is a real administrator).
func TestOnlyAGlobalAdminManagesApps(t *testing.T) {
	if (&conn{srv: &Server{}, sess: &Session{AuthMode: "none", Username: "whoever"}}).mayManageApps() {
		t.Error("a session with sign-in off may manage apps")
	}
	admin := func(access string) *conn {
		cn := rawAdminConn(t, false)
		cn.sess.Readable = []string{"r1"}
		cn.sess.Pages = map[string]string{"containers": access}
		cn.routerID = "r1"
		cn.srv.rbac = rbac.New(cn.srv.auditDB, func() []rbac.Router { return []rbac.Router{{ID: "r1"}} })
		cn.userID = cn.srv.userIDFor("boss")
		return cn
	}
	if !admin("write").mayManageApps() {
		t.Error("a global administrator may not manage apps: the check above proves nothing")
	}
	if admin("read").mayManageApps() {
		t.Error("an administrator with only read on Containers may manage apps")
	}
	// Write on Containers is not enough: an operator is not a global administrator.
	cn := admin("write")
	role, err := cn.srv.auditDB.CreateRole(map[string]any{"name": "Containers"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cn.srv.auditDB.SetRolePages(role.ID, []db.RolePage{{Page: "containers", Access: "write"}}); err != nil {
		t.Fatal(err)
	}
	op, err := cn.srv.store.CreateUser(store.NewUser{Username: "op", Password: "a-long-enough-password", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	opID, _ := op["id"].(string)
	if err := cn.srv.auditDB.UpsertGrant(db.GrantSpec{PrincipalType: "user", PrincipalID: opID, RoleID: role.ID, ScopeType: "global"}); err != nil {
		t.Fatal(err)
	}
	cn.sess = &Session{Username: "op", AuthMode: "modern", Readable: []string{"r1"}, Pages: map[string]string{"containers": "write"}}
	cn.userID = opID
	if !cn.canPage("containers", "write") {
		t.Fatal("the operator has no write on Containers: the next check would pass for the wrong reason")
	}
	if cn.mayManageApps() {
		t.Error("an operator with write on Containers may manage apps; only a global administrator may")
	}
}
