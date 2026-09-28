package verify

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// NO FORM CONTROL IS LEFT FOR THE BROWSER TO PAINT.
//
// ── THE DEFECT THIS EXISTS FOR ──────────────────────────────────────────────
//
// A `button`, `select`, `input` or `textarea` that no CSS rule gives a
// background is drawn by the user agent in its system colours - `ButtonFace`
// and `Field`. Measured in the running app on 2026-09-28: rgb(107,107,107)
// under `color-scheme: dark`, and a pale beige under light. Neither belongs to
// any of the palettes in `app.css`, so the control reads as a washed-out slab
// whatever theme is chosen.
//
// It kept coming back because it is INVISIBLE TO EVERY OTHER CHECK. The markup
// is valid, the page renders, tsc is happy, and no test reads a stylesheet. The
// only signal was somebody looking at the page and saying the colour was wrong,
// which is how every instance so far was found.
//
// ── WHY THIS CHECKS THE MECHANISM AND NOT EVERY CONTROL ─────────────────────
//
// The obvious ledger - "every control in the markup names a class that some
// stylesheet gives a background" - CANNOT BE WRITTEN HONESTLY HERE. Controls in
// this app are painted by id (`#navRouterSelect`), by descendant selector
// (`.bw-toolbar input`, `.apps-modal-card input`) and by class, and deciding
// which rule wins for a given element is what a CSS engine does. A regex that
// guessed would report the working topbar controls as broken, and a ledger that
// cries wolf gets suppressed rather than read.
//
// So the FLOOR is checked instead. `app.css` carries a zero-specificity
// `:where(...)` rule that paints every control the browser would otherwise
// claim, which closes the class by construction rather than by enumeration:
// with it in place there is no control left for the user agent to paint. This
// proves that rule is still there and still covers what it claims, and that
// `.sbtn` - the app's own button class, and the carrier of the most recent
// instance - paints itself rather than leaning on the floor.
//
// ── IT FAILS IN BOTH DIRECTIONS ─────────────────────────────────────────────
//
// Deleting the floor fails. Narrowing it fails. Letting `.sbtn` or one of its
// variants go back to declaring layout with no colour fails. A variant recorded
// here that app.css no longer defines fails too, so the list cannot quietly
// become one nobody re-measures.

// nativeAppearanceInputs are the input types the floor may exclude, and the
// only ones.
//
// Each draws its own widget rather than a box: giving a checkbox a background
// paints OVER the tick instead of behind it. Excluding `text`, `search`,
// `number`, `password` or an absent type would reopen the hole, which is why
// this is a closed list rather than "whatever the rule happens to exclude".
var nativeAppearanceInputs = map[string]bool{
	"checkbox": true,
	"radio":    true,
	"range":    true,
	"color":    true,
	"file":     true,
	"hidden":   true,
}

// paintedButtonVariants is every `.sbtn-*` variant.
//
// The base `.sbtn` carries the neutral treatment, so a variant may override any
// of background, colour and border. What it must not do is declare a background
// and leave the other two to whatever precedes it - the drift that made
// `.sbtn-outline` a hole: it was used in a dozen places while undefined, so
// those buttons fell through to a bare `.sbtn` that had no paint of its own.
var paintedButtonVariants = []string{
	"sbtn-primary",
	"sbtn-warn",
	"sbtn-danger",
	"sbtn-purple",
	"sbtn-ghost",
	"sbtn-outline",
}

