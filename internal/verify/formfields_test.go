package verify

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// EVERY FIELD IS ONE SHAPE: LABEL, CONTROL, HELP - IN A DIALOG OR ON A PAGE.
//
// ── THE DEFECT THIS EXISTS FOR ──────────────────────────────────────────────
//
// Measured on the NetWatch dialog, 2026-10-12: a toggle 38px tall in one row and
// 54px in the next, help text staggering every row below it, a select a few
// pixels off the inputs beside it, and placeholders in the browser's own grey.
// The text box, the select and the toggle had been built for the Settings page,
// one per line, and every dialog assembled them by hand with its own inline
// margins. Each mismatch was patched where somebody saw it, and nothing stopped
// the next dialog repeating all of them, because no check reads a stylesheet or
// a field's markup.
//
// ── WHAT IS CHECKED HERE, AND WHAT IS NOT ───────────────────────────────────
//
// The MARKUP and the STYLESHEET's half of the contract: every control in a
// dialog sits in an `.ff` of exactly label, control and help; toggles are
// `.ff-toggle`, never the Settings page's `.stoggle` panel; no part of a field
// carries an inline style; and app.css still defines the one control height,
// the subgrid and the themed placeholder. GEOMETRY - whether the boxes really
// line up - needs a browser, and tools/form-geometry.js measures it there. A
// green run here says the markup is right, not that the pixels are.
//
// ── IT FAILS IN BOTH DIRECTIONS ─────────────────────────────────────────────
//
// An exception recorded below that no longer breaks a rule fails, so the list
// cannot outlive its reasons.

// dialogControlExceptions are controls in a dialog that are not fields, each
// with why. Keyed "file#id".
var dialogControlExceptions = map[string]string{}

// stoggleFiles are the TypeScript files that may still draw the Settings page's
// `.stoggle` panel, because what they draw is on a PAGE, not in a dialog. A
// dialog toggle is `.ff-toggle`, from toggleHtml() in web/src/form.ts.
var stoggleFiles = map[string]string{
	"web/src/pages/packages.ts": "the bare switch beside the firmware upgrade button on the Packages page: an action, not a field",
	"web/src/pages/interfaces-history.ts": "the bare switch inside the traffic-history note in the Interface dialog's extra panel, " +
		"set in a sentence about recording: an action, not a form field",
}

// stoggleRe finds the .stoggle panel however it is spelled: in a class
// attribute, alone or with a variant, or assigned as a className.
var ffTablerTS = regexp.MustCompile(`class="(?:[^"]* )?form-(?:control|select|label)(?:-sm)?[ "]`)

var stoggleRe = regexp.MustCompile(`class="stoggle[ "]|className = 'stoggle[ ']`)

var (
	ffTagRe     = regexp.MustCompile(`(?s)<(/?)([a-zA-Z][a-zA-Z0-9-]*)((?:[^>"']|"[^"]*"|'[^']*')*?)(/?)>`)
	ffAttrRe    = regexp.MustCompile(`([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*"([^"]*)"`)
	ffCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	ffVoidTags  = map[string]bool{"input": true, "br": true, "hr": true, "img": true, "meta": true,
		"link": true, "source": true, "wbr": true, "area": true, "col": true, "embed": true, "param": true, "track": true}
	// ffTablerForm are the vendored Tabler stylesheet's form classes. It is still
	// loaded for its layout utilities, but its controls are a second control
	// system with their own height, padding and colours.
	ffTablerForm = []string{"form-control", "form-control-sm", "form-select", "form-select-sm", "form-label"}
	// ffParts are the classes whose box the stylesheet owns outright.
	ffParts = []string{"ff", "sform-label", "sform-input", "ff-help", "ff-toggle"}
	// ffHideOnly is the one inline style a field part may carry: hidden until a
	// script shows it.
	ffHideOnly = regexp.MustCompile(`^\s*display\s*:\s*none\s*;?\s*$`)
)

type ffNode struct {
	tag     string
	attrs   map[string]string
	classes map[string]bool
	kids    []*ffNode
	parent  *ffNode
	line    int
}

func (n *ffNode) has(c string) bool { return n.classes[c] }

func (n *ffNode) within(c string) bool {
	for p := n.parent; p != nil; p = p.parent {
		if p.has(c) {
			return true
		}
	}
	return false
}

