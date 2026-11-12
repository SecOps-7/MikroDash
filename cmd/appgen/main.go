// Command appgen builds the Apps Usage catalogue from its hand-curated source.
//
// INPUT: testdata/apps/catalogue.json, which names each app, its category, the
// v2fly/domain-list-community lists that hold its domains, any domains added by
// hand, its AS numbers and well-known ports, and where its logo comes from.
//
// OUTPUT, both committed:
//
//	internal/apps/catalogue_gen.json   every app with its expanded domains, which
//	                                   internal/apps embeds
//	web/public/vendor/app-icons/       one SVG per app that has a logo
//
// Generating needs local copies of the sources at the versions the catalogue
// pins: a v2fly checkout, the simple-icons npm package, a gilbarbara/logos
// checkout, and a directory holding the Iconify packages @iconify-json/cib,
// /arcticons and /lucide as ij-<prefix>/package. `-check` needs none of them: it runs in
// tools/verify.sh on any clone and fails when an output no longer matches the
// catalogue it was made from, or breaks a rule below.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	catalogueRel = "testdata/apps/catalogue.json"
	outRel       = "internal/apps/catalogue_gen.json"
	iconsRel     = "web/public/vendor/app-icons"
)

// ── THE CATALOGUE, AS WRITTEN BY HAND ───────────────────────────────────────

type portRange struct {
	Proto string `json:"proto"`
	From  int    `json:"from"`
	To    int    `json:"to"`
}

type iconRef struct {
	// Source is "simple-icons", "gilbarbara", or an Iconify set: "cib"
	// (CoreUI Brands), "arcticons" or "lucide".
	Source string `json:"source"`
	Slug   string `json:"slug"`
}

type srcApp struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Category string      `json:"category"`
	Lists    []string    `json:"lists"`
	Domains  []string    `json:"domains"`
	Icon     *iconRef    `json:"icon"`
	Color    string      `json:"color"`
	ASNs     []uint      `json:"asns"`
	Ports    []portRange `json:"ports"`
	Umbrella bool        `json:"umbrella"`
}

type catalogue struct {
	Sources    map[string]map[string]string `json:"sources"`
	Categories []string                     `json:"categories"`
	Apps       []srcApp                     `json:"apps"`
}

// ── THE GENERATED BUNDLE ────────────────────────────────────────────────────

// App is one entry of the bundle internal/apps embeds. The field names are the
// contract with that package and with the page.
type App struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Color    string `json:"color"`
	// Logo is how the page draws the app: "mask" (a single-colour shape the
	// page fills with Color, or with the text colour when Dark), "image" (a
	// full-colour logo shown as it is) or "" (no logo: the page draws a
	// monogram tile).
	Logo string `json:"logo"`
	// Dark is true when Color is too dark to read on the dark theme; the page
	// then fills a mask logo with the text colour instead.
	Dark     bool        `json:"dark"`
	Domains  []string    `json:"domains"`
	ASNs     []uint      `json:"asns"`
	Ports    []portRange `json:"ports"`
	Umbrella bool        `json:"umbrella"`
}

type bundle struct {
	// CatalogueSHA256 is the hash of the catalogue file this was made from,
	// which is how -check knows the bundle is stale without the sources.
	CatalogueSHA256 string                       `json:"catalogueSha256"`
	Sources         map[string]map[string]string `json:"sources"`
	Categories      []string                     `json:"categories"`
	Apps            []App                        `json:"apps"`
	// Dropped counts domains a v2fly list gave an app that an EARLIER app had
	// already claimed. Recorded so a change in it is visible in review.
	Dropped int `json:"dropped"`
}