func TestNoControlIsLeftForTheBrowserToPaint(t *testing.T) {
	css := stripCSSComments(mustRead(t, filepath.Join(repoRoot(t), "web", "public", "app.css")))

	// ── THE FLOOR EXISTS ─────────────────────────────────────────────────────
	floor := regexp.MustCompile(`(?s):where\(\s*button\s*,\s*select\s*,\s*textarea\s*,\s*input(.*?)\)\s*\{(.*?)\}`).
		FindStringSubmatch(css)
	if floor == nil {
		t.Fatal("app.css has no `:where(button, select, textarea, input...)` rule.\n" +
			"That rule is the floor under every form control. Without it, a control no other rule\n" +
			"paints is drawn by the browser in ButtonFace/Field - rgb(107,107,107) under\n" +
			"color-scheme dark, pale beige under light - which is in none of the palettes.\n" +
			"If something replaced it, point this check at the replacement.")
	}
	selector, body := floor[1], floor[2]
	for _, need := range []string{"background-color", "color"} {
		if !strings.Contains(body, need) {
			t.Errorf("the control floor sets no %s: {%s}\n"+
				"It must set both, or the browser still supplies whichever one is missing.",
				need, strings.TrimSpace(body))
		}
	}
	// A LITERAL COLOUR HERE WOULD BE WRONG IN EVERY PALETTE BUT ONE.
	if !strings.Contains(body, "var(--") {
		t.Errorf("the control floor uses no theme token: {%s}\n"+
			"app.css defines many palettes and a light variant of several; a hard-coded colour is\n"+
			"correct in at most one of them.", strings.TrimSpace(body))
	}

	// ── AND EXCLUDES ONLY THE CONTROLS THAT PAINT THEMSELVES ─────────────────
	//
	// Both directions: an exclusion that is not a native-appearance widget
	// reopens the hole for that type, and a native widget the floor stops
	// excluding gets a flat background painted over its own drawing.
	excluded := map[string]bool{}
	for _, m := range regexp.MustCompile(`:not\(\[type=([a-z]+)\]\)`).FindAllStringSubmatch(selector, -1) {
		excluded[m[1]] = true
	}
	for typ := range excluded {
		if !nativeAppearanceInputs[typ] {
			t.Errorf("the control floor excludes input[type=%s], which is not a native-appearance widget.\n"+
				"That means a %s input with no class of its own is painted by the browser again.\n"+
				"If it really does draw itself, add it to nativeAppearanceInputs with the reason.", typ, typ)
		}
	}
	for typ := range nativeAppearanceInputs {
		if !excluded[typ] {
			t.Errorf("input[type=%s] is recorded as drawing its own widget, but the control floor no longer\n"+
				"excludes it - so the floor now paints a background over that widget. Either exclude it\n"+
				"again, or drop it from nativeAppearanceInputs because it stopped being native.", typ)
		}
	}

	// ── .sbtn PAINTS ITSELF ──────────────────────────────────────────────────
	//
	// It is the app's button class and the carrier of the most recent instance:
	// `class="sbtn"` with no variant is the obvious thing to write, and it used
	// to produce browser chrome. Leaning on the floor would be enough to look
	// right and still be wrong - a button is not a card, and the floor's card
	// background is not a button's.
	base := ruleBody(css, ".sbtn")
	if base == "" {
		t.Fatal("app.css has no `.sbtn{...}` base rule - this check no longer knows where to look")
	}
	for _, need := range []string{"background", "color", "border"} {
		if !strings.Contains(base, need) {
			t.Errorf(".sbtn's base rule sets no %s: {%s}\n"+
				"A button written as class=\"sbtn\" with no variant must still be a themed button;\n"+
				"leaving %s out hands that one back to the user agent.",
				need, strings.TrimSpace(base), need)
		}
	}
	if strings.Contains(base, "border:none") {
		t.Error(".sbtn's base rule is back to `border:none`.\n" +
			"Every variant declares its own 1px border, so that only ever reached a BARE .sbtn -\n" +
			"the one case with no colour of its own, which is exactly the case that broke.")
	}

	// ── AND SO DOES EVERY VARIANT ────────────────────────────────────────────
	var missing []string
	for _, v := range paintedButtonVariants {
		b := ruleBody(css, "."+v)
		if b == "" {
			missing = append(missing, v)
			continue
		}
		for _, need := range []string{"background", "color", "border"} {
			if !strings.Contains(b, need) {
				t.Errorf(".%s sets no %s: {%s}\n"+
					"A variant declaring only some of the three inherits the rest from whatever precedes\n"+
					"it, which is how .sbtn-outline became a hole.", v, need, strings.TrimSpace(b))
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("recorded .sbtn variants that app.css no longer defines: %s\n"+
			"Either the variant was deleted - drop it from paintedButtonVariants in the same commit -\n"+
			"or it was renamed, and the markup still naming it is now unpainted.",
			strings.Join(missing, ", "))
	}

	// ── THE CHECK CAN STILL SEE A KNOWN-BAD RULE ─────────────────────────────
	//
	// ruleBody is a regex over minified CSS, and a scan that has stopped
	// matching anything passes everything. This proves it still reads a rule
	// shaped like the one that broke.
	if got := ruleBody(".probe{display:inline-flex;border:none}", ".probe"); !strings.Contains(got, "border:none") {
		t.Errorf("ruleBody no longer reads a rule it is pointed at (got %q) - every check above is vacuous", got)
	}
}

// ruleBody returns the declarations of the first rule whose selector list
// contains exactly `sel`, or "" when there is none.
//
// Anchored on a whole selector rather than a substring: `.sbtn` must not match
// `.sbtn-primary`, and `.sbtn:hover` is a different rule from `.sbtn`.
func ruleBody(css, sel string) string {
	for _, m := range regexp.MustCompile(`([^{}]+)\{([^}]*)\}`).FindAllStringSubmatch(css, -1) {
		for _, part := range strings.Split(m[1], ",") {
			if strings.TrimSpace(part) == sel {
				return m[2]
			}
		}
	}
	return ""
}

// stripCSSComments removes /* ... */ so this check cannot match app.css's own
// explanation of the floor, which necessarily quotes the selector it describes.
func stripCSSComments(css string) string {
	return regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")
}
