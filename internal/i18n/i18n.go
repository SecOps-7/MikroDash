// Package i18n is MikroDash's interface translation (#94): finding the text in
// the markup that a translator translates, putting translations in its place,
// and checking a catalog against the source.
//
// ── KEYED BY THE ENGLISH ─────────────────────────────────────────────────────
//
// A catalog is `web/locales/<lang>.json`, `{"English": "translation"}`, gettext
// style: the source text IS the key, so markup and code stay readable English
// and nobody invents key names. A string with no translation stays English, so
// a catalog that falls behind the source degrades, it never breaks.
//
// ── ONE SCANNER, FOR EXTRACTING AND FOR TRANSLATING ─────────────────────────
//
// `Units` and `Translate` walk the markup with the same scanner, so the text a
// translator is shown and the text that gets replaced cannot drift apart. The
// invariant that makes the build safe: translating with an empty catalog
// returns the markup byte for byte (pinned against every page).
//
// ── WHAT IS TEXT, AND WHAT IS NOT ───────────────────────────────────────────
//
//   - A RUN of text and inline tags (strong, em, code, br …) is one unit, tags
//     included, so a translator can reorder a sentence around its emphasis.
//     Any other tag ends the run: a <span id> is a slot the code writes into,
//     and moving it would break the page.
//   - The title, placeholder, aria-label and alt attributes are units.
//   - Nothing inside <script>, <style>, <pre> or <textarea>, or inside an
//     element marked translate="no" (the HTML standard's own attribute) is
//     touched. Router data never reaches markup source at all: it is written
//     by code at run time, and code translates only what it passes to t().
package i18n

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// A Unit is one translatable piece of markup.
type Unit struct {
	// Key is the text as a translator sees it: entities decoded, whitespace
	// collapsed, inline tags kept.
	Key string
	// Attr is "attr" for an attribute value, "" for text.
	Attr string
}

// inline tags stay inside a unit; any other tag ends it.
var inline = map[string]bool{"strong": true, "b": true, "em": true, "i": true, "br": true,
	"kbd": true, "code": true, "small": true, "u": true}

// skipped elements' content is never text to translate.
var skipped = map[string]bool{"script": true, "style": true, "pre": true, "textarea": true}

// The attributes that carry words.
var attrRe = regexp.MustCompile(`(\s(?:title|placeholder|aria-label|alt)\s*=\s*)("([^"]*)"|'([^']*)')`)

var letter = regexp.MustCompile(`[A-Za-z]`)
var space = regexp.MustCompile(`\s+`)

// Collapse is how a key is normalised: entities decoded, runs of ASCII
// whitespace one space, trimmed.
//
// ONLY ASCII IS TRIMMED, to match what Translate puts back around a unit.
// strings.TrimSpace also trims a non-breaking space, so a unit ending in
// &nbsp; lost it from its key and the replacement never restored it: found by
// the round-trip test on the Bandwidth page's "RX &nbsp; download".
func Collapse(s string) string {
	return strings.Trim(space.ReplaceAllString(html.UnescapeString(s), " "), " ")
}

type token struct {
	kind string // text, tag, raw (a comment)
	s    string
	name string // tag name, lower case, without a slash
	end  bool   // a closing tag
}

