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
// rule. These are not rows. Renewing a DHCP lease, taking a backup, scheduling
// a package change, applying those changes, upgrading RouterBOOT, a torch run
// and the container verbs are VERBS the pages already offer, each with its own handler, its own audit
// row and — for the two that reboot — its own typed-back router name. They have
// no menu to write and nothing to read back, so `change_row` cannot express
// them, and one tool per verb would put a description each on every request.
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
// Every action marked TypedName (the four that reboot, and script_run) requires
// the router's name typed back, exactly as the page does. That word comes from the
// approval the operator sends, and this tool takes no `confirm` argument at all:
// a model that could supply it would be answering its own confirmation.

// ActionToolName is the second tool that changes anything through a DECLARED
// action. The two raw command tools below are a different thing again, and are
// not advertised at all.
const ActionToolName = "run_action"

// RawCommandToolName and BulkToolName are the raw command tools: one RouterOS
// command the model composed, and an ordered list of them.
//
// ── ADVERTISED ONLY PAST THE GATES ──────────────────────────────────────────
//
// They are NOT in `All()` and not in `Permitted()`, so the generated catalogue
// and the page-permission path never carry them. Since 2026-09-21 (the
// operator's choice, with a Settings switch) internal/server adds `RawTools()`
// to one question's tool list when that viewer passes the standing gates: a
// signed-in global administrator, and the `aiAllowRawCommands` setting on.
// Everyone else is never told they exist. The names live here too because the
// executor recognises a call by name: a model can invent one, and inventing it
// must reach the gates rather than a "no such tool".
//
// The third gate is per command: the router's name typed back on every one,
// whatever `aiConfirmWrites` says. Both reach the operator through the ordinary
// proposal dialog (`kind: "command"`).
const (
	RawCommandToolName = "run_command"
	BulkToolName       = "bulk_execute"
)

// RawPlanMaxSteps is the most commands one bulk_execute may hold.
const RawPlanMaxSteps = 20

// RawTools are the two raw command tools, for a viewer past the standing gates.
func RawTools() []Tool {
	syntax := "One RouterOS command in CLI form: a menu path, a verb and name=value words, such as " +
		"`/ip/firewall/filter/add chain=input action=accept comment=\"office\"` or " +
		"`/interface/set .id=*3 disabled=no`. No scripting: no newline, `;`, `:`, `[ ]`, `{ }`, " +
		"`$`, backtick or backslash."
	return []Tool{{
		Name: RawCommandToolName,
		Description: "Run ONE RouterOS command on the router the operator has selected, for what no " +
			"other tool covers. " + syntax + " It skips MikroDash's own checks, read-back and undo, so " +
			"prefer change_row, plan_changes and run_action. Every command, even a print, is put to " +
			"the operator, who types the router's name to run it; the result is the router's reply.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "The command."},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
		Access: AccessWrite,
	}, {
		Name: BulkToolName,
		Description: fmt.Sprintf("Run up to %d RouterOS commands in order on the router the operator has "+
			"selected, approved once by the operator typing the router's name. Each is as run_command "+
			"takes it. %s It stops at the first command the router refuses.", RawPlanMaxSteps, syntax),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"commands": map[string]any{
					"type": "array", "minItems": 1, "maxItems": RawPlanMaxSteps,
					"items":       map[string]any{"type": "string"},
					"description": "The commands, in the order they run.",
				},
			},
			"required":             []string{"commands"},
			"additionalProperties": false,
		},
		Access: AccessWrite,
	}}
}

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
		// MikroMCP parity, 2026-09-21: the RouterOS version upgrade the Packages
		// page already offers, and a bare reboot, the operator's choice.
		{Key: "routeros_upgrade_and_reboot", Page: "packages", TypedName: true,
			Summary: "Download and install the RouterOS update the router has found (see list_packages " +
				"for the installed and latest version). THIS REBOOTS THE ROUTER."},
		{Key: "reboot", Page: "packages", TypedName: true,
			Summary: "Reboot the router. It is unreachable for a minute or two."},
		// A SCRIPT RUNS CODE: behind codeGate on the Scripts page, and for the
		// assistant also the raw-command gate with the name typed back, as
		// changing a script's source is (the operator's choice, 2026-09-18).
		{Key: "script_run", Page: "scripts", Target: "script", TypedName: true,
			Summary: "Run one of the router's own scripts, by name, as the Scripts page's Run does. " +
				"Only when raw commands are enabled for the assistant and a global administrator approves."},
		{Key: "fetch_url", Page: "files", Target: "url",
			Summary: "Have the router download one file from an http:// or https:// address into its own " +
				"storage. It is saved under the address's file name; *.auto.* files and packages are refused."},
		// THE ONE TARGET THAT IS NOT A NAME: a date and time, validated strictly
		// by the server, which RouterOS itself does not do for the time.
		{Key: "clock_set", Page: "clock", Target: "date and time",
			Summary: "Set the router's date and time by hand, in its own time zone (list_clock shows the " +
				"current ones): target \"YYYY-MM-DD HH:MM:SS\". If NTP is enabled it will correct the clock " +
				"at its next sync, so for a lasting fix check the NTP client instead."},
		{Key: "certificate_sign", Page: "certificates", Target: "certificate",
			Summary: "Self-sign a certificate that has not been signed yet (create it first with change_row), " +
				"generating its key on the router."},
		// THE CONFIG IS A CREDENTIAL (the peer's private key), so it is never
		// returned to the model: approving this opens the WireGuard page's own
		// configuration dialog in the operator's browser, fetched and audited as
		// that dialog always is.
		{Key: "wireguard_show_config", Page: "wireguard", Target: "peer",
			Summary: "Show a WireGuard peer's client configuration (QR code and .conf) to the operator, " +
				"in their browser. Pass the peer's name or public key. You are never given the " +
				"configuration: it holds the peer's private key."},
		// A DIAGNOSTIC, and an action rather than a read tool because it loads
		// the router's CPU while it runs: the operator decided torch needs write
		// access to Tools, and so it is always proposed. The duration is fixed.
		{Key: "torch", Page: "tools", Target: "interface",
			Summary: "Watch one interface's traffic for 5 seconds with /tool/torch and report the " +
				"busiest flows by protocol, address and port, with their average rates. It loads " +
				"the router's CPU while it runs."},
		// CONTAINERS, the operator's choice: the Containers page's row actions and
		// its delete, by container NAME. Starting one runs its image; changing
		// what an image or command is stays behind codeGate on change_row.
		{Key: "container_start", Page: "containers", Target: "container",
			Summary: "Start a stopped container."},
		{Key: "container_stop", Page: "containers", Target: "container",
			Summary: "Stop a running container."},
		{Key: "container_remove", Page: "containers", Target: "container",
			Summary: "Remove a container. Its image layers go with it; its mounts and env lists stay."},
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