// ffParse builds a tree from one of web/src/ui's templates. A tag scanner
// rather than an HTML parser: the templates are this repository's own, written
// as well-formed markup, and a parser would be a dependency for one test. A
// closing tag that closes nothing is reported, so a template it cannot read
// fails loudly instead of being scanned wrong.
func ffParse(t *testing.T, name, src string) *ffNode {
	t.Helper()
	src = ffCommentRe.ReplaceAllStringFunc(src, func(c string) string {
		return strings.Repeat("\n", strings.Count(c, "\n")) // keep line numbers
	})
	root := &ffNode{tag: "#root", classes: map[string]bool{}}
	cur := root
	for _, m := range ffTagRe.FindAllStringSubmatchIndex(src, -1) {
		closing, tag := src[m[2]:m[3]] == "/", strings.ToLower(src[m[4]:m[5]])
		line := strings.Count(src[:m[0]], "\n") + 1
		if closing {
			for p := cur; ; p = p.parent {
				if p == nil {
					t.Errorf("%s:%d: </%s> closes nothing open", name, line, tag)
					break
				}
				if p.tag == tag {
					cur = p.parent
					break
				}
			}
			continue
		}
		n := &ffNode{tag: tag, attrs: map[string]string{}, classes: map[string]bool{}, parent: cur, line: line}
		for _, a := range ffAttrRe.FindAllStringSubmatch(src[m[6]:m[7]], -1) {
			n.attrs[strings.ToLower(a[1])] = a[2]
		}
		for _, c := range strings.Fields(n.attrs["class"]) {
			n.classes[c] = true
		}
		cur.kids = append(cur.kids, n)
		if !ffVoidTags[tag] && src[m[8]:m[9]] != "/" {
			cur = n
		}
	}
	return root
}

// ffPlacement are the properties that place a control in a row without
// changing the box itself.
var ffPlacement = map[string]bool{"width": true, "min-width": true, "max-width": true, "flex": true,
	"flex-basis": true, "flex-grow": true, "flex-shrink": true, "margin-left": true, "margin-right": true}

func ffPlacementOnly(style string) bool {
	for _, decl := range strings.Split(style, ";") {
		prop, _, ok := strings.Cut(decl, ":")
		if !ok {
			if strings.TrimSpace(decl) != "" {
				return false
			}
			continue
		}
		if !ffPlacement[strings.ToLower(strings.TrimSpace(prop))] {
			return false
		}
	}
	return true
}

func ffInCell(n *ffNode) bool {
	for p := n.parent; p != nil; p = p.parent {
		if p.tag == "td" {
			return true
		}
	}
	return false
}

func ffWalk(n *ffNode, fn func(*ffNode)) {
	fn(n)
	for _, k := range n.kids {
		ffWalk(k, fn)
	}
}

func TestDialogFieldsFollowTheContract(t *testing.T) {
	root := repoRoot(t)
	files := readFiles(t, root, "web/src/ui/", func(rel string) bool { return strings.HasSuffix(rel, ".html") })
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)

	exceptionsSeen := map[string]bool{}
	dialogs := 0
	for _, name := range names {
		ffWalk(ffParse(t, name, files[name]), func(n *ffNode) {
			if n.has("rtr-modal") {
				dialogs++
			}
			at := name + ":" + strconv.Itoa(n.line)

			// ── EVERYWHERE: a page's forms follow the same contract as a dialog's.
			if n.has("stoggle") && !n.has("stoggle-bare") {
				t.Errorf("%s: a toggle is an .ff field with .ff-toggle, not the old .stoggle panel", at)
			}
			for _, c := range ffTablerForm {
				if n.has(c) {
					t.Errorf("%s: .%s is the vendored Tabler control, a second control system; use .sform-input (.sform-sm in a toolbar)", at, c)
				}
			}
			for _, c := range ffParts {
				st, ok := n.attrs["style"]
				if !ok || !n.has(c) || ffHideOnly.MatchString(st) {
					continue
				}
				// A control may be PLACED in its row (width, flex), never resized:
				// its height, padding and type are the stylesheet's.
				if c == "sform-input" && ffPlacementOnly(st) {
					continue
				}
				t.Errorf("%s: .%s carries style=%q; a field's spacing and size belong to the stylesheet", at, c, st)
				break
			}
			// A location search keeps its suggestion list as an extra child: it is
			// absolutely positioned, so it takes no row and the shape holds.
			var kids []*ffNode
			for _, k := range n.kids {
				if !k.has("cpick-list") {
					kids = append(kids, k)
				}
			}
			if n.has("ff") && (len(kids) != 3 || !kids[0].has("sform-label") || !kids[2].has("ff-help")) {
				t.Errorf("%s: an .ff is exactly a .sform-label, its control, and an .ff-help (empty is fine)", at)
			}

			// ── IN A DIALOG, every control is a field. A page may also have a lone
			// toolbar control (a search box), which is .sform-input without one.
			if !n.has("rtr-modal") && !n.within("rtr-modal") {
				return
			}
			switch n.tag {
			case "input", "select", "textarea":
			default:
				return
			}
			typ := strings.ToLower(n.attrs["type"])
			if typ == "hidden" || typ == "file" {
				return
			}
			if key := name + "#" + n.attrs["id"]; dialogControlExceptions[key] != "" {
				exceptionsSeen[key] = true
				return
			}
			if typ == "checkbox" || typ == "radio" {
				if !n.within("ff-toggle") && !n.within("ff-multi") {
					t.Errorf("%s: a %s in a dialog is an .ff-toggle or part of an .ff-multi", at, typ)
				}
				return
			}
			if !n.has("sform-input") {
				t.Errorf("%s: a dialog %s is an .sform-input", at, n.tag)
			}
			// A cell of a data-entry table is labelled by its column header.
			if !n.within("ff") && !ffInCell(n) {
				t.Errorf("%s: a dialog %s sits in an .ff field", at, n.tag)
			}
		})
	}
	if dialogs < 10 {
		t.Fatalf("found %d dialogs in web/src/ui - the scan has stopped seeing them", dialogs)
	}
	for key := range dialogControlExceptions {
		if !exceptionsSeen[key] {
			t.Errorf("%s is recorded as an exception but is no longer a control in a dialog - remove it", key)
		}
	}
}

