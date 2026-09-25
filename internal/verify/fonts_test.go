package verify

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// EVERY SHIPPED FONT FAMILY IS CREDITED, COUNTED AND OFFERED - AND NOTHING ELSE IS.
//
// ── WHY THIS EXISTS ────────────────────────────────────────────────────────
//
// The About page's Fonts row read "JetBrains Mono, Oxanium" long after
// twenty-six families shipped. It was wrong by twenty-four and nothing failed,
// because until now the string `woff2` appeared NOWHERE in this repository: not
// in a test, not in a build script, not in a fetch tool. The bundle was
// described in four places and measured in none.
//
// ── THE ONE THAT MATTERS IS NOT THE PAGE ───────────────────────────────────
//
// A stale count on a settings tab is untidy. A family shipped without its
// copyright block in `OFL.txt` is an OFL section 2 breach, because the licence
// requires the notice to travel with the files. That is the direction this test
// exists for; the other three come almost free once the family set is derived.
//
// ── ALL FOUR FAIL IN BOTH DIRECTIONS ───────────────────────────────────────
//
// A family added to disk and not credited fails. A credit for a family no
// longer shipped fails too - a stale attribution reads as a considered one, and
// the next person adds to the list rather than auditing it. The same holds for
// the counts and for the picker.

// fontDirsRel are the two places fonts ship. BOTH: Syne ships only from the
// second, so reading `web/public/fonts` alone reports 25 where 26 ship.
var fontDirsRel = []string{
	filepath.Join("web", "public", "fonts"),
	filepath.Join("web", "public", "vendor", "fonts"),
}

// reWeight strips a NUMERIC weight suffix: `dm-sans-400` is the family
// `dm-sans`, and the suffix must be digits or `dm-sans` itself collapses to
// `dm`.
var reWeight = regexp.MustCompile(`-[0-9]+$`)

