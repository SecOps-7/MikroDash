package aitools

import (
	"mikrodash/internal/areas"
	"regexp"
	"sort"
	"strings"
	"testing"

	"mikrodash/internal/resource"
)

// What a model is told exists.
//
// The ledger in `internal/verify` compares the generated artefact against the
// registry. That is the record. This is the behaviour behind it: the filtering
// that decides what one viewer is offered, and the descriptions that decide what
// the model reaches for.

func allowAll(string, string) bool { return true }
func denyAll(string, string) bool  { return false }

// readOnly is the common case and the one worth naming: a viewer who may look at
// everything and change nothing.
func readOnly(_, access string) bool { return access == AccessRead }

// TestPermittedHidesAToolWhosePageIsDenied, and shows it when the page is not.
//
// BOTH DIRECTIONS, because a filter that hides everything passes the first half
// perfectly. A viewer denied the Firewall page must never be told a firewall tool
// exists — refusing at call time would be safe and would still be wrong: the
// model would report that MikroDash would not let it read their firewall, which
// reads as a fault rather than as a permission.
func TestPermittedHidesAToolWhosePageIsDenied(t *testing.T) {
	all := All()
	if len(all) < 10 {
		t.Fatalf("only %d tools — the registry is not being read", len(all))
	}
	if got := Permitted(allowAll); len(got) != len(all) {
		t.Errorf("a viewer allowed everything was offered %d of %d tools", len(got), len(all))
	}

	for _, tool := range Permitted(denyAll) {
		// Nothing survives a blanket denial. The write tool is not advertised
		// either, because its resource enum is built from write permissions.
		t.Errorf("tool %q survived a blanket denial", tool.Name)
	}

	// And one page at a time, which is the real case.
	target := all[0].Page
	if target == "" {
		t.Fatal("the first tool has no owning page")
	}
	one := Permitted(func(p, _ string) bool { return p != target })
	for _, tool := range one {
		if tool.Page == target {
			t.Errorf("tool %q was offered though page %q is denied", tool.Name, target)
		}
	}
	if len(one) == len(all) {
		t.Errorf("denying page %q removed nothing", target)
	}
}

// TestAViewerWhoMayChangeNothingIsNotOfferedTheWriteTool.
//
// Offering a tool whose every call would be refused teaches the model that
// writing is something this app does badly, rather than something this person
// may not do — and an operator reads that as a fault.
func TestAViewerWhoMayChangeNothingIsNotOfferedTheWriteTool(t *testing.T) {
	var sawWrite bool
	reads := 0
	for _, tool := range Permitted(readOnly) {
		if tool.Access == AccessWrite {
			sawWrite = true
		}
		if tool.Access == AccessRead {
			reads++
		}
	}
	if sawWrite {
		t.Error("a read-only viewer was offered the write tool")
	}
	// THE CONTROL. Without it a filter that returned nothing at all would pass
	// the assertion above and prove nothing.
	if reads == 0 {
		t.Error("a read-only viewer was offered no read tools either, so this proved nothing")
	}
}

