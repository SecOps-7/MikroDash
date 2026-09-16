// Package aitools is the catalogue of tools an assistant may call, generated
// from the resource registry rather than written by hand.
//
// ── WHY GENERATED, AND NOT A LIST ───────────────────────────────────────────
//
// `internal/resource` already declares every menu this app reads and writes,
// with its fields, their types and the page whose permission owns it. A
// hand-written tool list beside that would be a second copy of the same facts,
// and `resource.All()`'s own comment records what that costs: a guard test
// enumerated sixteen resources against a registry of twenty, and the four it
// missed went unchecked for as long as the list had been typed.
//
// So the catalogue is derived. A resource added for a page becomes a tool the
// assistant can call, and `cmd/toolgen` plus the ledger in internal/verify make
// a resource with no tool, or a tool naming no resource, a build failure.
//
// ── READ ONLY, AND THE BOUNDARY IS STRUCTURAL ───────────────────────────────
//
// Every tool here is a LIST. There is no create, no set, no remove, and no
// `Action` — `resource.Action` carries a `Verb` that becomes a RouterOS command
// under the resource's menu, which is a mutation by construction and is excluded
// by name rather than by convention.
//
// That is not the whole safety argument, it is the first half. The second is
// that nothing in this package executes anything: it describes tools and
// resolves them, and the caller does the reading, so the permission check cannot
// be bypassed by a tool that decided to be helpful.
//
// ── THE MODEL CHOOSES WHEN TO READ, WHICH IS NEW HERE ───────────────────────
//
// Every other read in this app is demand-driven: a collector runs because a page
// or a card is open, and `roscache` coalesces what several of them ask for. A
// tool loop inverts that — the model picks the menu and the moment — and the
// bottleneck this app is organised around is concurrent API channels on the
// router, not CPU here. The caller therefore caps the loop; this package keeps
// the surface small so there is less for a confused model to spend it on.
package aitools

import (
	"fmt"
	"sort"
	"strings"

	"mikrodash/internal/resource"
)

// namePrefix is what every tool is called.
//
// ONE VERB, and the tools are distinguished only by what follows it. A model
// that has learned `list_x` can guess `list_y`, and a catalogue where some tools
// are `get_` and others `show_` invites a call to a name that does not exist.
const namePrefix = "list_"

// Tool is one callable, in the shape the OpenAI wire format expects under
// `tools[].function`.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Parameters is a JSON Schema object. Every tool here takes NO arguments:
	// the resource decides the menu, and a model that could pass a menu could
	// pass one this app never declared.
	Parameters map[string]any `json:"parameters"`

	// Resource is the registry key this tool reads. Not sent to the model —
	// the caller resolves the tool back to a resource with it.
	Resource string `json:"-"`
	// Page is the permission that owns the data, checked before the read.
	Page string `json:"-"`
}

// noArgs is the schema every tool carries.
//
// ── AN EMPTY OBJECT, NOT AN ABSENT ONE ──────────────────────────────────────
//
// Some endpoints reject a function whose `parameters` is missing, and some
// models invent arguments for one that is merely `{}` with no `properties`.
// Declaring an explicit object with no properties and `additionalProperties`
// false is the form that both accept and that says what is true: this tool takes
// nothing.
func noArgs() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

// All is the catalogue, ordered by name.
//
// Ordered because it is generated into a file and sent on every request: an
// unstable order would produce a diff on every regeneration and would defeat any
// prompt caching the endpoint does.
func All() []Tool {
	rs := resource.All()
	out := make([]Tool, 0, len(rs))
	for _, r := range rs {
		out = append(out, Tool{
			Name:        namePrefix + r.Key,
			Description: describe(r),
			Parameters:  noArgs(),
			Resource:    r.Key,
			Page:        r.Page,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// describe is what the model reads to decide whether to call this tool.
//
// ── IT NAMES THE MENU, DELIBERATELY ─────────────────────────────────────────
//
// An operator asking "what is in my firewall" does not say "fwFilter", and a
// model choosing between thirty tools needs something to match on. The RouterOS
// path is the one vocabulary shared between the question, the documentation and
// the answer — so it goes in the description, where it costs a few tokens and
// saves a wrong call.
//
// The FIELD NAMES go in too. Without them a model asking for firewall rules does
// not know whether it will get back a chain, an action or a comment, and tends
// to call two tools to find out.
func describe(r *resource.Resource) string {
	var b strings.Builder
	label := r.Label
	if label == "" {
		label = r.Key
	}
	fmt.Fprintf(&b, "List the router's %s rows, read from %s.", label, r.Menu)

	names := make([]string, 0, len(r.Fields))
	for _, f := range r.Fields {
		// A secret is never returned — `RowValues` drops it — so advertising it
		// would describe a field the answer cannot contain.
		if f.Type == resource.TypeSecret {
			continue
		}
		names = append(names, f.Name)
	}
	if len(names) > 0 {
		fmt.Fprintf(&b, " Each row carries: %s.", strings.Join(names, ", "))
	}
	// SAID OUT LOUD, because a model that believes it can write will propose a
	// call that does not exist and then explain what it did.
	b.WriteString(" Read only: this cannot change anything.")
	return b.String()
}

// ByName resolves a tool the model asked for.
//
// AN UNKNOWN NAME RETURNS FALSE and the caller must refuse. A model inventing a
// plausible tool name is ordinary behaviour rather than an attack, and the
// answer is the same either way: this app runs what it declared, and nothing
// else.
func ByName(name string) (Tool, bool) {
	for _, t := range All() {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Permitted filters the catalogue to what this viewer may read.
//
// ── THE CATALOGUE IS FILTERED, NOT JUST THE EXECUTION ───────────────────────
//
// Refusing at call time would be safe and would still be wrong: the model would
// see a tool for the Firewall page, call it, be refused, and tell the operator
// that MikroDash would not let it read their firewall — which reads as a fault
// rather than as a permission. A viewer denied a page is never told the tool
// exists.
//
// The caller re-checks before reading anyway. This decides what is ADVERTISED;
// that decides what happens.
func Permitted(can func(page string) bool) []Tool {
	out := []Tool{}
	for _, t := range All() {
		if t.Page != "" && !can(t.Page) {
			continue
		}
		out = append(out, t)
	}
	return out
}
