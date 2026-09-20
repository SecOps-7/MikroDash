package verify

// THE GEO DATA LEDGER — that what the app reads is what the image ships.
//
// ── THE FAILURE THIS EXISTS FOR ────────────────────────────────────────────
//
// `internal/geo` and `internal/asn` both degrade quietly when their database is
// absent: no country flags, no organisation badges, every other number intact.
// That is the right behaviour at runtime and it is a terrible way to find out
// that a build stopped shipping a file. Nothing fails, no page errors, and the
// data simply stops appearing — which reads exactly like a router with boring
// traffic.
//
// So the filenames each package looks for, and the paths the Dockerfile puts
// them at, are pinned against each other here. A rename on either side fails
// rather than silently emptying half the Connections page.
//
// ── AND THE CREDIT, WHICH IS A LICENCE TERM ────────────────────────────────
//
// Both databases are DB-IP Lite under CC BY 4.0, whose terms require "a link
// back to DB-IP.com on pages that display or use results from the database".
// The pages that display them are pinned too: dropping the credit while still
// shipping the data is a licence breach that no other check would notice.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The databases, as each package names them and as the Dockerfile places them.
var geoDatabases = []struct {
	file    string // the basename both sides must agree on
	pkg     string // the package whose mmdbName constant holds it
	pkgFile string
}{
	{"dbip-city-lite.mmdb", "internal/geo", "internal/geo/mmdb.go"},
	{"dbip-asn-lite.mmdb", "internal/asn", "internal/asn/asn.go"},
}

// The attribution DB-IP asks for. Matched as far as the vendor name, which
// is a markdown link in the notices file.
const dbipCredit = "IP Geolocation by ["

func TestEveryGeoDatabaseIsFetchedAndShipped(t *testing.T) {
	root := repoRoot(t)
	docker, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatalf("reading Dockerfile: %v", err)
	}
	df := string(docker)

	for _, db := range geoDatabases {
		// THE PACKAGE ASKS FOR THIS NAME. Read from the source rather than
		// restated here, so the constant moving fails this test.
		src, err := os.ReadFile(filepath.Join(root, db.pkgFile))
		if err != nil {
			t.Fatalf("reading %s: %v", db.pkgFile, err)
		}
		if !strings.Contains(string(src), `"`+db.file+`"`) {
			t.Errorf("%s no longer names %q; the Dockerfile still ships it, so every "+
				"lookup would answer nothing", db.pkgFile, db.file)
		}
		// THE IMAGE MUST FETCH IT. The download line is month-stamped, so the
		// stem is what is pinned.
		stem := strings.TrimSuffix(db.file, ".mmdb")
		if !strings.Contains(df, "download.db-ip.com/free/"+stem+"-") {
			t.Errorf("the Dockerfile does not fetch %s; %s would find no database "+
				"and the app would degrade silently", stem, db.pkg)
		}
		// AND PUT IT WHERE THE BINARY LOOKS. `-geo` defaults to /app/geo.
		if !strings.Contains(df, "/app/geo/"+db.file) {
			t.Errorf("the Dockerfile never copies %s into /app/geo; it is fetched "+
				"and then thrown away", db.file)
		}
	}
}

// THE PREVIOUS-MONTH FALLBACK. DB-IP publishes monthly and the new file appears
// partway through the first day, so a build at 00:30 on the 1st fails for a
// reason that has nothing to do with the build. Both fetches must carry it.
func TestEveryGeoFetchFallsBackAMonth(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatalf("reading Dockerfile: %v", err)
	}
	df := string(b)
	fetches := strings.Count(df, "download.db-ip.com/free/")
	if fetches != len(geoDatabases) {
		t.Fatalf("the Dockerfile has %d DB-IP fetches for %d databases; this ledger "+
			"has drifted from the build", fetches, len(geoDatabases))
	}
	// One loop per fetch, each trying this month then the previous one.
	if got := strings.Count(df, `for m in "$this" "$prev"`); got != len(geoDatabases) {
		t.Errorf(`%d fetches try "$this" then "$prev"; all %d must, or a build on `+
			`the 1st of a month fails for no reason of its own`, got, len(geoDatabases))
	}
}