// THE TYPESCRIPT HALF: field markup built in code obeys the same rules.
func TestFieldMarkupInTypeScriptCarriesNoInlineStyle(t *testing.T) {
	root := repoRoot(t)
	files := readFiles(t, root, "web/src/", func(rel string) bool {
		return strings.HasSuffix(rel, ".ts") && !strings.HasPrefix(rel, "web/src/gen/") && !isTestSource(rel)
	})
	// A class attribute naming a field part, with a style attribute in the same
	// tag, in either order.
	part := `(?:ff|ff-help|ff-toggle|sform-label)`
	styled := []*regexp.Regexp{
		regexp.MustCompile(`class="(?:[^"]* )?` + part + `(?: [^"]*)?"[^>]*\sstyle="`),
		regexp.MustCompile(`style="[^"]*"[^>]*\sclass="(?:[^"]* )?` + part + `(?: [^"]*)?"`),
		// A control may be placed in a row (flex, width, margin) but its height,
		// padding and type are the stylesheet's, or one box drifts off the rest.
		regexp.MustCompile(`class="(?:[^"]* )?sform-input(?: [^"]*)?"[^>]*\sstyle="[^"]*(?:height|padding|font)`),
	}
	stoggleSeen := map[string]bool{}
	for rel, src := range files {
		for _, re := range styled {
			for _, m := range re.FindAllStringIndex(src, -1) {
				t.Errorf("%s:%d: a field part carries an inline style - size and spacing belong to the stylesheet",
					rel, strings.Count(src[:m[0]], "\n")+1)
			}
		}
		for _, m := range ffTablerTS.FindAllStringIndex(src, -1) {
			t.Errorf("%s:%d: a Tabler form class - use .sform-input (.sform-sm in a toolbar)",
				rel, strings.Count(src[:m[0]], "\n")+1)
		}
		if stoggleRe.MatchString(src) {
			if _, ok := stoggleFiles[rel]; !ok {
				t.Errorf("%s draws the Settings page's .stoggle panel; a dialog toggle is toggleHtml() from web/src/form.ts", rel)
			}
			stoggleSeen[rel] = true
		}
	}
	for rel := range stoggleFiles {
		if !stoggleSeen[rel] {
			t.Errorf("%s is recorded as drawing .stoggle on a page but no longer does - remove it from stoggleFiles", rel)
		}
	}
}

