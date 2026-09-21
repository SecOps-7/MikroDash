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
	"errors"
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

type pkgAutoUpgradeReq struct {
	On bool `json:"on"`
}

func (cn *conn) pkgErr(code string, extra map[string]any) {
	m := map[string]any{"code": code}
	for k, v := range extra {
		m[k] = v
	}
	EvPackagesError.Send(cn.srv.hub, cn.c, m)
}

// pkgReady resolves the collector, or reports why it cannot.
//
// A collector switched off for this router has no inventory, so there is nothing
// to target and nothing to show afterwards. The writes go through the session
// rather than the collector precisely because the collector may be idle — but
// without its payload the actions have no subject.
// refreshPackages re-reads the package menus, when that collector is enabled.
// Gated, because a disabled collector must not be started by an action (#105);
// the action itself still runs against whatever the payload holds.
func (cn *conn) refreshPackages(coll *collect.Packages) {
	if coll != nil && cn.rsession != nil && cn.rsession.CollectorEnabled("packages") {
		coll.RefreshNow()
	}
}

// pkgCollector is pkgReady WITHOUT the error frame: the extracted run*
// functions report "unavailable" in their outcome, and the socket adapter sends
// it once. Sending it here as well put two frames on the wire for one click, and
// an assistant-run action would have sent a page error nobody asked for.
func (cn *conn) pkgCollector() *collect.Packages {
	if cn.routerID == "" || cn.rsession == nil {
		return nil
	}
	return cn.rsession.Packages()
}

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
	case errors.Is(err, errWriteRateLimited):
		return "rate-limited"
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
	EvPackagesCaps.Send(cn.srv.hub, cn.c, map[string]any{
		"permitted":  cn.canPage("packages", "write"),
		"routerName": cn.rsession.Label,
	})
}

