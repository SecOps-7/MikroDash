package server

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
)

// The Containers page's Apps tab: RouterOS's app store (/app, 7.21+).
//
// ── WHAT AN APP IS ──────────────────────────────────────────────────────────
//
// RouterOS ships a catalog of container apps as rows of /app, every one of them
// present whether it is installed or not. Enabling a row installs it: RouterOS
// pulls the images, makes the veth, the NAT and the firewall redirects, and
// starts it, reporting how far it has got in `status`. Disabling stops it;
// `cleanup` removes it AND ITS DATA. Apps need `/app/settings disk` set first
// (the setup card).
//
// ── STOPPED OR NOT INSTALLED: THE ROW'S `interface` ─────────────────────────
//
// Both are disabled rows. Measured on CHR Test (7.24.3), 2026-09-19: an
// installed app keeps its veth (`interface=veth-app-<name>`) and its container
// while stopped; a never-installed app, and one after `cleanup`, reads
// `interface=none` and has no container. `app-size` does NOT tell them apart:
// the docs say cleanup empties it, but 7.24.3 keeps reporting the old size.
//
// ── WHO MAY INSTALL ─────────────────────────────────────────────────────────
//
// An app is somebody else's code running on the router with firewall rules of
// its own, so installing, starting, stopping and removing one takes a GLOBAL
// ADMIN (codeAllowed, the rule the container image and script fields follow)
// as well as write on the Containers page (the operator's call, 2026-09-19).
// Anyone who may read the page sees the store and what is installed.
//
// ── READ WITHOUT SECRETS ────────────────────────────────────────────────────
//
// An /app row carries its `secrets` and its whole compose `yaml`. The store is
// read through appProps, which names neither; TestTheAppStoreReadsNoSecret
// holds it to that.
//
// ── HTTPS LINKS NEED IP CLOUD ───────────────────────────────────────────────
//
// An app's `use-https` (on by default) puts its UI behind RouterOS's reverse
// proxy on an HTTPS address, and that needs IP Cloud to have given the router
// a DNS name. Without one the install never finishes: it sits at "wait for
// reverse proxy" (read live on CHR Test, 7.24.3, where the same app with
// use-https=no ran in 27 s). So Install sets use-https from whether IP Cloud
// has a DNS name, before it enables the app: one click works on a router with
// no cloud as well as on one with it, and the hero says which it gets.
//
// ── ONE WRITE PATH ──────────────────────────────────────────────────────────
//
// Every change goes: permission, the verb looked up in appVerbs (never taken
// from the browser), the rate-limited write queue, the app found by name in a
// FRESH read, the audit row written BEFORE the command (an install can take
// minutes and a dropped connection must not leave it unrecorded), then the
// command by the row's id. Every change is then followed: the app's own row is
// re-read every appPollEvery until it reaches the verb's goal (appGoal), fails
// or appFollowMax passes, each reading sent to the page as progress.

// appProps is the store's read: everything the cards show, and never `secrets`
// or `yaml`.
var appProps = []string{".id", "name", "category", "description", "project-page", "default-credentials",
	"default-network", "firewall-redirects", "disabled", "running", "status", "ui-url", "interface", "app-size",
	"data-size", "memory-current", "cpu-usage", "custom"}

// appVerbs is every change the page can ask for, and the /app command it runs.
var appVerbs = map[string]string{
	"install": "enable",
	"start":   "enable",
	"stop":    "disable",
	"restart": "restart",
	"remove":  "cleanup",
}

// appFailureWords mark a status as a failure. Anything else on an app that is
// enabled and not yet running is progress: RouterOS reports far more steps than
// its docs list ("initalizing network: adding bridge", "wait for reverse proxy",
// read live on 7.24.3), so a list of progress words misread a healthy install
// as an error. The follow's own time cap bounds a step that never ends.
var appFailureWords = []string{"error", "fail", "unable", "cannot", "can't", "could not", "no space", "not enough", "invalid"}

var (
	appPollEvery = 2 * time.Second
	appFollowMax = 10 * time.Minute
)

