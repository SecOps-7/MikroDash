package verify

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestFixtureSchemasMatchReality: no Go test may create a table looser than the
// real one.
//
// ── THREE BUGS IN ONE DAY, ALL THIS SHAPE ───────────────────────────────────
//
// A test creates its own tables. If that DDL is looser than the database's, the
// code under test is checked against a database THAT CANNOT EXIST -- and it
// passes, while the code cannot read a real row. On 2026-08-26 it happened three
// times running. `sites.description` is NULLABLE; a fixture declared it
// `NOT NULL DEFAULT ”`, so `GetSite` scanning into a Go `string` passed the test
// and would have answered 500 for any site without a description, which is most
// of them.
//
// ── THE AUTHORITY IS THE REAL DDL, AND IT USED TO BE A FROZEN COPY ──────────
//
// This read `internal/verify/testdata/schema.sql`, on a premise stated in full
// at the time: "This was meant to be rewritten to compare fixtures against Go's
// own migrations. THERE ARE NONE. `internal/db` opens an existing database and
// refuses one below `MinSchema`; nothing in this repository creates a table
// outside a test." It ended "If Go ever grows migrations, this should read them
// instead."
//
// Go grew both. `internal/db/schema_ddl.go` builds a fresh database and
// `portMigrations` moves an old one, so the frozen file became a hand-kept copy
// of something the code already states - and it had already drifted FOUR TABLES
// behind without anything failing, because this check only ever compared
// FIXTURES against it. A premise that has expired reads exactly like one that is
// true.
//
// So it reads the real thing now, and `testdata/schema.sql` is gone. The parse
// is the same: `freshSchemaDDL` and the four table constants it concatenates are
// plain CREATE TABLE text in Go raw strings.
func TestFixtureSchemasMatchReality(t *testing.T) {
	root := repoRoot(t)

	var ddl strings.Builder
	for _, rel := range []string{"schema_ddl.go", "cfg_schema.go", "ztp_schema.go",
		"rollup.go", "notify_schema.go"} {
		ddl.WriteString(mustRead(t, filepath.Join(root, "internal", "db", rel)))
		ddl.WriteString("\n")
	}
	real := parseTables(t, ddl.String())
	if len(real) < 20 {
		t.Fatalf("only %d tables read out of internal/db - the parse broke, and every "+
			"fixture would then look fine", len(real))
	}

	fixtures := readFiles(t, root, "internal/", func(r string) bool {
		return strings.HasSuffix(r, "_test.go")
	})

	checked, notes := 0, 0
	var strict []string
	for rel, body := range fixtures {
		for name, cols := range parseTables(t, body) {
			realCols, known := real[name]
			if !known {
				// A table this schema does not have is a test's own scratch
				// table, not a fixture of the real one.
				continue
			}
			checked++
			for col, spec := range cols {
				want, ok := realCols[col]
				if !ok {
					t.Errorf("%s: fixture table %s declares column %q, which the real schema does "+
						"not have - the test is exercising a database that cannot exist",
						rel, name, col)
					continue
				}
				// STRICTER THAN REALITY IS THE DEFECT CLASS, and it is REPORTED
				// RATHER THAN FAILED -- exactly as the JavaScript original did,
				// which called these "notes".
				//
				// A fixture declaring NOT NULL where the column is nullable
				// never shows the code the NULL it will meet in production. That
				// is how `sites.description` shipped: the fixture said
				// `NOT NULL DEFAULT ''`, `GetSite` scanned into a Go `string`,
				// and it would have answered 500 for any site without a
				// description.
				//
				// They are not failures here because dozens predate this test,
				// and turning a pre-existing backlog into a red build teaches
				// people to delete the check. The COUNT is printed so the
				// backlog is visible and can shrink deliberately.
				if spec.notNull && !want.notNull {
					notes++
					strict = append(strict, rel+": "+name+"."+col)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no fixture table matched a real table - either the fixtures moved or the parse " +
			"broke; this test is checking nothing")
	}
	sort.Strings(strict)
	t.Logf("%d fixture table(s) checked against the frozen schema; %d column(s) declared stricter "+
		"than reality", checked, notes)
	for _, sline := range strict {
		t.Logf("  stricter than the real schema: %s", sline)
	}
}

type colSpec struct{ notNull bool }

var (
	createHead = regexp.MustCompile(`(?is)CREATE TABLE(?:\s+IF NOT EXISTS)?\s+(\w+)\s*\(`)
	colLine    = regexp.MustCompile(`^\s*(\w+)\s+(TEXT|INTEGER|REAL|BLOB|NUMERIC)\b(.*)$`)
	alterAdd   = regexp.MustCompile(`(?i)ALTER TABLE\s+(\w+)\s+ADD COLUMN\s+([^;\n]+)`)
)

// parseTables reads CREATE TABLE blocks into table -> column -> spec.
//
// The body is found by COUNTING PARENTHESES, not by a regex. A lazy `\)` match
// runs past the end of one table into the next whenever several are declared in
// one string -- which is how every Go fixture writes them -- and the result was
// one table appearing to hold every other table's columns.
func parseTables(t *testing.T, src string) map[string]map[string]colSpec {
	t.Helper()
	out := map[string]map[string]colSpec{}
	for _, loc := range createHead.FindAllStringSubmatchIndex(src, -1) {
		name := strings.ToLower(src[loc[2]:loc[3]])
		depth, end := 1, -1
		for i := loc[1]; i < len(src); i++ {
			switch src[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			continue
		}
		cols := map[string]colSpec{}
		// ── SPLIT ON TOP-LEVEL COMMAS, NOT ON NEWLINES ───────────────────
		//
		// One column per line is a convention, not a rule, and
		// `internal/db/schema_ddl.go` does not follow it: it continues a table
		// with `, acknowledged_at INTEGER, acknowledged_by TEXT);` on one line.
		// A newline split read that as a single unparseable line and dropped BOTH
		// columns, so the real schema looked as though it lacked them and every
		// fixture that declared them was reported as impossible.
		//
		// Depth-aware, because `NUMERIC(10,2)` and `CHECK (x IN ('a','b'))` both
		// contain commas that do not separate columns.
		for _, line := range splitColumns(stripSQLComments(src[loc[1]:end])) {
			trimmed := strings.TrimSpace(strings.ToUpper(line))
			if strings.HasPrefix(trimmed, "PRIMARY KEY") || strings.HasPrefix(trimmed, "FOREIGN KEY") ||
				strings.HasPrefix(trimmed, "UNIQUE") || strings.HasPrefix(trimmed, "CHECK") {
				continue
			}
			cm := colLine.FindStringSubmatch(line)
			if cm == nil {
				continue
			}
			rest := strings.ToUpper(cm[3])
			// `INTEGER PRIMARY KEY` IS THE ROWID ALIAS, and SQLite makes it NOT
			// NULL implicitly. Reading it as nullable made every fixture that
			// spells the constraint out look stricter than reality.
			cols[strings.ToLower(cm[1])] = colSpec{
				notNull: strings.Contains(rest, "NOT NULL") || strings.Contains(rest, "PRIMARY KEY"),
			}
		}
		if len(cols) > 0 {
			out[name] = cols
		}
	}
	// COLUMNS ADDED LATER ARE STILL COLUMNS. The schema was built by migrations,
	// and several tables gained fields through `ALTER TABLE ... ADD COLUMN`.
	// Reading only CREATE TABLE reported those as columns "the real schema does
	// not have" -- accusing correct fixtures of inventing them.
	for _, m := range alterAdd.FindAllStringSubmatch(src, -1) {
		table := strings.ToLower(m[1])
		if out[table] == nil {
			continue
		}
		cm := colLine.FindStringSubmatch(" " + m[2])
		if cm == nil {
			continue
		}
		rest := strings.ToUpper(cm[3])
		out[table][strings.ToLower(cm[1])] = colSpec{
			notNull: strings.Contains(rest, "NOT NULL") || strings.Contains(rest, "PRIMARY KEY"),
		}
	}
	return out
}

// stripSQLComments removes `-- ...` to end of line.
//
// BEFORE THE SPLIT, not after. `internal/db/schema_ddl.go` documents columns
// with a comment on the line above, and a comma-separated piece then BEGINS with
// that comment - so the anchored column regex saw `-- 1 = Administrator...` and
// dropped the column behind it. `roles.builtin` and `roles.created_at` went that
// way, and every fixture that declared them was reported as impossible.
func stripSQLComments(body string) string {
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// splitColumns cuts a CREATE TABLE body into one string per column definition,
// on commas that are not inside parentheses.
func splitColumns(body string) []string {
	out := []string{}
	depth, start := 0, 0
	for i, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(body[start:i]))
				start = i + 1
			}
		}
	}
	// TRIMMED, and it is not cosmetic. The column regex ends `(.*)$`, and `.`
	// does not match a newline while `$` here means end of text - so a piece
	// carrying the newline that followed its column could never match, and the
	// column was silently dropped from the schema this test calls reality.
	return append(out, strings.TrimSpace(body[start:]))
}