// THE STYLESHEET'S HALF: one control height, the subgrid, the themed placeholder.
func TestFormFieldStylesheetContract(t *testing.T) {
	css := mustRead(t, filepath.Join(repoRoot(t), "web", "public", "app.css"))
	flat := regexp.MustCompile(`\s+`).ReplaceAllString(css, " ")
	for _, want := range []struct{ re, why string }{
		{`--ctl-h: [0-9.]+rem`, "--ctl-h, the one control height, is defined"},
		{`\.sform-input\{[^}]*height:var\(--ctl-h\)`, ".sform-input is --ctl-h tall"},
		{`\.ff-toggle \{[^}]*height: var\(--ctl-h\)`, "a toggle box is --ctl-h tall, like an input"},
		{`\.ff \{[^}]*grid-template-rows: subgrid;[^}]*grid-row: span 3`, "an .ff is a three-row subgrid, so rows align"},
		{`\.sform-input::placeholder\{color:var\(--text-muted\)`, "placeholders take the palette's muted text, not the browser's grey"},
		{`select\.sform-input\{appearance:none`, "a select is drawn by the stylesheet, not the browser"},
		{`--ctl-h-xs: [0-9.]+rem`, "--ctl-h-xs, the table-row button height, is defined"},
		{`td \.sbtn,td \.sbtn\.is-sm\{height:var\(--ctl-h-xs\)`, "a button in a table cell is row-sized by context"},
	} {
		if !regexp.MustCompile(want.re).MatchString(flat) {
			t.Errorf("app.css no longer holds the form contract: %s (looked for /%s/)", want.why, want.re)
		}
	}
}

// ONE CONTROL, EVERYWHERE: a text, number, select or textarea control anywhere
// in the frontend is .sform-input.
//
// Toolbars grew their own: .conn-src-filter, .rt-sel, .rpt-select, the bandwidth
// toolbar's input/select rules, Tabler's .form-control. Each was a box of its own
// height, padding and colour, which is the inconsistency the form contract ended
// in dialogs, repeated on pages. Templates and the markup TypeScript builds are
// both read; a native widget (checkbox, radio, range, colour, file) draws itself
// and is not checked.

// controlExceptions are controls that are deliberately not the shared box, keyed
// "file#id", each with why.
var controlExceptions = map[string]string{
	"web/src/ui/page-terminal.html#terminalInput": "the terminal's command line, drawn as a prompt inside the " +
		"terminal's own surface; a form box there would read as a dialog field",
	"web/src/ui/page-config-management.html#cfgEdBody": "the template editor: a full-height code pane with a " +
		"gutter, not a form field",
}

// controlExemptFiles are whole files outside the app's stylesheet.
var controlExemptFiles = map[string]string{
	"web/src/ui/login.html": "the sign-in page is standalone: it loads its own small stylesheet, not app.css",
}

var (
	ffControlRe = regexp.MustCompile(`<(input|select|textarea)\s[^>]*>`)
	ffNativeRe  = regexp.MustCompile(`type="(?:checkbox|radio|hidden|file|range|color|submit|button)"`)
	ffIDRe      = regexp.MustCompile(`\sid="([^"]*)"`)
	ffClassRe   = regexp.MustCompile(`\sclass="([^"]*)"`)
)

func TestEveryControlIsTheSharedControl(t *testing.T) {
	root := repoRoot(t)
	files := readFiles(t, root, "web/src/", func(rel string) bool {
		return (strings.HasSuffix(rel, ".html") || strings.HasSuffix(rel, ".ts")) &&
			!strings.HasPrefix(rel, "web/src/gen/") && !isTestSource(rel)
	})
	seen := map[string]bool{}
	for rel, src := range files {
		if controlExemptFiles[rel] != "" {
			seen[rel] = true
			continue
		}
		for _, m := range ffControlRe.FindAllStringIndex(src, -1) {
			tag := src[m[0]:m[1]]
			if ffNativeRe.MatchString(tag) {
				continue
			}
			id := ""
			if g := ffIDRe.FindStringSubmatch(tag); g != nil {
				id = g[1]
			}
			key := rel + "#" + id
			if controlExceptions[key] != "" {
				seen[key] = true
				continue
			}
			cls := ""
			if g := ffClassRe.FindStringSubmatch(tag); g != nil {
				cls = g[1]
			}
			if !strings.Contains(" "+cls+" ", " sform-input ") {
				t.Errorf("%s:%d: %q is not .sform-input (.sform-sm in a toolbar) - one control, everywhere",
					rel, strings.Count(src[:m[0]], "\n")+1, strings.TrimSpace(tag[:min(len(tag), 80)]))
			}
		}
	}
	for key := range controlExceptions {
		if !seen[key] {
			t.Errorf("%s is recorded as an exception but no longer exists as a control - remove it", key)
		}
	}
	for rel := range controlExemptFiles {
		if !seen[rel] {
			t.Errorf("%s is recorded as exempt but was not read - remove it", rel)
		}
	}
}

