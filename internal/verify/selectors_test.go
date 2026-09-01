package verify

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// selectorTags are real HTML element names. A bare tag in a selector is only
// interesting when it is NOT one of these, because then it is a typo or a custom
// element nothing renders.
var selectorTags = map[string]bool{
	"tr": true, "td": true, "th": true, "thead": true, "tbody": true, "title": true,
	"path": true, "svg": true, "g": true, "option": true, "input": true, "select": true,
	"button": true, "a": true, "li": true, "ul": true, "div": true, "span": true,
	"canvas": true, "form": true, "label": true, "text": true,
}

type selToken struct{ kind, name string }

// TestSelectorsMatchSomethingThePortProduces: every class, id, attribute and tag
// the port queries for is something the port itself renders.
//
// A selector that matches nothing returns null or an empty list, and the code
// around it does nothing at all -- no error, no warning. It is the single most
// common way a rename half-lands: the markup is updated, one query is not, and
// the feature quietly stops working on one page.
func TestSelectorsMatchSomethingThePortProduces(t *testing.T) {
	root := repoRoot(t)

	tsFiles := readFiles(t, root, "web/src/", func(r string) bool { return hasExt(r, ".ts") })
	html := joined(readFiles(t, root, "web/src/ui/", func(r string) bool { return hasExt(r, ".html") }))

	call := `(?:querySelectorAll|querySelector|closest|matches)(?:<[^>]*>)?`
	selRe := regexp.MustCompile(call + `\(\s*'([^']*)'`)
	strip := regexp.MustCompile(call + `\(\s*'[^']*'`)
	blockC := regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineC := regexp.MustCompile(`(^|[^:])//[^\n]*`)

	// THE HAYSTACK EXCLUDES THE SELECTORS THEMSELVES. Otherwise a selector would
	// prove its own token exists simply by being written down, and this check
	// could never fail.
	var hayParts []string
	selectorsByFile := map[string][]string{}
	for rel, body := range tsFiles {
		clean := lineC.ReplaceAllString(blockC.ReplaceAllString(body, " "), "$1")
		for _, m := range selRe.FindAllStringSubmatch(clean, -1) {
			selectorsByFile[rel] = append(selectorsByFile[rel], m[1])
		}
		hayParts = append(hayParts, strip.ReplaceAllString(clean, "Q("))
	}
	hay := strings.Join(hayParts, "\n") + "\n" + html

	total := 0
	for _, sels := range selectorsByFile {
		total += len(sels)
	}
	if total < 80 {
		t.Fatalf("only %d selectors found — the scan broke", total)
	}

	answered := func(tok selToken) bool {
		switch tok.kind {
		case "class":
			return regexp.MustCompile(`[\s"'`+"`"+`.]`+regexp.QuoteMeta(tok.name)+`[\s"'`+"`"+`,{:.\[]`).MatchString(hay) ||
				strings.Contains(hay, "."+tok.name)
		case "attr":
			// FOUR WAYS AN ATTRIBUTE GETS WRITTEN, and all four count: the
			// literal `x="…"`, a boolean with no `=`, `setAttribute('x', …)`,
			// and the `dataset.xY = …` property form, which produces the
			// attribute at runtime with the literal appearing nowhere.
			// Requiring only the first accused four working code paths on this
			// check's first run, which is how a check gets ignored.
			if strings.Contains(hay, tok.name+"=") || strings.Contains(hay, "["+tok.name) {
				return true
			}
			if regexp.MustCompile(`\s` + regexp.QuoteMeta(tok.name) + `[\s>"'` + "`" + `]`).MatchString(hay) {
				return true
			}
			if regexp.MustCompile(`setAttribute\(\s*['"` + "`" + `]` + regexp.QuoteMeta(tok.name) + `['"` + "`" + `]`).MatchString(hay) {
				return true
			}
			camel := dashToCamel(strings.TrimPrefix(tok.name, "data-"))
			// AN ASSIGNMENT, NOT A READ. `row.dataset.ruleId !== …` reads what
			// the template should have written; counting that as proof it WAS
			// written lets the template drop it entirely.
			return regexp.MustCompile(`dataset\.`+camel+`\s*=(?:[^=]|$)`).MatchString(hay) ||
				regexp.MustCompile(`dataset\['`+regexp.QuoteMeta(tok.name)+`'\]\s*=(?:[^=]|$)`).MatchString(hay)
		case "id":
			return strings.Contains(hay, `"`+tok.name+`"`) ||
				strings.Contains(hay, `'`+tok.name+`'`) ||
				strings.Contains(hay, `id="`+tok.name)
		default:
			return strings.Contains(hay, "<"+tok.name)
		}
	}

	var missing []string
	for rel, sels := range selectorsByFile {
		for _, sel := range sels {
			for _, tok := range selectorTokens(sel) {
				if !answered(tok) {
					missing = append(missing, rel+": "+sel+" — no "+tok.kind+" "+tok.name)
				}
			}
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("selector matches nothing the port produces: %s", m)
	}
	t.Logf("%d selectors across %d modules, every token produced by the page",
		total, len(selectorsByFile))
}

// selectorTokens splits a CSS selector into the things that must exist.
func selectorTokens(sel string) []selToken {
	var out []selToken
	// Attribute VALUES are masked: `[data-x="tr"]` must not look like a `tr` tag.
	masked := regexp.MustCompile(`=\s*("[^"]*"|'[^']*')`).ReplaceAllString(sel, "=_")
	for _, part := range regexp.MustCompile(`[\s>+~,]+`).Split(masked, -1) {
		if part == "" {
			continue
		}
		if inner := regexp.MustCompile(`:not\(([^)]*)\)`).FindStringSubmatch(part); inner != nil {
			out = append(out, selectorTokens(inner[1])...)
		}
		base := regexp.MustCompile(`:[a-z-]+(\([^)]*\))?`).ReplaceAllString(part, "")
		for _, m := range regexp.MustCompile(`\.([A-Za-z][\w-]*)`).FindAllStringSubmatch(base, -1) {
			out = append(out, selToken{"class", m[1]})
		}
		for _, m := range regexp.MustCompile(`\[([A-Za-z][\w-]*)`).FindAllStringSubmatch(base, -1) {
			out = append(out, selToken{"attr", m[1]})
		}
		for _, m := range regexp.MustCompile(`#([A-Za-z][\w-]*)`).FindAllStringSubmatch(base, -1) {
			out = append(out, selToken{"id", m[1]})
		}
		if tag := regexp.MustCompile(`^([a-z]+)`).FindStringSubmatch(base); tag != nil &&
			!selectorTags[tag[1]] {
			out = append(out, selToken{"tag", tag[1]})
		}
	}
	return out
}
