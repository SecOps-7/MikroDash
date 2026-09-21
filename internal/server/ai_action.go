package server

// `run_action`: the assistant asking for one of the pages' own actions.
//
// ── IT RUNS THE PAGE'S CODE, NOT A COPY OF IT ───────────────────────────────
//
// Each action here calls the same `run*` function the socket handler calls —
// `runWanLease`, `runBackupNow`, `runPackageSchedule`, `runPackageApply`,
// `runFirmwareUpgrade`, and the Tools page's `runTorch` and `runBtest` — which is why those
// were extracted first. Two paths to
// one router command would be two places for the guard, the audit row and the
// refresh to drift apart.
//
// ── NOTHING RUNS UNTIL THE OPERATOR SAYS SO ─────────────────────────────────
//
// Every call raises a proposal, whatever `aiConfirmWrites` says, and the work
// happens on approval. The reboot-class actions additionally need the router's
// name typed back, and that word arrives on the approval frame: the tool takes
// no `confirm` argument, so a model cannot answer its own confirmation.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mikrodash/internal/aiprovider"
	"mikrodash/internal/aitools"
	"mikrodash/internal/diag"
	"mikrodash/internal/resource"
)

// runAIActionTool is `run_action`. Like every tool it returns an ANSWER rather
// than an error: a refusal is something the model must be able to relay.
func (cn *conn) runAIActionTool(tc aiprovider.ToolCall) string {
	var args struct {
		Action string `json:"action"`
		Target string `json:"target"`
		Mode   string `json:"mode"`
	}
	if json.Unmarshal([]byte(tc.Function.Arguments), &args) != nil {
		return "Those arguments were not valid JSON. Send `action`, and `target` or `mode` " +
			"where the action needs them."
	}
	spec, ok := aitools.ActionByKey(args.Action)
	if !ok {
		// NOT ECHOED: a name the model invented becomes established by repetition.
		return "There is no such action. Use one of the names the tool lists."
	}
	// RE-CHECKED, though the enum was already filtered: the enum was built when
	// the question was asked and a role can be edited while an answer is composed.
	if !cn.canPage(spec.Page, "write") {
		return "You do not have permission to run that, so nothing was proposed."
	}
	if cn.rsession == nil {
		return "No device is selected, so nothing was proposed."
	}
	target, mode, refusal := actionArgs(spec, args.Target, args.Mode)
	if refusal != "" {
		return refusal
	}
	return cn.raiseAIAction(spec, target, mode)
}

// actionArgs checks what the named action needs, and returns the refusal when it
// is missing or outside the declared set. Pure, so the rules can be exercised
// without a router.
func actionArgs(spec aitools.ActionSpec, rawTarget, rawMode string) (target, mode, refusal string) {
	target = strings.TrimSpace(rawTarget)
	if spec.Target != "" && target == "" {
		return "", "", fmt.Sprintf(
			"That action needs the %s name in `target`, so nothing was proposed.", spec.Target)
	}
	mode = strings.TrimSpace(rawMode)
	if len(spec.Modes) > 0 {
		if mode == "" {
			return "", "", fmt.Sprintf("That action needs `mode`: one of %s. Nothing was proposed.",
				strings.Join(spec.Modes, ", "))
		}
		known := false
		for _, m := range spec.Modes {
			if m == mode {
				known = true
				break
			}
		}
		if !known {
			return "", "", fmt.Sprintf("`mode` must be one of %s, so nothing was proposed.",
				strings.Join(spec.Modes, ", "))
		}
	}
	return target, mode, ""
}

// raiseAIAction stores the intent and puts it in front of the operator. It
// returns at once, for the reason raiseAIProposal does: a human takes longer
// than the model request this runs inside.
func (cn *conn) raiseAIAction(spec aitools.ActionSpec, target, mode string) string {
	return cn.raiseAIActionWarned(spec, target, mode, "", nil)
}