// ONE ACTION BUTTON, ONE ICON BUTTON, AND NAMED COMPONENTS.
//
// Buttons had the controls' problem: .cfg-btn, .topo-btn, .apps-btn, Tabler's
// .btn, pager and toolbar classes, and dozens of .sbtn with inline padding and
// font sizes - a Load button 36px tall beside 30px controls. Now an action is
// .sbtn (--ctl-h, or .is-sm for --ctl-h-sm beside toolbar controls), a glyph is
// .icon-btn, and anything else must be one of the components below, each named
// with what it is. A class that is neither fails; so does an entry nobody uses.

// buttonComponents are the button classes that are not actions: tabs, chips,
// segmented choices, close and move glyphs, headers. Each draws itself.
var buttonComponents = map[string]string{
	"stab": "a tab", "ptab": "a tab (Access Management's sections)", "nav-group-hdr": "a sidebar category header",
	"rtr-modal-close": "a dialog's close glyph", "topo-panel-close": "the topology panel's close glyph",
	"apps-close": "an app card's close glyph", "dash-remove-btn": "a dashboard card's remove glyph in edit mode",
	"fw-move": "an ordered table's move arrows", "res-neg": "a field's \"not\" toggle",
	"poll-profile-btn": "a segmented preset choice", "view-preset-btn": "a segmented preset choice",
	"bw-unit-btn": "a segmented unit choice", "cfg-seg": "a segmented choice",
	"dvm-range": "a segmented time-range choice", "ifh-range": "a segmented time-range choice",
	"chip": "the one clickable chip (app.css CHIPS): a filter, a toggle, a picked device", "sec-sev": "a severity filter chip",
	"apps-seg-btn": "a segmented show filter",
	"ai-suggest":   "a suggested-question chip", "wm-swatch": "a colour swatch",
	"wm-card-row": "a client row in the Wi-Fi map's access point card, which opens that client's card",
	"wl-sort-btn": "a sortable column header", "wl-group-toggle": "a group's disclosure header",
	"dp-act": "the device picker's own controls", "dp-all": "the device picker's own controls",
	"dp-clear": "the device picker's own controls", "dp-more": "the device picker's own controls",
	"chip-x": "a picked chip's remove glyph", "cpick-clear": "the location search's Clear, part of its control row",
	"nchan-gear": "a notification event's settings gear, beside its toggle",
	"apps-copy":  "a Copy link set inside a credential line", "notif-clear-btn": "the notification panel's Clear link",
	"au-tile":       "an app's tile on Apps Usage, which opens the devices using it",
	"au-orbit-item": "an app's logo on the Apps Usage share ring, which opens the devices using it",
}

// buttonIDs are buttons that are components of their own, by id.
var buttonIDs = map[string]string{
	"burgerBtn":       "the phone menu button, a floating glass control over the page",
	"routerSelectBtn": "the top bar's router chip: status dot, name and caret",
}

var (
	ffButtonRe    = regexp.MustCompile(`<button\s[^>]*>`)
	ffBtnStyleBad = regexp.MustCompile(`(?:^|;)\s*(?:height|padding[-a-z]*|font-size|line-height|background[-a-z]*|border[-a-z]*|color)\s*:`)
	ffStyleRe     = regexp.MustCompile(`\sstyle="([^"]*)"`)
)

