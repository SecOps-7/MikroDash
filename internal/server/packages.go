package server

// The Packages page's four socket actions.
//
// The per-package verbs are cheap and reversible: enable, disable and uninstall
// do not act, they SCHEDULE, and unschedule undoes any of them. Nothing happens
// until apply-changes reboots the router. That asymmetry is the shape of this
// whole file — three ordinary writes and one that is gated twice.
//
// ON THE SECOND GATE. `packages:apply` reboots a production router, and the
// browser has to send the router's own NAME back before it will run. That is not
// an "are you sure": a misclick cannot produce a router's name, and neither can
// a click on the router you thought you were looking at. Reproduced from the
// live app exactly, and the operator confirmed the button ships as-is.
//
// ON THE MISSING SECOND PERMISSION, BECAUSE ITS ABSENCE IS DELIBERATE. Node gates
// each of these on `_pageAllowed(socket,'packages',<access>)` AND
// `_socketCan(socket,'router:write',rid)`. This port checks only the page half,
// and the two are equivalent AT THIS CALL SITE: rbac.js confers router:write
// from any write-level page row — "Any write row also confers router:write" — so
// a role granting packages:write grants router:write in the same scope, and the
// conjunction cannot be narrower than its first term. That reasoning does NOT
// generalise. A call site needing router:write WITHOUT a page write would have
// to resolve the permission properly, and internal/rbac answers pages only.

import (
	"encoding/json"
	"log"
	"strings"

	"mikrodash/internal/audit"
	"mikrodash/internal/collect"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
)

// pkgScheduleCmd maps the browser's verb to a RouterOS menu. A verb that is not
// here is refused: this is an allow-list, not a lookup with a fallback.
var pkgScheduleCmd = map[string]string{
	"enable":     "/system/package/enable",
	"disable":    "/system/package/disable",
	"uninstall":  "/system/package/uninstall",
	"unschedule": "/system/package/unschedule",
}

type pkgScheduleReq struct {
	Action string `json:"action"`
	Name   string `json:"name"`
}

type pkgApplyReq struct {
	Confirm string `json:"confirm"`
}

func (cn *conn) pkgErr(code string, extra map[string]any) {
	m := map[string]any{"code": code}
	for k, v := range extra {
		m[k] = v
	}
	cn.srv.hub.Send(cn.c, "packages:error", m)
}

// pkgReady resolves the collector, or reports why it cannot.
//
// A collector switched off for this router has no inventory, so there is nothing
// to target and nothing to show afterwards. The writes go through the session
// rather than the collector precisely because the collector may be idle — but
// without its payload the actions have no subject.
func (cn *conn) pkgReady() *collect.Packages {
	if cn.routerID == "" || cn.rsession == nil {
		cn.pkgErr("unavailable", nil)
		return nil
	}
	p := cn.rsession.Packages()
	if p == nil {
		cn.pkgErr("unavailable", nil)
		return nil
	}
	return p
}

// rosWriteFail separates the two refusals that look alike and mean different
// things to whoever is looking at the button: one is "you cannot", the other is
// "the RouterOS user cannot".
func rosWriteFail(err error) string {
	m := strings.ToLower(err.Error())
	switch {
	case strings.Contains(m, "not enough permission"),
		strings.Contains(m, "permission denied"),
		strings.Contains(m, "no permissions"):
		return "router-write-policy"
	case strings.Contains(m, "no such"), strings.Contains(m, "unknown command"):
		return "unsupported"
	}
	return "failed"
}

// packagesCaps answers what this SOCKET may do.
//
// The page draws its action buttons from this rather than from the payload:
// whether somebody may act is a property of the session, and the collector
// payload is shared by every viewer of the router.
func (cn *conn) packagesCaps() {
	if cn.pkgReady() == nil {
		return
	}
	if !cn.canPage("packages", "read") {
		cn.pkgErr("denied", nil)
		return
	}
	cn.srv.hub.Send(cn.c, "packages:caps", map[string]any{
		"permitted":  cn.canPage("packages", "write"),
		"routerName": cn.rsession.Label,
	})
}

