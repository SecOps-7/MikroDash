package verify

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"mikrodash/internal/i18n"
)

// TestTranslationsKeepUpWithTheInterface is the drift check #94's review asked
// for: every catalog in web/locales, against the text the interface shows now.
//
//   - STALE (fails): a translation of text that no longer exists anywhere. It
//     is harmless on the page and dead in the file, and it is how a renamed
//     label ("Collection Method" becoming "Collection method") would otherwise
//     go unnoticed.
//   - BROKEN (fails): a translation that loses or invents a {placeholder} or an
//     inline tag, which would print "{n}" or drop the emphasis.
//   - UNTRANSLATED (reported, never a failure): the English still showing. The
//     review's point was that visibility is the value; a new English string
//     must not block a change.
//
// And for the code: a t() whose first argument is not a plain quoted string
// fails, because a variable passed to t() is how router data would reach a
// catalog, and a concatenation cannot be a key.
func TestTranslationsKeepUpWithTheInterface(t *testing.T) {
	root := repoRoot(t)
	src, bad, err := i18n.Sources(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(src) < 1000 {
		t.Fatalf("only %d source strings found; the extraction has stopped seeing the interface", len(src))
	}
	for f, lines := range bad {
		t.Errorf("%s: t() on lines %v is not given a plain quoted string. Pass the English as a "+
			"literal and put values in {placeholders}: t('{n} devices', { n })", f, lines)
	}
	source := make(map[string]bool, len(src))
	for k := range src {
		source[k] = true
	}
	cats, err := i18n.Locales(filepath.Join(root, "web", "locales"))
	if err != nil {
		t.Fatal(err)
	}
	codes := make([]string, 0, len(cats))
	for c := range cats {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, code := range codes {
		p := i18n.Check(cats[code], source)
		for _, k := range p.Stale {
			t.Errorf("%s: %q translates text the interface no longer has; remove it, or move it "+
				"to the new wording", code, k)
		}
		for _, k := range p.Broken {
			t.Errorf("%s: the translation of %q loses or adds a {placeholder} or a tag", code, k)
		}
		t.Logf("%s (%s): %d of %d strings untranslated", code, cats[code].Name(code), p.Untranslated, len(source))
	}
	if len(codes) == 0 {
		t.Logf("no translations yet: %d source strings", len(source))
	}
}

// The check itself must be able to fail: each of its three failures, on a
// catalog built to have it, with a clean catalog as the control.
func TestTheTranslationCheckCanFail(t *testing.T) {
	source := map[string]bool{"Save": true, "{n} devices": true}
	if p := i18n.Check(i18n.Catalog{"Save": "保存", "{n} devices": "{n} 台设备"}, source); len(p.Stale)+len(p.Broken) != 0 {
		t.Fatalf("control: a clean catalog failed: %+v", p)
	}
	if p := i18n.Check(i18n.Catalog{"Old label": "旧"}, source); len(p.Stale) != 1 {
		t.Error("a stale key was not caught")
	}
	if p := i18n.Check(i18n.Catalog{"{n} devices": "设备"}, source); len(p.Broken) != 1 {
		t.Error("a lost placeholder was not caught")
	}
	if _, bad := i18n.TSLiterals("x = t(hostName);"); len(bad) != 1 {
		t.Error("t() given a variable was not caught")
	}
}

// tlCallers is the ledger of files allowed to call tl(): the ones that render
// labels declared in Go. tl() takes a variable by design, so the only thing
// keeping router data out of it is where it is called, and this list is that.
var tlCallers = map[string]string{
	"web/src/resource.ts":   "the resource forms: field labels and help, action labels, the resource's name",
	"web/src/pages/area.ts": "the generated pages: tab titles, nav entries, column headers",
	"web/src/main.ts":       "the page header's title",
}

// TestOnlyTheLabelRenderersCallTL holds the ledger in both directions: a file
// outside it that calls tl() fails, and so does a file in it that no longer
// does, because a stale entry is an allowance nobody is using.
func TestOnlyTheLabelRenderersCallTL(t *testing.T) {
	root := repoRoot(t)
	seen := map[string]bool{}
	err := filepath.WalkDir(filepath.Join(root, "web", "src"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "gen" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".ts") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if i18n.TLCalls(string(b)) == 0 {
			return nil
		}
		seen[rel] = true
		if _, ok := tlCallers[rel]; !ok {
			t.Errorf("%s calls tl(), which is only for labels declared in Go. Text written in "+
				"this file goes through t('literal'); if it really renders Go labels, add it to "+
				"tlCallers with the reason", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for f := range tlCallers {
		if !seen[f] {
			t.Errorf("tlCallers lists %s, which no longer calls tl(); remove the entry", f)
		}
	}
}