// AppRow is one app of the store, as the page draws it.
type AppRow struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Category           string `json:"category"`
	Description        string `json:"description"`
	ProjectPage        string `json:"projectPage"`
	DefaultCredentials string `json:"defaultCredentials"`
	DefaultNetwork     string `json:"defaultNetwork"`
	// Ports is RouterOS's firewall-redirects: "8123:8123:tcp:web,…".
	Ports string `json:"ports"`
	// State is available, installing, running, stopped or error.
	State   string `json:"state"`
	Status  string `json:"status"`
	UIURL   string `json:"uiUrl"`
	AppSize string `json:"appSize"`
	Data    string `json:"dataSize"`
	Memory  string `json:"memory"`
	CPU     string `json:"cpu"`
	Custom  bool   `json:"custom"`
}

// AppDisk is a disk the setup card can offer: mounted, with a filesystem.
type AppDisk struct {
	Slot string `json:"slot"`
	FS   string `json:"fs"`
	Free string `json:"free"`
	Size string `json:"size"`
}

// AppsPayload is `apps:state`: the store, whether it is set up, and what this
// viewer may do.
type AppsPayload struct {
	RouterID string `json:"routerId"`
	// Supported is false when the router has no /app (RouterOS before 7.21, or
	// no container package); Reason says which.
	Supported bool   `json:"supported"`
	Reason    string `json:"reason"`
	// Ready is setup done: a disk is set for apps.
	Ready     bool      `json:"ready"`
	Disk      string    `json:"disk"`
	LanBridge string    `json:"lanBridge"`
	RouterIP  string    `json:"routerIp"`
	Disks     []AppDisk `json:"disks"`
	Bridges   []string  `json:"bridges"`
	Apps      []AppRow  `json:"apps"`
	// HTTPSLinks is IP Cloud having a DNS name: installed apps get an HTTPS
	// address. Without it they get a plain HTTP one (see the file's header).
	HTTPSLinks bool `json:"httpsLinks"`
	// MayManage is a global admin with write on Containers.
	MayManage bool `json:"mayManage"`
	// Code is empty, or one of: denied, unavailable, request, failed.
	Code    string `json:"code"`
	Message string `json:"message"`
}

