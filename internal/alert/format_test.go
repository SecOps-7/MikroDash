package alert

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type corpus struct {
	Renders []struct {
		Name string         `json:"name"`
		Tpl  string         `json:"tpl"`
		Vars map[string]any `json:"vars"`
		Want string         `json:"want"`
	} `json:"renders"`
	Ifaces []struct {
		Name  string  `json:"name"`
		Iface string  `json:"iface"`
		Type  *string `json:"type"`
		Want  string  `json:"want"`
		Key   string  `json:"key"`
	} `json:"ifaces"`
	Labels []struct {
		Type *string `json:"type"`
		Want string  `json:"want"`
	} `json:"labels"`
}

func load(t *testing.T) corpus {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "alerter-cases.json"))
	if err != nil {
		t.Fatalf("corpus: %v (regenerate with tools/alerter-cases.js)", err)
	}
	var c corpus
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Renders) < 15 || len(c.Ifaces) < 15 || len(c.Labels) < 10 {
		t.Fatal("the corpus is not the generated one")
	}
	return c
}

func TestRenderMatchesTheLiveModule(t *testing.T) {
	for _, c := range load(t).Renders {
		t.Run(c.Name, func(t *testing.T) {
			// A key whose value is `undefined` in JavaScript is ABSENT from the
			// serialised corpus, which is exactly the distinction under test: it
			// arrives here as a missing key, while an explicit null arrives as a
			// present nil.
			if got := Render(c.Tpl, c.Vars); got != c.Want {
				t.Errorf("Render(%q, %v)\n  got  %q\n  live %q", c.Tpl, c.Vars, got, c.Want)
			}
		})
	}
}

func TestIfaceTypeMatchesTheLiveModule(t *testing.T) {
	for _, c := range load(t).Ifaces {
		t.Run(c.Name, func(t *testing.T) {
			typ := ""
			if c.Type != nil {
				typ = *c.Type
			}
			got := IfaceType(c.Iface, typ)
			if got != c.Want {
				t.Errorf("IfaceType(%q, %q) = %q, live says %q", c.Iface, typ, got, c.Want)
			}
			if k := IfaceTypeKey(got); k != c.Key {
				t.Errorf("IfaceTypeKey(%q) = %q, live says %q", got, k, c.Key)
			}
		})
	}
}

func TestLabelForMatchesTheLiveModule(t *testing.T) {
	for _, c := range load(t).Labels {
		typ := ""
		if c.Type != nil {
			typ = *c.Type
		}
		if got := LabelFor(typ); got != c.Want {
			t.Errorf("LabelFor(%q) = %q, live says %q", typ, got, c.Want)
		}
	}
}

// ABSENT AND NULL ARE DIFFERENT. The live guard is `=== undefined`, so a
// template variable that is present-but-null renders the WORD "null" — which an
// operator reads in an alert. Asserted directly because the corpus cannot carry
// an undefined and a missing key distinctly in JSON.
func TestAbsentRendersEmptyAndNullRendersTheWord(t *testing.T) {
	if got := Render("{{a}}", map[string]any{}); got != "" {
		t.Errorf("an absent key rendered %q, want empty", got)
	}
	if got := Render("{{a}}", map[string]any{"a": nil}); got != "null" {
		t.Errorf("a null value rendered %q; the live guard tests `=== undefined` only, so it "+
			"renders the word", got)
	}
}

// The cap counts what SURVIVES the control-character strip, not what arrived.
func TestControlCharactersAreStrippedBeforeTheCap(t *testing.T) {
	var b []byte
	for i := 0; i < 300; i++ {
		b = append(b, 'x', '\n')
	}
	got := Render("{{a}}", map[string]any{"a": string(b)})
	if len(got) != 200 {
		t.Fatalf("length %d, want 200", len(got))
	}
	for i := range got {
		if got[i] == '\n' {
			t.Fatal("a newline survived; the strip runs before the cap")
		}
	}
}
