package i18n

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE INVARIANT THE BUILD RESTS ON. Against every real page: an empty catalog
// changes nothing, and neither does a catalog that maps every unit to itself.
// The second is the stronger one: it exercises every replacement path and
// proves each puts back what it took out. It found a real fault on its first
// run: a unit ending in &nbsp; lost it.
func TestTranslateLeavesEveryPageAsItWasWhenNothingChanges(t *testing.T) {
	files, _ := filepath.Glob("../../web/src/ui/*.html")
	if len(files) < 40 {
		t.Fatalf("only %d pages found; the glob has stopped seeing the markup", len(files))
	}
	units := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if got := Translate(src, nil); got != src {
			t.Errorf("%s: an empty catalog changed the page", f)
		}
		self := Catalog{}
		for _, u := range Units(src) {
			self[u.Key] = u.Key
			units++
		}
		// Identity is byte-exact only where a unit had no entities and no inner
		// runs of whitespace; everywhere it must be the same TEXT and the same
		// tags.
		got := Translate(src, self)
		if Collapse(stripAll(got)) != Collapse(stripAll(src)) {
			t.Errorf("%s: translating every unit to itself changed the page's text", f)
		}
		if strings.Count(got, "<") != strings.Count(src, "<") {
			t.Errorf("%s: translating every unit to itself changed the markup's tags", f)
		}
	}
	if units < 1000 {
		t.Errorf("only %d units across the pages; the scanner is missing text", units)
	}
}

func stripAll(s string) string { return tagRe.ReplaceAllString(s, " ") }

// WHAT COUNTS AS TEXT: runs keep their inline tags, other tags end them,
// attributes are units, and skipped or translate="no" content is not.
func TestUnits(t *testing.T) {
	src := `<div class="x" title="Close the dialog">` +
		`<p>Opens <strong>one UDP port</strong>, which must be published.</p>` +
		`<span id="rate">—</span>` +
		`<label>Name <span id="n"></span> here</label>` +
		`<input placeholder='Search devices…'>` +
		`<script>var s = "Not text";</script>` +
		`<svg><text>Not text either</text></svg>` +
		`<span translate="no">RouterBOARD <b>hEX</b></span>` +
		`<!-- A comment is not text -->` +
		`<button>Save &amp; close</button></div>`
	var got []string
	for _, u := range Units(src) {
		got = append(got, u.Key)
	}
	want := []string{
		"Close the dialog",
		"Opens <strong>one UDP port</strong>, which must be published.",
		"Name", "here",
		"Search devices…",
		"Save & close",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("units:\n got %q\nwant %q", got, want)
	}
}

func TestTranslate(t *testing.T) {
	src := `<p title="Close">
    Opens <strong>one UDP port</strong>, now.
  </p><button>Save &amp; close</button><span translate="no">Save &amp; close</span>`
	cat := Catalog{
		"Close": "关闭",
		"Opens <strong>one UDP port</strong>, now.": "现在打开<strong>一个 UDP 端口</strong>。",
		"Save & close": "保存 & 关闭",
	}
	got := Translate(src, cat)
	want := `<p title="关闭">
    现在打开<strong>一个 UDP 端口</strong>。
  </p><button>保存 &amp; 关闭</button><span translate="no">Save &amp; close</span>`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	// A translation carrying markup the source does not have: escaped, never
	// live. It reached the page on the first run, when Translate trusted Check.
	evil := Translate(`<b>Hi</b><p>Name</p>`, Catalog{"Name": `<img src=x onerror=alert(1)>`})
	if strings.Contains(evil, "<img") {
		t.Errorf("a tag in a translation reached the page: %s", evil)
	}
	// Control: the source's own tag does pass.
	if got := Translate(`<p>Opens <b>it</b></p>`, Catalog{"Opens <b>it</b>": "打开<b>它</b>"}); !strings.Contains(got, "<b>它</b>") {
		t.Errorf("the source's own tag was escaped: %s", got)
	}
}

func TestCheck(t *testing.T) {
	source := map[string]bool{"Save": true, "{n} devices": true, "Opens <strong>it</strong>": true, "Open": true}
	p := Check(Catalog{
		"Save":                      "保存",
		"{n} devices":               "设备",
		"Opens <strong>it</strong>": "打开它",
		"Gone from the UI":          "已不存在",
	}, source)
	if strings.Join(p.Stale, "|") != "Gone from the UI" {
		t.Errorf("stale: %q", p.Stale)
	}
	if strings.Join(p.Broken, "|") != "Opens <strong>it</strong>|{n} devices" {
		t.Errorf("broken: %q", p.Broken)
	}
	if p.Untranslated != 1 {
		t.Errorf("untranslated: %d, want 1 (Open)", p.Untranslated)
	}
	if q := Check(Catalog{"Save": "保存", "{n} devices": "{n} 台设备"}, source); len(q.Stale)+len(q.Broken) != 0 {
		t.Errorf("control: a clean catalog reported %+v", q)
	}
}