// packagesSchedule runs one reversible per-package verb.
// runPackageSchedule schedules or cancels one package change and REPORTS what
// happened. Split out for `run_action`; `via` is the audit provenance.
func (cn *conn) runPackageSchedule(action, name, via string) writeOutcome {
	coll := cn.pkgCollector()
	if coll == nil {
		return writeOutcome{Code: "unavailable"}
	}
	if !cn.canPage("packages", "write") {
		cn.recorder().Denied(audit.Event{
			Action: "package.schedule", TargetType: "package",
			RouterID: cn.routerID, TargetName: name,
		})
		return writeOutcome{Code: "denied"}
	}

	cmd := pkgScheduleCmd[action]
	if cmd == "" || name == "" {
		return writeOutcome{Code: "bad-request"}
	}

	// RE-READ FIRST. The row is resolved from the collector's payload, and a
	// package's `.id` CHANGES when it is installed or removed — measured on the
	// CHR: scheduling a package straight after an apply-and-reboot addressed the
	// id it had before, and the router answered "no such item". The collector's
	// copy can also be a minute old on a page nobody has open.
	cn.refreshPackages(coll)

	// RESOLVED AGAINST WHAT THE COLLECTOR HAS JUST READ, never against an id the
	// browser sent. A stale or crafted page cannot then address a row that was
	// never on screen.
	var target *collect.Package
	if last := coll.Last(); last != nil {
		for i := range last.Packages {
			if last.Packages[i].Name == name {
				target = &last.Packages[i]
				break
			}
		}
	}
	if target == nil || target.ID == "" {
		return writeOutcome{Code: "no-such-package", Name: name}
	}

	// Queued, unlike the Node original — whose own comment says its three
	// package actions "were written before _routerWriteQueue existed". The
	// mechanism changes, the behaviour does not, and a write landing on the
	// wrong router because of a router switch mid-flight is what the queue
	// prevents.
	err := cn.inWriteQueue(func() error {
		_, e := cn.rsession.Exec(routeros.Cmd{Path: cmd, Args: []string{"=.id=" + target.ID}})
		return e
	})
	if err != nil {
		return writeOutcome{Code: rosWriteFail(err), Name: name,
			Detail: map[string]any{"message": safe.Message(err.Error())}}
	}

	note := "scheduled; inert until apply-changes reboots the router"
	if action == "unschedule" {
		note = "scheduled change cancelled"
	}
	extra := []audit.KV(nil)
	if via != "" {
		extra = append(extra, audit.KV{Key: "via", Value: via})
	}
	cn.recorder().Record(audit.Event{
		Action: "package." + action, TargetType: "package",
		TargetID: target.ID, TargetName: target.Name, RouterID: cn.routerID,
		Note: note, Extra: extra,
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
	return writeOutcome{Action: action, Name: name}
}

func (cn *conn) packagesSchedule(raw json.RawMessage) {
	var req pkgScheduleReq
	_ = json.Unmarshal(raw, &req)
	out := cn.runPackageSchedule(req.Action, req.Name, "")
	if out.Code != "" {
		detail := map[string]any{}
		for k, v := range out.Detail {
			detail[k] = v
		}
		if out.Name != "" {
			detail["name"] = out.Name
		}
		cn.pkgErr(out.Code, detail)
		return
	}
	EvPackagesOk.Send(cn.srv.hub, cn.c, map[string]any{"action": out.Action, "name": out.Name})
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
	err := cn.inWriteQueue(func() error {
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
	EvPackagesOk.Send(cn.srv.hub, cn.c, map[string]any{"action": "check"})
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
	var req pkgApplyReq
	_ = json.Unmarshal(raw, &req)
	out := cn.runRouterOSUpgrade(req.Confirm, "")
	if out.Code != "" {
		cn.pkgErr(out.Code, out.Detail)
		return
	}
	// `routerId` is the router the dialog waits to see come back.
	body := map[string]any{"action": "upgrade", "routerName": out.Name, "routerId": cn.routerID}
	for _, k := range []string{"latest", "rebooting"} {
		if v, ok := out.Detail[k]; ok {
			body[k] = v
		}
	}
	EvPackagesOk.Send(cn.srv.hub, cn.c, body)
}

// runRouterOSUpgrade downloads and installs the RouterOS update the router has
// found, which REBOOTS it, and reports what happened. Split out for
// `run_action` (MikroMCP's manage_upgrade), as runFirmwareUpgrade is; the
// page's handler above wraps it. `confirm` is the router's name typed back.
func (cn *conn) runRouterOSUpgrade(confirm, via string) writeOutcome {
	if cn.routerID == "" || cn.rsession == nil {
		return writeOutcome{Code: "unavailable"}
	}
	if !cn.canPage("packages", "write") {
		cn.recorder().Denied(audit.Event{Action: "package.upgrade", TargetType: "router",
			TargetID: cn.routerID, RouterID: cn.routerID})
		return writeOutcome{Code: "denied"}
	}
	// The same second gate `packagesApply` uses: prove the operator knows which
	// router this is, case-insensitively and trimmed. It is not a typing test.
	name := cn.rsession.Label
	if name == "" || !strings.EqualFold(strings.TrimSpace(confirm), name) {
		return writeOutcome{Code: "confirm-mismatch", Name: name, Detail: map[string]any{"routerName": name}}
	}

	var out writeOutcome
	err := cn.inWriteQueue(func() error {
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
			out = writeOutcome{Code: "nothing-to-update", Name: name,
				Detail: map[string]any{"installed": installed, "latest": latest}}
			return nil
		}

		log.Printf("[packages] upgrade on %s — %s to %s, router will reboot",
			name, orQuestion(installed), latest)
		EvPackagesApplying.Send(cn.srv.hub, cn.c, map[string]any{"routerName": name, "count": 1, "upgrade": true})

		// BEFORE THE CALL: the router reboots while the command is in flight, so
		// a row written afterwards would be lost exactly when it matters most.
		extra := []audit.KV{
			{Key: "from", Value: installed},
			{Key: "to", Value: latest},
			{Key: "channel", Value: row["channel"]},
		}
		if via != "" {
			extra = append(extra, audit.KV{Key: "via", Value: via})
		}
		cn.recorder().Record(audit.Event{
			Action: "package.upgrade", TargetType: "router",
			TargetID: cn.routerID, TargetName: name, RouterID: cn.routerID,
			Extra: extra,
			Note:  "downloaded the RouterOS update and rebooted the router",
		})

		if _, werr := cn.rsession.Exec(routeros.Cmd{Path: "/system/package/update/install"}); werr != nil {
			return werr
		}
		out = writeOutcome{Action: "upgrade", Name: name, Detail: map[string]any{"latest": latest}}
		return nil
	})
	if err != nil {
		// A LOST CONNECTION HERE IS THE EXPECTED OUTCOME, not a failure: the
		// router is rebooting as it answers. Reporting it as an error would tell
		// the operator the upgrade failed when it is in fact under way.
		if code := rosWriteFail(err); code == "failed" {
			return writeOutcome{Action: "upgrade", Name: name, Detail: map[string]any{"rebooting": true}}
		}
		return writeOutcome{Code: rosWriteFail(err), Name: name,
			Detail: map[string]any{"message": safe.Message(err.Error())}}
	}
	return out
}

// packagesReboot answers `packages:reboot`: restart the router, nothing else.
func (cn *conn) packagesReboot(raw json.RawMessage) {
	var req pkgApplyReq
	_ = json.Unmarshal(raw, &req)
	out := cn.runReboot(req.Confirm, "")
	if out.Code != "" {
		cn.pkgErr(out.Code, out.Detail)
		return
	}
	EvPackagesOk.Send(cn.srv.hub, cn.c, map[string]any{"action": "reboot", "routerName": out.Name,
		"routerId": cn.routerID, "rebooting": true})
}

// runReboot restarts the router (MikroMCP's reboot, the operator's choice on
// 2026-09-21). Before, a reboot was offered only as the last step of an apply
// or a firmware upgrade. It owns the Packages page's write permission, as
// those do, and the router's name typed back, as they do.
func (cn *conn) runReboot(confirm, via string) writeOutcome {
	if cn.routerID == "" || cn.rsession == nil {
		return writeOutcome{Code: "unavailable"}
	}
	if !cn.canPage("packages", "write") {
		cn.recorder().Denied(audit.Event{Action: "router.reboot", TargetType: "router",
			TargetID: cn.routerID, RouterID: cn.routerID})
		return writeOutcome{Code: "denied"}
	}
	name := cn.rsession.Label
	if name == "" || !strings.EqualFold(strings.TrimSpace(confirm), name) {
		return writeOutcome{Code: "confirm-mismatch", Name: name, Detail: map[string]any{"routerName": name}}
	}
	err := cn.inWriteQueue(func() error {
		log.Printf("[packages] reboot of %s", name)
		var extra []audit.KV
		if via != "" {
			extra = append(extra, audit.KV{Key: "via", Value: via})
		}
		// BEFORE THE CALL, as every reboot-class action records.
		cn.recorder().Record(audit.Event{
			Action: "router.reboot", TargetType: "router",
			TargetID: cn.routerID, TargetName: name, RouterID: cn.routerID,
			Extra: extra, Note: "rebooted the router",
		})
		_, werr := cn.rsession.Exec(routeros.Cmd{Path: "/system/reboot"})
		return werr
	})
	// A LOST CONNECTION IS THE EXPECTED OUTCOME: the router is going down.
	if err != nil && rosWriteFail(err) != "failed" {
		return writeOutcome{Code: rosWriteFail(err), Name: name,
			Detail: map[string]any{"message": safe.Message(err.Error())}}
	}
	return writeOutcome{Action: "reboot", Name: name, Detail: map[string]any{"rebooting": true}}
}

// orQuestion is the log's placeholder for a router that did not report its
// installed version — the original writes `?` there rather than an empty gap.
func orQuestion(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// runPackageApply applies scheduled package changes, which REBOOTS the router,
// and reports what happened. Split out for `run_action`.
//
// `confirm` is the router's name typed back — the second gate, and the only one
// of its kind here.
func (cn *conn) runPackageApply(confirm, via string) writeOutcome {
	coll := cn.pkgCollector()
	if coll == nil {
		return writeOutcome{Code: "unavailable"}
	}
	if !cn.canPage("packages", "write") {
		cn.recorder().Denied(audit.Event{Action: "package.apply", RouterID: cn.routerID})
		return writeOutcome{Code: "denied"}
	}

	// Case-insensitive and trimmed, matching the live app: the point is to prove
	// the operator knows which router this is, not to test their typing.
	name := cn.rsession.Label
	if name == "" || !strings.EqualFold(strings.TrimSpace(confirm), name) {
		return writeOutcome{Code: "confirm-mismatch", Name: name,
			Detail: map[string]any{"routerName": name}}
	}

	// RE-READ FIRST, for the reason the schedule path re-reads: this decides
	// whether to REBOOT. Measured on the CHR — after an apply, the collector's
	// payload still listed the change it had just applied, so a second apply
	// found "pending" work and rebooted the router for nothing.
	cn.refreshPackages(coll)

	var pending []collect.Package
	if last := coll.Last(); last != nil {
		for _, p := range last.Packages {
			if p.Scheduled != "" {
				pending = append(pending, p)
			}
		}
	}
	if len(pending) == 0 {
		return writeOutcome{Code: "nothing-scheduled", Name: name}
	}

	names := make([]string, 0, len(pending))
	for _, p := range pending {
		verb := p.ScheduledAction
		if verb == "" {
			verb = "change"
		}
		names = append(names, p.Name+":"+verb)
	}

	EvPackagesApplying.Send(cn.srv.hub, cn.c, map[string]any{"routerName": name, "count": len(pending)})

	// RECORDED BEFORE THE CALL, and that ordering is the point. The router
	// reboots as it answers, so the connection is expected to drop while the
	// command is in flight; writing the row afterwards would lose the record of
	// the most consequential action this app can take.
	extra := []audit.KV{{Key: "scheduled", Value: names}}
	if via != "" {
		extra = append(extra, audit.KV{Key: "via", Value: via})
	}
	cn.recorder().Record(audit.Event{
		Action: "package.apply", TargetType: "router",
		TargetID: cn.routerID, TargetName: name, RouterID: cn.routerID,
		Extra: extra,
		Note:  "applied scheduled package changes and rebooted the router",
	})

	err := cn.inWriteQueue(func() error {
		_, e := cn.rsession.Exec(routeros.Cmd{Path: "/system/package/apply-changes"})
		return e
	})
	if err != nil {
		// A LOST CONNECTION HERE IS THE EXPECTED OUTCOME, NOT A FAILURE. The
		// router is rebooting because it was told to. Only a refusal the router
		// actually articulated — a permission policy, an absent command — is
		// worth reporting; anything else is the reboot happening.
		if code := rosWriteFail(err); code != "failed" {
			return writeOutcome{Code: code, Name: name,
				Detail: map[string]any{"message": safe.Message(err.Error())}}
		}
		return writeOutcome{Action: "apply", Name: name, Detail: map[string]any{"rebooting": true}}
	}
	return writeOutcome{Action: "apply", Name: name}
}

func (cn *conn) packagesApply(raw json.RawMessage) {
	var req pkgApplyReq
	_ = json.Unmarshal(raw, &req)
	out := cn.runPackageApply(req.Confirm, "")
	if out.Code != "" {
		cn.pkgErr(out.Code, out.Detail)
		return
	}
	body := map[string]any{"action": "apply", "routerName": out.Name}
	if reboot, _ := out.Detail["rebooting"].(bool); reboot {
		body["rebooting"] = true
	}
	EvPackagesOk.Send(cn.srv.hub, cn.c, body)
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
		EvPackagesNotes.Send(cn.srv.hub, cn.c, map[string]any{
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
	EvPackagesNotes.Send(cn.srv.hub, cn.c, map[string]any{
		"version": version, "notes": notes})
}

// packagesFwUpgrade answers `packages:fwupgrade` — write the RouterBOOT
// firmware the board is carrying, and reboot into it.
//
// ── TWO COMMANDS, AND THE SECOND IS THE POINT ───────────────────────────────
//
// `/system/routerboard/upgrade` stages the bootloader; the board runs the new
// one only after a restart, which is why MikroTik's own instruction is "execute
// the command, followed by a reboot". Staging it and leaving the reboot to
// somebody else would report success for a change nothing has applied.
//
// ── THE SAME TWO GATES AS AN APPLY, FOR THE SAME REASON ─────────────────────
//
// Write permission, then the router's name typed back. This reboots a production
// router, and the name is what makes "the wrong router" a hard mistake rather
// than an easy one.
//
// ── AND THE ROW IS RE-READ, NEVER TRUSTED FROM THE PAYLOAD ──────────────────
//
// The button was drawn from a payload that may be minutes old. A board already
// carrying its upgrade firmware has nothing to gain from a reboot, so the pair
// is read fresh and the action refused when they match.
// runFirmwareUpgrade upgrades the RouterBOOT firmware and reboots, and reports
// what happened. Split out for `run_action`; `confirm` is the router's name
// typed back.
func (cn *conn) runFirmwareUpgrade(confirm, via string) writeOutcome {
	if cn.routerID == "" || cn.rsession == nil {
		return writeOutcome{Code: "unavailable"}
	}
	if !cn.canPage("packages", "write") {
		cn.recorder().Denied(audit.Event{Action: "package.fwupgrade", TargetType: "router",
			TargetID: cn.routerID, RouterID: cn.routerID})
		return writeOutcome{Code: "denied"}
	}

	name := cn.rsession.Label
	if name == "" || !strings.EqualFold(strings.TrimSpace(confirm), name) {
		return writeOutcome{Code: "confirm-mismatch", Name: name,
			Detail: map[string]any{"routerName": name}}
	}

	var out writeOutcome
	err := cn.inWriteQueue(func() error {
		rows, rerr := cn.rsession.Exec(routeros.Cmd{Path: "/system/routerboard/print"})
		if rerr != nil {
			return rerr
		}
		row := routeros.Reply{}
		if len(rows) > 0 {
			row = rows[0]
		}
		// A CHR or an x86 install has no routerboard at all, which is not a
		// failure and not something to reboot for.
		if !isTruthy(row["routerboard"]) {
			out = writeOutcome{Code: "no-routerboard", Name: name}
			return nil
		}
		current, upgrade := row["current-firmware"], row["upgrade-firmware"]
		if upgrade == "" || (current != "" && current == upgrade) {
			out = writeOutcome{Code: "firmware-current", Name: name,
				Detail: map[string]any{"current": current, "upgrade": upgrade}}
			return nil
		}

		log.Printf("[packages] routerboot upgrade on %s — %s to %s, router will reboot",
			name, orQuestion(current), upgrade)
		EvPackagesApplying.Send(cn.srv.hub, cn.c, map[string]any{"routerName": name, "count": 1, "upgrade": true})

		// BEFORE THE CALL, as the apply and the RouterOS upgrade both record: the
		// router reboots while the command is in flight, so a row written
		// afterwards is lost exactly when it matters.
		extra := []audit.KV{
			{Key: "from", Value: current},
			{Key: "to", Value: upgrade},
		}
		if via != "" {
			extra = append(extra, audit.KV{Key: "via", Value: via})
		}
		cn.recorder().Record(audit.Event{
			Action: "package.fwupgrade", TargetType: "router",
			TargetID: cn.routerID, TargetName: name, RouterID: cn.routerID,
			Extra: extra,
			Note:  "upgraded the RouterBOOT firmware and rebooted the router",
		})

		if _, werr := cn.rsession.Exec(routeros.Cmd{Path: "/system/routerboard/upgrade"}); werr != nil {
			return werr
		}
		// THE REBOOT IS WHAT APPLIES IT. A failure here is reported as the reboot
		// happening, below, which is the same shape the apply path has.
		if _, werr := cn.rsession.Exec(routeros.Cmd{Path: "/system/reboot"}); werr != nil {
			return werr
		}
		out = writeOutcome{Action: "fwupgrade", Name: name, Detail: map[string]any{"latest": upgrade}}
		return nil
	})
	if err != nil {
		// A LOST CONNECTION HERE IS THE EXPECTED OUTCOME, not a failure: the
		// router is rebooting because it was told to.
		if code := rosWriteFail(err); code == "failed" {
			return writeOutcome{Action: "fwupgrade", Name: name, Detail: map[string]any{"rebooting": true}}
		}
		return writeOutcome{Code: rosWriteFail(err), Name: name,
			Detail: map[string]any{"message": safe.Message(err.Error())}}
	}
	return out
}

func (cn *conn) packagesFwUpgrade(raw json.RawMessage) {
	var req pkgApplyReq
	_ = json.Unmarshal(raw, &req)
	out := cn.runFirmwareUpgrade(req.Confirm, "")
	if out.Code != "" {
		cn.pkgErr(out.Code, out.Detail)
		return
	}
	body := map[string]any{"action": "fwupgrade", "routerName": out.Name, "routerId": cn.routerID}
	for _, k := range []string{"latest", "rebooting"} {
		if v, ok := out.Detail[k]; ok {
			body[k] = v
		}
	}
	EvPackagesOk.Send(cn.srv.hub, cn.c, body)
}

// packagesAutoUpgrade answers `packages:autoupgrade` — turn RouterBOOT's own
// `auto-upgrade` on or off.
//
// ── NO REBOOT, AND NO CONFIRMATION ──────────────────────────────────────────
//
// This writes one boolean that decides what the board does at its NEXT boot, so
// there is nothing to interrupt and nothing to type back. It is the one
// RouterBOARD setting this app writes: `protected-routerboot` and the boot
// device are not offered, and RouterOS asks for a physical button press for
// those anyway.
//
// ── READ BACK BEFORE IT REPORTS SUCCESS ─────────────────────────────────────
//
// The #97 contract for every write here: the row is re-read and compared, and an
// acknowledgement the router did not apply is reported as an unknown outcome
// rather than as success. The audit row carries what the setting was and what it
// became, from the reads rather than from the request.
func (cn *conn) packagesAutoUpgrade(raw json.RawMessage) {
	if cn.routerID == "" || cn.rsession == nil {
		cn.pkgErr("unavailable", nil)
		return
	}
	if !cn.canPage("packages", "write") {
		cn.recorder().Denied(audit.Event{Action: "package.autoupgrade", TargetType: "router",
			TargetID: cn.routerID, RouterID: cn.routerID})
		cn.pkgErr("denied", nil)
		return
	}

	var req pkgAutoUpgradeReq
	_ = json.Unmarshal(raw, &req)
	want := "no"
	if req.On {
		want = "yes"
	}
	name := cn.rsession.Label

	err := cn.inWriteQueue(func() error {
		before, rerr := cn.autoUpgradeNow()
		if rerr != nil {
			return rerr
		}
		if _, werr := cn.rsession.Exec(routeros.Cmd{
			Path: "/system/routerboard/settings/set", Args: []string{"=auto-upgrade=" + want},
		}); werr != nil {
			return werr
		}
		after, rerr := cn.autoUpgradeNow()
		if rerr != nil {
			// The write was accepted and the read was not, so what the router
			// holds is unknown. Said plainly rather than reported as success.
			cn.recorder().Record(audit.Event{
				Action: "package.autoupgrade", TargetType: "router",
				TargetID: cn.routerID, TargetName: name, RouterID: cn.routerID,
				Outcome: "outcome-unknown",
				Before:  map[string]any{"autoUpgrade": before},
				After:   map[string]any{"autoUpgrade": want},
			})
			cn.pkgErr("outcome-unknown", nil)
			return nil
		}
		cn.recorder().Record(audit.Event{
			Action: "package.autoupgrade", TargetType: "router",
			TargetID: cn.routerID, TargetName: name, RouterID: cn.routerID,
			Before: map[string]any{"autoUpgrade": before},
			After:  map[string]any{"autoUpgrade": after},
			Note:   "changed the RouterBOOT auto-upgrade setting",
		})
		if after != want {
			cn.pkgErr("outcome-unknown", nil)
			return nil
		}
		// The collector's slow lane would carry the new value in its own time;
		// the page asked for this change and should see it now.
		if coll := cn.rsession.Packages(); coll != nil {
			coll.RefreshNow()
		}
		EvPackagesOk.Send(cn.srv.hub, cn.c, map[string]any{
			"action": "autoupgrade", "routerName": name, "on": after == "yes"})
		return nil
	})
	if err != nil {
		cn.pkgErr(rosWriteFail(err), map[string]any{"message": safe.Message(err.Error())})
	}
}

// autoUpgradeNow reads the setting as the router holds it, "yes" or "no".
func (cn *conn) autoUpgradeNow() (string, error) {
	rows, err := cn.rsession.Exec(routeros.Cmd{Path: "/system/routerboard/settings/print"})
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	if isTruthy(rows[0]["auto-upgrade"]) {
		return "yes", nil
	}
	return "no", nil
}

// isTruthy is RouterOS's own spelling of a boolean on the wire.
func isTruthy(v string) bool { return v == "true" || v == "yes" }
