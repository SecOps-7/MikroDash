package verify

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ONE LABEL PILL, ONE CHIP.
//
// A state, a kind, a flag, a protocol, an action: every label in a row is the
// .pill in app.css's PILLS section, in a palette tone. Before it there were 42
// pill classes, Tabler's .badge beside them and inline-styled spans, in sizes
// from .58 to .82rem and corners from 3 to 20px. Anything clicked to turn on
// or off - a filter, a toggle, a picked device - is the one .chip (CHIPS),
// which replaced nine chip classes with three different on-state names. This
// keeps a new one from starting: a class named like a pill or a chip must be
// one of the two or a recorded component, and nothing draws a pill inline.

// pillComponents are the classes named like a pill that are deliberately not
// the label pill. The ledger fails both ways: an unrecorded one is a new pill
// shape, and a recorded one the stylesheet no longer defines has to go.
var pillComponents = map[string]string{
	"card-badge":   "the count pill beside a title: it counts rows, it does not label one",
	"app-badge":    "an app's logo on Apps Usage: a tile holding the logo, tinted with the brand colour",
	"app-badge-sm": "an app's logo on Apps Usage, at table size",
	"app-badge-md": "an app's logo on Apps Usage, round, on the share ring",
	"app-badge-lg": "an app's logo on Apps Usage, at tile size",
	"wn-ssid-pill": "a Wi-Fi network's name, a step larger than a label on purpose",
	"rtr-dd-chips": "the row of router-dropdown site chips",
	"topo-chip-tx": "the expand count on a topology node, SVG text inside the node's chip",
	"topo-badges":  "a layout row that holds pills",
	// Not named like a pill, but shaped like one (pillShaped):
	"dvm-tab-count": "a count beside a tab label: it counts, it does not label",
	"cfg-seg-n":     "a count inside a segmented control's option",
	"about-count":   "a release's change count on About",
	"wl-sort-btn":   "a sort button on the Wi-Fi Networks page",
	"md-code":       "inline code in an assistant answer: code, like kbd and code",
	"dp-pills":      "the row of picked-device chips",
	"dv-badges":     "a layout row that holds pills",
	"cfg-tpl-pills": "a layout row that holds pills",
}

var pillTones = []string{"rx", "tx", "ok", "warn", "err", "alt", "pink", "muted"}

// The clickable chip's tones (app.css CHIPS): blue by default, or a palette role.
var chipTones = []string{"rx", "ok", "warn", "err", "alt", "muted"}

// isPillOrChip is the one label pill and the one chip, with their tones and parts.
func isPillOrChip(name string) bool {
	return name == "pill" || strings.HasPrefix(name, "pill-") || name == "chip" || strings.HasPrefix(name, "chip-")
}

// A class token named like a pill: badge, pill or chip as a whole word of it.
var pillNameRe = regexp.MustCompile(`^(?:[a-z0-9]+-)*(?:badge|badges|pill|pills|chip|chips)(?:-[a-z0-9]+)*$`)

// The properties that give a pill its shape and colour. Placement (margins,
// alignment, flex) stays free.
var pillShapeProp = regexp.MustCompile(`(?i)(?:^|;)\s*(font-size|font-family|font-weight|padding(?:-[a-z]+)?|border-radius|background(?:-color)?|border(?:-color)?|color|text-transform|letter-spacing)\s*:`)

// pillShaped is a pill by its shape rather than its name: small text, a rounded
// corner, a thin vertical padding and a palette-tinted fill. `.conn-st` and
// `.wl-std` were label pills named neither pill nor badge, and only this
// finds the next one.
var (
	pillFontRe  = regexp.MustCompile(`font-size:\s*\.(\d+)rem`)
	pillPadRe   = regexp.MustCompile(`padding:\s*([0-9.]+)(rem|em|px)?`)
	pillTintRe  = regexp.MustCompile(`background(?:-color)?:\s*(?:rgb\(from var\(--(?:accent|text-muted|border)|color-mix\([^)]*var\(--(?:accent|text-muted|border))`)
	pillRadius  = regexp.MustCompile(`border-radius\s*:`)
	pillCodeTag = regexp.MustCompile(`(?:^|[\s>+~])(?:kbd|code)$`)
)

