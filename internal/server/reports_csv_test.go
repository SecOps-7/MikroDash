package server

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every CSV download names its charset.
//
// ── FOUND BY COMPARING THE RESPONSES, NOT THE SOURCES ──────────────────────
//
// On 2026-08-29 the report exports were compared against the live app over a
// window of shared history. The BODIES were byte-identical — same rows, same
// column order, and even the same float rendering
// (`11.818181818181818`) — but the headers were not:
//
//	port   text/csv
//	live   text/csv; charset=utf-8
//
// Reading the two sources would have concluded they agreed:
// `res.setHeader('Content-Type', 'text/csv')` is what `src/index.js` spells.
// Express appends the charset on `send()` for a text type, so the difference
// exists only in what reaches the browser — which is the only place it matters.
//
// And it does matter: without the charset a browser or spreadsheet guesses the
// encoding, so an alert detail or router label carrying a non-ASCII rune opens
// as mojibake. `audit_api.go` already sent it, so this was the port disagreeing
// with itself as much as with the live response.
//
// ── A SOURCE PIN, BECAUSE THE PROPERTY IS "EVERY SUCH RESPONSE" ────────────
//
// A handler test would cover the handler it names. The defect was one site out
// of two spelling it differently, so the assertion is over all of them.
func TestEveryCSVResponseDeclaresUTF8(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`Content-Type",\s*"text/csv([^"]*)"`)
	seen := 0
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			seen++
			if !strings.Contains(strings.ToLower(m[1]), "charset=utf-8") {
				t.Errorf("%s sends Content-Type \"text/csv%s\" — the live response carries "+
					"`; charset=utf-8` (Express appends it), and without it a spreadsheet "+
					"guesses the encoding and a non-ASCII router label opens as mojibake",
					f.Name(), m[1])
			}
		}
	}
	if seen == 0 {
		t.Fatal("no text/csv Content-Type found in internal/server — this test is measuring nothing")
	}
	t.Logf("%d CSV response(s) checked", seen)
}