// raiseAIActionWarned is raiseAIAction carrying a guard's warning: `ack` is its
// fingerprint, replayed at approval, and `warning` its detail with the code, as
// the dialog shows it for a row write.
func (cn *conn) raiseAIActionWarned(spec aitools.ActionSpec, target, mode, ack string, warning map[string]any) string {
	tok, err := cn.addProposal(&aiWriteProposal{actionKey: spec.Key, target: target, mode: mode, ack: ack})
	if errors.Is(err, errProposalsFull) {
		return "There are already several things waiting for the operator to answer. " +
			"Ask them to deal with those before proposing another."
	}
	if err != nil {
		return "That action could not be put to the operator, so nothing was run."
	}

	warnCode, _ := warning["code"].(string)
	shown, _ := warning["warning"].(map[string]any)
	if shown == nil {
		shown = map[string]any{}
	}
	// WHY the name is typed: script_run runs code; every other typed-name
	// action reboots. The card says which, and so does the reply to the model.
	reason := ""
	if spec.TypedName {
		reason = "reboot"
		if spec.Key == "script_run" {
			reason = "run"
		}
	}
	EvAIPropose.Send(cn.srv.hub, cn.c, map[string]any{
		"token": tok, "kind": "action", "action": spec.Key, "label": aiActionLabel(spec, mode),
		"name": target, "command": aiActionCommand(spec, target, mode),
		"routerName": cn.rsession.Label, "typedName": spec.TypedName, "typedReason": reason,
		"credentials": spec.Credentials,
		"warnCode":    warnCode, "warning": shown, "values": map[string]string{},
	})

	if reason == "run" {
		return "Waiting for confirmation. MikroDash is asking the operator to confirm this by " +
			"typing the router's name, because it runs code on the router. It has not run yet: " +
			"tell them what the script will do."
	}
	if spec.TypedName {
		return "Waiting for confirmation. MikroDash is asking the operator to confirm this by " +
			"typing the router's name, because it reboots the router. It has not run yet: tell " +
			"them what it will do."
	}
	return "Waiting for confirmation. MikroDash is showing the operator this action in a " +
		"confirmation dialog, and it runs when they accept it. Tell them what it will do. It " +
		"has not run yet."
}

// aiActionLabel is what the dialog calls the action.
func aiActionLabel(spec aitools.ActionSpec, mode string) string {
	switch spec.Key {
	case "wan_dhcp_renew":
		return "Renew DHCP lease"
	case "wan_dhcp_release":
		return "Release DHCP lease"
	case "backup_run":
		return "Back up now"
	case "packages_schedule":
		if mode == "unschedule" {
			return "Cancel a scheduled package change"
		}
		return "Schedule a package " + mode
	case "packages_apply_and_reboot":
		return "Apply package changes and reboot"
	case "firmware_upgrade_and_reboot":
		return "Upgrade RouterBOOT firmware and reboot"
	case "routeros_upgrade_and_reboot":
		return "Upgrade RouterOS and reboot"
	case "reboot":
		return "Reboot the router"
	case "script_run":
		return "Run a script"
	case "certificate_sign":
		return "Sign a certificate"
	case "fetch_url":
		return "Router downloads a file"
	case "wireguard_show_config":
		return "Show a WireGuard client configuration"
	case "torch":
		return "Watch an interface's traffic"
	case "bandwidth_test":
		return "Bandwidth test"
	case "container_start":
		return "Start a container"
	case "container_stop":
		return "Stop a container"
	case "container_remove":
		return "Remove a container"
	}
	return spec.Key
}

// aiActionCommand is the RouterOS command the action will send, for the dialog.
// Built here rather than taken from the model, as the write proposal's is.
func aiActionCommand(spec aitools.ActionSpec, target, mode string) string {
	switch spec.Key {
	case "wan_dhcp_renew":
		return "/ip/dhcp-client/renew (" + target + ")"
	case "wan_dhcp_release":
		return "/ip/dhcp-client/release (" + target + ")"
	case "backup_run":
		return "/system/backup/save, then /export"
	case "packages_schedule":
		return "/system/package/" + mode + " (" + target + ")"
	case "packages_apply_and_reboot":
		return "/system/package/apply-changes"
	case "firmware_upgrade_and_reboot":
		return "/system/routerboard/upgrade, then /system/reboot"
	case "routeros_upgrade_and_reboot":
		return "/system/package/update/install"
	case "reboot":
		return "/system/reboot"
	case "script_run":
		return "/system/script/run (" + target + ")"
	case "certificate_sign":
		return "/certificate/sign (" + target + ")"
	case "fetch_url":
		return "/tool/fetch url=" + target
	case "wireguard_show_config":
		return "Opens this peer's configuration dialog on the WireGuard page (" + target + ")"
	case "torch":
		return fmt.Sprintf("/tool/torch interface=%s duration=%ds", target, diag.TorchDefaultSeconds)
	case "container_start", "container_stop":
		return "/container/" + strings.TrimPrefix(spec.Key, "container_") + " (" + target + ")"
	case "container_remove":
		return "/container/remove (" + target + ")"
	case "bandwidth_test":
		// The login is typed at approval and is not part of what is shown here.
		return fmt.Sprintf("/tool/bandwidth-test address=%s duration=%ds protocol=%s direction=%s",
			target, diag.BtestDefaultSeconds, diag.BtestProtocols[0], diag.BtestDirections[0])
	}
	return ""
}

