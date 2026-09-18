package aitools

import (
	"fmt"
	"sort"
	"strings"
)

// `run_action`: the page actions the assistant can ask for.
//
// ── WHY A SECOND WRITER, AND ONLY ONE MORE ──────────────────────────────────
//
// `change_row` covers everything that IS a row: a queue, a user, a firewall
// rule. These five are not rows. Renewing a DHCP lease, taking a backup,
// scheduling a package change, applying those changes and upgrading RouterBOOT
// are VERBS the pages already offer, each with its own handler, its own audit
// row and — for the two that reboot — its own typed-back router name. They have
// no menu to write and nothing to read back, so `change_row` cannot express
// them, and one tool per verb would put five more descriptions on every request.
//
// ── EVERY CALL IS A PROPOSAL ────────────────────────────────────────────────
//
// Unlike `change_row`, which the `aiConfirmWrites` setting can let through, an
// action ALWAYS goes to the operator first. None of these can be undone from the
// app: a released lease is a dropped uplink, a reboot is a reboot, and a package
// uninstall takes effect at the next one. The setting is about ordinary edits.
//
// ── THE TYPED NAME IS THE OPERATOR'S, NEVER THE MODEL'S ─────────────────────
//
// `packages_apply_and_reboot` and `firmware_upgrade_and_reboot` require the
// router's name typed back, exactly as the page does. That word comes from the
// approval the operator sends, and this tool takes no `confirm` argument at all:
// a model that could supply it would be answering its own confirmation.

// ActionToolName is the second tool that changes anything through a DECLARED
// action. The two raw command tools below are a different thing again, and are
// not advertised at all.
const ActionToolName = "run_action"

// RawCommandToolName and BulkToolName are the raw command tools: one RouterOS
// command the model composed, and an ordered list of them.
//
// ── DECLARED HERE, ADVERTISED NOWHERE ───────────────────────────────────────
//
// They are NOT in `All()` and not in `Permitted()`, so no model is ever told
// they exist, and the generated catalogue does not carry them. The names live
// here because the executor has to recognise a call by name — a model can invent
// a name, and inventing this one must reach the gates rather than a "no such
// tool" that would read as the feature being merely hidden.
//
// The gates are in internal/server: a signed-in global administrator, the
// `aiAllowRawCommands` setting, and the router's name typed back on every single
// command. Slice 4 of the MikroMCP parity work builds them; the frontend is not
// wired, deliberately.
const (
	RawCommandToolName = "run_command"
	BulkToolName       = "bulk_execute"
)

// ActionSpec is one declared action: what it is called, which page's write
// permission owns it, and what it needs.
type ActionSpec struct {
	Key  string
	Page string
	// Summary is what the model reads in the enum's description.
	Summary string
	// Target names the argument this action needs, "" when it needs none. It is
	// a NAME the model already has from a list tool — an interface, a package —
	// never a RouterOS id, which the model has no business quoting back.
	Target string
	// Modes are the values `mode` may take, for an action that has more than one
	// shape. Empty means the action takes no mode.
	Modes []string
	// TypedName marks a reboot-class action: the operator must type the router's
	// name into the confirmation before it runs.
	TypedName bool
	// Credentials marks an action that logs in somewhere else — the bandwidth
	// test's far server — for which the APPROVER types the user and password in
	// the dialog. The model never supplies, sees or is told them.
	Credentials bool
}

