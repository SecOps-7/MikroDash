package verify

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// EVERY OUTCOME A WRITER NAMES IS ONE THE TABLE ACCEPTS.
//
// `audit_events` declares `CHECK (outcome IN ('ok','denied','failed'))`, and
// SQLite enforces it: a row with any other value is not written at all. So an
// outcome the schema does not admit is not a mislabelled row, it is a MISSING
// one - and the missing ones are the failures, which is the half of the trail
// that matters.
//
// It happened. Six writers said "error": a backup that did not run, a scheduled
// backup that did not run, a config deploy that did not apply, a report that did
// not build, and both of the assistant's raw-command paths. Every one of those
// failures was dropped at the INSERT, silently, for as long as the code existed.
//
// ── WHY NOTHING CAUGHT IT, WHICH IS THE POINT OF THIS FILE ──────────────────
//
// `internal/server/ai_raw_test.go` built its own table with
// `CHECK (outcome IN ('ok','denied','error'))` - a DIFFERENT constraint from
// the real schema. The test agreed with itself whatever it wrote, and
// production rejected the row. That is the trap CLAUDE.md names for identity
// columns, met here in a CHECK: read the real table. And the one test that did
// look at outcomes, TestInsertAuditEventDefaults, pins that an UNSET outcome
// becomes legal - never that a WRONG one does.
//
// So this reads the schema itself, and the writers themselves, and fails in
// BOTH directions: a literal no CHECK admits is a failure, and a value the
// CHECK admits that no writer can produce is also one, because a constraint
// nobody writes to has stopped describing the app.
var outcomeCheckRe = regexp.MustCompile(`(?s)outcome\s+TEXT\s+NOT\s+NULL\s+CHECK\s*\(\s*outcome\s+IN\s*\(([^)]*)\)`)

// outcomeAssign finds `Outcome: "x"` in a composite literal and `outcome = "x"`
// in an assignment, which are the two shapes every writer uses.
func TestEveryAuditOutcomeIsOneTheSchemaAdmits(t *testing.T) {
	root := repoRoot(t)

	// 1. The permitted set, read out of the DDL rather than restated here.
	ddl := readFiles(t, root, "internal/db", func(rel string) bool {
		return strings.HasSuffix(rel, "schema_ddl.go")
	})
	var allowed map[string]bool
	for _, src := range ddl {
		m := outcomeCheckRe.FindStringSubmatch(string(src))
		if m == nil {
			continue
		}
		allowed = map[string]bool{}
		for _, part := range strings.Split(m[1], ",") {
			if v, err := strconv.Unquote(strings.ReplaceAll(strings.TrimSpace(part), "'", `"`)); err == nil {
				allowed[v] = true
			}
		}
	}
	if len(allowed) == 0 {
		t.Fatal("internal/db/schema_ddl.go: could not read the audit_events outcome CHECK; " +
			"this test reads nothing and must be re-aimed rather than left passing")
	}

	// 2. Every literal any writer assigns to an outcome.
	files := readFiles(t, root, "", func(rel string) bool {
		return strings.HasSuffix(rel, ".go") && !isTestSource(rel) &&
			!strings.HasPrefix(rel, "third_party/") && !strings.HasPrefix(rel, ".temp/")
	})
	seen := map[string]string{} // value -> where
	note := func(v, where string) {
		if _, ok := seen[v]; !ok {
			seen[v] = where
		}
	}
	for rel, src := range files {
		f, err := parser.ParseFile(token.NewFileSet(), rel, src, 0)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		lit := func(e ast.Expr) (string, bool) {
			b, ok := e.(*ast.BasicLit)
			if !ok || b.Kind != token.STRING {
				return "", false
			}
			s, err := strconv.Unquote(b.Value)
			return s, err == nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CompositeLit:
				// ONLY an audit.Event or db.Event. `planStep` in ai_raw.go also
				// has an `Outcome`, and it is a payload for the model rather
				// than a row in this table - a scan that took every struct
				// would report it and be wrong.
				sel, ok := x.Type.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Event" && sel.Sel.Name != "DBEvent") {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || (pkg.Name != "audit" && pkg.Name != "db") {
					return true
				}
				for _, e := range x.Elts {
					kv, ok := e.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Outcome" {
						if v, ok := lit(kv.Value); ok {
							note(v, rel)
						}
					}
				}
			case *ast.AssignStmt:
				// `outcome = "x"`, the shape the six used.
				for i, lhs := range x.Lhs {
					id, ok := lhs.(*ast.Ident)
					if !ok || !strings.EqualFold(id.Name, "outcome") || i >= len(x.Rhs) {
						continue
					}
					if v, ok := lit(x.Rhs[i]); ok {
						note(v, rel)
					}
				}
			}
			return true
		})
	}

	// 3. Both directions.
	var stray []string
	for v, where := range seen {
		if v != "" && !allowed[v] {
			stray = append(stray, v+" ("+where+")")
		}
	}
	sort.Strings(stray)
	for _, s := range stray {
		t.Errorf("an audit outcome the schema does not admit: %s. "+
			"audit_events CHECKs the value, so this row is not written at all - and it is the "+
			"failure rows that go missing. Use one of %v", s, sortedKeys(allowed))
	}

	var unwritten []string
	for v := range allowed {
		if _, ok := seen[v]; !ok {
			unwritten = append(unwritten, v)
		}
	}
	sort.Strings(unwritten)
	for _, v := range unwritten {
		t.Errorf("the schema admits outcome %q and nothing writes it. Either a writer was lost, "+
			"or the CHECK has stopped describing the app and should be narrowed", v)
	}
}
