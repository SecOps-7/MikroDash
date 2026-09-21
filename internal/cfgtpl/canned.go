package cfgtpl

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
)

// The canned library: templates that ship in the binary.
//
// ── A RELEASE UPDATES THEM WITHOUT TOUCHING ANYONE'S COPY ───────────────────
//
// They are read from the embedded files, never stored, so a new release's
// fixes reach every install. An operator who wants to change one clones it
// into a custom template, which records the canned id and version it came
// from; that copy is theirs and a release never edits it.
//
// ── EACH ONE IS HELD TO THE SAME RULES AS A TEMPLATE AN OPERATOR WRITES ─────
//
// canned_test.go parses and analyses every body, holds its variables to its
// placeholders, requires every lock-class template to accept MikroDash's own
// address before its first drop, and pins each body's fingerprint in a ledger
// that fails in both directions, so a changed body is seen in review.

//go:embed canned/manifest.json canned/*.rsc
var cannedFS embed.FS

// Canned is one shipped template.
type Canned struct {
	ID          string   `json:"id"`
	Version     int      `json:"version"`
	Category    string   `json:"category"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Variables   []VarDef `json:"variables"`
	Body        string   `json:"body"`
}

// CannedPrefix marks a canned template's id where a template id is expected:
// `canned:home-firewall`.
const CannedPrefix = "canned:"

// CannedCategories is the library's order.
var CannedCategories = []string{"home", "office", "security", "monitoring", "network"}

var cannedAll = mustLoadCanned()

// CannedTemplates is the library, in category order.
func CannedTemplates() []Canned { return cannedAll }

// CannedByID finds one shipped template.
func CannedByID(id string) (Canned, bool) {
	for _, c := range cannedAll {
		if c.ID == id {
			return c, true
		}
	}
	return Canned{}, false
}

func mustLoadCanned() []Canned {
	out, err := loadCanned()
	if err != nil {
		panic("the canned template library is broken: " + err.Error())
	}
	return out
}

func loadCanned() ([]Canned, error) {
	raw, err := cannedFS.ReadFile("canned/manifest.json")
	if err != nil {
		return nil, err
	}
	var list []Canned
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	order := map[string]int{}
	for i, c := range CannedCategories {
		order[c] = i
	}
	for i := range list {
		c := &list[i]
		if _, ok := order[c.Category]; !ok {
			return nil, fmt.Errorf("%s: category %q is not one of %v", c.ID, c.Category, CannedCategories)
		}
		body, err := cannedFS.ReadFile("canned/" + c.ID + ".rsc")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.ID, err)
		}
		c.Body = string(body)
		if c.Variables == nil {
			c.Variables = []VarDef{}
		}
		if c.Tags == nil {
			c.Tags = []string{}
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return order[list[i].Category] < order[list[j].Category] })
	return list, nil
}