func main() {
	v2fly := flag.String("v2fly", "", "a checkout of v2fly/domain-list-community at the pinned commit")
	si := flag.String("simple-icons", "", "the simple-icons npm package directory at the pinned version")
	gb := flag.String("gilbarbara", "", "a checkout of gilbarbara/logos at the pinned commit")
	ij := flag.String("iconify", "", "a directory holding ij-cib, ij-arcticons and ij-lucide, each an @iconify-json package")
	check := flag.Bool("check", false, "fail if the committed outputs are stale or break a rule, instead of writing them")
	flag.Parse()

	raw, err := os.ReadFile(catalogueRel)
	if err != nil {
		fail(err)
	}
	var cat catalogue
	if err := json.Unmarshal(raw, &cat); err != nil {
		fail(fmt.Errorf("%s: %w", catalogueRel, err))
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])

	if *check {
		if err := checkOutputs(cat, hash); err != nil {
			fail(err)
		}
		fmt.Printf("appgen: %s and %s are current (%d apps)\n", outRel, iconsRel, len(cat.Apps))
		return
	}
	if *v2fly == "" || *si == "" || *gb == "" || *ij == "" {
		fail(errors.New("generating needs -v2fly, -simple-icons, -gilbarbara and -iconify (or pass -check)"))
	}
	b, icons, err := build(cat, hash, *v2fly, *si, *gb, *ij)
	if err != nil {
		fail(err)
	}
	if err := write(b, icons); err != nil {
		fail(err)
	}
	fmt.Printf("appgen: wrote %s (%d apps, %d domains, %d dropped as claimed earlier) and %d logos\n",
		outRel, len(b.Apps), countDomains(b), b.Dropped, len(icons))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "appgen:", err)
	os.Exit(1)
}

// ── BUILD ───────────────────────────────────────────────────────────────────

func build(cat catalogue, hash, v2flyDir, siDir, gbDir, ijDir string) (bundle, map[string][]byte, error) {
	if err := validate(cat); err != nil {
		return bundle{}, nil, err
	}
	siColours, err := simpleIconColours(siDir)
	if err != nil {
		return bundle{}, nil, err
	}
	b := bundle{CatalogueSHA256: hash, Sources: cat.Sources, Categories: cat.Categories}
	icons := map[string][]byte{}
	claimed := map[string]string{}
	for _, a := range cat.Apps {
		out := App{ID: a.ID, Name: a.Name, Category: a.Category, ASNs: a.ASNs, Ports: a.Ports,
			Umbrella: a.Umbrella}
		if out.ASNs == nil {
			out.ASNs = []uint{}
		}
		if out.Ports == nil {
			out.Ports = []portRange{}
		}
		var domains []string
		for _, l := range a.Lists {
			d, err := expandList(v2flyDir, l, map[string]bool{})
			if err != nil {
				return bundle{}, nil, fmt.Errorf("%s: %w", a.ID, err)
			}
			domains = append(domains, d...)
		}
		domains = append(domains, a.Domains...)
		// FIRST CLAIM WINS, in catalogue order: the catalogue lists specific apps
		// before the companies that host them, so `xbox.com` stays Xbox's even
		// though v2fly's microsoft list includes the xbox list.
		out.Domains = []string{}
		for _, d := range dedupe(domains) {
			if owner, ok := claimed[d]; ok && owner != a.ID {
				b.Dropped++
				continue
			}
			claimed[d] = a.ID
			out.Domains = append(out.Domains, d)
		}

		colour := a.Color
		if a.Icon != nil {
			svg, err := readIcon(*a.Icon, siDir, gbDir, ijDir)
			if err != nil {
				return bundle{}, nil, fmt.Errorf("%s: %w", a.ID, err)
			}
			icons[a.ID] = svg
			// gilbarbara's logos are full colour and shown as they are; every
			// other set is a single-colour shape the page fills.
			if a.Icon.Source == "gilbarbara" {
				out.Logo = "image"
			} else {
				out.Logo = "mask"
			}
			if lic, own := siColours["!"+a.Icon.Slug]; own && a.Icon.Source == "simple-icons" {
				return bundle{}, nil, fmt.Errorf("%s: the Simple Icons logo %q has its own licence (%s), not CC0", a.ID, a.Icon.Slug, lic)
			}
			if colour == "" && a.Icon.Source == "simple-icons" {
				colour = siColours[a.Icon.Slug]
			}
		}
		if colour == "" {
			return bundle{}, nil, fmt.Errorf("%s: no colour: give it one, or a simple-icons logo", a.ID)
		}
		out.Color = strings.ToUpper(colour)
		out.Dark = tooDark(out.Color)
		b.Apps = append(b.Apps, out)
	}
	return b, icons, nil
}

