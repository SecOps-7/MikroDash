// settingswritegen writes the TypeScript view of the settings WRITE tables.
//
// ── WHY THE BROWSER NEEDS THESE AT ALL ──────────────────────────────────────
//
// The Settings form is one bag of inputs, and every input's `.value` is a
// string. The server's validator is not so relaxed: `intFields` are parsed and
// range-checked, `strFields` are trimmed, `boolFields` accept ONLY a real `true`
// or the string "true", and `credFields` are deliberately NOT trimmed. So the
// collector has to know which kind each key is before it can build a body, and
// the only authority on that is the table the server itself reads.
//
// Sending everything as a string and letting the server coerce is ALMOST right,
// which is what makes it dangerous: it silently loses the browser-side
// `smtpPort || 587` fallback and the `updateCheckHours` clamp, and it would trim
// `smtpUser`, which the server pointedly does not.
//
// ── WHY A GO GENERATOR AND NOT A tools/*.js ONE ─────────────────────────────
//
// The 105 JS generators read the Node source, which was deleted at cutover, so
// every one of their `--check` runs now skips. A generator whose source is a
// committed file under `internal/` actually runs on a clean clone, which is the
// only kind worth adding. `cmd/pagesgen` is the model.
//
// ── IT READS THE JSON, NOT THE PACKAGE ──────────────────────────────────────
//
// `internal/store` embeds this same file into an UNEXPORTED `wtables`. Exporting
// an accessor purely so a generator could reach it would widen the store's API
// for no runtime caller; reading the committed JSON is the same bytes with no
// new surface, and `-check` is what keeps the two in step.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	srcRel = "internal/store/settings_write_tables.json"
	outRel = "web/src/gen/settings-write-fields.ts"
)

type writeTables struct {
	IntFields    map[string][2]int `json:"intFields"`
	StrFields    []string          `json:"strFields"`
	BoolFields   []string          `json:"boolFields"`
	CredFields   []string          `json:"credFields"`
	SpecialCases []string          `json:"specialCases"`
}

func main() {
	src := flag.String("src", srcRel, "the frozen table to read")
	out := flag.String("out", outRel, "file to write")
	check := flag.Bool("check", false, "fail if the committed file is stale instead of writing it")
	flag.Parse()

	raw, err := os.ReadFile(*src)
	if err != nil {
		fmt.Fprintln(os.Stderr, "settingswritegen:", err)
		os.Exit(1)
	}
	var t writeTables
	if err := json.Unmarshal(raw, &t); err != nil {
		fmt.Fprintf(os.Stderr, "settingswritegen: %s: %v\n", *src, err)
		os.Exit(1)
	}
	body := render(t)

	if *check {
		have, err := os.ReadFile(*out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "settingswritegen: %s is missing; run "+
				"`go run ./cmd/settingswritegen`\n", *out)
			os.Exit(1)
		}
		if !bytes.Equal(bytes.TrimSpace(have), bytes.TrimSpace(body)) {
			fmt.Fprintf(os.Stderr, "settingswritegen: %s is STALE — the write tables "+
				"changed and the TypeScript was not regenerated.\n"+
				"Run: go run ./cmd/settingswritegen\n", *out)
			os.Exit(1)
		}
		fmt.Printf("settingswritegen: %s is current (%d int, %d str, %d bool, %d cred)\n",
			*out, len(t.IntFields), len(t.StrFields), len(t.BoolFields), len(t.CredFields))
		return
	}

	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "settingswritegen:", err)
		os.Exit(1)
	}
	fmt.Printf("settingswritegen: wrote %s\n", *out)
}

func render(t writeTables) []byte {
	var b strings.Builder
	b.WriteString("// GENERATED from " + srcRel + " — do not edit.\n" +
		"//\n" +
		"// Rebuild: `go run ./cmd/settingswritegen`. `-check` runs in tools/verify.sh.\n" +
		"//\n" +
		"// This is the SERVER's own classification of every settings key, so the form\n" +
		"// collector can send a number where a number is expected and a boolean where a\n" +
		"// boolean is expected. The server accepts only a real `true` or the string\n" +
		"// \"true\" for a boolean — `1` and \"on\" both read as FALSE — and it IGNORES an\n" +
		"// invalid value rather than clamping it, so a key of the wrong type does not\n" +
		"// error, it silently fails to save.\n" +
		"//\n" +
		"// NOT every key here has an input on the Settings page, and not every input is\n" +
		"// here. The collector intersects this with the form map and skips whatever has\n" +
		"// no element.\n\n" +
		"/** Integer keys and their inclusive [min, max]. Out of range is IGNORED server-side. */\n" +
		"export const INT_FIELDS: Readonly<Record<string, readonly [number, number]>> = {\n")

	intKeys := make([]string, 0, len(t.IntFields))
	for k := range t.IntFields {
		intKeys = append(intKeys, k)
	}
	sort.Strings(intKeys)
	for _, k := range intKeys {
		r := t.IntFields[k]
		b.WriteString("  " + strconv.Quote(k) + ": [" +
			strconv.Itoa(r[0]) + ", " + strconv.Itoa(r[1]) + "],\n")
	}
	b.WriteString("};\n")

	list := func(name, doc string, vals []string) {
		v := append([]string(nil), vals...)
		sort.Strings(v)
		b.WriteString("\n/** " + doc + " */\nexport const " + name + ": readonly string[] = [\n")
		for _, s := range v {
			b.WriteString("  " + strconv.Quote(s) + ",\n")
		}
		b.WriteString("];\n")
	}
	list("STR_FIELDS", "Trimmed and cut to 256 by the server.", t.StrFields)
	list("BOOL_FIELDS", "Only a real `true` or the string \"true\" counts as true.", t.BoolFields)
	list("CRED_FIELDS",
		"Sealed at rest and NOT trimmed. A masked value is dropped; an EMPTY STRING is a destructive clear.",
		t.CredFields)
	list("SPECIAL_CASES",
		"Validated outside the four tables — see internal/store/settings_write.go.",
		t.SpecialCases)
	return []byte(b.String())
}