// tokenize splits markup into text, tags and comments. Tags are read with
// quotes respected, so a '>' inside an attribute value does not end one.
func tokenize(src string) []token {
	var out []token
	i := 0
	for i < len(src) {
		if strings.HasPrefix(src[i:], "<!--") {
			j := strings.Index(src[i+4:], "-->")
			if j < 0 {
				out = append(out, token{kind: "raw", s: src[i:]})
				break
			}
			out = append(out, token{kind: "raw", s: src[i : i+4+j+3]})
			i += 4 + j + 3
			continue
		}
		if src[i] == '<' && i+1 < len(src) && (isAlpha(src[i+1]) || src[i+1] == '/' || src[i+1] == '!') {
			j, q := i+1, byte(0)
			for j < len(src) {
				c := src[j]
				if q != 0 {
					if c == q {
						q = 0
					}
				} else if c == '"' || c == '\'' {
					q = c
				} else if c == '>' {
					break
				}
				j++
			}
			tag := src[i:min(j+1, len(src))]
			t := token{kind: "tag", s: tag}
			body := strings.TrimPrefix(tag[1:], "/")
			t.end = strings.HasPrefix(tag, "</")
			k := 0
			for k < len(body) && (isAlpha(body[k]) || (k > 0 && (body[k] == '-' || isDigit(body[k])))) {
				k++
			}
			t.name = strings.ToLower(body[:k])
			out = append(out, t)
			i = j + 1
			continue
		}
		j := strings.IndexByte(src[i+1:], '<')
		if j < 0 {
			out = append(out, token{kind: "text", s: src[i:]})
			break
		}
		out = append(out, token{kind: "text", s: src[i : i+1+j]})
		i += 1 + j
	}
	return out
}

func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isVoid(name string) bool {
	switch name {
	case "br", "img", "input", "meta", "link", "hr", "source", "wbr":
		return true
	}
	return false
}

func isSelfClosing(t token) bool { return isVoid(t.name) || strings.HasSuffix(t.s, "/>") }

// walk scans markup and calls text for every text run and attr for every
// attribute value, in order. Each returns the replacement for what it was given
// (or it unchanged); walk returns the markup rebuilt from them.
func walk(src string, text func(run string) string, attr func(val string) string) string {
	var b strings.Builder
	var run []token
	skipDepth, skipName := 0, ""
	// translate="no": the element's name and how deep inside it we are.
	noName, noDepth := "", 0

	flush := func() {
		if len(run) == 0 {
			return
		}
		var raw strings.Builder
		for _, t := range run {
			raw.WriteString(t.s)
		}
		s := raw.String()
		run = run[:0]
		switch {
		case noDepth > 0:
			b.WriteString(s)
		case balanced(s):
			b.WriteString(text(s))
		default:
			// Unbalanced inline tags cannot be one unit: translate the text
			// pieces alone, tags left where they are.
			for _, t := range tokenize(s) {
				if t.kind == "text" {
					b.WriteString(text(t.s))
				} else {
					b.WriteString(t.s)
				}
			}
		}
	}

	for _, t := range tokenize(src) {
		if skipDepth > 0 {
			b.WriteString(t.s)
			if t.kind == "tag" && t.name == skipName && !isSelfClosing(t) {
				if t.end {
					skipDepth--
				} else {
					skipDepth++
				}
			}
			continue
		}
		if t.kind == "text" || (t.kind == "tag" && inline[t.name] && !attrRe.MatchString(t.s) &&
			!strings.Contains(t.s, "translate=")) {
			run = append(run, t)
			continue
		}
		flush()
		if t.kind != "tag" {
			b.WriteString(t.s)
			continue
		}
		s := t.s
		if !t.end && noDepth == 0 {
			s = attrRe.ReplaceAllStringFunc(s, func(m string) string {
				sm := attrRe.FindStringSubmatch(m)
				val, quote := sm[3], `"`
				if strings.HasPrefix(sm[2], "'") {
					val, quote = sm[4], "'"
				}
				return sm[1] + quote + attr(val) + quote
			})
		}
		b.WriteString(s)
		switch {
		case isSelfClosing(t):
		case noDepth > 0 && t.name == noName:
			if t.end {
				noDepth--
			} else {
				noDepth++
			}
		case noDepth > 0:
		case skipped[t.name] && !t.end:
			skipDepth, skipName = 1, t.name
		case !t.end && strings.Contains(t.s, `translate="no"`):
			noName, noDepth = t.name, 1
		}
	}
	flush()
	return b.String()
}