// AppsProgressPayload is `apps:progress`: one app's install or change, as it goes.
type AppsProgressPayload struct {
	RouterID string `json:"routerId"`
	Name     string `json:"name"`
	Verb     string `json:"verb"`
	Status   string `json:"status"`
	Running  bool   `json:"running"`
	UIURL    string `json:"uiUrl"`
	// Done ends it; Code is then empty (it worked) or one of: unavailable,
	// denied, request, notfound, failed, timeout.
	Done    bool   `json:"done"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type appsDoReq struct {
	Name    string `json:"name"`
	Verb    string `json:"verb"`
	Confirm string `json:"confirm"`
}

// confirmed: REMOVE DELETES THE APP'S DATA, so it needs the app's name typed
// on the page, checked here too: a page that skipped its own dialog still
// cannot remove anything.
func (r appsDoReq) confirmed() bool {
	return r.Verb != "remove" || (r.Name != "" && r.Confirm == r.Name)
}

type appsSetupReq struct {
	Disk      string `json:"disk"`
	LanBridge string `json:"lanBridge"`
}

// appReader is the part of a router connection the store needs.
type appReader interface {
	Exec(cmd routeros.Cmd) ([]routeros.Reply, error)
}

// appState reads a row's place in the store.
func appState(r routeros.Reply) string {
	switch {
	case r["running"] == "true":
		return "running"
	case r["disabled"] == "true" && appInstalled(r):
		return "stopped"
	case r["disabled"] == "true":
		return "available"
	case appFailed(r["status"]):
		return "error"
	default:
		return "installing"
	}
}

// appInstalled: the app still has its network (see "STOPPED OR NOT INSTALLED").
func appInstalled(r routeros.Reply) bool {
	return r["interface"] != "" && r["interface"] != "none"
}

func appFailed(status string) bool {
	s := strings.ToLower(status)
	for _, w := range appFailureWords {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func appRow(r routeros.Reply) AppRow {
	return AppRow{ID: r[".id"], Name: r["name"], Category: r["category"], Description: r["description"],
		ProjectPage: r["project-page"], DefaultCredentials: r["default-credentials"],
		DefaultNetwork: r["default-network"], Ports: r["firewall-redirects"], State: appState(r),
		Status: r["status"], UIURL: r["ui-url"], AppSize: r["app-size"], Data: r["data-size"],
		Memory: r["memory-current"], CPU: r["cpu-usage"], Custom: r["custom"] == "true"}
}

// readAppStore reads the store and its setup, one menu at a time.
func readAppStore(rs appReader) AppsPayload {
	out := AppsPayload{Disks: []AppDisk{}, Bridges: []string{}, Apps: []AppRow{}}
	rows, err := rs.Exec(routeros.Cmd{Path: "/app/print", Args: []string{"=.proplist=" + strings.Join(appProps, ",")},
		Timeout: 20 * time.Second})
	if err != nil {
		var trap *routeros.Trap
		if errors.As(err, &trap) {
			out.Reason = "This router has no app store: it needs RouterOS 7.21 or later with the container package."
			return out
		}
		out.Code, out.Message = "failed", safe.Message(err.Error())
		return out
	}
	out.Supported = true
	for _, r := range rows {
		out.Apps = append(out.Apps, appRow(r))
	}
	if s, err := rs.Exec(routeros.Cmd{Path: "/app/settings/print",
		Args: []string{"=.proplist=disk,lan-bridge,assumed-lan-bridge,router-ip,assumed-router-ip"}}); err == nil && len(s) > 0 {
		out.Disk = s[0]["disk"]
		out.LanBridge = firstSet(s[0]["lan-bridge"], s[0]["assumed-lan-bridge"])
		out.RouterIP = firstSet(s[0]["router-ip"], s[0]["assumed-router-ip"])
	}
	out.Ready = out.Disk != "" && out.Disk != "none"
	if d, err := rs.Exec(routeros.Cmd{Path: "/disk/print", Args: []string{"=.proplist=slot,fs,mounted,free,size"}}); err == nil {
		for _, r := range d {
			if r["mounted"] == "true" && r["fs"] != "" && r["fs"] != "-" {
				out.Disks = append(out.Disks, AppDisk{Slot: r["slot"], FS: r["fs"], Free: r["free"], Size: r["size"]})
			}
		}
	}
	if b, err := rs.Exec(routeros.Cmd{Path: "/interface/bridge/print", Args: []string{"=.proplist=name"}}); err == nil {
		for _, r := range b {
			out.Bridges = append(out.Bridges, r["name"])
		}
	}
	out.HTTPSLinks = cloudHasName(rs)
	return out
}

// cloudHasName is IP Cloud having given the router a DNS name, which an app's
// HTTPS address needs. A router without /ip/cloud (or with it off) has none.
func cloudHasName(rs appReader) bool {
	rows, err := rs.Exec(routeros.Cmd{Path: "/ip/cloud/print", Args: []string{"=.proplist=dns-name"}})
	return err == nil && len(rows) > 0 && rows[0]["dns-name"] != ""
}

func firstSet(vals ...string) string {
	for _, v := range vals {
		if v != "" && v != "none" {
			return v
		}
	}
	return ""
}

// mayManageApps: a global admin with write on Containers.
func (cn *conn) mayManageApps() bool {
	return cn.canPage("containers", "write") && cn.codeAllowed()
}

func emptyApps(routerID, code, msg string) AppsPayload {
	return AppsPayload{RouterID: routerID, Code: code, Message: msg, Disks: []AppDisk{}, Bridges: []string{}, Apps: []AppRow{}}
}

// appsList answers `apps:list`.
func (cn *conn) appsList() {
	sc := cn.scope()
	if sc.routerID == "" || sc.rs == nil {
		EvAppsState.Send(cn.srv.hub, cn.c, emptyApps("", "unavailable", ""))
		return
	}
	if !cn.canPageIn(sc, "containers", "read") {
		EvAppsState.Send(cn.srv.hub, cn.c, emptyApps(sc.routerID, "denied", ""))
		return
	}
	may := cn.mayManageApps()
	// OFF THE READ LOOP: the store is a hundred rows and three more menus.
	go func() {
		out := readAppStore(sc.rs)
		out.RouterID, out.MayManage = sc.routerID, may
		EvAppsState.Send(cn.srv.hub, cn.c, out)
	}()
}

// appsDo answers `apps:do`: install, start, stop, restart or remove one app.
func (cn *conn) appsDo(raw json.RawMessage) {
	var req appsDoReq
	_ = json.Unmarshal(raw, &req)
	sc := cn.scope()
	done := func(code, msg string) {
		EvAppsProgress.Send(cn.srv.hub, cn.c, AppsProgressPayload{RouterID: sc.routerID, Name: req.Name, Verb: req.Verb,
			Done: true, Code: code, Message: msg})
	}
	verb, known := appVerbs[req.Verb]
	if sc.routerID == "" || sc.rs == nil {
		done("unavailable", "")
		return
	}
	if !known || req.Name == "" {
		done("request", "That is not something this page can do to an app.")
		return
	}
	action := "app." + req.Verb
	if !cn.mayManageApps() {
		cn.recorder().Denied(audit.Event{Action: action, TargetType: "app", TargetName: req.Name, RouterID: sc.routerID})
		done("denied", "Installing and managing apps needs a global administrator.")
		return
	}
	if !req.confirmed() {
		done("request", "Type the app's name to confirm removing it.")
		return
	}
	go func() {
		err := cn.inWriteQueue(func() error {
			return runAppVerb(sc.rs, req.Name, verb, req.Verb == "install", func() {
				cn.recorder().Record(audit.Event{Action: action, TargetType: "app", TargetName: req.Name,
					RouterID: sc.routerID, Note: appNotes[req.Verb]})
			})
		})
		switch {
		case errors.Is(err, errAppNotFound):
			done("notfound", "This router has no app of that name.")
			return
		case err != nil:
			done(rosWriteFail(err), safe.Message(err.Error()))
			return
		}
		cn.followApp(sc.rs, sc.routerID, req.Name, req.Verb)
	}()
}

var errAppNotFound = errors.New("no such app")

// runAppVerb finds the app by name in a fresh read, records the change (before
// it happens), then runs the command on the row's id. An install first sets the
// app's use-https from IP Cloud (see the file's header).
func runAppVerb(rs appReader, name, verb string, install bool, record func()) error {
	rows, err := rs.Exec(routeros.Cmd{Path: "/app/print",
		Args: []string{"=.proplist=.id,name", "?name=" + name}})
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0][".id"] == "" {
		return errAppNotFound
	}
	id := rows[0][".id"]
	record()
	if install {
		https := "no"
		if cloudHasName(rs) {
			https = "yes"
		}
		if _, err := rs.Exec(routeros.Cmd{Path: "/app/set", Args: []string{"=numbers=" + id, "=use-https=" + https}}); err != nil {
			return err
		}
	}
	_, err = rs.Exec(routeros.Cmd{Path: "/app/" + verb, Args: []string{"=numbers=" + id}, Timeout: 60 * time.Second})
	return err
}

var appNotes = map[string]string{
	"install": "installs a container app from RouterOS's app store: pulls its images and adds its network and firewall rules (an HTTPS link when IP Cloud has a DNS name, plain HTTP otherwise)",
	"start":   "starts an installed container app",
	"stop":    "stops a container app",
	"restart": "restarts a container app",
	"remove":  "removes a container app and deletes its data",
}

// followApp re-reads one app until it reaches its verb's goal, fails or
// appFollowMax passes, and
// sends each reading as progress. A router switch or closed socket stops it.
func (cn *conn) followApp(rs appReader, routerID, name, verb string) {
	quit := make(chan struct{})
	cn.appsMu.Lock()
	if cn.appsQuit != nil {
		close(cn.appsQuit)
	}
	cn.appsQuit = quit
	cn.appsMu.Unlock()
	res := followAppRow(rs, name, appGoal[verb], quit, func(r routeros.Reply) {
		EvAppsProgress.Send(cn.srv.hub, cn.c, AppsProgressPayload{RouterID: routerID, Name: name, Verb: verb,
			Status: r["status"], Running: r["running"] == "true", UIURL: r["ui-url"]})
	})
	if res.code == codeScanStopped {
		return
	}
	EvAppsProgress.Send(cn.srv.hub, cn.c, AppsProgressPayload{RouterID: routerID, Name: name, Verb: verb, Done: true,
		Code: res.code, Message: res.message, Running: res.running, UIURL: res.uiURL, Status: res.status})
}

type followResult struct {
	code, message, status, uiURL string
	running                      bool
}

// appGoal is the state each verb is followed to. A stop and a remove are
// followed too: cleanup takes seconds, and a store read before it ends shows
// the app half-removed, with nothing to say when it finishes.
var appGoal = map[string]string{"install": "running", "start": "running", "restart": "running",
	"stop": "stopped", "remove": "available"}

// followAppRow is the loop itself, apart from the socket so it can be tested.
func followAppRow(rs appReader, name, goal string, quit <-chan struct{}, progress func(routeros.Reply)) followResult {
	deadline := time.Now().Add(appFollowMax)
	tick := time.NewTicker(appPollEvery)
	defer tick.Stop()
	for {
		rows, err := rs.Exec(routeros.Cmd{Path: "/app/print",
			Args: []string{"=.proplist=name,disabled,running,status,ui-url,interface", "?name=" + name}})
		if err != nil {
			return followResult{code: "failed", message: safe.Message(err.Error())}
		}
		if len(rows) != 1 {
			return followResult{code: "notfound", message: "The app is no longer in the store."}
		}
		r := rows[0]
		progress(r)
		state := appState(r)
		switch {
		case state == goal:
			return followResult{running: state == "running", status: r["status"], uiURL: r["ui-url"]}
		case state == "error":
			return followResult{code: "failed", status: r["status"], message: "RouterOS reports: " + safe.Message(r["status"])}
		case goal == "running" && (state == "stopped" || state == "available"):
			return followResult{code: "failed", status: r["status"], message: "The app stopped before it was running."}
		}
		if time.Now().After(deadline) {
			return followResult{code: "timeout", status: r["status"],
				message: "Not " + goal + " after 10 minutes; it may still be working. The store shows its state."}
		}
		select {
		case <-quit:
			return followResult{code: codeScanStopped}
		case <-tick.C:
		}
	}
}

// stopApps ends this connection's install being followed: releaseRouter calls it.
func (cn *conn) stopApps() {
	cn.appsMu.Lock()
	defer cn.appsMu.Unlock()
	if cn.appsQuit != nil {
		close(cn.appsQuit)
		cn.appsQuit = nil
	}
}

// appsSetup answers `apps:setup`: the disk apps are stored on, and optionally
// the LAN bridge. Each value must be one the router just offered; setup never
// formats or erases anything.
func (cn *conn) appsSetup(raw json.RawMessage) {
	var req appsSetupReq
	_ = json.Unmarshal(raw, &req)
	sc := cn.scope()
	if sc.routerID == "" || sc.rs == nil {
		EvAppsState.Send(cn.srv.hub, cn.c, emptyApps("", "unavailable", ""))
		return
	}
	if !cn.mayManageApps() {
		cn.recorder().Denied(audit.Event{Action: "app.setup", TargetType: "app", RouterID: sc.routerID})
		EvAppsState.Send(cn.srv.hub, cn.c, emptyApps(sc.routerID, "denied", "Setting up apps needs a global administrator."))
		return
	}
	may := true
	go func() {
		store := readAppStore(sc.rs)
		store.RouterID, store.MayManage = sc.routerID, may
		if msg := validateAppSetup(store, req); msg != "" {
			store.Code, store.Message = "request", msg
			EvAppsState.Send(cn.srv.hub, cn.c, store)
			return
		}
		args := []string{"=disk=" + req.Disk}
		if req.LanBridge != "" {
			args = append(args, "=lan-bridge="+req.LanBridge)
		}
		err := cn.inWriteQueue(func() error {
			cn.recorder().Record(audit.Event{Action: "app.setup", TargetType: "app", TargetName: req.Disk, RouterID: sc.routerID,
				Note: "sets the disk (and LAN bridge) RouterOS's app store installs to"})
			_, err := sc.rs.Exec(routeros.Cmd{Path: "/app/settings/set", Args: args})
			return err
		})
		if err != nil {
			store.Code, store.Message = rosWriteFail(err), safe.Message(err.Error())
			EvAppsState.Send(cn.srv.hub, cn.c, store)
			return
		}
		out := readAppStore(sc.rs)
		out.RouterID, out.MayManage = sc.routerID, may
		EvAppsState.Send(cn.srv.hub, cn.c, out)
	}()
}

// validateAppSetup: the disk and bridge must be ones the router just offered.
func validateAppSetup(store AppsPayload, req appsSetupReq) string {
	if !store.Supported {
		return "This router has no app store."
	}
	okDisk := false
	for _, d := range store.Disks {
		if d.Slot == req.Disk {
			okDisk = true
		}
	}
	if !okDisk {
		return "Pick one of the router's mounted disks."
	}
	if req.LanBridge == "" {
		return ""
	}
	for _, b := range store.Bridges {
		if b == req.LanBridge {
			return ""
		}
	}
	return "Pick one of the router's bridges, or none."
}
