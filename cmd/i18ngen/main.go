// Command i18ngen writes web/locales/source.json: every English string the
// interface shows, with the files it comes from. It is what a translator
// translates from, and what the drift check compares each catalog with.
//
// The strings are the markup's text and titles (internal/i18n.Units) and every
// literal the TypeScript passes to t() (internal/i18n.TSLiterals), found by the
// same code the build translates with, so the list cannot describe text the
// build would not translate. `-check` runs in tools/verify.sh.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"mikrodash/internal/i18n"
)

const outRel = "web/locales/source.json"

func main() {
	out := flag.String("out", outRel, "file to write")
	check := flag.Bool("check", false, "fail if the committed file is stale instead of writing it")
	flag.Parse()

	src, bad, err := i18n.Sources(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, "i18ngen:", err)
		os.Exit(1)
	}
	if len(bad) > 0 {
		for f, lines := range bad {
			fmt.Fprintf(os.Stderr, "i18ngen: %s: t() on lines %v is not given a plain quoted string\n", f, lines)
		}
		os.Exit(1)
	}
	body, err := json.MarshalIndent(src, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "i18ngen:", err)
		os.Exit(1)
	}
	body = append(body, '\n')

	if *check {
		have, err := os.ReadFile(*out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "i18ngen: %s is missing; run `go run ./cmd/i18ngen`\n", *out)
			os.Exit(1)
		}
		if !bytes.Equal(bytes.TrimSpace(have), bytes.TrimSpace(body)) {
			fmt.Fprintf(os.Stderr, "i18ngen: %s is STALE — the interface's text changed and the "+
				"source list was not regenerated.\nRun: go run ./cmd/i18ngen\n", *out)
			os.Exit(1)
		}
		fmt.Printf("i18ngen: %s is current (%d strings)\n", *out, len(src))
		return
	}
	if err := os.MkdirAll("web/locales", 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "i18ngen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "i18ngen:", err)
		os.Exit(1)
	}
	fmt.Printf("i18ngen: wrote %s — %d strings\n", *out, len(src))
}
