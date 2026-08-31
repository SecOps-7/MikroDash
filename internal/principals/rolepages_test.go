package principals

// `ParseRolePages` against the LIVE `_parseRolePages`, executed.
//
// The corpus is `tools/rolepages-cases.js`, which lifts the function out of
// `src/index.js` by content anchor — walking to its closing brace rather than
// taking a fixed number of lines — and drives it with the REAL page registry, so
// "unknown page" means what it means in the app.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type rolePagesCase struct {
	Why         string         `json:"why"`
	Body        map[string]any `json:"body"`
	Error       *string        `json:"error"`
	HasValue    bool           `json:"hasValue"`
	ValueIsNull bool           `json:"valueIsNull"`
	Value       []RolePage     `json:"value"`
}

func TestParseRolePagesMatchesTheLiveFunction(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "rolepages-cases.json"))
	if err != nil {
		t.Fatalf("no corpus — run: node tools/rolepages-cases.js: %v", err)
	}
	var corpus struct {
		PageKeys []string        `json:"pageKeys"`
		Cases    []rolePagesCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(corpus.Cases) == 0 {
		t.Fatal("the corpus is empty")
	}

	refused := 0
	for _, c := range corpus.Cases {
		if c.Error != nil {
			refused++
		}
	}
	if refused == 0 || refused == len(corpus.Cases) {
		t.Fatalf("%d of %d cases are refused; the corpus does not separate the branches",
			refused, len(corpus.Cases))
	}

	// THE REGISTRY THE CORPUS USED. Reading it out of the corpus rather than
	// declaring one here is what keeps the two runs asking the same question: a
	// hand-written registry that happened to omit a key would turn every "valid"
	// case into an unknown-page refusal and the test would still pass.
	known := map[string]bool{}
	for _, k := range corpus.PageKeys {
		known[k] = true
	}
	if len(known) < 3 {
		t.Fatalf("the corpus recorded %d page keys; it needs at least three", len(known))
	}

	for _, c := range corpus.Cases {
		t.Run(c.Why, func(t *testing.T) {
			got, err := ParseRolePages(c.Body, known)

			if c.Error != nil {
				if err == nil {
					t.Fatalf("accepted, as %+v; the live function refused with %q", got, *c.Error)
				}
				// EXACTLY the live string. These are rendered verbatim in the
				// role editor, and two of them name the offending key.
				if err.Error() != *c.Error {
					t.Errorf("error = %q, live = %q", err.Error(), *c.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused with %q; the live function accepted", err)
			}

			// THE DANGEROUS PAIR. `{value: null}` is "leave the role alone";
			// `{value: []}` is "this role now confers nothing".
			if c.ValueIsNull {
				if got.Submitted {
					t.Errorf("Submitted is true for an ABSENT pages key. The caller writes when it " +
						"is set, so this strips every page from the role on an unrelated edit.")
				}
				return
			}
			if !got.Submitted {
				t.Fatalf("Submitted is false for a SUBMITTED matrix, so the caller would leave the "+
					"role's pages alone — silently ignoring the operator. Live value: %v", c.Value)
			}
			if len(got.Pages) != len(c.Value) {
				t.Fatalf("%d page(s), live %d: %+v vs %+v", len(got.Pages), len(c.Value),
					got.Pages, c.Value)
			}
			// ORDER, not set membership — the live function appends in order and
			// the editor renders them that way.
			for i := range c.Value {
				if got.Pages[i] != c.Value[i] {
					t.Errorf("row %d = %+v, live = %+v", i, got.Pages[i], c.Value[i])
				}
			}
		})
	}
}

// TestAnEmptyMatrixIsNotAnAbsentOne — the pair again, stated as its own property.
//
// The corpus covers it, but it is the one thing about this function that a
// reader must not have to infer, and a corpus case can be deleted.
func TestAnEmptyMatrixIsNotAnAbsentOne(t *testing.T) {
	known := map[string]bool{"dashboard": true}

	absent, err := ParseRolePages(map[string]any{}, known)
	if err != nil {
		t.Fatal(err)
	}
	if absent.Submitted {
		t.Error("an absent pages key reads as submitted")
	}

	empty, err := ParseRolePages(map[string]any{"pages": []any{}}, known)
	if err != nil {
		t.Fatal(err)
	}
	if !empty.Submitted {
		t.Error("an empty array reads as absent — the operator's revocation would be ignored")
	}
	if len(empty.Pages) != 0 {
		t.Errorf("an empty array produced %d page(s)", len(empty.Pages))
	}
}