func TestEveryButtonIsAnActionAnIconOrANamedComponent(t *testing.T) {
	root := repoRoot(t)
	files := readFiles(t, root, "web/src/", func(rel string) bool {
		return (strings.HasSuffix(rel, ".html") || strings.HasSuffix(rel, ".ts")) &&
			!strings.HasPrefix(rel, "web/src/gen/") && !isTestSource(rel)
	})
	usedClass, usedID := map[string]bool{}, map[string]bool{}
	for rel, src := range files {
		if controlExemptFiles[rel] != "" {
			continue
		}
		for _, m := range ffButtonRe.FindAllStringIndex(src, -1) {
			tag := src[m[0]:m[1]]
			at := rel + ":" + strconv.Itoa(strings.Count(src[:m[0]], "\n")+1)
			var classes []string
			if g := ffClassRe.FindStringSubmatch(tag); g != nil {
				// A class assembled in code ('... ' + cls + '...') still names its
				// fixed classes; the quote marks split them off.
				for _, c := range strings.Fields(strings.NewReplacer("'", " ", "+", " ").Replace(g[1])) {
					classes = append(classes, c)
				}
			}
			has := func(c string) bool {
				for _, x := range classes {
					if x == c {
						return true
					}
				}
				return false
			}
			if has("sbtn") || has("icon-btn") {
				if g := ffStyleRe.FindStringSubmatch(tag); g != nil && ffBtnStyleBad.MatchString(g[1]) {
					t.Errorf("%s: a button sized or painted inline (%q) - .sbtn/.is-sm and .icon-btn own that", at, g[1])
				}
				continue
			}
			named := false
			for _, c := range classes {
				if buttonComponents[c] != "" {
					usedClass[c], named = true, true
				}
			}
			if !named {
				if g := ffIDRe.FindStringSubmatch(tag); g != nil && buttonIDs[g[1]] != "" {
					usedID[g[1]], named = true, true
				}
			}
			if !named {
				t.Errorf("%s: %q is not an action (.sbtn), an icon (.icon-btn) or a named component", at, tag[:min(len(tag), 90)])
			}
		}
	}
	for c := range buttonComponents {
		if !usedClass[c] {
			t.Errorf("button component %q is recorded but no button uses it - remove it", c)
		}
	}
	for id := range buttonIDs {
		if !usedID[id] {
			t.Errorf("button id %q is recorded but no such button exists - remove it", id)
		}
	}
}

// NO WIDGET IS LEFT FOR THE BROWSER TO DRAW.
//
// A checkbox, radio, slider, colour well, file button and scrollbar are drawn
// by the browser in its system colours unless the stylesheet draws them: a warm
// khaki grey under dark, a pale beige under light. The Backups page's selection
// checkboxes were exactly that (2026-10-12): they set accent-color, which only
// colours the CHECKED state. app.css now draws each widget from the palette in
// its NATIVE WIDGETS section; this holds that section, and forbids accent-color
// anywhere, since reaching for it is how an unticked widget goes back to khaki.
func TestNativeWidgetsAreDrawnFromThePalette(t *testing.T) {
	root := repoRoot(t)
	css := mustRead(t, filepath.Join(root, "web", "public", "app.css"))
	flat := regexp.MustCompile(`\s+`).ReplaceAllString(css, " ")
	for _, want := range []struct{ re, why string }{
		{`:where\(input\[type=checkbox\], input\[type=radio\]\) \{ appearance: none;`, "checkboxes and radios are drawn by the stylesheet"},
		{`:where\(input\[type=checkbox\], input\[type=radio\]\):checked, :where\(input\[type=checkbox\]\):indeterminate \{ background: var\(--accent-rx\)`, "a ticked box takes the accent"},
		{`:where\(input\[type=range\]\)::-webkit-slider-thumb \{[^}]*background: var\(--accent-rx\)`, "a slider's thumb is drawn"},
		{`:where\(input\[type=range\]\)::-webkit-slider-runnable-track \{`, "a slider's track is drawn"},
		{`:where\(input\[type=file\]\)::file-selector-button \{`, "a file input's button is drawn"},
		{`:where\(input\[type=color\]\)::-webkit-color-swatch \{`, "a colour well is drawn"},
		{`:root \{ scrollbar-color: `, "scrollbars take the palette"},
	} {
		if !regexp.MustCompile(want.re).MatchString(flat) {
			t.Errorf("app.css no longer draws a native widget: %s (looked for /%s/)", want.why, want.re)
		}
	}
	decl := regexp.MustCompile(`accent-color\s*:`)
	files := readFiles(t, root, "web/", func(rel string) bool {
		return (strings.HasSuffix(rel, ".css") || strings.HasSuffix(rel, ".html") || strings.HasSuffix(rel, ".ts")) &&
			!strings.HasPrefix(rel, "web/src/gen/") && !strings.Contains(rel, "/vendor/") &&
			!strings.HasPrefix(rel, "web/dist/") && !isTestSource(rel) && !strings.HasSuffix(rel, "login.html")
	})
	for rel, src := range files {
		for _, m := range decl.FindAllStringIndex(src, -1) {
			t.Errorf("%s:%d: accent-color colours only a CHECKED widget, so the unticked one stays the browser's - "+
				"the NATIVE WIDGETS section in app.css draws widgets instead", rel, strings.Count(src[:m[0]], "\n")+1)
		}
	}
}

