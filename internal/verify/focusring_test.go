package verify

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// THE KEYBOARD FOCUS RING MAY BE REPLACED, NEVER JUST SWITCHED OFF.
//
// `web/public/app.css` draws one ring for the whole app with
// `:focus-visible{outline:2px solid var(--accent-rx) !important}`, and its
// comment explains the !important at length: a dozen component classes set
// `outline:none` in their BASE rule, and two id selectors outrank any
// class-based focus rule, so without it the ring is suppressed across most of
// the interactive surface. Before that rule existed, tabbing through the app
// gave no indication of position at all (WCAG 2.4.7).
//
// That argument had nothing behind it but the `!important` and a paragraph.
// Any later rule with enough specificity - an id, or another !important - takes
// the ring away again, silently, and the suite would not notice: no test reads
// this stylesheet, and a missing focus ring renders perfectly.
//
// So this is the ledger, and it fails in BOTH directions. A suppression that is
// not declared below fails, because it is an accessibility regression nobody
// asked for. A declaration whose selector no longer appears fails too, because
// a list of excuses nobody re-measures is worse than no list.
//
// TO ADD ONE: say what the REPLACEMENT indicator is, and add its selector to
// `needs` so the replacement is checked as well. "It looked wrong" is not a
// reason on its own - a control that cannot show focus cannot be used from a
// keyboard.
var focusSuppressions = map[string]string{
	// The terminal's caret line. The ring was drawn around the thing you type
	// on, which in a console is a box around the cursor. Replaced by the text
	// caret in the prompt's colour, plus the prompt lifting to full strength
	// while focus is there - both theme tokens, so both work in every palette.
	"#terminalInput:focus,#terminalInput:focus-visible": ".term-live:focus-within .term-prompt",
}

// outlineOff finds a declaration block that takes the ring away with enough
// force to beat the app-wide rule: `outline` set to none/0 with !important.
var outlineOff = regexp.MustCompile(`(?m)^\s*([^{}\n]+?)\s*\{[^{}]*outline\s*:\s*(?:none|0)[^;}]*!important[^{}]*\}`)

func TestNothingSuppressesTheFocusRingWithoutReplacingIt(t *testing.T) {
	root := repoRoot(t)
	files := readFiles(t, root, "web/public", func(rel string) bool {
		return strings.HasSuffix(rel, ".css")
	})
	if len(files) == 0 {
		t.Fatal("no stylesheet was read; this check would pass by finding nothing")
	}

	all := map[string]string{} // selector -> the file it is in
	var every strings.Builder  // every sheet, for finding replacements anywhere
	for rel, src := range files {
		every.WriteString(string(src))
		for _, m := range outlineOff.FindAllStringSubmatch(string(src), -1) {
			all[strings.Join(strings.Fields(m[1]), "")] = rel
		}
	}
	squashed := strings.Join(strings.Fields(every.String()), "")

	// A declared suppression owes a replacement, and the replacement is looked
	// for across ALL the sheets rather than the one the suppression is in -
	// they are separate files and either may hold it.
	for sel, replacement := range focusSuppressions {
		if _, ok := all[sel]; !ok {
			continue // staleness is reported below
		}
		if replacement == "" {
			t.Errorf("%s is declared with no replacement indicator at all", sel)
			continue
		}
		if !strings.Contains(squashed, strings.Join(strings.Fields(replacement), "")) {
			t.Errorf("%s suppresses the focus ring and its declared replacement %q is in no "+
				"stylesheet. A control that cannot show focus cannot be used from a keyboard",
				sel, replacement)
		}
	}

	var stray []string
	for sel, rel := range all {
		if _, ok := focusSuppressions[sel]; !ok {
			stray = append(stray, sel+" ("+rel+")")
		}
	}
	sort.Strings(stray)
	for _, s := range stray {
		t.Errorf("%s takes the app-wide keyboard focus ring away and is not declared in "+
			"focusSuppressions. Either it is an accessibility regression, or it is deliberate "+
			"and owes a replacement indicator and a line saying what it is", s)
	}

	for sel := range focusSuppressions {
		if _, ok := all[sel]; !ok {
			t.Errorf("focusSuppressions names %q, which no longer suppresses anything: "+
				"the entry is stale, or the rule moved somewhere this check cannot see it", sel)
		}
	}
}
