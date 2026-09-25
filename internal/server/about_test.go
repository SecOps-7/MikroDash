package server

import (
	"strings"
	"testing"
)

// THE CHANGELOG IS PARSED, NOT RENDERED AS MARKDOWN.
//
// The About page shows a scannable list, so the parser's job is to find the
// releases, their sections and their top-level bullets, and to drop everything
// that is detail about something else. Every case below is a way to get a list
// that looks right and says something the changelog did not.
func TestTheChangelogParsesIntoReleases(t *testing.T) {
	src := strings.Join([]string{
		"# Changelog",
		"",
		"All notable changes will be documented in this file.",
		"",
		"## [0.8.68] - Traffic history per interface",
		"",
		"### New",
		"",
		"- **Traffic history (#59).** Click an interface and see its traffic, in the",
		"  dialog that already opens. **Live** (last 60 seconds) and **30 min** come",
		"  from the page's own stream.",
		"  - A **Record** switch on each interface.",
		"  - Totals and peak for the range.",
		"- A [linked thing](https://example.invalid/x) in a bullet.",
		"",
		"### Fixed",
		"",
		"- The `smtpPort` was read as a string.",
		"",
		"## [0.8.1] - 2026-09-01",
		"",
		"### Internal",
		"",
		"- Cutover.",
	}, "\n")

	rels := parseChangelog(src)
	if len(rels) != 2 {
		t.Fatalf("parsed %d releases, want 2: %+v", len(rels), rels)
	}

	// ── THE HEADING IS A TITLE OR A DATE, AND THEY ARE DIFFERENT THINGS ───
	//
	// The file uses both shapes. Reading a title as a date puts
	// "Traffic history per interface" in the column where every other row shows
	// a day, which is the sort of wrong that looks like a layout bug.
	if rels[0].Version != "0.8.68" || rels[0].Title != "Traffic history per interface" ||
		rels[0].Date != "" {
		t.Errorf("first release parsed as %+v", rels[0])
	}
	if rels[1].Version != "0.8.1" || rels[1].Date != "2026-09-01" || rels[1].Title != "" {
		t.Errorf("a dated heading parsed as %+v", rels[1])
	}

	// ── INDENTED BULLETS ARE DETAIL, NOT CHANGES ─────────────────────────
	//
	// "A Record switch on each interface" qualifies the line above it. Promoted
	// to a sibling it reads as a separate change that was never announced, and
	// the count beside the version says four where the release had three.
	if len(rels[0].Entries) != 3 {
		t.Fatalf("first release has %d entries, want 3 (two New, one Fixed): %+v",
			len(rels[0].Entries), rels[0].Entries)
	}

	// The section heading becomes the badge, so a bullet under `### Fixed` must
	// not carry `New`.
	kinds := []string{rels[0].Entries[0].Kind, rels[0].Entries[1].Kind, rels[0].Entries[2].Kind}
	if kinds[0] != "New" || kinds[1] != "New" || kinds[2] != "Fixed" {
		t.Errorf("entry kinds are %v, want [New New Fixed]", kinds)
	}

	// ── MARKDOWN IS STRIPPED, BECAUSE THE PAGE RENDERS TEXT ──────────────
	//
	// The bullets reach the browser through `esc()` into a text node, so any
	// markup left here is shown literally: asterisks, backticks and a raw URL
	// in the middle of a sentence.
	first := rels[0].Entries[0].Text
	if strings.Contains(first, "*") || !strings.HasPrefix(first, "Traffic history") {
		t.Errorf("bold was not stripped: %q", first)
	}

	// ── A WRAPPED BULLET IS ONE BULLET ───────────────────────────────────
	//
	// THE BUG THIS EXISTS FOR, WHICH SHIPPED TO A BROWSER. The changelog wraps
	// at column 96, and a continuation line is indented exactly like a nested
	// bullet. Dropping it left every entry on the About page ending mid
	// sentence, and nothing upstream complained: the payload was valid JSON
	// and each string was plausible prose. Only the rendered page showed it.
	want := "Traffic history (#59). Click an interface and see its traffic, in the " +
		"dialog that already opens. Live (last 60 seconds) and 30 min come " +
		"from the page's own stream."
	if first != want {
		t.Errorf("a wrapped bullet did not come back whole:\n got %q\nwant %q", first, want)
	}
	// The continuation must be JOINED, not concatenated: the line break stood
	// for a space, and losing it welds two words together.
	if strings.Contains(first, "thedialog") {
		t.Error("lines were joined with no space")
	}
	if got := rels[0].Entries[1].Text; strings.Contains(got, "http") ||
		!strings.Contains(got, "A linked thing in a bullet") {
		t.Errorf("a link kept its URL instead of its label: %q", got)
	}
	if got := rels[0].Entries[2].Text; strings.Contains(got, "`") {
		t.Errorf("backticks survived: %q", got)
	}
}

// AN ABSENT CHANGELOG IS AN EMPTY LIST, NOT A PANIC.
//
// The file ships in the image, so its absence is a packaging fault. The page
// says so; the parser simply has nothing to return.
func TestAnAbsentChangelogParsesToNothing(t *testing.T) {
	if got := parseChangelog(""); len(got) != 0 {
		t.Errorf("an empty changelog produced %d releases", len(got))
	}
	// A file with prose and no releases is the same case.
	if got := parseChangelog("# Changelog\n\nNothing yet.\n"); len(got) != 0 {
		t.Errorf("a changelog with no releases produced %d", len(got))
	}
}

// THE BUILD STAMPS ARE EMPTY ON A LOCAL BUILD, and the page drops what is
// empty. This pins that they are variables the linker can set rather than
// constants, because `-X` cannot write a constant and the failure is silent:
// the flag is accepted and the value never changes.
func TestTheBuildStampsAreLinkerWritable(t *testing.T) {
	old := BuildCommit
	defer func() { BuildCommit = old }()
	BuildCommit = "deadbee"
	if BuildCommit != "deadbee" {
		t.Error("BuildCommit is not assignable, so -ldflags -X cannot set it")
	}
}