// actionApproval is what the operator's approval frame carries besides the
// token: the router name they typed back, and a login an action needs. None of
// it ever came from the model.
type actionApproval struct {
	Confirm  string `json:"confirm"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// approveAIAction runs an action the operator accepted. `in.Confirm` is the
// router name they typed, which only the reboot-class actions read — and they
// check it themselves, against the router's own label.
func (cn *conn) approveAIAction(p *aiWriteProposal, in actionApproval) {
	confirm := in.Confirm
	spec, ok := aitools.ActionByKey(p.actionKey)
	if !ok {
		cn.aiActionDone(p.actionKey, false, "That action is not available on this build.")
		return
	}
	// CHECKED AGAIN AT APPROVAL: the proposal may have been raised minutes ago
	// and a role can be edited in between.
	if !cn.canPage(spec.Page, "write") {
		cn.aiActionDone(spec.Key, false, "You do not have permission to run that.")
		return
	}
	if cn.rsession == nil {
		cn.aiActionDone(spec.Key, false, "No device is selected.")
		return
	}

	var out writeOutcome
	switch spec.Key {
	case "wan_dhcp_renew":
		out = cn.runWanLease("renew", cn.wanLeaseID(p.target), p.target, p.ack, "agent")
	case "wan_dhcp_release":
		out = cn.runWanLease("release", cn.wanLeaseID(p.target), p.target, p.ack, "agent")
	case "backup_run":
		out = cn.runBackupNow("agent")
	case "packages_schedule":
		out = cn.runPackageSchedule(p.mode, p.target, "agent")
	case "packages_apply_and_reboot":
		out = cn.runPackageApply(confirm, "agent")
	case "firmware_upgrade_and_reboot":
		out = cn.runFirmwareUpgrade(confirm, "agent")
	case "routeros_upgrade_and_reboot":
		out = cn.runRouterOSUpgrade(confirm, "agent")
	case "reboot":
		out = cn.runReboot(confirm, "agent")
	case "script_run":
		out = cn.runScriptAction(p.target, confirm)
	case "certificate_sign":
		out = cn.runNamedRowAction(resource.Certificate, p.target, "sign")
	case "fetch_url":
		out = cn.runFetchURL(p.target, "agent")
	case "wireguard_show_config":
		out = cn.runWgShowConfig(p.target)
	case "torch":
		out = cn.runTorchAction(p.target)
	case "bandwidth_test":
		out = cn.runBtestAction(p.target, in.User, in.Password)
	case "container_start", "container_stop":
		out = cn.runContainerAction(p.target, strings.TrimPrefix(spec.Key, "container_"))
	case "container_remove":
		out = cn.runContainerRemove(p.target)
	}

	cn.answerAIAction(spec, p, out)
}

// answerAIAction reports an approved action's outcome.
//
// A GUARD WARNED, as the WAN page's self-cutoff does for the uplink MikroDash
// reaches the router on: put to the operator again WITH the warning and its
// fingerprint, as change_row's guardGate does. Before, the approval always sent
// no acknowledgement, and the action could never run from here.
func (cn *conn) answerAIAction(spec aitools.ActionSpec, p *aiWriteProposal, out writeOutcome) {
	if fp, gate := guardGate(out); gate {
		cn.raiseAIActionWarned(spec, p.target, p.mode, fp, gateDetail(out))
		cn.aiActionDone(spec.Key, false, "Not run yet: MikroDash reaches this router through "+
			"that uplink, so it is asking the operator again with that warning shown.")
		return
	}
	if out.Code != "" {
		cn.aiActionDone(spec.Key, false, aiActionRefusal(spec, out))
		return
	}
	cn.aiActionDone(spec.Key, true, aiActionApplied(spec, p, out))
}

// wanLeaseID resolves an uplink NAME to the dhcp-client id the action needs.
//
// The model never quotes an id: it names an interface, as list_wan_status
// reports it, and the id is looked up here from a fresh read. An interface with
// no DHCP client yields "", which `runWanLease` refuses as a bad request.
func (cn *conn) wanLeaseID(iface string) string {
	rows, _, _, err := cn.wanRead()
	if err != nil {
		return ""
	}
	for _, r := range rows {
		if strings.EqualFold(r["interface"], iface) {
			return r[".id"]
		}
	}
	return ""
}

// aiActionRefusal says why an action did not run, in the page's own words.
func aiActionRefusal(spec aitools.ActionSpec, out writeOutcome) string {
	switch out.Code {
	case "denied":
		return "Not run: you may not run that."
	case "unavailable":
		return "Not run: the router is not reachable, or that page's collector is not running."
	case "bad-request":
		if spec.Target != "" {
			return "Not run: no " + spec.Target + " of that name was found on this router."
		}
		return "Not run: that request was incomplete."
	case "stale-row":
		return "Not run: that uplink changed on the router since it was read."
	case "not-configured":
		return "Not run: backups are not configured for this router, so there is no password to " +
			"encrypt one with."
	case "no-such-package":
		return "Not run: there is no package of that name on this router."
	case "confirm-mismatch":
		return "Not run: the router's name was not typed back correctly."
	case "nothing-scheduled":
		return "Not run: there are no scheduled package changes to apply."
	case "no-routerboard":
		return "Not run: this router has no RouterBOOT firmware to upgrade."
	case "firmware-current":
		return "Not run: the RouterBOOT firmware is already current."
	case "rate-limited":
		return "Not run: too many changes to this router in the last minute."
	case "busy":
		return "Not run: another diagnostic is still running for this operator."
	case "interface":
		return "Not run: this router has no interface of that name."
	case "not-applicable":
		return "Not run: it is already in that state."
	case "address", "request":
		if msg, _ := out.Detail["message"].(string); msg != "" {
			return "Not run: " + msg + "."
		}
	}
	if msg, _ := out.Detail["message"].(string); msg != "" {
		return "Not run: the router refused it: " + msg
	}
	return "Not run: the router refused it."
}

// aiActionApplied says what happened, in terms the operator can check.
func aiActionApplied(spec aitools.ActionSpec, p *aiWriteProposal, out writeOutcome) string {
	switch spec.Key {
	case "wan_dhcp_renew", "wan_dhcp_release":
		return fmt.Sprintf("Done: the DHCP lease on %s was %sd. The lease state settles over the "+
			"next few seconds.", quoted(out.Name), out.Action)
	case "backup_run":
		outcome, _ := out.Detail["outcome"].(string)
		changed, _ := out.Detail["changed"].(bool)
		if outcome == "skipped" {
			return "A backup was already running for this router, so this one was skipped: that " +
				"run is the restore point."
		}
		if !changed {
			return "Done: a backup ran. The configuration had not changed since the last one."
		}
		return "Done: a backup ran and the configuration had changed, so a new pair was stored."
	case "packages_schedule":
		if p.mode == "unschedule" {
			return fmt.Sprintf("Done: the scheduled change on %s was cancelled.", quoted(p.target))
		}
		return fmt.Sprintf("Done: %s is scheduled to %s. Nothing happens until the scheduled "+
			"changes are applied, which reboots the router.", quoted(p.target), p.mode)
	case "packages_apply_and_reboot":
		return "Done: the scheduled package changes were applied and the router is rebooting. It " +
			"will be unreachable for a minute or two."
	case "firmware_upgrade_and_reboot":
		return "Done: the RouterBOOT firmware upgrade was started and the router is rebooting."
	case "routeros_upgrade_and_reboot":
		if latest, _ := out.Detail["latest"].(string); latest != "" {
			return "Done: RouterOS " + latest + " is being installed and the router is rebooting. It will " +
				"be unreachable for a few minutes."
		}
		return "Done: the RouterOS update is being installed and the router is rebooting."
	case "reboot":
		return "Done: the router is rebooting. It will be unreachable for a minute or two."
	case "script_run":
		return fmt.Sprintf("Done: the script %s was run. Its effects are whatever it does; check them with "+
			"the relevant list_ tool.", quoted(p.target))
	case "certificate_sign":
		return fmt.Sprintf("Done: %s was signed. It now has a key and a fingerprint.", quoted(p.target))
	case "fetch_url":
		return fmt.Sprintf("Done: the router downloaded it as %s. list_files shows it.", quoted(out.Name))
	case "wireguard_show_config":
		return "Done: the configuration is open in the operator's browser. It holds the peer's private " +
			"key, so it is not part of this conversation."
	case "torch":
		if r, ok := out.Detail["torch"].(*diag.TorchResult); ok {
			return torchSummary(r)
		}
	case "bandwidth_test":
		if r, ok := out.Detail["btest"].(*diag.BtestResult); ok {
			return btestSummary(r)
		}
	case "container_start":
		return fmt.Sprintf("Done: %s was started. It may take a few seconds to show as running.", quoted(p.target))
	case "container_stop":
		return fmt.Sprintf("Done: %s was stopped.", quoted(p.target))
	case "container_remove":
		return fmt.Sprintf("Done: %s was removed.", quoted(p.target))
	}
	return "Done."
}

// aiActionDone tells the page, and through it the model's next turn, what became
// of the action.
func (cn *conn) aiActionDone(key string, applied bool, text string) {
	cn.aiWritten(map[string]any{
		"applied": applied, "resource": key, "name": "", "text": text,
	})
}

// rowIDByName resolves a NAME the model took from a list tool to the row's id,
// from a fresh read: the model never quotes a RouterOS id back.
func (cn *conn) rowIDByName(res *resource.Resource, name string) string {
	rows, err := cn.readMenu(res)
	if err != nil {
		return ""
	}
	for _, r := range rows {
		if r["name"] == name {
			return r[".id"]
		}
	}
	return ""
}

// runNamedRowAction runs a resource's row action on the row with that name,
// through the page's own path (runRowAction), with the agent's provenance.
func (cn *conn) runNamedRowAction(res *resource.Resource, name, action string) writeOutcome {
	id := cn.rowIDByName(res, name)
	if id == "" {
		return writeOutcome{Code: "bad-request"}
	}
	return cn.runRowAction(res, &resRequest{ID: id, ExpectedIdentity: name, Action: action}, "agent")
}

// runScriptAction is an approved script_run. RUNNING A SCRIPT IS RUNNING CODE:
// the raw-command gate first (a signed-in global administrator, the
// aiAllowRawCommands setting), then the router's name typed back, then the
// Scripts page's Run, whose codeGate checks the administrator again.
func (cn *conn) runScriptAction(name, confirm string) writeOutcome {
	if _, refusal := cn.rawCommandGate("raw.script"); refusal != "" {
		return writeOutcome{Code: "denied"}
	}
	if cn.rsession == nil {
		return writeOutcome{Code: "unavailable"}
	}
	if label := cn.rsession.Label; label == "" || !strings.EqualFold(strings.TrimSpace(confirm), label) {
		return writeOutcome{Code: "confirm-mismatch", Name: label, Detail: map[string]any{"routerName": label}}
	}
	return cn.runNamedRowAction(resource.Script, name, "run")
}

// runContainerAction is an approved container_start or container_stop: the
// Containers page's own row action, through the same path, with the agent's
// provenance.
func (cn *conn) runContainerAction(name, verb string) writeOutcome {
	return cn.runNamedRowAction(resource.Container, name, verb)
}

// runContainerRemove is an approved container_remove: the page's delete.
func (cn *conn) runContainerRemove(name string) writeOutcome {
	id := cn.rowIDByName(resource.Container, name)
	if id == "" {
		return writeOutcome{Code: "bad-request"}
	}
	return cn.removeRow(resource.Container, &resRequest{ID: id, ExpectedIdentity: name}, "agent")
}
