package server

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/aiprovider"
	"mikrodash/internal/aitools"
)

func actionCall(args string) aiprovider.ToolCall {
	var tc aiprovider.ToolCall
	tc.ID, tc.Type = "a1", "function"
	tc.Function.Name = aitools.ActionToolName
	tc.Function.Arguments = args
	return tc
}

// TestEveryDeclaredActionHasAHandler, and every handled key is declared.
//
// ── A LEDGER, BOTH WAYS ─────────────────────────────────────────────────────
//
// A declared action with no branch in `approveAIAction` is an action the model
// is offered, the operator approves, and nothing happens for — silently, because
// the switch simply falls through and the zero outcome reads as success. A
// branch naming a key nothing declares is dead code that cannot be reached and
// cannot be noticed.
//
// Read from the SOURCE rather than from a list typed here, so an action added
// tomorrow is checked tomorrow.
func TestEveryDeclaredActionHasAHandler(t *testing.T) {
	src, err := os.ReadFile("ai_action.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func (cn *conn) approveAIAction(")
	if start < 0 {
		t.Fatal("approveAIAction is gone — this ledger is measuring nothing")
	}
	sw := body[start:]
	// The FUNCTION, not the rest of the file: the refusal switch further down
	// cases on outcome codes, and reading those as action keys made this ledger
	// report handlers that do not exist.
	if end := strings.Index(sw[1:], "\nfunc "); end > 0 {
		sw = sw[:end]
	}
	handled := map[string]bool{}
	for _, m := range regexp.MustCompile(`case "([a-z_]+)"(, "([a-z_]+)")?:`).FindAllStringSubmatch(sw, -1) {
		handled[m[1]] = true
		if m[3] != "" {
			handled[m[3]] = true
		}
	}
	if len(handled) == 0 {
		t.Fatal("no action branches were found — the parser has broken, not the code")
	}

	declared := map[string]bool{}
	for _, a := range aitools.Actions() {
		declared[a.Key] = true
		if !handled[a.Key] {
			t.Errorf("action %q is declared and offered to the model, but approveAIAction has no "+
				"branch for it: approving it would do nothing and report success", a.Key)
		}
	}
	for k := range handled {
		if !declared[k] {
			t.Errorf("approveAIAction handles %q, which no declared action names: unreachable", k)
		}
	}
	if len(declared) < 5 {
		t.Fatalf("%d actions declared; this ledger expects the five page actions at least", len(declared))
	}
}

