package verify

// Every field on server.Options must be set by the binary that builds one.
//
// ── THE BUG THAT MADE THIS NECESSARY ────────────────────────────────────────
//
// `Options.OriginPatterns` existed from the v0.8.0 cutover and NOTHING EVER SET
// IT. No flag, no env var, no literal. Empty means "same-origin only" to
// `coder/websocket`, so every install behind a reverse proxy - where the
// browser's Origin and this process's Host differ by definition - was refused at
// the handshake with "request Origin ... is not authorized for Host ...".
//
// It shipped in every published Go image, 0.8.1 through 0.8.20, and arrived as
// issue #128 from an operator who could not open the UI through their proxy.
//
// Nothing could have caught it. The field compiled, the struct was complete, the
// tests passed, and the doc comment beside the field actively said empty was
// what a proxied deployment wanted - which is backwards, and is presumably why
// no flag was ever written.
//
// ── WHY THE WHOLE STRUCT AND NOT JUST THAT FIELD ────────────────────────────
//
// A gate naming `OriginPatterns` would pin the bug already fixed and nothing
// else. The CLASS is "a server option that exists and no caller sets", and it is
// invisible by construction: an unset option is the zero value, which is always
// a legal value and usually a quiet one. Checking every field costs the same and
// covers the next one.
//
// An option that is deliberately left to its zero value belongs in
// `optionsExpectedUnset` WITH A REASON, and an entry that starts being set is a
// failure too - otherwise the list becomes an excuse rather than a record.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// optionsExpectedUnset: fields cmd/mikrodash deliberately leaves at zero.
//
// Empty today, and that is the point: every option the server offers is
// reachable from the command line.
var optionsExpectedUnset = map[string]string{}

var (
	optionsFieldRe = regexp.MustCompile(`(?m)^\t([A-Z][A-Za-z0-9]*)\s+[^/\s]`)
	optionsSetRe   = regexp.MustCompile(`(?m)^\t\t([A-Z][A-Za-z0-9]*):`)
)

// between returns the text from the line after `start` up to the first line
// equal to `end`, which is how both the struct and the literal are delimited.
func between(t *testing.T, src, start, end string) string {
	t.Helper()
	i := strings.Index(src, start)
	if i < 0 {
		t.Fatalf("could not find %q - this gate is reading the wrong file", start)
	}
	rest := src[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("could not find %q after %q", end, start)
	}
	return rest[:j]
}

func TestEveryServerOptionIsSetByTheBinary(t *testing.T) {
	srv, err := os.ReadFile("../server/server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	main, err := os.ReadFile("../../cmd/mikrodash/main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}

	declared := map[string]bool{}
	for _, m := range optionsFieldRe.FindAllStringSubmatch(
		between(t, string(srv), "type Options struct {\n", "\n}\n"), -1) {
		declared[m[1]] = true
	}
	set := map[string]bool{}
	for _, m := range optionsSetRe.FindAllStringSubmatch(
		between(t, string(main), "server.Options{\n", "\n\t})\n"), -1) {
		set[m[1]] = true
	}

	// Believability floor. Both halves are read out of source text, so a
	// delimiter that stops matching would leave this test comparing two empty
	// sets and passing.
	if len(declared) < 10 {
		t.Fatalf("found only %d Options fields - the struct scan is broken, "+
			"not the code it is checking", len(declared))
	}
	if len(set) < 10 {
		t.Fatalf("found only %d fields set in cmd/mikrodash - the literal scan "+
			"is broken", len(set))
	}

	for f := range declared {
		if set[f] {
			continue
		}
		if why, ok := optionsExpectedUnset[f]; ok {
			t.Logf("Options.%s is deliberately unset: %s", f, why)
			continue
		}
		t.Errorf("server.Options.%s is declared and cmd/mikrodash never sets it, "+
			"so it is permanently its zero value. Wire it to a flag, or record "+
			"why it cannot be in optionsExpectedUnset. "+
			"(This is exactly how OriginPatterns shipped broken for 20 releases.)", f)
	}
	for f := range optionsExpectedUnset {
		if !declared[f] {
			t.Errorf("optionsExpectedUnset names %q, which is not an Options "+
				"field any more - delete the entry", f)
		}
		if set[f] {
			t.Errorf("optionsExpectedUnset says %q is unset, but cmd/mikrodash "+
				"sets it now - delete the entry rather than leaving a note that "+
				"has stopped being true", f)
		}
	}
	t.Logf("%d server options, %d set by the binary", len(declared), len(set))
}
