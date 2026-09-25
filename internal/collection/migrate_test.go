package collection

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

// THE MIGRATION MATCHES THE LIVE `planMigration`, BRANCH FOR BRANCH.
//
// The corpus is produced by RUNNING the live function — the collection corpus
// — over the same fleet, so these are the plans live actually made rather than
// what its source appeared to say.
//
// The fleet in every case carries three shapes on purpose: an ordinary router, a
// router PINNED with its own mode, and a router with a `collection` block that
// has overrides but no mode. The middle one must never appear in a plan; the
// third one must, and the difference between them is `collection.mode` rather
// than `collection`.
func TestPlanMigrationMatchesLive(t *testing.T) {
	b, err := os.ReadFile("../../testdata/collection-cases.json")
	if err != nil {
		t.Fatalf("reading the lifted corpus: %v", err)
	}
	var doc struct {
		LegacyStreamKeys map[string]string `json:"legacyStreamKeys"`
		Migration        []struct {
			Name     string         `json:"name"`
			Settings map[string]any `json:"settings"`
			Routers  []struct {
				ID         string `json:"id"`
				Collection *struct {
					Mode      string         `json:"mode"`
					Overrides map[string]any `json:"overrides"`
				} `json:"collection"`
			} `json:"routers"`
			Plan []struct {
				ID         string `json:"id"`
				Collection struct {
					Mode      string          `json:"mode"`
					Overrides map[string]bool `json:"overrides"`
				} `json:"collection"`
			} `json:"plan"`
		} `json:"migration"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Migration) == 0 {
		t.Fatal("the corpus has no migration cases, so this measures nothing")
	}

	// THE KEY TABLE IS LIVE'S, not a list typed here. A key added or renamed
	// upstream fails here rather than being silently ignored by the port.
	if len(doc.LegacyStreamKeys) != len(legacyStreamKeys) {
		t.Errorf("the port knows %d legacy keys; live has %d",
			len(legacyStreamKeys), len(doc.LegacyStreamKeys))
	}
	for _, k := range legacyStreamKeys {
		if got, ok := doc.LegacyStreamKeys[k.key]; !ok {
			t.Errorf("%s is not a live legacy key", k.key)
		} else if got != k.collector {
			t.Errorf("%s maps to %q here and %q in live", k.key, k.collector, got)
		}
	}

	for _, c := range doc.Migration {
		t.Run(c.Name, func(t *testing.T) {
			routers := make([]MigrationRouter, 0, len(c.Routers))
			for _, r := range c.Routers {
				mr := MigrationRouter{ID: r.ID}
				if r.Collection != nil {
					mr.Collection = &Router{Mode: r.Collection.Mode}
				}
				routers = append(routers, mr)
			}
			got := PlanMigration(c.Settings, routers)

			if len(got) != len(c.Plan) {
				t.Fatalf("planned %d write(s), live planned %d: got=%+v want=%+v",
					len(got), len(c.Plan), got, c.Plan)
			}
			for i := range c.Plan {
				if got[i].ID != c.Plan[i].ID {
					t.Errorf("plan[%d] is for %q, live chose %q", i, got[i].ID, c.Plan[i].ID)
				}
				if got[i].Mode != c.Plan[i].Collection.Mode {
					t.Errorf("plan[%d] mode = %q, live = %q",
						i, got[i].Mode, c.Plan[i].Collection.Mode)
				}
				want := c.Plan[i].Collection.Overrides
				if len(got[i].Overrides) != len(want) {
					t.Errorf("plan[%d] overrides = %v, live = %v", i, got[i].Overrides, want)
					continue
				}
				for k, v := range want {
					if got[i].Overrides[k] != v {
						t.Errorf("plan[%d] override %s = %v, live = %v",
							i, k, got[i].Overrides[k], v)
					}
				}
			}
		})
	}
}

// A PINNED ROUTER IS NEVER IN A PLAN, asserted across every case at once.
//
// Separate from the per-case comparison because it is the property with the
// worst failure: overwriting an operator's explicit per-router choice with a
// global that no longer has a UI, which they cannot then see or undo.
func TestNoCaseEverPlansAPinnedRouter(t *testing.T) {
	b, err := os.ReadFile("../../testdata/collection-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Migration []struct {
			Settings map[string]any `json:"settings"`
		} `json:"migration"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	fleet := []MigrationRouter{
		{ID: "plain"},
		{ID: "pinned-poll", Collection: &Router{Mode: "poll"}},
		{ID: "pinned-stream", Collection: &Router{Mode: "stream"}},
		{ID: "block-no-mode", Collection: &Router{}},
	}
	sawPlain, sawBlockNoMode := false, false
	for _, c := range doc.Migration {
		for _, p := range PlanMigration(c.Settings, fleet) {
			switch p.ID {
			case "pinned-poll", "pinned-stream":
				t.Errorf("a router with its own mode was planned: %+v", p)
			case "plain":
				sawPlain = true
			case "block-no-mode":
				sawBlockNoMode = true
			}
		}
	}
	// A test that never planned anything would pass the assertion above without
	// exercising it.
	if !sawPlain {
		t.Error("no case planned the unpinned router, so the exclusion above proves nothing")
	}
	if !sawBlockNoMode {
		t.Error("no case planned the router whose block has no mode — that is the " +
			"case separating `collection.mode` from `collection`")
	}
}

// THE MIXED BRANCH RECORDS ONLY THE POLLED COLLECTORS.
//
// Recording the streaming ones too would freeze them at a value the operator
// never set, so a later change to the default would not reach this install.
func TestTheMixedBranchRecordsOnlyWhatWasPolled(t *testing.T) {
	got := PlanMigration(map[string]any{
		"streamSystem": true, "streamPing": false,
		"streamConns": false, "streamTalkers": true, "streamIfrates": true,
	}, []MigrationRouter{{ID: "r"}})
	if len(got) != 1 || got[0].Mode != "stream" {
		t.Fatalf("want one stream plan, got %+v", got)
	}
	keys := make([]string, 0, len(got[0].Overrides))
	for k, v := range got[0].Overrides {
		if v {
			t.Errorf("override %s is TRUE; only polled collectors are recorded", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "streamConns" || keys[1] != "streamPing" {
		t.Errorf("overrides = %v, want exactly the two that were false", keys)
	}
}