// TestTheWriteToolOffersOnlyTheResourcesThisViewerMayChange.
//
// The enum IS the permission, made visible. A resource that reached it without
// a write grant would be a change the model proposes, the operator is asked to
// approve, and the server then refuses.
func TestTheWriteToolOffersOnlyTheResourcesThisViewerMayChange(t *testing.T) {
	// One page, chosen from the registry rather than named here.
	target := resource.All()[0].Page
	only := func(p, access string) bool {
		if access == AccessWrite {
			return p == target
		}
		return true
	}
	// BY NAME since plan_changes (2026-09-21): there are several write tools,
	// and the plan's step enum must be the same list as change_row's.
	var write, plan *Tool
	for _, tool := range Permitted(only) {
		cp := tool
		switch tool.Name {
		case WriteToolName:
			write = &cp
		case PlanToolName:
			plan = &cp
		}
	}
	if plan == nil {
		t.Fatal("plan_changes was not offered beside change_row")
	}
	stepProps, _ := plan.Parameters["properties"].(map[string]any)["steps"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	planEnum, _ := stepProps["resource"].(map[string]any)["enum"].([]string)
	if write == nil {
		t.Fatalf("no write tool was offered to a viewer who may write page %q", target)
	}
	props, _ := write.Parameters["properties"].(map[string]any)
	res, _ := props["resource"].(map[string]any)
	enum, _ := res["enum"].([]string)
	if len(enum) == 0 {
		t.Fatal("the write tool offered no resources at all")
	}
	if strings.Join(planEnum, ",") != strings.Join(enum, ",") {
		t.Errorf("plan_changes offers %v, change_row %v; a plan may change only what change_row may", planEnum, enum)
	}
	for _, key := range enum {
		r := resource.ByKey(key)
		if r == nil {
			t.Errorf("the write tool offers %q, which is not a resource", key)
			continue
		}
		if r.Page != target {
			t.Errorf("the write tool offers %q on page %q, which this viewer may not write",
				key, r.Page)
		}
	}
	if len(enum) == len(resource.All()) {
		t.Error("denying every other page narrowed the enum not at all")
	}
}

// TestPermittedReturnsAnEmptySliceRatherThanNil. It is marshalled into a request
// body, and a Go nil slice encodes as `null`, which some endpoints reject
// outright and others read as "tools are broken" rather than "there are none".
func TestPermittedReturnsAnEmptySliceRatherThanNil(t *testing.T) {
	if got := Permitted(denyAll); got == nil {
		t.Error("a fully denied viewer yields nil, which marshals to null")
	}
}

// TestByNameRefusesAnInventedName.
//
// A model inventing a plausible tool name is ordinary behaviour rather than an
// attack, and the answer is the same either way: this app runs what it declared.
func TestByNameRefusesAnInventedName(t *testing.T) {
	for _, bad := range []string{"", "list_", "list_nothingHere", "run_command",
		"LIST_DNSSTATIC", "list_dnsStatic ", "change_", "changerow"} {
		if _, ok := ByName(bad); ok {
			t.Errorf("ByName accepted %q", bad)
		}
	}
	for _, good := range []string{All()[0].Name, WriteToolName} {
		if _, ok := ByName(good); !ok {
			t.Errorf("ByName refused %q, which it generated itself", good)
		}
	}
}

// TestEveryDescriptionNamesItsMenu.
//
// The RouterOS path is the one vocabulary shared between the operator's
// question, the documentation and the answer. Without it a model choosing
// between thirty similarly-named tools matches on the key, which is this app's
// word rather than MikroTik's.
func TestEveryDescriptionNamesItsMenu(t *testing.T) {
	checked := 0
	for _, tool := range All() {
		if tool.Access != AccessRead {
			continue
		}
		checked++
		// A LIVE TOOL has no resource, and names the menu its collector reads
		// instead. Re-aimed for `list_interface_traffic`: the rule was always
		// "name the RouterOS path and say it cannot write", and that still holds.
		// A DIAGNOSTIC TOOL has no resource either, and names the command it runs.
		// Re-aimed for `ping` (slice 8) under the same rule.
		if tool.Collector != "" || tool.Diagnostic != "" {
			if !liveMenuPath.MatchString(tool.Description) || !strings.Contains(tool.Description, "Read only") {
				t.Errorf("live tool %q does not name the menu it reads or say it cannot write: %s",
					tool.Name, tool.Description)
			}
			continue
		}
		res := resource.ByKey(tool.Resource)
		if res == nil {
			t.Errorf("tool %q names no live resource", tool.Name)
			continue
		}
		if !strings.Contains(tool.Description, res.Menu) {
			t.Errorf("tool %q does not name its menu %q: %s", tool.Name, res.Menu, tool.Description)
		}
		if !strings.Contains(tool.Description, "Read only") {
			t.Errorf("tool %q does not say it cannot write; a model that believes it can "+
				"will propose a call that does not exist and then explain what it did", tool.Name)
		}
	}
	if checked == 0 {
		t.Error("no read tools were checked")
	}
}

// liveMenuPath is a RouterOS menu path as a live tool's description names it,
// such as /interface/monitor-traffic or /system/package.
var liveMenuPath = regexp.MustCompile(`(^|\s)/[a-z][a-z0-9-]*(/[a-z0-9-]+)*`)

// TestNoSecretFieldIsAdvertised.
//
// `RowValues` drops secrets, so a description listing one promises a field the
// answer cannot contain — and a model told a pre-shared key is available will
// call the tool to fetch it and then report that MikroDash returned nothing.
//
// THE FLOOR MATTERS HERE. A registry with no secret fields would pass this test
// without exercising it, and the check would then sit green through the change
// that introduced one.
func TestNoSecretFieldIsAdvertised(t *testing.T) {
	checked := 0
	for _, tool := range All() {
		res := resource.ByKey(tool.Resource)
		if res == nil {
			continue
		}
		for _, f := range res.Fields {
			if f.Type != resource.TypeSecret {
				continue
			}
			checked++
			if strings.Contains(tool.Description, f.Name) {
				t.Errorf("tool %q advertises secret field %q, which RowValues drops",
					tool.Name, f.Name)
			}
		}
	}
	if checked == 0 {
		t.Error("no resource declares a secret field, so this check proved nothing — " +
			"either the registry changed or TypeSecret is no longer how secrets are marked")
	}
}

// TestEveryReadToolTakesNoArguments.
//
// The resource decides the menu. A model that could pass one could pass a menu
// this app never declared, and the read path would be executing a string the
// model chose rather than a resource the registry owns.
//
// The write tool is the exception BY DESIGN — it takes a resource, an optional
// id and a set of values — and what bounds it is not the absence of arguments
// but the enum, the validator and the write pipeline. Its own shape is pinned
// below rather than waved through.
//
// DIAGNOSTIC TOOLS ARE THE SECOND EXCEPTION, since slice 8: a ping needs a
// target. What they take is pinned exactly by TestADiagnosticTakesOnlyItsTarget,
// and none of it names a menu or a command.
//
// A GROUPED LIST TOOL IS THE THIRD (2026-09-18): Address Lists takes `list`, a
// value to filter the declared menu by, never a menu. Pinned exactly by
// TestAGroupedListToolTakesOnlyItsGroup.
func TestEveryReadToolTakesNoArguments(t *testing.T) {
	for _, tool := range All() {
		if tool.Access != AccessRead || tool.Diagnostic != "" || tool.GroupBy != "" {
			continue
		}
		props, ok := tool.Parameters["properties"].(map[string]any)
		if !ok {
			t.Errorf("tool %q declares no properties object; some endpoints reject that "+
				"and some models invent arguments for it", tool.Name)
			continue
		}
		if len(props) != 0 {
			t.Errorf("tool %q accepts %d arguments", tool.Name, len(props))
		}
		if extra, _ := tool.Parameters["additionalProperties"].(bool); extra {
			t.Errorf("tool %q permits additional properties", tool.Name)
		}
	}
}

// TestADiagnosticTakesOnlyItsTarget pins every diagnostic tool's arguments, both
// ways: an argument added to a tool fails here until it is named, and a tool
// added without an entry fails too.
func TestADiagnosticTakesOnlyItsTarget(t *testing.T) {
	want := map[string][]string{"ping": {"address", "count"}, "traceroute": {"address", "maxHops"},
		"security_scan": {}, "read_file": {"name"},
		"export_config": {"menu"}, "list_routers": {}}
	seen := 0
	for _, tool := range All() {
		if tool.Diagnostic == "" {
			continue
		}
		seen++
		args, ok := want[tool.Name]
		if !ok {
			t.Errorf("diagnostic tool %q has no pinned argument list here", tool.Name)
			continue
		}
		props, _ := tool.Parameters["properties"].(map[string]any)
		got := make([]string, 0, len(props))
		for k := range props {
			got = append(got, k)
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(args, ",") {
			t.Errorf("tool %q takes %v, want exactly %v", tool.Name, got, args)
		}
		if extra, _ := tool.Parameters["additionalProperties"].(bool); extra {
			t.Errorf("tool %q permits additional properties", tool.Name)
		}
	}
	if seen != len(want) {
		t.Errorf("%d diagnostic tools, %d pinned here", seen, len(want))
	}
}

// TestTheWriteToolTakesExactlyResourceIdValuesAndDelete.
//
// Pinned because the argument list is the model's whole reach into the write
// path. An argument naming a menu, a command or a router id would be the model
// choosing something the registry is supposed to decide.
//
// RE-AIMED FOR `delete`, deliberately. The operator asked the assistant to delete
// a row and it said it had no delete action. `delete` is a boolean that routes
// the same `resource` and `id` to `removeRow`, the form's own delete path; it
// names nothing the registry decides, and a delete is always put to the operator.
//
// RE-AIMED AGAIN FOR `before`, deliberately. The assistant could only append, and
// in a firewall the first match decides, so an appended rule is very often one
// that never runs. `before` is the id of a ROW — the same thing the page's drag
// sends as its anchor — routing the same `resource` and `id` to `moveRow`, the
// page's own move path. It is not an index and not a menu: the registry still
// decides what may be reordered (`Movable`) and the row is resolved against the
// table as the router holds it at the moment of the move.
//
// RE-AIMED FOR `undo` (2026-09-21), deliberately. A boolean routing `resource`
// to histStep, the page's own Undo path, for the newest entry on that
// resource's stack and only when the assistant made it (ai_undo.go). It names
// no row and no value: the history decides what is undone.
func TestTheWriteToolTakesExactlyResourceIdValuesAndDelete(t *testing.T) {
	tool, ok := ByName(WriteToolName)
	if !ok {
		t.Fatalf("%q is not in the catalogue", WriteToolName)
	}
	if tool.Access != AccessWrite {
		t.Errorf("%q declares access %q", WriteToolName, tool.Access)
	}
	props, _ := tool.Parameters["properties"].(map[string]any)
	want := map[string]bool{"resource": true, "id": true, "values": true, "delete": true,
		"before": true, "undo": true}
	for name := range props {
		if !want[name] {
			t.Errorf("the write tool accepts %q, which the write path never asked for", name)
		}
		delete(want, name)
	}
	for name := range want {
		t.Errorf("the write tool is missing argument %q", name)
	}
	if extra, _ := tool.Parameters["additionalProperties"].(bool); extra {
		t.Error("the write tool permits additional properties, so a model can pass anything")
	}
}

// TestTheReadCatalogueIsOrderedAndTheWritersAreLast.
//
// The read tools are generated into a committed file and sent on every request:
// an unstable order produces a diff on every regeneration and defeats any prompt
// caching the endpoint does. The writers are appended AFTER that sort, so the
// tools that change anything are not buried alphabetically among fifty that
// cannot.
//
// RE-AIMED for `run_action` (slice 3): there are now TWO writers, `change_row`
// for rows and `run_action` for the pages' own verbs, and they are the last two
// entries in that order. RE-AIMED AGAIN for `plan_changes` (2026-09-21), which
// is change_row several times over and sits beside it.
func TestTheReadCatalogueIsOrderedAndTheWritersAreLast(t *testing.T) {
	all := All()
	writers := []string{WriteToolName, PlanToolName, ActionToolName}
	body := all[:len(all)-len(writers)]
	prev := ""
	for _, tool := range body {
		if tool.Name <= prev {
			t.Errorf("%q follows %q", tool.Name, prev)
		}
		prev = tool.Name
		if tool.Access != AccessRead {
			t.Errorf("tool %q is not a read tool but sits in the read catalogue", tool.Name)
		}
	}
	for i, want := range writers {
		got := all[len(body)+i]
		if got.Name != want {
			t.Errorf("writer %d is %q, want %q", i, got.Name, want)
		}
		if got.Access != AccessWrite {
			t.Errorf("%q is not declared a write tool", got.Name)
		}
	}
}

// TestTheInterfaceTrafficToolIsOfferedByTheInterfacesPage. The operator's report
// was that the assistant had no way to see every interface's throughput. It must
// be advertised to a viewer who may read Interfaces, and to nobody else.
func TestTheInterfaceTrafficToolIsOfferedByTheInterfacesPage(t *testing.T) {
	const name = "list_interface_traffic"
	has := func(tools []Tool) bool {
		for _, tl := range tools {
			if tl.Name == name {
				return true
			}
		}
		return false
	}
	if !has(Permitted(func(page, access string) bool { return page == "interfaces" && access == AccessRead })) {
		t.Errorf("%s is not offered to a viewer who may read Interfaces", name)
	}
	if has(Permitted(func(page, access string) bool { return page == "firewall" })) {
		t.Errorf("%s is offered to a viewer who may not read Interfaces", name)
	}
	tool, ok := ByName(name)
	if !ok || tool.Collector != "ifStatus" || tool.Access != AccessRead {
		t.Errorf("%s resolves to %+v", name, tool)
	}
}

// TestEveryLiveToolDeclaresItsFreshness. A live tool with no bound would fall
// through to whatever the reader defaults to, which is a decision nobody made.
func TestEveryLiveToolDeclaresItsFreshness(t *testing.T) {
	live := 0
	for _, tl := range All() {
		if tl.Collector == "" {
			if tl.Freshness != "" {
				t.Errorf("tool %q declares freshness %q but reads no collector", tl.Name, tl.Freshness)
			}
			continue
		}
		live++
		if tl.Freshness != FreshLive && tl.Freshness != FreshMetadata {
			t.Errorf("live tool %q declares freshness %q; want %q or %q",
				tl.Name, tl.Freshness, FreshLive, FreshMetadata)
		}
	}
	if live == 0 {
		t.Error("no live tools found; this check measured nothing")
	}
}

// TestEveryLiveToolIsOfferedOnlyByItsPage. Every live tool, present and future,
// is advertised to a viewer who may read its page and to nobody who may read
// only a different one. Written over the whole catalogue so a new live tool is
// covered without anyone remembering to add a case.
func TestEveryLiveToolIsOfferedOnlyByItsPage(t *testing.T) {
	has := func(tools []Tool, name string) bool {
		for _, tl := range tools {
			if tl.Name == name {
				return true
			}
		}
		return false
	}
	checked := 0
	for _, lt := range All() {
		if lt.Collector == "" {
			continue
		}
		checked++
		page := lt.Page
		if !has(Permitted(func(p, a string) bool { return p == page && a == AccessRead }), lt.Name) {
			t.Errorf("%s is not offered to a viewer who may read %q", lt.Name, page)
		}
		if has(Permitted(func(p, a string) bool { return p != page }), lt.Name) {
			t.Errorf("%s is offered to a viewer who may read every page except %q", lt.Name, page)
		}
	}
	if checked < 2 {
		t.Errorf("only %d live tools checked; the catalogue is not being read", checked)
	}
}

// TestAGroupedListToolTakesOnlyItsGroup, both ways: a grouped resource's tool
// takes exactly its grouping field and nothing else, and every grouped area
// table has such a tool.
func TestAGroupedListToolTakesOnlyItsGroup(t *testing.T) {
	seen := 0
	for _, tool := range All() {
		if tool.GroupBy == "" {
			continue
		}
		seen++
		props, _ := tool.Parameters["properties"].(map[string]any)
		if len(props) != 1 || props[tool.GroupBy] == nil {
			t.Errorf("tool %q takes %v, want exactly %q", tool.Name, props, tool.GroupBy)
		}
		if extra, _ := tool.Parameters["additionalProperties"].(bool); extra {
			t.Errorf("tool %q permits additional properties", tool.Name)
		}
		if tool.GroupBy != areas.GroupByFor(tool.Resource) {
			t.Errorf("tool %q groups by %q, its area by %q", tool.Name, tool.GroupBy, areas.GroupByFor(tool.Resource))
		}
	}
	if seen == 0 {
		t.Error("no list tool is grouped, though Address Lists declares GroupBy")
	}
}

// TestExactlyTheFirewallsOrderedTablesAreMovable, in BOTH directions.
//
// ── A LEDGER, BECAUSE THE SET IS A BLAST RADIUS ─────────────────────────────
//
// `Movable` decides which of the router's ordered tables a MODEL may reorder.
// Widening it is a real decision — the routing rules, the IPsec policies, the
// OSPF templates and the simple queues are all ordered, and all consequential —
// so a resource joining or leaving this set fails here and is made deliberately.
//
// Named rather than derived from the same expression `Movable` uses: a test that
// recomputed the predicate would agree with any predicate.
func TestExactlyTheFirewallsOrderedTablesAreMovable(t *testing.T) {
	want := map[string]bool{
		"fwFilter": true, "fwNat": true, "fwMangle": true, "fwRaw": true,
		"fwFilter6": true, "fwNat6": true, "fwMangle6": true, "fwRaw6": true,
	}
	got := map[string]bool{}
	ordered := 0
	for _, r := range resource.All() {
		if r.Ordered {
			ordered++
		}
		if Movable(r) {
			got[r.Key] = true
			if !want[r.Key] {
				t.Errorf("%s is movable by the assistant and this ledger does not say so. "+
					"Widening what a model may reorder is a decision, not a side effect", r.Key)
			}
		}
	}
	for key := range want {
		if !got[key] {
			t.Errorf("%s is no longer movable by the assistant, though the ledger says it is. "+
				"If that was deliberate, remove it here", key)
		}
	}
	// The other half of the claim: there ARE ordered tables outside the
	// firewall, so "only the firewall's" is a restriction rather than a
	// description of everything that exists.
	if ordered <= len(want) {
		t.Errorf("%d ordered resources, %d of them movable: nothing is being held back, so "+
			"this ledger is not measuring the restriction it describes", ordered, len(want))
	}
	if Movable(nil) {
		t.Error("a nil resource is movable")
	}
}

// TestTheWriteToolOffersBeforeOnlyWhenSomethingCanMove, both ways. The enum is
// the permission for `resource`; the same has to hold for `before`, or a viewer
// who may write only the DNS page is told about an argument every use of which
// would be refused.
func TestTheWriteToolOffersBeforeOnlyWhenSomethingCanMove(t *testing.T) {
	props := func(pages ...string) map[string]any {
		can := func(page, access string) bool {
			if access != AccessWrite {
				return true
			}
			for _, p := range pages {
				if p == page {
					return true
				}
			}
			return false
		}
		for _, tool := range Permitted(can) {
			if tool.Name == WriteToolName {
				p, _ := tool.Parameters["properties"].(map[string]any)
				return p
			}
		}
		t.Fatalf("no write tool was offered to a viewer who may write %v", pages)
		return nil
	}
	if props("firewall")["before"] == nil {
		t.Error("a viewer who may write the Firewall page is not told how to move a rule, " +
			"so the assistant can still only append")
	}
	if p := props("dns"); p["before"] != nil {
		t.Error("a viewer who may write only the DNS page is offered `before`, every use of " +
			"which would be refused")
	} else if p["values"] == nil {
		t.Fatal("that viewer was offered no write tool at all; this test is measuring nothing")
	}
}
