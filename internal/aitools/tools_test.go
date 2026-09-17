package aitools

import (
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
	var write *Tool
	for _, tool := range Permitted(only) {
		if tool.Access == AccessWrite {
			cp := tool
			write = &cp
		}
	}
	if write == nil {
		t.Fatalf("no write tool was offered to a viewer who may write page %q", target)
	}
	props, _ := write.Parameters["properties"].(map[string]any)
	res, _ := props["resource"].(map[string]any)
	enum, _ := res["enum"].([]string)
	if len(enum) == 0 {
		t.Fatal("the write tool offered no resources at all")
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
		if tool.Collector != "" {
			if !strings.Contains(tool.Description, "/interface") || !strings.Contains(tool.Description, "Read only") {
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
func TestEveryReadToolTakesNoArguments(t *testing.T) {
	for _, tool := range All() {
		if tool.Access != AccessRead {
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
func TestTheWriteToolTakesExactlyResourceIdValuesAndDelete(t *testing.T) {
	tool, ok := ByName(WriteToolName)
	if !ok {
		t.Fatalf("%q is not in the catalogue", WriteToolName)
	}
	if tool.Access != AccessWrite {
		t.Errorf("%q declares access %q", WriteToolName, tool.Access)
	}
	props, _ := tool.Parameters["properties"].(map[string]any)
	want := map[string]bool{"resource": true, "id": true, "values": true, "delete": true}
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

// TestTheReadCatalogueIsOrderedAndTheWriteToolIsLast.
//
// The read tools are generated into a committed file and sent on every request:
// an unstable order produces a diff on every regeneration and defeats any prompt
// caching the endpoint does. The write tool is appended AFTER that sort, so the
// one tool that changes anything is not buried alphabetically among thirty that
// cannot.
func TestTheReadCatalogueIsOrderedAndTheWriteToolIsLast(t *testing.T) {
	all := All()
	prev := ""
	for _, tool := range all[:len(all)-1] {
		if tool.Name <= prev {
			t.Errorf("%q follows %q", tool.Name, prev)
		}
		prev = tool.Name
		if tool.Access != AccessRead {
			t.Errorf("tool %q is not a read tool but sits in the read catalogue", tool.Name)
		}
	}
	if last := all[len(all)-1]; last.Name != WriteToolName {
		t.Errorf("the catalogue ends with %q, not the write tool", last.Name)
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