// packagesSchedule runs one reversible per-package verb.
func (cn *conn) packagesSchedule(raw json.RawMessage) {
	coll := cn.pkgReady()
	if coll == nil {
		return
	}
	var req pkgScheduleReq
	_ = json.Unmarshal(raw, &req)

	if !cn.canPage("packages", "write") {
		cn.recorder().Denied(audit.Event{
			Action: "package.schedule", TargetType: "package",
			RouterID: cn.routerID, TargetName: req.Name,
		})
		cn.pkgErr("denied", nil)
		return
	}

	cmd := pkgScheduleCmd[req.Action]
	if cmd == "" || req.Name == "" {
		cn.pkgErr("bad-request", nil)
		return
	}

	// RESOLVED AGAINST WHAT THE COLLECTOR LAST READ, never against an id the
	// browser sent. A stale or crafted page cannot then address a row that was
	// never on screen.
	var target *collect.Package
	if last := coll.Last(); last != nil {
		for i := range last.Packages {
			if last.Packages[i].Name == req.Name {
				target = &last.Packages[i]
				break
			}
		}
	}
	if target == nil || target.ID == "" {
		cn.pkgErr("no-such-package", map[string]any{"name": req.Name})
		return
	}

	// Queued, unlike the Node original — whose own comment says its three
	// package actions "were written before _routerWriteQueue existed". The
	// mechanism changes, the behaviour does not, and a write landing on the
	// wrong router because of a router switch mid-flight is what the queue
	// prevents.
	err := cn.rsession.InWriteQueue(func() error {
		_, e := cn.rsession.Exec(routeros.Cmd{Path: cmd, Args: []string{"=.id=" + target.ID}})
		return e
	})
	if err != nil {
		cn.pkgErr(rosWriteFail(err), map[string]any{"name": req.Name, "message": safe.Message(err.Error())})
		return
	}

	note := "scheduled; inert until apply-changes reboots the router"
	if req.Action == "unschedule" {
		note = "scheduled change cancelled"
	}
	cn.recorder().Record(audit.Event{
		Action: "package." + req.Action, TargetType: "package",
		TargetID: target.ID, TargetName: target.Name, RouterID: cn.routerID, Note: note,
	})
	// Re-read rather than assume: the pending banner must show what the router
	// did, not what the browser hoped it did.
	//
	// #105: the REFRESH is gated, the ACTION is not. A disabled collector is a
	// null stub on the live side, so its refresh does nothing there — but the
	// package write itself still runs, and gating `pkgReady` instead would refuse
	// an action the original performs.
	if cn.rsession.CollectorEnabled("packages") {
		coll.RefreshNow()
	}
	cn.srv.hub.Send(cn.c, "packages:ok", map[string]any{"action": req.Action, "name": req.Name})
}

// packagesCheck asks the router to contact MikroTik's update servers.
func (cn *conn) packagesCheck() {
	coll := cn.pkgReady()
	if coll == nil {
		return
	}
	if !cn.canPage("packages", "write") {
		cn.recorder().Denied(audit.Event{Action: "package.check", RouterID: cn.routerID})
		cn.pkgErr("denied", nil)
		return
	}
	// Reaches MikroTik's servers, so it is a button rather than a poll. The
	// background check on the System page is unaffected.
	err := cn.rsession.InWriteQueue(func() error {
		_, e := cn.rsession.Exec(routeros.Cmd{Path: "/system/package/update/check-for-updates"})
		return e
	})
	if err != nil {
		cn.pkgErr(rosWriteFail(err), map[string]any{"message": safe.Message(err.Error())})
		return
	}
	cn.recorder().Record(audit.Event{
		Action: "package.check", TargetType: "package",
		RouterID: cn.routerID, Note: "contacted MikroTik update servers",
	})
	// Gated as above.
	if cn.rsession.CollectorEnabled("packages") {
		coll.RefreshNow()
	}
	cn.srv.hub.Send(cn.c, "packages:ok", map[string]any{"action": "check"})
}