func pillShaped(body string) bool {
	fs := pillFontRe.FindStringSubmatch(body)
	pad := pillPadRe.FindStringSubmatch(body)
	if fs == nil || pad == nil || !pillRadius.MatchString(body) || !pillTintRe.MatchString(body) {
		return false
	}
	size, _ := strconv.ParseFloat("0."+fs[1], 64)
	top, _ := strconv.ParseFloat(pad[1], 64)
	if pad[2] == "rem" || pad[2] == "em" {
		top *= 16
	}
	return size <= .75 && top <= 4
}

func TestEveryLabelIsTheOnePill(t *testing.T) {
	root := repoRoot(t)
	appCSS := mustRead(t, filepath.Join(root, "web", "public", "app.css"))
	flat := regexp.MustCompile(`\s+`).ReplaceAllString(appCSS, " ")
	if !regexp.MustCompile(`\.pill\{--pill:[^}]*border-radius:4px[^}]*color:rgb\(from var\(--pill\) r g b / \.95\);background:rgb\(from var\(--pill\) r g b / \.1\);border:1px solid rgb\(from var\(--pill\) r g b / \.25\)`).MatchString(flat) {
		t.Errorf("app.css no longer holds the pill contract: one shape, the tone's colour as text at .95, fill at .1, border at .25")
	}
	for _, tone := range pillTones {
		if !strings.Contains(flat, ".pill-"+tone+"{") {
			t.Errorf("app.css has no .pill-%s: every PillTone in web/src/palette.ts needs its rule", tone)
		}
	}
	// The one chip: an outline that fills in its tone when on.
	if !regexp.MustCompile(`\.chip\{--chip:var\(--accent-rx\)[^}]*border-radius:4px`).MatchString(flat) ||
		!strings.Contains(flat, ".chip.is-on{color:rgb(from var(--chip) r g b / .95);background:rgb(from var(--chip) r g b / .12)") {
		t.Errorf("app.css no longer holds the chip contract: one 4px outline, filled in the tone's tint when .is-on")
	}
	for _, tone := range chipTones {
		if !strings.Contains(flat, ".chip-"+tone+"{") {
			t.Errorf("app.css has no .chip-%s, which chipTones names", tone)
		}
	}
	palette := mustRead(t, filepath.Join(root, "web", "src", "palette.ts"))
	if m := regexp.MustCompile(`export type PillTone =([^;]*);`).FindStringSubmatch(palette); m == nil {
		t.Errorf("web/src/palette.ts no longer declares PillTone")
	} else {
		var declared []string
		for _, q := range regexp.MustCompile(`'([a-z]+)'`).FindAllStringSubmatch(m[1], -1) {
			declared = append(declared, q[1])
		}
		want := append([]string(nil), pillTones...)
		sort.Strings(declared)
		sort.Strings(want)
		if strings.Join(declared, ",") != strings.Join(want, ",") {
			t.Errorf("PillTone is %v, the stylesheet's tones are %v: one list, both sides", declared, want)
		}
	}

	// The stylesheet: a class named like a pill is the pill or a component.
	comment := regexp.MustCompile(`(?s)/\*.*?\*/`)
	rule := regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`)
	classRe := regexp.MustCompile(`\.([a-zA-Z][\w-]*)`)
	defined := map[string]bool{}
	css := readFiles(t, root, "web/public/", func(rel string) bool {
		return strings.HasSuffix(rel, ".css") && !strings.Contains(rel, "/vendor/")
	})
	for rel, src := range css {
		src = comment.ReplaceAllStringFunc(src, func(c string) string { return strings.Repeat("\n", strings.Count(c, "\n")) })
		for _, m := range rule.FindAllStringSubmatchIndex(src, -1) {
			sel := src[m[2]:m[3]]
			for _, c := range classRe.FindAllStringSubmatch(sel, -1) {
				defined[c[1]] = true
			}
			// The subject of each selector: its last compound's classes.
			shaped := pillShaped(src[m[4]:m[5]])
			for _, s := range strings.Split(sel, ",") {
				parts := strings.Fields(strings.NewReplacer(">", " ", "+", " ", "~", " ").Replace(s))
				if len(parts) == 0 {
					continue
				}
				subject := parts[len(parts)-1]
				if shaped && !pillCodeTag.MatchString(strings.TrimSpace(s)) {
					named := classRe.FindAllStringSubmatch(subject, -1)
					recorded := false
					for _, c := range named {
						if _, ok := pillComponents[c[1]]; ok || isPillOrChip(c[1]) {
							recorded = true
						}
					}
					if !recorded {
						t.Errorf("%s:%d: %s is shaped like a pill (small, rounded, thin, tinted) - a label is .pill with a tone; a deliberate other component is recorded in pillComponents",
							rel, strings.Count(src[:m[4]], "\n")+1, strings.TrimSpace(s))
					}
				}
				for _, c := range classRe.FindAllStringSubmatch(subject, -1) {
					name := c[1]
					if isPillOrChip(name) || !pillNameRe.MatchString(name) {
						continue
					}
					if _, ok := pillComponents[name]; ok {
						continue
					}
					if p := pillShapeProp.FindStringSubmatch(src[m[4]:m[5]]); p != nil {
						t.Errorf("%s:%d: .%s draws a pill (%s) - a label is .pill with a tone; a deliberate other component is recorded in pillComponents",
							rel, strings.Count(src[:m[4]], "\n")+1, name, p[1])
					}
				}
			}
		}
	}
	for name := range pillComponents {
		if !defined[name] {
			t.Errorf("pillComponents records .%s, which no stylesheet defines any more - remove it from the ledger", name)
		}
	}

	// The markup: no Tabler badge, no unknown pill class, no inline pill.
	markup := readFiles(t, root, "web/src/", func(rel string) bool {
		return (strings.HasSuffix(rel, ".html") || strings.HasSuffix(rel, ".ts")) &&
			!strings.HasPrefix(rel, "web/src/gen/") && !isTestSource(rel)
	})
	classAttr := regexp.MustCompile(`class="([^"]*)"`)
	spanStyle := regexp.MustCompile(`<span\b[^<>]*\sstyle="([^"]*)"`)
	tones := map[string]bool{"pill": true, "pill-dot": true, "pill-sm": true, "chip": true, "chip-n": true, "chip-x": true}
	for _, tone := range pillTones {
		tones["pill-"+tone] = true
	}
	for _, tone := range chipTones {
		tones["chip-"+tone] = true
	}
	for rel, src := range markup {
		line := func(i int) int { return strings.Count(src[:i], "\n") + 1 }
		for _, m := range classAttr.FindAllStringSubmatchIndex(src, -1) {
			for _, tok := range strings.Fields(src[m[2]:m[3]]) {
				if strings.ContainsAny(tok, "'+$") || strings.HasSuffix(tok, "-") {
					continue // a class assembled in code; its literal parts are checked where they are written
				}
				if tok == "badge" || strings.HasPrefix(tok, "badge-") {
					t.Errorf("%s:%d: Tabler's .%s - a label is .pill with a tone", rel, line(m[0]), tok)
					continue
				}
				if tones[tok] || !pillNameRe.MatchString(tok) {
					continue
				}
				if _, ok := pillComponents[tok]; !ok {
					t.Errorf("%s:%d: class %q is named like a pill but is neither .pill nor a recorded component", rel, line(m[0]), tok)
				}
			}
		}
		for _, m := range spanStyle.FindAllStringSubmatchIndex(src, -1) {
			st := src[m[2]:m[3]]
			if strings.Contains(st, "border-radius") && strings.Contains(st, "padding") {
				t.Errorf("%s:%d: a span styled inline as a pill - use pill(text, tone) from web/src/dom.ts", rel, line(m[0]))
			}
		}
	}
}