// expandList reads one v2fly list, following `include:` lines.
//
// What is kept: bare domains and `domain:` entries (matched as suffixes), and
// `full:` entries (matched exactly; recorded with a leading "=" so the two can
// be told apart). What is skipped: `regexp:` and `keyword:` entries, which need
// a matcher this classifier does not have, and any entry tagged `@ads`, because
// an advert served beside an app is not that app's traffic.
func expandList(dir, name string, seen map[string]bool) ([]string, error) {
	if seen[name] {
		return nil, nil
	}
	seen[name] = true
	f, err := os.Open(filepath.Join(dir, "data", name))
	if err != nil {
		return nil, fmt.Errorf("v2fly list %q: %w", name, err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		entry, attrs := fields[0], fields[1:]
		ads := false
		for _, a := range attrs {
			if a == "@ads" {
				ads = true
			}
		}
		switch {
		case strings.HasPrefix(entry, "include:"):
			sub, err := expandList(dir, strings.TrimPrefix(entry, "include:"), seen)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
		case ads, strings.HasPrefix(entry, "regexp:"), strings.HasPrefix(entry, "keyword:"):
		case strings.HasPrefix(entry, "full:"):
			out = append(out, "="+strings.ToLower(strings.TrimPrefix(entry, "full:")))
		default:
			out = append(out, strings.ToLower(strings.TrimPrefix(entry, "domain:")))
		}
	}
	return out, sc.Err()
}

var svgOpen = regexp.MustCompile(`(?s)<svg\b[^>]*>`)

// readIcon returns the logo, cleaned for serving: no XML prolog, no comments
// and no <title> (the page labels the logo itself, so a second label would be
// read out twice).
func readIcon(ic iconRef, siDir, gbDir, ijDir string) ([]byte, error) {
	var p string
	switch ic.Source {
	case "simple-icons":
		p = filepath.Join(siDir, "icons", ic.Slug+".svg")
	case "gilbarbara":
		p = filepath.Join(gbDir, "logos", ic.Slug+".svg")
	case "cib", "arcticons", "lucide":
		return iconifySVG(filepath.Join(ijDir, "ij-"+ic.Source, "package", "icons.json"), ic.Slug)
	default:
		return nil, fmt.Errorf("unknown logo source %q", ic.Source)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return cleanSVG(string(raw), p)
}

// iconifySVG builds a standalone SVG from an Iconify JSON package: the icon's
// body inside an <svg> with the set's (or the icon's own) view box. An alias is
// followed to its parent; the rotations and flips an alias may add are not used
// by any icon this catalogue names, and are refused rather than ignored.
func iconifySVG(pkg, name string) ([]byte, error) {
	raw, err := os.ReadFile(pkg)
	if err != nil {
		return nil, err
	}
	type icon struct {
		Body   string `json:"body"`
		Left   *int   `json:"left"`
		Top    *int   `json:"top"`
		Width  *int   `json:"width"`
		Height *int   `json:"height"`
	}
	var set struct {
		Icons   map[string]icon `json:"icons"`
		Aliases map[string]struct {
			Parent string `json:"parent"`
			Rotate int    `json:"rotate"`
			HFlip  bool   `json:"hFlip"`
			VFlip  bool   `json:"vFlip"`
		} `json:"aliases"`
		Width  *int `json:"width"`
		Height *int `json:"height"`
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("%s: %w", pkg, err)
	}
	if al, ok := set.Aliases[name]; ok {
		if al.Rotate != 0 || al.HFlip || al.VFlip {
			return nil, fmt.Errorf("%s: %s is a transformed alias", pkg, name)
		}
		name = al.Parent
	}
	ic, ok := set.Icons[name]
	if !ok {
		return nil, fmt.Errorf("%s has no icon %q", pkg, name)
	}
	pick := func(own, def *int, fallback int) int {
		switch {
		case own != nil:
			return *own
		case def != nil:
			return *def
		}
		return fallback
	}
	left, top := pick(ic.Left, nil, 0), pick(ic.Top, nil, 0)
	w, h := pick(ic.Width, set.Width, 16), pick(ic.Height, set.Height, 16)
	s := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="%d %d %d %d">%s</svg>`, left, top, w, h, ic.Body)
	return cleanSVG(s, pkg+"#"+name)
}

// cleanSVG strips what a served logo must not carry and checks it is an SVG.
func cleanSVG(s, from string) ([]byte, error) {
	p := from
	s = regexp.MustCompile(`(?s)<\?xml.*?\?>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?s)<title>.*?</title>`).ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	if !svgOpen.MatchString(s) {
		return nil, fmt.Errorf("%s is not an SVG", p)
	}
	if strings.Contains(strings.ToLower(s), "<script") {
		return nil, fmt.Errorf("%s carries a script", p)
	}
	return []byte(s + "\n"), nil
}

// simpleIconColours returns each Simple Icons logo's brand colour, and the
// slugs of those that carry a licence of their own.
//
// SIMPLE ICONS IS CC0 AS A PROJECT, NOT ICON BY ICON: its DISCLAIMER says an
// icon may be under its own terms, recorded in the data file's `license` field.
// On 16.37.0 Webex's is Cisco's trademark terms. A logo with a licence of its
// own is refused, so one cannot slip in under the project's CC0.
func simpleIconColours(siDir string) (map[string]string, error) {
	raw, err := os.ReadFile(filepath.Join(siDir, "data", "simple-icons.json"))
	if err != nil {
		return nil, err
	}
	var icons []struct {
		Slug    string          `json:"slug"`
		Hex     string          `json:"hex"`
		License json.RawMessage `json:"license"`
	}
	if err := json.Unmarshal(raw, &icons); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, i := range icons {
		out[i.Slug] = "#" + i.Hex
		if len(i.License) > 0 {
			out["!"+i.Slug] = string(i.License)
		}
	}
	return out, nil
}

// tooDark is true when a colour would vanish on the dark theme's surfaces: a
// WCAG relative luminance below 0.07. Apple's, X's and TikTok's black, for
// example; Netflix red and Steam navy-blue stay in colour.
func tooDark(hexColour string) bool {
	h := strings.TrimPrefix(hexColour, "#")
	if len(h) != 6 {
		return false
	}
	ch := func(i int) float64 {
		v, _ := strconv.ParseUint(h[i:i+2], 16, 8)
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(0)+0.7152*ch(2)+0.0722*ch(4) < 0.07
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func countDomains(b bundle) int {
	n := 0
	for _, a := range b.Apps {
		n += len(a.Domains)
	}
	return n
}

// ── WRITE ───────────────────────────────────────────────────────────────────

func write(b bundle, icons map[string][]byte) error {
	data, err := json.MarshalIndent(b, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(outRel, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(iconsRel, 0o755); err != nil {
		return err
	}
	// Remove logos for apps that no longer have one, so the directory is
	// exactly the catalogue's.
	old, _ := filepath.Glob(filepath.Join(iconsRel, "*.svg"))
	for _, p := range old {
		if _, keep := icons[strings.TrimSuffix(filepath.Base(p), ".svg")]; !keep {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	for id, svg := range icons {
		if err := os.WriteFile(filepath.Join(iconsRel, id+".svg"), svg, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ── RULES, CHECKED WITHOUT THE SOURCES ──────────────────────────────────────

var (
	idRule     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	colourRule = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

// validate holds the hand-written catalogue to its own rules.
func validate(cat catalogue) error {
	cats := map[string]bool{}
	for _, c := range cat.Categories {
		cats[c] = true
	}
	ids := map[string]bool{}
	for _, a := range cat.Apps {
		switch {
		case !idRule.MatchString(a.ID):
			return fmt.Errorf("app id %q: lower-case letters, digits and hyphens only", a.ID)
		case ids[a.ID]:
			return fmt.Errorf("app id %q appears twice", a.ID)
		case strings.TrimSpace(a.Name) == "":
			return fmt.Errorf("%s: no name", a.ID)
		case !cats[a.Category]:
			return fmt.Errorf("%s: category %q is not in the catalogue's list", a.ID, a.Category)
		case a.Color != "" && !colourRule.MatchString(a.Color):
			return fmt.Errorf("%s: colour %q is not #RRGGBB", a.ID, a.Color)
		case len(a.Lists) == 0 && len(a.Domains) == 0 && len(a.ASNs) == 0 && len(a.Ports) == 0:
			return fmt.Errorf("%s: nothing can identify it (no list, domain, AS number or port)", a.ID)
		}
		for _, p := range a.Ports {
			if (p.Proto != "tcp" && p.Proto != "udp") || p.From < 1 || p.To > 65535 || p.From > p.To {
				return fmt.Errorf("%s: port rule %+v", a.ID, p)
			}
		}
		ids[a.ID] = true
	}
	return nil
}

func checkOutputs(cat catalogue, hash string) error {
	if err := validate(cat); err != nil {
		return err
	}
	raw, err := os.ReadFile(outRel)
	if err != nil {
		return err
	}
	var b bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return fmt.Errorf("%s: %w", outRel, err)
	}
	if b.CatalogueSHA256 != hash {
		return fmt.Errorf("%s was made from a different %s: run cmd/appgen with the pinned sources", outRel, catalogueRel)
	}
	if len(b.Apps) != len(cat.Apps) {
		return fmt.Errorf("%s holds %d apps, the catalogue %d", outRel, len(b.Apps), len(cat.Apps))
	}
	owner := map[string]string{}
	wantIcons := map[string]bool{}
	for i, a := range b.Apps {
		src := cat.Apps[i]
		if a.ID != src.ID || a.Name != src.Name || a.Category != src.Category {
			return fmt.Errorf("%s entry %d is %s, the catalogue's is %s", outRel, i, a.ID, src.ID)
		}
		if !colourRule.MatchString(a.Color) {
			return fmt.Errorf("%s: colour %q", a.ID, a.Color)
		}
		if a.Dark != tooDark(a.Color) {
			return fmt.Errorf("%s: dark=%v disagrees with its colour %s", a.ID, a.Dark, a.Color)
		}
		if len(a.Domains) == 0 && len(a.ASNs) == 0 && len(a.Ports) == 0 {
			return fmt.Errorf("%s: every domain it had was claimed by an earlier app; it can never match", a.ID)
		}
		for _, d := range a.Domains {
			if o, dup := owner[d]; dup {
				return fmt.Errorf("domain %s is claimed by both %s and %s", d, o, a.ID)
			}
			owner[d] = a.ID
		}
		switch a.Logo {
		case "mask", "image":
			wantIcons[a.ID] = true
		case "":
		default:
			return fmt.Errorf("%s: logo kind %q", a.ID, a.Logo)
		}
		if (a.Logo == "") != (src.Icon == nil) {
			return fmt.Errorf("%s: the catalogue's logo and the bundle's disagree", a.ID)
		}
	}
	files, _ := filepath.Glob(filepath.Join(iconsRel, "*.svg"))
	have := map[string]bool{}
	for _, p := range files {
		id := strings.TrimSuffix(filepath.Base(p), ".svg")
		have[id] = true
		if !wantIcons[id] {
			return fmt.Errorf("%s has no app", p)
		}
		svg, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !svgOpen.Match(svg) || strings.Contains(strings.ToLower(string(svg)), "<script") {
			return fmt.Errorf("%s is not a clean SVG", p)
		}
	}
	var missing []string
	for id := range wantIcons {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return fmt.Errorf("logos missing from %s: %v", iconsRel, missing)
	}
	return nil
}