func TestPickLang(t *testing.T) {
	have := []string{"zh-CN", "de"}
	cases := []struct{ cookie, accept, want string }{
		{"", "", ""},
		{"zh-CN", "", "zh-CN"},
		{"en", "zh-CN,zh;q=0.9", ""},
		{"", "zh-TW,zh;q=0.9", "zh-CN"},
		{"", "fr-FR,de;q=0.8", "de"},
		{"", "en-GB,zh;q=0.5", ""},
		{"", "fr,es", ""},
		{"xx-YY", "", ""},
		{"../etc", "de", "de"},
	}
	for _, c := range cases {
		if got := PickLang(c.cookie, c.accept, have); got != c.want {
			t.Errorf("PickLang(%q, %q) = %q, want %q", c.cookie, c.accept, got, c.want)
		}
	}
	if ValidLang("../x") || !ValidLang("zh-CN") || !ValidLang("de") || ValidLang("") {
		t.Error("ValidLang")
	}
}

func TestTSLiterals(t *testing.T) {
	src := "const a = t('Save');\n" +
		"x.t('not ours'); st('not ours either');\n" +
		"el.textContent = t(\"Don't {n} go\", { n });\n" +
		"const b = t('It\\'s here');\n" +
		"const c = t(name);\n" +
		"const d = t(`template ${x}`);\n" +
		"const e = t('Hi ' + who);\n" +
		"// a comment says t(name) and t('Not a key')\n" +
		"/* and t(x) here too */ const u = 'http://t(x)';\n" +
		"export function t(src: string) { return src; }\n"
	lits, bad := TSLiterals(src)
	if strings.Join(lits, "|") != "Save|Don't {n} go|It's here" {
		t.Errorf("literals: %q", lits)
	}
	if len(bad) != 3 || bad[0] != 5 || bad[1] != 6 || bad[2] != 7 {
		t.Errorf("refused lines: %v, want [5 6 7]", bad)
	}
}

func TestCatalogMeta(t *testing.T) {
	c := Catalog{"@name": "简体中文", "Save": "保存"}
	if c.Name("zh-CN") != "简体中文" || (Catalog{}).Name("de") != "de" {
		t.Error("Name")
	}
	if _, ok := c.Strings()["@name"]; ok {
		t.Error("an @ entry is in the strings")
	}
	if p := Check(c, map[string]bool{"Save": true}); len(p.Stale) != 0 {
		t.Errorf("@name reported stale: %v", p.Stale)
	}
}

func TestTLCalls(t *testing.T) {
	src := "// tl(x) in a comment\nexport function tl(label: string) {}\n" +
		"const s = 'tl(y)';\nh.textContent = tl(f.label);\nx.stl(1); o.tl(2);\n"
	if n := TLCalls(src); n != 1 {
		t.Errorf("TLCalls = %d, want 1: the call, not the comment, the definition, the string "+
			"or a method of the same name", n)
	}
}

func TestGoLabels(t *testing.T) {
	g := GoLabels()
	// A field label, an area title and a page title, each from its registry.
	for _, want := range []string{"Canonical Name", "Dashboard"} {
		if len(g[want]) == 0 {
			t.Errorf("GoLabels lacks %q", want)
		}
	}
	// Placeholders and options are router values, never labels.
	for _, never := range []string{"pool.ntp.org", "server.lan"} {
		if len(g[never]) != 0 {
			t.Errorf("GoLabels has %q, which is an example value, not a label", never)
		}
	}
	if len(g) < 300 {
		t.Errorf("GoLabels found only %d labels; a registry has stopped being read", len(g))
	}
}

func TestColumnLabel(t *testing.T) {
	for in, want := range map[string]string{"nextPool": "Next Pool", "name": "Name", "mtu": "Mtu", "l2mtu": "L2mtu", "rxBps": "Rx Bps"} {
		if got := ColumnLabel(in); got != want {
			t.Errorf("ColumnLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTSLiteralsDecodesUnicodeEscapes(t *testing.T) {
	lits, bad := TSLiterals(`x = t('Type its name \u2014 {name}');`)
	if len(bad) != 0 || len(lits) != 1 || lits[0] != "Type its name — {name}" {
		t.Errorf("got %q (refused %v), want the escape decoded as the browser decodes it", lits, bad)
	}
}
