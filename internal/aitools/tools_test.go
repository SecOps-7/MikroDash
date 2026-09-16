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

func allowAll(string) bool { return true }
func denyAll(string) bool  { return false }

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

	denied := Permitted(denyAll)
	for _, tool := range denied {
		// The only survivor of a blanket denial would be an UNGATED tool, and
		// there must not be one: every tool reads a menu some page owns.
		t.Errorf("tool %q survived a blanket denial — it is gated on page %q", tool.Name, tool.Page)
	}

	// And one page at a time, which is the real case.
	target := all[0].Page
	if target == "" {
		t.Fatal("the first tool has no owning page")
	}
	one := Permitted(func(p string) bool { return p != target })
	for _, tool := range one {
		if tool.Page == target {
			t.Errorf("tool %q was offered though page %q is denied", tool.Name, target)
		}
	}
	if len(one) == len(all) {
		t.Errorf("denying page %q removed nothing", target)
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
		"LIST_DNSSTATIC", "list_dnsStatic "} {
		if _, ok := ByName(bad); ok {
			t.Errorf("ByName accepted %q", bad)
		}
	}
	real := All()[0].Name
	if _, ok := ByName(real); !ok {
		t.Errorf("ByName refused %q, which it generated itself", real)
	}
}

// TestEveryDescriptionNamesItsMenu.
//
// The RouterOS path is the one vocabulary shared between the operator's
// question, the documentation and the answer. Without it a model choosing
// between thirty similarly-named tools matches on the key, which is this app's
// word rather than MikroTik's.
func TestEveryDescriptionNamesItsMenu(t *testing.T) {
	for _, tool := range All() {
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

// TestEveryToolTakesNoArguments.
//
// The resource decides the menu. A model that could pass one could pass a menu
// this app never declared, and the read path would be executing a string the
// model chose rather than a resource the registry owns.
func TestEveryToolTakesNoArguments(t *testing.T) {
	for _, tool := range All() {
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

// TestAllIsOrderedByName. It is generated into a committed file and sent on
// every request: an unstable order produces a diff on every regeneration and
// defeats any prompt caching the endpoint does.
func TestAllIsOrderedByName(t *testing.T) {
	prev := ""
	for _, tool := range All() {
		if tool.Name <= prev {
			t.Errorf("%q follows %q", tool.Name, prev)
		}
		prev = tool.Name
	}
}
