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
// ── ONE TOOL WRITES, AND IT IS NAMED ────────────────────────────────────────
//
// Every tool here is a LIST except exactly one, `change_row`. There is still no
// `Action` — `resource.Action` carries a `Verb` that becomes a RouterOS command
// under the resource's menu, so an action is a command the model would be
// choosing, and it is excluded by name rather than by convention.
//
// `change_row` proposes a change to one row of one declared resource. It cannot
// name a menu, cannot send a command, and reaches the router only through the
// same pipeline a human form does — permission, rate limit, fresh read,
// staleness, guards, read-back, history and audit. What the model supplies is a
// resource key this registry declares and a set of field values the validator
// checks.
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
	//
	// EMPTY ON THE WRITE TOOL, which is not bound to one menu: the model names
	// the resource in its arguments, and the caller resolves it there.
	Resource string `json:"-"`
	// Page is the permission that owns the data, checked before the read.
	//
	// EMPTY ON THE WRITE TOOL, for the same reason. It is not ungated: the
	// resources it will accept are filtered per viewer before it is advertised,
	// and the page is checked again per call against whichever one is named.
	Page string `json:"-"`
	// Collector is the live collector a LIVE tool reads, instead of a resource's
	// menu. Not sent to the model. Empty on every resource tool and on the write
	// tool; see liveTools.
	Collector string `json:"-"`
	// Freshness is how old a LIVE tool's reading may be before it is re-read:
	// FreshLive or FreshMetadata. Empty on resource tools, which always read
	// the menu fresh.
	Freshness string `json:"-"`
	// Access is "read" or "write". It decides which permission `Permitted`
	// consults, and it is what a gate checks rather than inferring intent from
	// a tool's name.
	Access string `json:"-"`
}

// Freshness bounds for live tools, spelled once.
//
// ── TWO, BECAUSE A RE-READ COSTS DIFFERENT AMOUNTS ───────────────────────────
//
// LIVE data (rates, sessions, clients) is worth a re-read after a few seconds:
// the question is "what is it doing now", and the collector usually already has
// a reading that young because the page is open. METADATA (packages, users,
// neighbours) changes when somebody edits the router; re-reading it on every
// question spends router commands to learn what was already known, so it is
// refreshed only once the collector's own staleness rule calls it old.
const (
	FreshLive     = "live"
	FreshMetadata = "metadata"
)

// Access levels, spelled once.
const (
	AccessRead  = "read"
	AccessWrite = "write"
)

// WriteToolName is the ONE tool that changes anything.
//
// ── ONE, NOT ONE PER RESOURCE ───────────────────────────────────────────────
//
// A `write_<resource>` beside every `list_<resource>` would double the
// catalogue, and every one of those descriptions is sent on every request. The
// model already learns a resource's field names from its list tool, so a second
// per-resource tool would mostly repeat them.
//
// It is also the honest shape for what this does: the model is not calling
// thirty different writers, it is proposing one change to one row, and the
// resource is an argument to that.
const WriteToolName = "change_row"

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
	out := make([]Tool, 0, len(rs)+1)
	keys := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, listTool(r))
		keys = append(keys, r.Key)
	}
	out = append(out, liveTools()...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sort.Strings(keys)
	// LAST, and after the sort, so the read catalogue keeps its stable order and
	// the one tool that changes anything is not buried alphabetically among
	// thirty that cannot.
	return append(out, writeTool(keys))
}

