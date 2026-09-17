package server

// `run_action`: the assistant asking for one of the pages' own actions.
//
// ── IT RUNS THE PAGE'S CODE, NOT A COPY OF IT ───────────────────────────────
//
// Each action here calls the same `run*` function the socket handler calls —
// `runWanLease`, `runBackupNow`, `runPackageSchedule`, `runPackageApply`,
// `runFirmwareUpgrade` — which is why those were extracted first. Two paths to
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
	"fmt"
	"strings"
	"time"

	"mikrodash/internal/aiprovider"
	"mikrodash/internal/aitools"
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
	tok, err := aiProposalToken()
	if err != nil {
		return "That action could not be put to the operator, so nothing was run."
	}

	cn.proposeMu.Lock()
	if cn.proposals == nil {
		cn.proposals = map[string]*aiWriteProposal{}
	}
	now := time.Now()
	for k, v := range cn.proposals {
		if now.Sub(v.raisedAt) > aiProposalTTL {
			delete(cn.proposals, k)
		}
	}
	if len(cn.proposals) >= aiMaxProposals {
		cn.proposeMu.Unlock()
		return "There are already several things waiting for the operator to answer. " +
			"Ask them to deal with those before proposing another."
	}
	cn.proposals[tok] = &aiWriteProposal{
		token: tok, actionKey: spec.Key, target: target, mode: mode, raisedAt: now,
	}
	cn.proposeMu.Unlock()

	EvAIPropose.Send(cn.srv.hub, cn.c, map[string]any{
		"token": tok, "kind": "action", "action": spec.Key, "label": aiActionLabel(spec, mode),
		"name": target, "command": aiActionCommand(spec, target, mode),
		"routerName": cn.rsession.Label, "typedName": spec.TypedName,
		"warnCode": "", "warning": map[string]any{}, "values": map[string]string{},
	})

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
	}
	return ""
}

// approveAIAction runs an action the operator accepted. `confirm` is the router
// name they typed, which only the reboot-class actions read — and they check it
// themselves, against the router's own label.
func (cn *conn) approveAIAction(p *aiWriteProposal, confirm string) {
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
		out = cn.runWanLease("renew", cn.wanLeaseID(p.target), p.target, "", "agent")
	case "wan_dhcp_release":
		out = cn.runWanLease("release", cn.wanLeaseID(p.target), p.target, "", "agent")
	case "backup_run":
		out = cn.runBackupNow("agent")
	case "packages_schedule":
		out = cn.runPackageSchedule(p.mode, p.target, "agent")
	case "packages_apply_and_reboot":
		out = cn.runPackageApply(confirm, "agent")
	case "firmware_upgrade_and_reboot":
		out = cn.runFirmwareUpgrade(confirm, "agent")
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
	case "self-cutoff", "stale-warning":
		return "Not run: MikroDash reaches this router through that uplink, so the action needs " +
			"the safety warning acknowledged in the dialog."
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
	}
	return "Done."
}

// aiActionDone tells the page, and through it the model's next turn, what became
// of the action.
func (cn *conn) aiActionDone(key string, applied bool, text string) {
	EvAIWritten.Send(cn.srv.hub, cn.c, map[string]any{
		"applied": applied, "resource": key, "name": "", "text": text,
	})
}
