package guard

// Would this routing-table change stop routing rules that look routes up in it?
//
// RouterOS does not refuse any of these. Measured on the CHR (7.24.1) with a rule
// `action=lookup-only-in-table table=T`: removing T leaves the rule pointing at a
// dangling `*id` and inactive; disabling T makes the rule inactive; unsetting
// `fib` leaves the rule active over a table with nothing installed for
// forwarding. Renaming T is harmless — the rule follows the table by id — so a
// rename is not warned about.
//
// WARN, NEVER REFUSE, like rulePath: breaking a rule is recoverable from the
// Routing Rules page, and taking a table out of service on purpose is a normal
// thing to do.
import (
	"encoding/json"
	"sort"
	"strings"
)

// TableState is one side of a routing-table write: whether the table exists,
// and the two properties that take it out of service.
type TableState struct {
	Present  bool
	Disabled bool
	FIB      bool
}

// TableRule is a routing rule as this guard reads it. `Table` is what the router
// printed, which is the table's name, or its `*id` once the table has no FIB.
type TableRule struct {
	ID       string
	Table    string
	Disabled bool
}

// CheckTableChange warns when a table that enabled rules name is removed,
// disabled, or loses its FIB. `name` and `id` identify the table as it was.
func CheckTableChange(action, name, id string, before, after TableState, rules []TableRule) Verdict {
	change := ""
	switch {
	case !before.Present:
		return Verdict{Level: "none"}
	case action == "delete" || !after.Present:
		change = "remove"
	case !before.Disabled && after.Disabled:
		change = "disable"
	case before.FIB && !after.FIB:
		change = "fib"
	default:
		return Verdict{Level: "none"}
	}
	var using []string
	for _, r := range rules {
		t := strings.TrimSpace(r.Table)
		if r.Disabled || t == "" {
			continue
		}
		if t == name || (id != "" && t == id) {
			using = append(using, r.ID)
		}
	}
	if len(using) == 0 {
		return Verdict{Level: "none"}
	}
	sort.Strings(using)
	fp, _ := json.Marshal([]any{"table-in-use", change, name, using})
	return Verdict{Level: "warn", Code: "table-in-use", Fingerprint: string(fp),
		Detail: map[string]any{"table": name, "change": change, "rules": len(using)}}
}