// Actions is the catalogue, in the order the enum lists them.
func Actions() []ActionSpec {
	return []ActionSpec{
		{Key: "wan_dhcp_renew", Page: "wan", Target: "interface",
			Summary: "Renew the DHCP lease on one uplink. The uplink drops for a few seconds."},
		{Key: "wan_dhcp_release", Page: "wan", Target: "interface",
			Summary: "Release the DHCP lease on one uplink. The uplink stays down until the " +
				"client rebinds."},
		{Key: "backup_run", Page: "backups",
			Summary: "Take a backup of the selected router now, if backups are configured for it."},
		{Key: "packages_schedule", Page: "packages", Target: "package",
			Modes: []string{"enable", "disable", "uninstall", "unschedule"},
			Summary: "Schedule a package change, or cancel one. Nothing happens until the " +
				"scheduled changes are applied, which reboots the router."},
		{Key: "packages_apply_and_reboot", Page: "packages", TypedName: true,
			Summary: "Apply the scheduled package changes. THIS REBOOTS THE ROUTER."},
		{Key: "firmware_upgrade_and_reboot", Page: "packages", TypedName: true,
			Summary: "Upgrade the RouterBOOT firmware. THIS REBOOTS THE ROUTER."},
		// A DIAGNOSTIC, and an action rather than a read tool because it loads
		// the router's CPU while it runs: the operator decided torch needs write
		// access to Tools, and so it is always proposed. The duration is fixed.
		{Key: "torch", Page: "tools", Target: "interface",
			Summary: "Watch one interface's traffic for 5 seconds with /tool/torch and report the " +
				"busiest flows by protocol, address and port, with their average rates. It loads " +
				"the router's CPU while it runs."},
		{Key: "bandwidth_test", Page: "tools", Target: "address", Credentials: true,
			Summary: "Run a 5-second TCP bandwidth test, both directions, from the router to another " +
				"MikroTik router's bandwidth server, and report the average throughput each way. It " +
				"saturates the link and loads both routers while it runs. The operator types the " +
				"far server's user and password when they confirm it; never ask for them."},
	}
}

// ActionByKey resolves a declared action. An unknown key returns false and the
// caller refuses: the enum is the allow-list.
func ActionByKey(key string) (ActionSpec, bool) {
	for _, a := range Actions() {
		if a.Key == key {
			return a, true
		}
	}
	return ActionSpec{}, false
}

// actionTool builds the tool over exactly the actions this viewer may run.
//
// The enum IS the permission, as `change_row`'s resource enum is: a viewer who
// may not write the Packages page is never told the reboot actions exist, so the
// model cannot propose one and be refused — which reads to an operator as
// MikroDash being broken rather than as a permission they do not have.
func actionTool(specs []ActionSpec) Tool {
	keys := make([]string, 0, len(specs))
	var b strings.Builder
	b.WriteString("Ask the operator to run one of the router's own actions. Every call is put " +
		"to them for confirmation and nothing happens until they accept it; say so rather than " +
		"reporting it as done. The actions are:")
	modes := map[string]bool{}
	targets := false
	for _, a := range specs {
		keys = append(keys, a.Key)
		fmt.Fprintf(&b, "\n- %s: %s", a.Key, a.Summary)
		if a.Target != "" {
			targets = true
			fmt.Fprintf(&b, " Pass the %s name as `target`.", a.Target)
		}
		if len(a.Modes) > 0 {
			fmt.Fprintf(&b, " Pass `mode`: %s.", strings.Join(a.Modes, ", "))
		}
		if a.TypedName {
			b.WriteString(" The operator has to type the router's name to confirm it.")
		}
		if a.Credentials {
			b.WriteString(" The operator types any login it needs in the confirmation.")
		}
		for _, m := range a.Modes {
			modes[m] = true
		}
	}
	props := map[string]any{
		"action": map[string]any{
			"type": "string", "enum": keys,
			"description": "Which action to put to the operator.",
		},
	}
	if targets {
		props["target"] = map[string]any{
			"type": "string",
			"description": "What the action is about — an interface or package NAME as a list_ " +
				"tool reported it. Not a RouterOS id.",
		}
	}
	if len(modes) > 0 {
		list := make([]string, 0, len(modes))
		for m := range modes {
			list = append(list, m)
		}
		sort.Strings(list)
		props["mode"] = map[string]any{
			"type": "string", "enum": list,
			"description": "Which shape of the action, where it has more than one.",
		}
	}
	return Tool{
		Name:        ActionToolName,
		Description: b.String(),
		Parameters: map[string]any{
			"type": "object", "properties": props,
			"required":             []string{"action"},
			"additionalProperties": false,
		},
		Access: AccessWrite,
	}
}