// packagesApply applies every scheduled change, which REBOOTS the router.
// packagesUpgrade answers `packages:upgrade` — download the RouterOS update and
// reboot into it.
//
// ── NOT GATED ON THE COLLECTOR, DELIBERATELY ───────────────────────────────
//
// Every other handler here starts with `pkgReady`, which refuses when the
// Packages collector is switched off. This one does not, and the original says
// why (#105): the write goes through the session's own connection, so an
// upgrade works on a router whose Packages collector is disabled. Only the
// refresh afterwards would need the collector — and the router is rebooting
// anyway, so there is nothing to refresh.
//
// ── THE ROW IS READ FRESH, NEVER TRUSTED FROM THE PAYLOAD ──────────────────
//
// The button was drawn from a payload that may be minutes old. If somebody else
// has already installed the update, rebooting again achieves nothing and costs
// the network an outage — so the version pair is re-read and the action refused
// when there is nothing to do.
//
// ── AND THE AUDIT ROW IS WRITTEN BEFORE THE CALL ───────────────────────────
//
// The router reboots while the command is in flight, so a row written afterwards
// would be lost exactly when it matters most. This is the most consequential
// action in the app; `packagesApply` records the same way for the same reason.
func (cn *conn) packagesUpgrade(raw json.RawMessage) {
	if cn.routerID == "" || cn.rsession == nil {
		cn.pkgErr("unavailable", nil)
		return
	}
	if !cn.canPage("packages", "write") {
		cn.recorder().Denied(audit.Event{Action: "package.upgrade", TargetType: "router",
			TargetID: cn.routerID, RouterID: cn.routerID})
		cn.pkgErr("denied", nil)
		return
	}

	var req pkgApplyReq
	_ = json.Unmarshal(raw, &req)

	// The same second gate `packagesApply` uses: prove the operator knows which
	// router this is, case-insensitively and trimmed. It is not a typing test.
	name := cn.rsession.Label
	if name == "" || !strings.EqualFold(strings.TrimSpace(req.Confirm), name) {
		cn.pkgErr("confirm-mismatch", map[string]any{"routerName": name})
		return
	}

	err := cn.rsession.InWriteQueue(func() error {
		rows, rerr := cn.rsession.Exec(routeros.Cmd{Path: "/system/package/update/print"})
		if rerr != nil {
			return rerr
		}
		row := routeros.Reply{}
		if len(rows) > 0 {
			row = rows[0]
		}
		installed, latest := row["installed-version"], row["latest-version"]
		if latest == "" || (installed != "" && latest == installed) {
			cn.pkgErr("nothing-to-update", map[string]any{"installed": installed, "latest": latest})
			return nil
		}

		log.Printf("[packages] upgrade on %s — %s to %s, router will reboot",
			name, orQuestion(installed), latest)
		cn.srv.hub.Send(cn.c, "packages:applying",
			map[string]any{"routerName": name, "count": 1, "upgrade": true})

		cn.recorder().Record(audit.Event{
			Action: "package.upgrade", TargetType: "router",
			TargetID: cn.routerID, TargetName: name, RouterID: cn.routerID,
			Extra: []audit.KV{
				{Key: "from", Value: installed},
				{Key: "to", Value: latest},
				{Key: "channel", Value: row["channel"]},
			},
			Note: "downloaded the RouterOS update and rebooted the router",
		})

		if _, werr := cn.rsession.Exec(routeros.Cmd{Path: "/system/package/update/install"}); werr != nil {
			return werr
		}
		cn.srv.hub.Send(cn.c, "packages:ok",
			map[string]any{"action": "upgrade", "routerName": name, "latest": latest})
		return nil
	})
	if err != nil {
		// A LOST CONNECTION HERE IS THE EXPECTED OUTCOME, not a failure: the
		// router is rebooting as it answers. Reporting it as an error would tell
		// the operator the upgrade failed when it is in fact under way.
		if code := rosWriteFail(err); code == "failed" {
			cn.srv.hub.Send(cn.c, "packages:ok",
				map[string]any{"action": "upgrade", "routerName": name, "rebooting": true})
		} else {
			cn.pkgErr(code, map[string]any{"message": safe.Message(err.Error())})
		}
	}
}

