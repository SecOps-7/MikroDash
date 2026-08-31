package server

// No raw error value may reach an HTTP body.
//
// The messages this server writes by hand are safe by inspection and should
// arrive intact. The ones that come out of a driver are not: a SQLite failure
// names the database file, and the port's report endpoints answer 500s with
// detail the live app never sends — it has no try/catch there at all.
//
// A source check rather than a behavioural one, and deliberately narrow: it
// pins the SHAPE `writeJSONErr(..., <something>.Error())`, which is the shape
// that was there nineteen times before Part 29. It says nothing about the
// WebSocket error payloads, which carry RouterOS's own text on purpose — see the
// note in the test below.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Two shapes leak, and both had instances before Part 29:
//
//	writeJSONErr(w, status, err.Error())          — an HTTP body
//	map[string]any{"message": err.Error()}        — a WebSocket error payload
//
// The live app sends NEITHER: it has no try/catch on the report endpoints at
// all, and every one of its twenty-six write-error payloads reads
// `message: sanitizeErr(e)`. Not one sends `e.message`.
var leaks = []*regexp.Regexp{
	// The helper's own body is the sanitised form and must not match itself.
	regexp.MustCompile(`writeJSONErr\((?:[^)]|\)[^;\n])*?[^.\w]\w+\.Error\(\)\)`),
	regexp.MustCompile(`"message":\s*\w+\.Error\(\)`),
}

func TestNoRawErrorReachesAnHttpBody(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		src := string(body)
		for _, re := range leaks {
			for _, m := range re.FindAllString(src, -1) {
				if strings.Contains(m, "safe.Message(") {
					continue // already redacted
				}
				found++
				t.Errorf("%s sends a raw error value to the browser:\n    %s\n"+
					"Wrap it in safe.Message, which redacts paths, addresses, e-mail and tokens.",
					e.Name(), m)
			}
		}
	}
	if found == 0 {
		t.Log("no raw error reaches an HTTP body")
	}
}

// TestTheSanitisingHelperIsActuallyUsed — the guard above only proves the bad
// shape is absent, which a file that stopped answering errors at all would also
// satisfy. This proves the good shape is present.
func TestTheSanitisingHelperIsActuallyUsed(t *testing.T) {
	body, err := os.ReadFile("reports.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(body), "writeJSONErrFrom("); n < 15 {
		t.Errorf("reports.go calls writeJSONErrFrom %d times; it answered %d error paths when "+
			"this was written, and a sharp drop means they stopped being answered or went raw", n, n)
	}
}