// EVERY COLOUR IS THE PALETTE'S.
//
// Measured 2026-10-12: with the Nord palette chosen, 3,828 colour values on the
// app's pages were still the DEFAULT palette's exact blue, green or steel, and
// 3,631 under the light theme - because 743 colours were written out as
// literals (rgba(56,189,248,.15)) instead of taken from the palette, and a
// literal does not change when the palette does. They are relative colours now:
// rgb(from var(--accent-rx) r g b / .15) is the same colour on the default
// palette and the palette's own on every other. Eighteen variables were also
// used but never defined (--accent-red, --bg-page...), so those rules quietly
// fell back or dropped out.
//
// This holds the stylesheets and the markup's style attributes. A literal is
// allowed only where it IS the palette: the palette definitions themselves, a
// palette swatch (which shows its own palette whatever is chosen), and the
// neutrals black and white (a shadow, a scrim, text on a solid accent).
var colourLiteralRe = regexp.MustCompile(`#[0-9a-fA-F]{8}\b|#[0-9a-fA-F]{6}\b|#[0-9a-fA-F]{3,4}\b|rgba?\([^()]*\)`)

func colourIsNeutral(lit string) bool {
	l := strings.ToLower(strings.ReplaceAll(lit, " ", ""))
	for _, n := range []string{"#000", "#fff", "#000000", "#ffffff", "rgba(0,0,0", "rgb(0,0,0", "rgba(255,255,255", "rgb(255,255,255", "rgba(3,6,12"} {
		if strings.HasPrefix(l, n) {
			return true
		}
	}
	return false
}

func TestEveryColourIsThePalettes(t *testing.T) {
	root := repoRoot(t)
	comment := regexp.MustCompile(`(?s)/\*.*?\*/`)
	rule := regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`)
	css := readFiles(t, root, "web/public/", func(rel string) bool {
		return strings.HasSuffix(rel, ".css") && !strings.Contains(rel, "/vendor/") && !strings.HasSuffix(rel, "app-fonts.css")
	})
	for rel, src := range css {
		src = comment.ReplaceAllStringFunc(src, func(c string) string { return strings.Repeat("\n", strings.Count(c, "\n")) })
		for _, m := range rule.FindAllStringSubmatchIndex(src, -1) {
			sel := src[m[2]:m[3]]
			if strings.Contains(sel, "data-palette") || strings.TrimSpace(sel) == ":root" ||
				strings.HasSuffix(strings.TrimSpace(sel), `html[data-theme="light"]`) {
				continue // the palettes themselves
			}
			body := src[m[4]:m[5]]
			for _, decl := range strings.Split(body, ";") {
				prop, val, ok := strings.Cut(decl, ":")
				if !ok || strings.HasPrefix(strings.TrimSpace(prop), "--") {
					continue
				}
				for _, lit := range colourLiteralRe.FindAllString(val, -1) {
					if !colourIsNeutral(lit) {
						t.Errorf("%s:%d: %s: %s is a literal colour - take it from the palette: rgb(from var(--token) r g b / alpha)",
							rel, strings.Count(src[:m[4]], "\n")+1, strings.TrimSpace(prop), lit)
					}
				}
			}
		}
	}
	markup := readFiles(t, root, "web/src/", func(rel string) bool {
		return (strings.HasSuffix(rel, ".html") || strings.HasSuffix(rel, ".ts")) && !strings.HasPrefix(rel, "web/src/gen/") &&
			!isTestSource(rel) && !strings.HasSuffix(rel, "login.html")
	})
	tagRe := regexp.MustCompile(`<[a-zA-Z][^<>]*>`)
	styleRe := regexp.MustCompile(`style=(?:"([^"]*)"|'([^']*)')`)
	// An SVG presentation attribute cannot read a CSS variable, so a colour in one
	// is always a literal: it belongs in the element's style instead.
	svgAttrRe := regexp.MustCompile(`\s(?:fill|stroke|stop-color|flood-color)="(#[0-9a-fA-F]{3,8}|rgba?\([^)]*\))"`)
	swatch := regexp.MustCompile(`class="[^"]*\bswatch-(?:preview|dot)\b`)
	for rel, src := range markup {
		for _, m := range tagRe.FindAllStringIndex(src, -1) {
			tag := src[m[0]:m[1]]
			if swatch.MatchString(tag) {
				continue // a palette swatch shows its own palette
			}
			for _, a := range svgAttrRe.FindAllStringSubmatch(tag, -1) {
				if !colourIsNeutral(a[1]) {
					t.Errorf("%s:%d: an SVG colour attribute carries %s - put it in the style, from the palette", rel, strings.Count(src[:m[0]], "\n")+1, a[1])
				}
			}
			for _, s := range styleRe.FindAllStringSubmatch(tag, -1) {
				for _, lit := range colourLiteralRe.FindAllString(s[1]+s[2], -1) {
					if !colourIsNeutral(lit) {
						t.Errorf("%s:%d: a style attribute carries %s - take it from the palette", rel, strings.Count(src[:m[0]], "\n")+1, lit)
					}
				}
			}
		}
	}
}