// shippedFamilies is the family set derived from the files themselves, which is
// the only source here that cannot be wrong about what ships.
func shippedFamilies(t *testing.T) map[string]bool {
	t.Helper()
	root := repoRoot(t)
	fam := map[string]bool{}
	for _, dir := range fontDirsRel {
		names, err := filepath.Glob(filepath.Join(root, dir, "*.woff2"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		for _, n := range names {
			fam[reWeight.ReplaceAllString(strings.TrimSuffix(filepath.Base(n), ".woff2"), "")] = true
		}
	}
	// AN EMPTY SCAN IS A BROKEN SCAN, and an empty set would agree that every
	// credit is stale while reporting nothing uncredited.
	if len(fam) < 5 {
		t.Fatalf("found %d font famil(ies) on disk - this scan has broken, and an empty "+
			"result agrees with every assertion below", len(fam))
	}
	return fam
}

// reCreditedFamily finds a family heading in OFL.txt: a line with no indent
// whose NEXT line is an indented `Copyright`. That pairing is what distinguishes
// the twenty-six headings from the licence prose below them, which a section
// scan does not - the prose is full of lines beginning with a capital.
func creditedFamilies(t *testing.T) map[string]bool {
	t.Helper()
	src := mustRead(t, filepath.Join(repoRoot(t), "web", "public", "fonts", "OFL.txt"))
	lines := strings.Split(src, "\n")
	out := map[string]bool{}
	for i := 0; i+1 < len(lines); i++ {
		head, next := lines[i], lines[i+1]
		if head == "" || strings.HasPrefix(head, " ") || strings.HasPrefix(head, "\t") {
			continue
		}
		if !strings.HasPrefix(strings.TrimLeft(next, " \t"), "Copyright") ||
			next == strings.TrimLeft(next, " \t") {
			continue
		}
		out[strings.ReplaceAll(strings.ToLower(strings.TrimSpace(head)), " ", "-")] = true
	}
	if len(out) < 5 {
		t.Fatalf("parsed %d credit(s) from OFL.txt - this scan has broken", len(out))
	}
	return out
}

// credits matches a file name against a heading. THE HEADING IS THE LONGER
// NAME: the files say `plus-jakarta` and `source-sans` where the foundries say
// "Plus Jakarta Sans" and "Source Sans 3", so the disk name is a PREFIX of the
// credited one, never the reverse.
func credits(heading, family string) bool { return strings.HasPrefix(heading, family) }

func TestEveryShippedFontFamilyIsCredited(t *testing.T) {
	shipped, credited := shippedFamilies(t), creditedFamilies(t)

	var uncredited []string
	for f := range shipped {
		found := false
		for h := range credited {
			if credits(h, f) {
				found = true
				break
			}
		}
		if !found {
			uncredited = append(uncredited, f)
		}
	}
	var stale []string
	for h := range credited {
		found := false
		for f := range shipped {
			if credits(h, f) {
				found = true
				break
			}
		}
		if !found {
			stale = append(stale, h)
		}
	}
	sort.Strings(uncredited)
	sort.Strings(stale)

	if len(uncredited) > 0 {
		t.Errorf("%v ship under web/public/fonts or web/public/vendor/fonts and have NO "+
			"copyright block in OFL.txt - the OFL requires the notice to accompany the "+
			"files, so this is a licence breach, not an untidy page", uncredited)
	}
	if len(stale) > 0 {
		t.Errorf("OFL.txt credits %v, which no longer ship - a stale attribution reads as a "+
			"considered one, and the next person adds to the list rather than auditing it", stale)
	}
	t.Logf("%d famil(ies) shipped, %d credited", len(shipped), len(credited))
}

// reNoticeCount reads `(25 families, 98 files)` out of the notices' Fonts
// section. Those two numbers are the only measurable claim in that file.
var reNoticeCount = regexp.MustCompile(`\((\d+) families, (\d+) files\)`)

func TestTheNoticesFontCountIsTrue(t *testing.T) {
	root := repoRoot(t)
	src := mustRead(t, filepath.Join(root, "THIRD_PARTY_NOTICES.md"))
	_, rest, ok := strings.Cut(src, "## Fonts")
	if !ok {
		t.Fatal("THIRD_PARTY_NOTICES.md has no `## Fonts` section - this scan cannot read " +
			"its subject and must say so rather than pass")
	}
	section, _, _ := strings.Cut(rest, "\n## ")
	m := reNoticeCount.FindStringSubmatch(section)
	if m == nil {
		t.Fatal("the Fonts section no longer states `(N families, M files)` - either restore " +
			"the claim or delete this check deliberately, but do not leave it matching nothing")
	}

	// THE PRIMARY DIRECTORY ONLY, because that is what the sentence counts; the
	// vendored three are named rather than counted, right beside it.
	names, err := filepath.Glob(filepath.Join(root, "web", "public", "fonts", "*.woff2"))
	if err != nil || len(names) == 0 {
		t.Fatalf("globbed %d file(s) in web/public/fonts: %v", len(names), err)
	}
	fam := map[string]bool{}
	for _, n := range names {
		fam[reWeight.ReplaceAllString(strings.TrimSuffix(filepath.Base(n), ".woff2"), "")] = true
	}

	wantFam, _ := strconv.Atoi(m[1])
	wantFiles, _ := strconv.Atoi(m[2])
	if wantFam != len(fam) || wantFiles != len(names) {
		t.Errorf("THIRD_PARTY_NOTICES.md says web/public/fonts holds (%d families, %d files); "+
			"it holds (%d, %d). A number in a notices file that nothing re-measures is how "+
			"every expired premise here started", wantFam, wantFiles, len(fam), len(names))
	}
}

// TestTheFontPickerOffersWhatShips ties the branding picker to the files.
//
// A selectable family with no file renders as the fallback and looks like a
// bug in the theme; a shipped family nobody can select is dead weight in the
// image. Neither is visible without comparing the two lists.
func TestTheFontPickerOffersWhatShips(t *testing.T) {
	shipped := shippedFamilies(t)
	raw := mustRead(t, filepath.Join(repoRoot(t), "testdata", "appearance-tables.json"))
	var tables struct {
		Fonts []struct {
			ID string `json:"id"`
		} `json:"fonts"`
	}
	if err := json.Unmarshal([]byte(raw), &tables); err != nil {
		t.Fatalf("appearance-tables.json: %v", err)
	}
	if len(tables.Fonts) == 0 {
		t.Fatal("no fonts parsed from appearance-tables.json - this scan has broken")
	}

	// `system` is the one id with no file BY DESIGN: it is the browser's own
	// stack, which is why it is named here rather than inferred.
	const noFile = "system"

	offered := map[string]bool{}
	var missingFile []string
	for _, f := range tables.Fonts {
		offered[f.ID] = true
		if f.ID != noFile && !shipped[f.ID] {
			missingFile = append(missingFile, f.ID)
		}
	}
	var notOffered []string
	for f := range shipped {
		if !offered[f] {
			notOffered = append(notOffered, f)
		}
	}
	sort.Strings(missingFile)
	sort.Strings(notOffered)

	if len(missingFile) > 0 {
		t.Errorf("the branding picker offers %v, which ship no .woff2 - choosing one renders "+
			"the fallback, which reads as a broken theme rather than a missing file",
			missingFile)
	}
	if len(notOffered) > 0 {
		t.Errorf("%v ship but the branding picker does not offer them - bytes in the image "+
			"nobody can select", notOffered)
	}
}
