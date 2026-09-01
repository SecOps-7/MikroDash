package verify

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	attrUnshipped = "rendered by a page that is not in PORTED, so nothing binds it yet"
	attrMarkup    = "in extracted markup for a feature this port has not taken on; nothing renders " +
		"or reads it here, and the markup is verbatim so it cannot simply be deleted"
)

// attrsExpectedUnread: `data-` attributes the port renders and nothing reads.
//
// Each needs a reason, and an entry that starts being read becomes a failure --
// otherwise the list slowly becomes an excuse rather than a record.
var attrsExpectedUnread = map[string]string{
	"alert-id": "rendered by the live bell too and read by nothing there either; kept as the " +
		"row handle rather than invented later",
	"router-id":       attrUnshipped,
	"bulk":            attrMarkup,
	"role-preset":     attrMarkup,
	"val":             attrMarkup,
	"res-add-dynamic": attrMarkup,
	"sev":             attrMarkup,
	"unit-for":        attrMarkup,
}

// TestRenderedAttributesAreRead: a `data-` attribute the port writes into the DOM
// is read by something -- TypeScript, or a stylesheet selector.
//
// An attribute nothing reads is a hook to nowhere: the markup renders, no test
// notices, and whatever it was meant to drive silently does not happen.
func TestRenderedAttributesAreRead(t *testing.T) {
	root := repoRoot(t)

	tsFiles := readFiles(t, root, "web/src/", func(r string) bool { return hasExt(r, ".ts") })
	ts := joined(tsFiles)
	html := joined(readFiles(t, root, "web/src/ui/", func(r string) bool { return hasExt(r, ".html") }))
	css := mustRead(t, filepath.Join(root, "web", "public", "app.css"))

	rendered := map[string]bool{}
	for _, src := range []string{ts, html} {
		for _, m := range regexp.MustCompile(`\bdata-([a-z][a-z0-9-]*)\s*=`).FindAllStringSubmatch(src, -1) {
			rendered[m[1]] = true
		}
	}
	// A bare attribute, no value: `<td data-wrap>`.
	for _, m := range regexp.MustCompile(`\bdata-([a-z][a-z0-9-]*)(?:["'\s>])`).FindAllStringSubmatch(ts, -1) {
		rendered[m[1]] = true
	}

	read := map[string]bool{}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`['"]data-([a-z0-9-]+)['"]`),
		regexp.MustCompile(`\[data-([a-z0-9-]+)[\]=^]`),
		regexp.MustCompile(`dataset\.([a-zA-Z0-9]+)`),
	} {
		for _, m := range re.FindAllStringSubmatch(ts, -1) {
			read[m[1]] = true
		}
	}
	styled := map[string]bool{}
	for _, m := range regexp.MustCompile(`\[data-([a-z0-9-]+)`).FindAllStringSubmatch(css, -1) {
		styled[m[1]] = true
	}

	// FLOORS. Both halves are regex-found, and a pattern that stopped matching
	// would leave this comparing two empty sets and passing.
	if len(rendered) < 60 {
		t.Fatalf("only %d rendered attributes found — the scan broke", len(rendered))
	}
	if len(read) < 30 {
		t.Fatalf("only %d read attributes found — the scan broke", len(read))
	}

	// `dataset.fooBar` is how TypeScript reads `data-foo-bar`.
	isRead := func(a string) bool {
		return read[a] || read[dashToCamel(a)] || styled[a]
	}

	var unread []string
	for a := range rendered {
		if !isRead(a) {
			unread = append(unread, a)
		}
	}
	sort.Strings(unread)

	have := map[string]bool{}
	for _, a := range unread {
		have[a] = true
		if _, ok := attrsExpectedUnread[a]; !ok {
			t.Errorf("data-%s is rendered and nothing reads it — wire it up, or record why it "+
				"cannot be", a)
		}
	}
	for a := range attrsExpectedUnread {
		if !have[a] {
			t.Errorf("data-%s is recorded as unread, but something reads it now — delete the "+
				"entry rather than leaving a note that has stopped being true", a)
		}
	}
	t.Logf("%d rendered attributes, %d read, %d recorded as unread",
		len(rendered), len(read), len(unread))
}

func dashToCamel(s string) string {
	parts := strings.Split(s, "-")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}