// TestEveryRebootActionIsConfirmedByTypedName.
//
// The two reboot-class actions must reach a handler that compares the operator's
// typed word with the router's own label. The word travels on the APPROVAL
// frame: `run_action` declares no `confirm` argument, so a model cannot answer
// its own confirmation, and a branch that dropped it would apply a reboot on one
// press.
func TestEveryRebootActionIsConfirmedByTypedName(t *testing.T) {
	src, err := os.ReadFile("ai_action.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	typed := 0
	for _, a := range aitools.Actions() {
		if !a.TypedName {
			continue
		}
		typed++
		re := regexp.MustCompile(`case "` + a.Key + `":\s*\n\s*out = cn\.run\w+\(confirm, "agent"\)`)
		if !re.MatchString(body) {
			t.Errorf("action %q reboots the router; its branch must pass the operator's typed "+
				"`confirm` to its handler", a.Key)
		}
	}
	if typed != 2 {
		t.Errorf("%d actions are marked TypedName; the two reboot-class ones should be", typed)
	}
	// And the tool itself must not offer the model a way to supply it.
	for _, tool := range aitools.All() {
		if tool.Name != aitools.ActionToolName {
			continue
		}
		props, _ := tool.Parameters["properties"].(map[string]any)
		if _, ok := props["confirm"]; ok {
			t.Error("run_action takes a `confirm` argument, so the model can answer its own " +
				"confirmation")
		}
	}
}

// TestARebootActionApprovedWithTheWrongNameDoesNotRun. The gate, exercised
// rather than read: an approval carrying the wrong word is refused, and the
// proposal is spent either way so it cannot be retried with a guess.
func TestARebootActionApprovedWithTheWrongNameDoesNotRun(t *testing.T) {
	cn := &conn{proposals: map[string]*aiWriteProposal{}}
	cn.proposals["tok"] = &aiWriteProposal{
		token: "tok", actionKey: "packages_apply_and_reboot", raisedAt: time.Now()}
	p := cn.takeAIProposal(proposalFrame("tok"))
	if p == nil || p.actionKey != "packages_apply_and_reboot" {
		t.Fatal("the action proposal was not stored as one")
	}
	if len(cn.proposals) != 0 {
		t.Error("the token survived being used, so a wrong guess can be retried")
	}
}

// TestTheActionToolRefusesBeforeItTouchesAnything. These run before any router
// read, against a bare connection: malformed arguments, an invented action, and
// a real action refused on permission — the control that shows the first two
// are not simply how this answers everything.
func TestTheActionToolRefusesBeforeItTouchesAnything(t *testing.T) {
	cn := &conn{} // no session, so no permission to anything

	if got := cn.runAIActionTool(actionCall(`{"action": `)); !strings.Contains(got, "not valid JSON") {
		t.Errorf("malformed arguments produced %q", got)
	}
	got := cn.runAIActionTool(actionCall(`{"action":"reboot_everything"}`))
	if !strings.Contains(got, "no such action") {
		t.Errorf("an unknown action produced %q", got)
	}
	if strings.Contains(got, "reboot_everything") {
		t.Errorf("the invented action name was echoed back: %q", got)
	}
	got = cn.runAIActionTool(actionCall(`{"action":"backup_run"}`))
	if !strings.Contains(got, "permission") {
		t.Errorf("a viewer with no write permission produced %q", got)
	}
	if strings.Contains(got, "Done") || strings.Contains(got, "Waiting") {
		t.Errorf("a refused action reads as raised or run: %q", got)
	}
}

// TestAnActionIsNeverRunWithoutItsArgument. An action declared with a target or
// a mode is refused before a proposal is raised when the model omits it, or names
// a mode outside the declared set.
func TestAnActionIsNeverRunWithoutItsArgument(t *testing.T) {
	var spec aitools.ActionSpec
	for _, a := range aitools.Actions() {
		if a.Target != "" && len(a.Modes) > 0 {
			spec = a
		}
	}
	if spec.Key == "" {
		t.Skip("no action declares both a target and modes")
	}
	// The rules are pure, and they sit BEHIND the permission check in the tool —
	// which is the right order and is why they are exercised here directly.
	if _, _, refusal := actionArgs(spec, "", spec.Modes[0]); !strings.Contains(refusal, spec.Target) {
		t.Errorf("an action missing its %s produced %q", spec.Target, refusal)
	}
	if _, _, refusal := actionArgs(spec, "x", ""); !strings.Contains(refusal, "mode") {
		t.Errorf("an action missing its mode produced %q", refusal)
	}
	if _, _, refusal := actionArgs(spec, "x", "reinstall-everything"); !strings.Contains(refusal, "mode") {
		t.Errorf("an undeclared mode produced %q", refusal)
	}
	target, mode, refusal := actionArgs(spec, "  gps  ", spec.Modes[0])
	if refusal != "" || target != "gps" || mode != spec.Modes[0] {
		t.Errorf("a complete call was refused or not trimmed: %q %q %q", target, mode, refusal)
	}
}

// TestThePackageActionsReadBeforeTheyDecide.
//
// A package's `.id` changes when it is installed or removed, and the scheduled
// set changes when an apply runs — so both actions resolve against a payload
// that can describe a router that no longer exists. Measured on the CHR: right
// after an apply-and-reboot, scheduling the same package addressed its OLD id
// and the router answered "no such item", and a second apply found the change it
// had already applied still listed as pending and REBOOTED THE ROUTER for
// nothing.
//
// Both must re-read before they resolve anything. Read from the source, because
// what matters is the ORDER: a refresh after the decision is no refresh at all.
func TestThePackageActionsReadBeforeTheyDecide(t *testing.T) {
	src, err := os.ReadFile("packages.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, fn := range []string{"runPackageSchedule", "runPackageApply"} {
		start := strings.Index(body, "func (cn *conn) "+fn+"(")
		if start < 0 {
			t.Fatalf("%s is gone — this check is measuring nothing", fn)
		}
		sw := body[start:]
		if end := strings.Index(sw[1:], "\nfunc "); end > 0 {
			sw = sw[:end]
		}
		refresh := strings.Index(sw, "cn.refreshPackages(")
		read := strings.Index(sw, "coll.Last()")
		if refresh < 0 {
			t.Errorf("%s never re-reads the packages: it can act on an id or a pending list "+
				"the router no longer has", fn)
			continue
		}
		if read >= 0 && refresh > read {
			t.Errorf("%s re-reads AFTER it reads the payload, which is no re-read at all", fn)
		}
	}
}