// balanced reports whether a run's inline tags open and close in order.
func balanced(s string) bool {
	var stack []string
	for _, t := range tokenize(s) {
		if t.kind != "tag" || isSelfClosing(t) {
			continue
		}
		if !t.end {
			stack = append(stack, t.name)
			continue
		}
		if len(stack) == 0 || stack[len(stack)-1] != t.name {
			return false
		}
		stack = stack[:len(stack)-1]
	}
	return len(stack) == 0
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// Units is every translatable unit in markup, in order, duplicates included.
func Units(src string) []Unit {
	var out []Unit
	walk(src, func(run string) string {
		if k := Collapse(run); letter.MatchString(tagRe.ReplaceAllString(k, "")) {
			out = append(out, Unit{Key: k})
		}
		return run
	}, func(val string) string {
		if k := Collapse(val); letter.MatchString(k) {
			out = append(out, Unit{Key: k, Attr: "attr"})
		}
		return val
	})
	return out
}

// Translate puts a catalog's translations into markup. A unit with no
// translation is left exactly as it was, whitespace and entities included.
func Translate(src string, cat Catalog) string {
	if len(cat) == 0 {
		return src
	}
	return walk(src, func(run string) string {
		key := Collapse(run)
		tr := cat[key]
		if tr == "" {
			return run
		}
		lead := run[:len(run)-len(strings.TrimLeft(run, " \t\r\n"))]
		trail := run[len(strings.TrimRight(run, " \t\r\n")):]
		return lead + escapeKeepingTags(tr, tagRe.FindAllString(key, -1)) + trail
	}, func(val string) string {
		tr := cat[Collapse(val)]
		if tr == "" {
			return val
		}
		return html.EscapeString(tr)
	})
}

// escapeKeepingTags escapes a translation's text, passing through only the tags
// the SOURCE unit has (each as often as it has it). Anything else a translation
// carries is escaped and shows as text: a catalog is reviewed, but this is
// where it meets the page, so this is where the rule is enforced. Check reports
// the same mismatch before it gets here.
func escapeKeepingTags(s string, allowed []string) string {
	left := map[string]int{}
	for _, t := range allowed {
		left[t]++
	}
	var b strings.Builder
	last := 0
	for _, m := range tagRe.FindAllStringIndex(s, -1) {
		b.WriteString(html.EscapeString(s[last:m[0]]))
		if tag := s[m[0]:m[1]]; left[tag] > 0 {
			left[tag]--
			b.WriteString(tag)
		} else {
			b.WriteString(html.EscapeString(tag))
		}
		last = m[1]
	}
	b.WriteString(html.EscapeString(s[last:]))
	return b.String()
}

// Catalog is one language's translations. A key starting with "@" is about the
// catalog rather than a string in it: "@name" is the language's own name for
// itself ("简体中文"), which the language selectors show.
type Catalog map[string]string

// Name is the language's own name, or its code when the catalog gives none.
func (c Catalog) Name(code string) string {
	if n := c["@name"]; n != "" {
		return n
	}
	return code
}

// Strings is the catalog without its "@" entries.
func (c Catalog) Strings() map[string]string {
	out := make(map[string]string, len(c))
	for k, v := range c {
		if !strings.HasPrefix(k, "@") {
			out[k] = v
		}
	}
	return out
}

// Locales reads every catalog in dir (`<lang>.json`), by language code. The
// source list, `source.json`, is not a catalog. A missing dir is no languages.
func Locales(dir string) (map[string]Catalog, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	out := map[string]Catalog{}
	for _, f := range files {
		lang := strings.TrimSuffix(filepath.Base(f), ".json")
		if lang == "source" {
			continue
		}
		if !ValidLang(lang) || lang == "en" {
			return nil, fmt.Errorf("%s: %q is not a language this can serve (English is the source)", f, lang)
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var c Catalog
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		out[lang] = c
	}
	return out, nil
}

// langRe is a language code as the cookie and the file names carry it.
var langRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z][A-Za-z]{1,3})?$`)

// ValidLang reports whether s is shaped like a language code.
func ValidLang(s string) bool { return langRe.MatchString(s) }

var placeholderRe = regexp.MustCompile(`\{[a-zA-Z][a-zA-Z0-9]*\}`)

// Problems is what Check finds in one catalog against the source strings.
type Problems struct {
	// Stale keys translate text that no longer exists anywhere.
	Stale []string
	// Broken translations lose or invent a {placeholder} or an inline tag.
	Broken []string
	// Untranslated source strings: reported, never a failure.
	Untranslated int
}

// Check compares a catalog with the source strings.
func Check(cat Catalog, source map[string]bool) Problems {
	var p Problems
	for k, v := range cat.Strings() {
		if !source[k] {
			p.Stale = append(p.Stale, k)
			continue
		}
		if v != "" && (!sameSet(placeholderRe.FindAllString(k, -1), placeholderRe.FindAllString(v, -1)) ||
			!sameSet(tagRe.FindAllString(k, -1), tagRe.FindAllString(v, -1))) {
			p.Broken = append(p.Broken, k)
		}
	}
	for k := range source {
		if cat[k] == "" {
			p.Untranslated++
		}
	}
	sort.Strings(p.Stale)
	sort.Strings(p.Broken)
	return p
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// PickLang is the language to serve: the cookie's when it is one we have,
// otherwise the browser's first preference we have (by primary subtag, so
// zh-TW asks for Chinese and gets zh-CN when that is all there is), otherwise
// English, which is "". A cookie of "en" is a choice of English, and wins over
// the browser.
func PickLang(cookie, acceptLanguage string, available []string) string {
	if cookie == "en" {
		return ""
	}
	for _, l := range available {
		if l == cookie {
			return l
		}
	}
	for _, part := range strings.Split(acceptLanguage, ",") {
		tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if tag == "" || tag == "*" {
			continue
		}
		primary := strings.ToLower(strings.SplitN(tag, "-", 2)[0])
		if primary == "en" {
			return ""
		}
		for _, l := range available {
			if strings.EqualFold(l, tag) {
				return l
			}
		}
		for _, l := range available {
			if strings.ToLower(strings.SplitN(l, "-", 2)[0]) == primary {
				return l
			}
		}
	}
	return ""
}

// ── THE STRINGS THE CODE TRANSLATES ─────────────────────────────────────────

// tlCall finds a call of tl(, the door for labels declared in Go. Its argument
// is data by design, so what the gate checks is WHERE it is called.
var tlCall = regexp.MustCompile(`(^|[^A-Za-z0-9_$.])tl\(`)

// TLCalls is how many times the source calls tl(), comments, strings and the
// function's own definition aside.
func TLCalls(src string) int {
	_, code := blankComments(src)
	n := 0
	for _, m := range tlCall.FindAllStringIndex(code, -1) {
		if strings.HasSuffix(strings.TrimRight(code[:m[0]+1], " \t("), "function") {
			continue
		}
		n++
	}
	return n
}

// tCall finds a call of t( … and what its first argument begins with.
var tCall = regexp.MustCompile(`(^|[^A-Za-z0-9_$.])t\(\s*`)

// TSLiterals is every string a TypeScript source passes to t(), and the line
// of every t() call whose first argument is NOT a plain quoted literal. Those
// are refused: a variable is how router data would reach a catalog, and a
// template literal cannot be a key.
func TSLiterals(src string) (lits []string, bad []int) {
	// Calls are FOUND in `code`, where comments and the insides of strings are
	// blanked, so neither can look like a call ('http://t(x)' is a string);
	// each literal is then READ from the source at the same place.
	src, code := blankComments(src)
	for _, m := range tCall.FindAllStringIndex(code, -1) {
		i := m[1]
		line := strings.Count(src[:m[0]], "\n") + 1
		// A DEFINITION is not a call: `function t(` is where t() is written.
		if strings.HasSuffix(strings.TrimRight(src[:m[0]+1], " \t("), "function") {
			continue
		}
		if i >= len(src) || (src[i] != '\'' && src[i] != '"') {
			bad = append(bad, line)
			continue
		}
		q := src[i]
		var b strings.Builder
		j := i + 1
		for ; j < len(src) && src[j] != q && src[j] != '\n'; j++ {
			if src[j] == '\\' && j+1 < len(src) {
				j++
				switch src[j] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case 'u':
					// \u2014, as the browser reads it: the key must be the text
					// t() is given at run time, not its spelling in the source.
					if r, err := strconv.ParseUint(src[j+1:min(j+5, len(src))], 16, 32); err == nil && j+5 <= len(src) {
						b.WriteRune(rune(r))
						j += 4
					} else {
						b.WriteByte('u')
					}
				default:
					b.WriteByte(src[j])
				}
				continue
			}
			b.WriteByte(src[j])
		}
		// A literal followed by + is a concatenation, not a key.
		rest := strings.TrimLeft(src[min(j+1, len(src)):], " \t")
		if j >= len(src) || src[j] != q || strings.HasPrefix(rest, "+") {
			bad = append(bad, line)
			continue
		}
		lits = append(lits, b.String())
	}
	return lits, bad
}

// Sources is every source string in the frontend, with the files it appears
// in (relative to root, the repository), and every refused t() call by file
// and line. The markup is web/src/ui/*.html; the code is web/src/**/*.ts, less
// the generated tables; the labels declared in Go, which tl() renders
// (GoLabels); and the server's literal messages, which ts() renders
// (ServerMessages).
func Sources(root string) (map[string][]string, map[string][]int, error) {
	out := map[string][]string{}
	bad := map[string][]int{}
	add := func(k, file string) {
		for _, f := range out[k] {
			if f == file {
				return
			}
		}
		out[k] = append(out[k], file)
	}
	pages, err := filepath.Glob(filepath.Join(root, "web", "src", "ui", "*.html"))
	if err != nil {
		return nil, nil, err
	}
	for _, f := range pages {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, nil, err
		}
		rel, _ := filepath.Rel(root, f)
		for _, u := range Units(string(b)) {
			add(u.Key, filepath.ToSlash(rel))
		}
	}
	src := filepath.Join(root, "web", "src")
	err = filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
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
		lits, refused := TSLiterals(string(b))
		for _, l := range lits {
			add(l, rel)
		}
		if len(refused) > 0 {
			bad[rel] = refused
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	for k, froms := range GoLabels() {
		for _, f := range froms {
			add(k, f)
		}
	}
	msgs, err := ServerMessages(root)
	if err != nil {
		return nil, nil, err
	}
	for k, froms := range msgs {
		for _, f := range froms {
			add(k, f)
		}
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out, bad, nil
}

// blankComments returns the source with its comments blanked (a comment that
// mentions t() is prose, not a call), and a second copy with the insides of
// its strings blanked as well. Both keep every newline and every offset, so a
// position in one is the same position in the other. Strings are skipped while
// looking for comments: a '//' inside a quoted URL is not one.
func blankComments(src string) (string, string) {
	b := []byte(src)
	code := []byte(src)
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == '\'' || c == '"' || c == '`':
			for i++; i < len(b) && b[i] != c; i++ {
				if b[i] == '\\' {
					code[i] = ' '
					i++
				}
				if i < len(b) && b[i] != '\n' {
					code[i] = ' '
				}
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for ; i < len(b) && b[i] != '\n'; i++ {
				b[i], code[i] = ' ', ' '
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			for ; i < len(b) && !(b[i] == '*' && i+1 < len(b) && b[i+1] == '/'); i++ {
				if b[i] != '\n' {
					b[i], code[i] = ' ', ' '
				}
			}
			if i+1 < len(b) {
				b[i], b[i+1], code[i], code[i+1] = ' ', ' ', ' ', ' '
				i++
			}
		}
	}
	return string(b), string(code)
}