// CC BY 4.0 REQUIRES ATTRIBUTION, and THIRD_PARTY_NOTICES.md is where this app
// gives it. Nothing in the interface credits DB-IP, so that file and the README
// are the whole of it — which makes a check that they actually name both
// databases the only thing standing between shipping the data and shipping it
// uncredited.
func TestBothGeoDatabasesAreCreditedInTheNotices(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "THIRD_PARTY_NOTICES.md"))
	if err != nil {
		t.Fatalf("reading THIRD_PARTY_NOTICES.md: %v", err)
	}
	notices := string(b)
	// THE CREDIT IS MARKDOWN, so the name is a link and the literal sentence
	// never appears as one string. Both halves are checked instead.
	if !strings.Contains(notices, dbipCredit) {
		t.Errorf("THIRD_PARTY_NOTICES.md does not carry %q; the image ships DB-IP "+
			"data and nothing in the repository credits it", dbipCredit)
	}
	if !strings.Contains(notices, "https://db-ip.com") {
		t.Error("the DB-IP credit carries no link back to db-ip.com, which the " +
			"licence asks for")
	}
	// BOTH DATABASES BY NAME. Crediting the city database while silently adding
	// a second one is how the ASN file arrived uncredited in the first place.
	for _, db := range geoDatabases {
		if !strings.Contains(notices, db.file) {
			t.Errorf("THIRD_PARTY_NOTICES.md never names %s; it is shipped in the "+
				"image and covered by the same licence as the file beside it", db.file)
		}
	}
}

// ── THE SERVICE CATEGORY LEDGER ────────────────────────────────────────────
//
// A category is named in three places: `internal/asn/categories.go` produces
// it, `app.css` colours `.svc-<cat>`, and `CAT_COLOUR` in the Sankey repeats
// the palette so a service reads the same colour in the list and the diagram.
// Three copies of one list, and nothing made them agree.
//
// Both directions matter, and each has already gone wrong once:
//
//	a category with no colour   renders an unstyled word on the badge
//	a colour with no category   is dead paint — `.svc-dns` sat in app.css and
//	                            in CAT_COLOUR for months while no organisation
//	                            could ever carry that class, because the org
//	                            table it was written for never had a resolver
//	                            in it
func TestEveryServiceCategoryHasAColourAndEveryColourHasACategory(t *testing.T) {
	root := repoRoot(t)
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		return string(b)
	}

	// PRODUCED: the categories internal/asn can return, read from the map
	// itself rather than restated, so adding one here is not enough to pass.
	cats := map[string]bool{}
	for _, m := range reCategory.FindAllStringSubmatch(read("internal/asn/categories.go"), -1) {
		cats[m[1]] = true
	}
	// "other" is the fallback every unlisted network gets; it is produced by
	// Category's return rather than by a row in the map.
	cats["other"] = true
	if len(cats) < 3 {
		t.Fatalf("read %d categories from internal/asn/categories.go; the parse broke", len(cats))
	}

	css := read("web/public/app.css")
	sankey := read("web/src/pages/connections-sankey.ts")

	for cat := range cats {
		if !strings.Contains(css, ".svc-"+cat) {
			t.Errorf("internal/asn can return the category %q, which app.css has no "+
				".svc-%s rule for; the badge renders as an unstyled word", cat, cat)
		}
		if !strings.Contains(sankey, "\n  "+cat+":") {
			t.Errorf("internal/asn can return the category %q, which CAT_COLOUR in "+
				"connections-sankey.ts has no entry for; that node falls back to grey "+
				"while its badge is coloured", cat)
		}
	}

	// AND THE OTHER WAY. A colour nothing can produce is dead paint.
	for _, m := range reSankeyCategory.FindAllStringSubmatch(sankey, -1) {
		if !cats[m[1]] {
			t.Errorf("CAT_COLOUR colours %q, which no autonomous system in "+
				"internal/asn/categories.go is given; it is unreachable", m[1])
		}
	}
	for _, m := range reCSSCategory.FindAllStringSubmatch(css, -1) {
		if !cats[m[1]] {
			t.Errorf("app.css styles .svc-%s, which no autonomous system in "+
				"internal/asn/categories.go is given; it is unreachable", m[1])
		}
	}
}

var (
	// `13335:  "cdn", // Cloudflare`
	reCategory = regexp.MustCompile(`(?m)^\s*\d+:\s*"([a-z]+)"`)
	// `  cdn: '#38bdf8',` inside CAT_COLOUR
	reSankeyCategory = regexp.MustCompile(`(?m)^  ([a-z]+): '#[0-9a-fA-F]{6}',`)
	// `.svc-cdn      {background:…}` — the dark-theme rules only; the
	// light-theme overrides repeat the same names behind a selector prefix.
	reCSSCategory = regexp.MustCompile(`(?m)^\s{4}\.svc-([a-z]+)\s`)
)