// liveTools read what a COLLECTOR measures rather than what a menu holds.
//
// ── WHY A SECOND KIND OF TOOL ───────────────────────────────────────────────
//
// Every resource tool reads configuration: the rows of a menu, which is what
// the registry declares. Some questions are about what the router is DOING, and
// that is not a row in any menu this registry can list. Asked for per-interface
// throughput, the assistant had only `list_iface` (name, comment, disabled) and
// a summary naming the busiest few, and said so, correctly.
//
// The data was already in memory: the interface collector carries every
// interface's live rate, read from `/interface/monitor-traffic`, for the
// Interfaces page. A live tool hands that payload over, re-read only when it is
// more than a few seconds old, so a router whose Interfaces page is open pays
// nothing extra for the question.
//
// DECLARED, NOT GENERATED. Each collector's payload has its own shape and needs
// its own rendering, so there is no registry to derive these from. A ledger in
// internal/server holds every entry here to a reader there, in both directions.
func liveTools() []Tool {
	return []Tool{{
		Name: namePrefix + "interface_traffic",
		Description: "Read only. List EVERY interface with its live throughput, read from " +
			"/interface/monitor-traffic: rxMbps and txMbps now, plus running and disabled " +
			"state, type, comment, addresses, cumulative rx/tx bytes, and errors and drops " +
			"since the previous reading. Use it for any question about how much traffic an " +
			"interface, WAN or VLAN is carrying, or which one is busiest. The result says " +
			"how old the reading is; rates are refreshed if they are more than a few " +
			"seconds old.",
		Parameters: noArgs(),
		Collector:  "ifStatus",
		Freshness:  FreshLive,
		Page:       "interfaces",
		Access:     AccessRead,
	}}
}

// listTool is one resource's read tool.
func listTool(r *resource.Resource) Tool {
	return Tool{
		Name:        namePrefix + r.Key,
		Description: describe(r),
		Parameters:  noArgs(),
		Resource:    r.Key,
		Page:        r.Page,
		Access:      AccessRead,
	}
}

// writeTool builds the write tool over exactly the resources the caller says
// this viewer may change.
//
// ── THE ENUM IS THE PERMISSION, MADE VISIBLE ────────────────────────────────
//
// A viewer who may write three pages is offered a tool that accepts three
// resource names. The model is never told the others exist, so it does not
// propose a change it would only be refused for — which matters because a
// refusal reads to an operator as MikroDash being broken rather than as a
// permission they do not have.
//
// The caller re-checks the named resource's page before anything is written.
// This decides what is OFFERED; that decides what happens.
func writeTool(resources []string) Tool {
	return Tool{
		Name: WriteToolName,
		Description: "Make a change to ONE row on the router the operator has selected: " +
			"create it, edit it, or delete it. " +
			"Set `resource` to one of the listed names. Call that resource's list_ tool first " +
			"to see its field names, current rows and their ids. " +
			"To CREATE, omit `id` and give `values`. To EDIT, pass the row's `id` and the " +
			"`values` to change. To DELETE, pass the row's `id` and `delete: true`. " +
			"The change goes through MikroDash's own checks, audit trail and undo history. " +
			"The result says what happened: applied, or waiting for the operator to confirm " +
			"it, which is always the case for a delete and for a change that could cut " +
			"MikroDash off from the router. Never say a change was applied unless the result " +
			"says so.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"resource": map[string]any{
					"type": "string", "enum": resources,
					"description": "Which kind of row to change.",
				},
				"id": map[string]any{
					"type": "string",
					"description": "The RouterOS id of the row to edit, exactly as a list_ " +
						"tool reported it. Omit it to create a new row.",
				},
				"values": map[string]any{
					"type": "object",
					"description": "Field name to value, for a create or an edit. Use the field " +
						"names the resource's list_ tool describes, not RouterOS property names.",
				},
				"delete": map[string]any{
					"type":        "boolean",
					"description": "Set true, with `id`, to delete that row.",
				},
			},
			"required":             []string{"resource"},
			"additionalProperties": false,
		},
		Access: AccessWrite,
	}
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
func Permitted(can func(page, access string) bool) []Tool {
	out := []Tool{}
	writable := []string{}
	for _, r := range resource.All() {
		if can(r.Page, AccessRead) {
			out = append(out, listTool(r))
		}
		if can(r.Page, AccessWrite) {
			writable = append(writable, r.Key)
		}
	}
	for _, lt := range liveTools() {
		if can(lt.Page, AccessRead) {
			out = append(out, lt)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sort.Strings(writable)
	// NOT ADVERTISED AT ALL to a viewer who may change nothing. Offering a tool
	// whose every call would be refused teaches the model that writing is
	// something this app does badly, rather than something this person may not
	// do.
	if len(writable) > 0 {
		out = append(out, writeTool(writable))
	}
	return out
}