// orQuestion is the log's placeholder for a router that did not report its
// installed version — the original writes `?` there rather than an empty gap.
func orQuestion(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func (cn *conn) packagesApply(raw json.RawMessage) {
	coll := cn.pkgReady()
	if coll == nil {
		return
	}
	if !cn.canPage("packages", "write") {
		cn.recorder().Denied(audit.Event{Action: "package.apply", RouterID: cn.routerID})
		cn.pkgErr("denied", nil)
		return
	}

	var req pkgApplyReq
	_ = json.Unmarshal(raw, &req)

	// The second gate, and the only one of its kind here. Case-insensitive and
	// trimmed, matching the live app: the point is to prove the operator knows
	// which router this is, not to test their typing.
	name := cn.rsession.Label
	if name == "" || !strings.EqualFold(strings.TrimSpace(req.Confirm), name) {
		cn.pkgErr("confirm-mismatch", map[string]any{"routerName": name})
		return
	}

	var pending []collect.Package
	if last := coll.Last(); last != nil {
		for _, p := range last.Packages {
			if p.Scheduled != "" {
				pending = append(pending, p)
			}
		}
	}
	if len(pending) == 0 {
		cn.pkgErr("nothing-scheduled", nil)
		return
	}

	names := make([]string, 0, len(pending))
	for _, p := range pending {
		verb := p.ScheduledAction
		if verb == "" {
			verb = "change"
		}
		names = append(names, p.Name+":"+verb)
	}

	cn.srv.hub.Send(cn.c, "packages:applying",
		map[string]any{"routerName": name, "count": len(pending)})

	// RECORDED BEFORE THE CALL, and that ordering is the point. The router
	// reboots as it answers, so the connection is expected to drop while the
	// command is in flight; writing the row afterwards would lose the record of
	// the most consequential action this app can take.
	cn.recorder().Record(audit.Event{
		Action: "package.apply", TargetType: "router",
		TargetID: cn.routerID, TargetName: name, RouterID: cn.routerID,
		Extra: []audit.KV{{Key: "scheduled", Value: names}},
		Note:  "applied scheduled package changes and rebooted the router",
	})

	err := cn.rsession.InWriteQueue(func() error {
		_, e := cn.rsession.Exec(routeros.Cmd{Path: "/system/package/apply-changes"})
		return e
	})
	if err != nil {
		// A LOST CONNECTION HERE IS THE EXPECTED OUTCOME, NOT A FAILURE. The
		// router is rebooting because it was told to. Only a refusal the router
		// actually articulated — a permission policy, an absent command — is
		// worth reporting; anything else is the reboot happening.
		if code := rosWriteFail(err); code != "failed" {
			cn.pkgErr(code, map[string]any{"message": safe.Message(err.Error())})
			return
		}
		cn.srv.hub.Send(cn.c, "packages:ok",
			map[string]any{"action": "apply", "routerName": name, "rebooting": true})
		return
	}
	cn.srv.hub.Send(cn.c, "packages:ok", map[string]any{"action": "apply", "routerName": name})
}

// packagesNotes answers the Update dialog's request for a RouterOS changelog.
//
// ── IT NEVER REPORTS ON THE UPGRADE CHANNEL ─────────────────────────────────
//
// The live handler's own comment, and the reason this does not call `pkgErr`:
// that channel is the UPGRADE's, and the dialog renders `denied` on it as "You
// do not have permission to update this router" — which would be false and
// alarming for someone who can update perfectly well and merely cannot be shown
// a changelog. A notes failure answers on the notes channel and says only that
// the notes are missing.
//
// ── READ, NOT WRITE ─────────────────────────────────────────────────────────
//
// `packagesUpgrade` gates on `packages`/`write` because it reboots a router.
// This gates on `read`: it fetches public text and touches nothing.
//
// ── THE VERSION IS ECHOED BACK ──────────────────────────────────────────────
//
// So a slow reply for a router the operator has since switched away from can be
// discarded by the client. That is the second of the three client rules upstream
// recorded with this event.
func (cn *conn) packagesNotes(raw json.RawMessage) {
	var req struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(raw, &req)
	version := strings.TrimSpace(req.Version)

	no := func(why string) {
		cn.srv.hub.Send(cn.c, "packages:notes", map[string]any{
			"version": version, "error": why})
	}
	if cn.routerID == "" || cn.rsession == nil {
		no("unavailable")
		return
	}
	if !cn.canPage("packages", "read") {
		no("denied")
		return
	}
	notes, err := cn.srv.changelog.Notes(version)
	if err != nil {
		// SANITISED before anything reaches the browser, per CLAUDE.md. A fetch
		// failure can carry a hostname, a resolver message or a TLS chain.
		no(safe.Message(err.Error()))
		return
	}
	cn.srv.hub.Send(cn.c, "packages:notes", map[string]any{
		"version": version, "notes": notes})
}
