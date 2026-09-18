package server

import (
	"strings"
	"testing"

	"mikrodash/internal/resource"
)

// ── CODE IS HELD TO THE RAW-COMMAND GATE (the operator's choice, 2026-09-18) ──
//
// A script's source, its policy and its permission switch, and running it, are
// running commands by another route. codeDecision refuses them to anyone but a
// signed-in global administrator; change_row additionally needs the raw gate
// and a typed confirmation, which namesCode triggers.

func TestCodeChangesNeedAGlobalAdmin(t *testing.T) {
	stored := map[string]string{".id": "*1", "name": "backup", "source": ":log info x", "policy": "read", "comment": ""}
	for name, c := range map[string]struct {
		action string
		values map[string]string
		before map[string]string
	}{
		"changing the source":    {"update", map[string]string{"source": ":log info y"}, stored},
		"widening the policy":    {"update", map[string]string{"policy": "read,write,policy"}, stored},
		"a new script with code": {"create", map[string]string{"name": "n", "source": "/system reboot"}, nil},
		"running a script":       {"run", nil, stored},
	} {
		if v := codeDecision(resource.Script, c.action, c.values, c.before, false); !v.Refused() || v.Code != "code-requires-admin" {
			t.Errorf("%s without admin: %+v", name, v)
		}
		if v := codeDecision(resource.Script, c.action, c.values, c.before, true); v.Level != "none" {
			t.Errorf("%s as a global admin was refused: %+v", name, v)
		}
	}
	// CONTROLS: what does not change code is an ordinary write for anyone.
	for name, c := range map[string]struct {
		action string
		values map[string]string
	}{
		"a rename":                  {"update", map[string]string{"name": "backup-nightly"}},
		"a comment":                 {"update", map[string]string{"comment": "nightly"}},
		"the same source, resent":   {"update", map[string]string{"source": ":log info x"}},
		"a delete":                  {"delete", nil},
		"a new script with no code": {"create", map[string]string{"name": "empty"}},
	} {
		before := stored
		if c.action == "create" {
			before = nil
		}
		if v := codeDecision(resource.Script, c.action, c.values, before, false); v.Level != "none" {
			t.Errorf("%s was held to the code gate: %+v", name, v)
		}
	}
}

// change_row's trigger is wider than CodeChange, deliberately: the stored row
// is not read yet when the proposal is decided.
func TestNamingACodeFieldTriggersTheRawGate(t *testing.T) {
	if !namesCode(resource.Script, map[string]any{"source": "x"}) || !namesCode(resource.Script, map[string]any{"policy": "read"}) {
		t.Error("a change_row naming source or policy did not trigger the raw-command gate")
	}
	if namesCode(resource.Script, map[string]any{"name": "x", "comment": "y"}) {
		t.Error("a rename and a comment triggered the raw-command gate")
	}
	// And the tool itself refuses before permission for a viewer without the
	// raw gate: a bare connection has no global admin behind it.
	cn := &conn{}
	got := cn.runAIWriteTool(writeCall(`{"resource":"script","id":"*1","values":{"source":"/system reboot"}}`))
	if !strings.Contains(got, "permission") && !strings.Contains(got, "Raw RouterOS commands are not available") {
		t.Errorf("a code write by nobody produced %q", got)
	}
}

// Every Code field is a field the resource declares a guard for: a Code field
// on a resource without codeGate would be written unchecked.
func TestEveryCodeFieldIsGuarded(t *testing.T) {
	found := 0
	for _, res := range resource.All() {
		key := res.Key
		hasCode := false
		for _, f := range res.Fields {
			hasCode = hasCode || f.Code
		}
		for _, a := range res.Actions {
			hasCode = hasCode || a.RunsCode
		}
		if !hasCode {
			continue
		}
		found++
		guarded := false
		for _, g := range res.Guard {
			guarded = guarded || g == "codeGate"
		}
		if !guarded {
			t.Errorf("%s has code and does not declare codeGate, so its code is written unchecked", key)
		}
	}
	if found == 0 {
		t.Fatal("no resource has a Code field, so this check asks nothing")
	}
}

// THE SCHEDULER: on-event and policy are code; timing and enabling are not,
// because the operator's choice named "intervals, enable and disable" as working
// as normal.
func TestSchedulerCodeIsGatedAndTimingIsNot(t *testing.T) {
	stored := map[string]string{".id": "*0", "name": "nightly", "on-event": ":log info x", "interval": "1d",
		"policy": "read", "disabled": "true"}
	for name, values := range map[string]map[string]string{
		"new on-event": {"onEvent": "/system reboot"},
		"wider policy": {"policy": "read,write,policy"},
	} {
		if v := codeDecision(resource.Scheduler, "update", values, stored, false); !v.Refused() {
			t.Errorf("%s without admin: %+v", name, v)
		}
	}
	// A new task with code is gated even though nothing existed before.
	if v := codeDecision(resource.Scheduler, "create", map[string]string{"name": "n", "onEvent": ":log info y"}, nil, false); !v.Refused() {
		t.Errorf("a new task with an on-event, without admin: %+v", v)
	}
	for name, values := range map[string]map[string]string{
		"a new interval": {"interval": "1h"},
		"enabling it":    {"disabled": "false"},
		"a start time":   {"startTime": "04:00:00"},
		"a rename":       {"name": "nightly-2"},
	} {
		if v := codeDecision(resource.Scheduler, "update", values, stored, false); v.Level != "none" {
			t.Errorf("%s was held to the code gate: %+v", name, v)
		}
	}
}