// THE SCRIPT HALF: a colour set from code is the palette's too.
//
// Charts, gauges, maps and canvases set their colours in code, where neither the
// stylesheet check nor a page sweep reaches (a canvas has no computed style).
// web/src/palette.ts gives code the palette: paletteCss for anything the browser
// styles, paletteColour for a canvas, onPaletteChange to redraw. A literal colour
// in a script is refused, apart from neutrals, an HTML entity (&#039;) that only
// looks like one, and a stored key mapped to a token in the same pair (the Wi-Fi
// map's saved-plan colours: ['#38bdf8', '--accent-rx']).
func TestScriptColoursAreThePalettes(t *testing.T) {
	root := repoRoot(t)
	files := readFiles(t, root, "web/src/", func(rel string) bool {
		return strings.HasSuffix(rel, ".ts") && !strings.HasPrefix(rel, "web/src/gen/") && !isTestSource(rel)
	})
	block := regexp.MustCompile(`(?s)/\*.*?\*/`)
	line := regexp.MustCompile(`(^|[^:])//.*$`)
	lit := regexp.MustCompile(`(&)?(#[0-9a-fA-F]{6}\b|#[0-9a-fA-F]{3}\b|rgba?\(\s*\d[^)]*\))('\s*,\s*'--)?`)
	for rel, src := range files {
		src = block.ReplaceAllStringFunc(src, func(c string) string { return strings.Repeat("\n", strings.Count(c, "\n")) })
		for i, l := range strings.Split(src, "\n") {
			code := line.ReplaceAllString(l, "$1")
			for _, m := range lit.FindAllStringSubmatch(code, -1) {
				if m[1] != "" || m[3] != "" || colourIsNeutral(m[2]) {
					continue
				}
				t.Errorf("%s:%d: %s is a literal colour in code - use paletteCss/paletteColour from web/src/palette.ts", rel, i+1, m[2])
			}
		}
	}
}

// A DERIVED VARIABLE IS DERIVED. A custom property set in :root that no palette
// or theme overrides is the same in every palette, so if it holds a literal
// colour that colour is the default palette's everywhere. The count pills and
// the Devices status dots did exactly that (--card-badge-*, --dv-*): measured
// under Nord, 88 default-green dots on the Devices page. Such a variable is built
// from the palette's tokens instead.
func TestRootColourVariablesComeFromThePalette(t *testing.T) {
	css := mustRead(t, filepath.Join(repoRoot(t), "web", "public", "app.css"))
	css = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")
	rule := regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`)
	rootVars := map[string]string{}
	overridden := map[string]bool{}
	for _, m := range rule.FindAllStringSubmatch(css, -1) {
		sel := strings.TrimSpace(m[1])
		for _, decl := range strings.Split(m[2], ";") {
			k, v, ok := strings.Cut(decl, ":")
			k = strings.TrimSpace(k)
			if !ok || !strings.HasPrefix(k, "--") {
				continue
			}
			switch {
			case sel == ":root":
				rootVars[k] = v
			case strings.Contains(sel, "data-palette") || strings.HasSuffix(sel, `html[data-theme="light"]`):
				overridden[k] = true
			}
		}
	}
	if len(rootVars) < 10 {
		t.Fatalf("found %d :root variables - the scan has stopped seeing them", len(rootVars))
	}
	for k, v := range rootVars {
		if overridden[k] {
			continue
		}
		for _, lit := range colourLiteralRe.FindAllString(v, -1) {
			if !colourIsNeutral(lit) {
				t.Errorf("%s is %s in :root and no palette overrides it, so it is the default palette's colour in every palette - "+
					"derive it: rgb(from var(--token) r g b / alpha)", k, strings.TrimSpace(v))
			}
		}
	}
}
