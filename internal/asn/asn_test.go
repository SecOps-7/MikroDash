package asn

import (
	"os"
	"path/filepath"
	"testing"
)

// ── WHAT IS WORTH PINNING NOW THAT THIS IS A DATABASE ──────────────────────
//
// The old suite replayed 1604 cases derived from a checked-in prefix list, so
// it pinned the data. The data is no longer ours: it arrives monthly from
// DB-IP, and a test asserting that 8.8.8.8 is Google would be asserting
// something only the vendor controls. Pinning it would turn a routine data
// refresh into a red build.
//
// What IS ours, and what these pin:
//
//	the tidying     pure, and the only place a company name is invented
//	the categories  a number-keyed map, where a typo is invisible by eye
//	the type check  the mirror-image of internal/geo's: the WRONG database
//	                opens cleanly and answers nothing, which reads as "no
//	                network owns this" rather than as a misconfiguration
//	the degrading   no database must be a miss, never a panic
//
// The database itself is exercised by TestAgainstARealDatabase, which SKIPS
// when there is none — the same bargain internal/geo/mmdb_test.go makes.

func TestNameStripsLegalFormsAndAppliesAliases(t *testing.T) {
	cases := []struct{ raw, want string }{
		// The ones an operator sees every day.
		{"Google LLC", "Google"},
		{"Cloudflare, Inc.", "Cloudflare"},
		{"Amazon.com, Inc.", "Amazon"},
		{"Microsoft Corporation", "Microsoft"},
		{"Apple Inc.", "Apple"},
		{"Fastly, Inc.", "Fastly"},
		{"GitHub, Inc.", "GitHub"},
		{"Anthropic, PBC", "Anthropic"},
		{"Deutsche Telekom AG", "Deutsche Telekom"},
		{"OVH SAS", "OVH"},
		{"Quad9", "Quad9"},

		// ALIASES, which is the judgement half. AS32934 is still registered to
		// Facebook; nobody calls it that.
		{"Facebook, Inc.", "Meta"},
		{"nextdns, Inc.", "NextDNS"},
		{"AdGuard Software Limited", "AdGuard"},
		{"Cisco OpenDNS, LLC", "OpenDNS"},

		// TWO SPELLINGS OF ONE COMPANY MUST LAND ON ONE NAME. The Connections
		// page folds destinations onto this string, so a company reaching it
		// under two names becomes two nodes in the diagram.
		{"Akamai Technologies, Inc.", "Akamai"},
		{"Akamai International B.V.", "Akamai"},
		{"Google Asia Pacific Pte. Ltd.", "Google"},

		// Repeated stripping: both halves of a compound legal form go.
		{"Level 7 Wireless (Pty) Ltd", "Level 7 Wireless"},

		{"Telkom SA Ltd.", "Telkom SA"},
		{"Cogent Communications", "Cogent Communications"},

		// A SUFFIX IS A WHOLE WORD, and these are the proof. Every one is a
		// real organisation name out of the database — a scan of it on
		// 2026-09-20 found 123 names where a legal form sits INSIDE a word, so
		// this is a live hazard rather than a hypothetical one. Without the
		// separator check ASFINAG, an Austrian motorway operator, becomes
		// "ASFIN".
		{"ASFINAG", "ASFINAG"},
		{"DELFI UAB", "DELFI UAB"},
		{"Bezeq International-Ltd", "Bezeq International-Ltd"},
		{"COMCAST-SRL", "COMCAST-SRL"},
		{"Vodacom", "Vodacom"},

		// A name that is nothing but a legal form keeps something to show.
		{"Inc.", "Inc."},
		{"  Google LLC  ", "Google"},
	}
	for _, c := range cases {
		if got := Name(c.raw); got != c.want {
			t.Errorf("Name(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// AN AMBIGUOUS SUFFIX IS LEFT ON, and that is a decision rather than an
// oversight: "AS" is both a Scandinavian company form and the abbreviation for
// an autonomous system, so stripping it would mangle any network whose name
// ends in those letters. A slightly long badge beats a wrong name.
func TestAmbiguousSuffixesSurvive(t *testing.T) {
	for _, raw := range []string{"Telenor Norge AS", "Blix Solutions AS", "Altibox AS"} {
		if got := Name(raw); got != raw {
			t.Errorf("Name(%q) = %q; a trailing AS must be left alone", raw, got)
		}
	}
}

func TestCategoryIsKeyedOnTheNumberAndFallsBackToOther(t *testing.T) {
	if got := Category(13335); got != "cdn" {
		t.Errorf("Category(AS13335 Cloudflare) = %q, want cdn", got)
	}
	if got := Category(32934); got != "social" {
		t.Errorf("Category(AS32934 Meta) = %q, want social", got)
	}
	// THE dns COLOUR IS REACHABLE, which it was not before this change: the CSS
	// carried `.svc-dns` while no organisation could ever have that category.
	if got := Category(19281); got != "dns" {
		t.Errorf("Category(AS19281 Quad9) = %q, want dns; the svc-dns colour is dead again", got)
	}
	// An unknown network is "other" — a value the page renders, not a gap.
	if got := Category(64496); got != "other" {
		t.Errorf("Category(AS64496) = %q, want other", got)
	}
	if got := Category(0); got != "other" {
		t.Errorf("Category(0) = %q, want other", got)
	}
}

// The categories themselves are pinned by internal/verify's geodata ledger,
// which reads the map, app.css and the Sankey palette and fails in BOTH
// directions. A copy of the category list here would be a third place to keep
// in step, and the ledger's whole point is that copies drift — `.svc-dns` sat
// in app.css for months with nothing able to produce it.

// NO DATABASE IS A MISS, NOT A PANIC. A Connections page with no organisation
// badges is degraded; a collector that panics is broken.
func TestAMissingDatabaseDegrades(t *testing.T) {
	var d *DB
	if org, cat, ok := d.Lookup("8.8.8.8"); ok || org != "" || cat != "" {
		t.Errorf("a nil database answered (%q, %q, %v); it must miss", org, cat, ok)
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Error("Load of a directory with no database returned no error")
	}
}

// THE WRONG DATABASE MUST BE NAMED, NOT SILENTLY EMPTY. A City database opens
// cleanly through this reader and then answers with no organisation for every
// address on earth, which looks exactly like "nothing owns this".
func TestACityDatabaseIsRefused(t *testing.T) {
	dir := os.Getenv("MIKRODASH_GEO_DIR")
	if dir == "" {
		dir = "/app/geo"
	}
	city := filepath.Join(dir, "dbip-city-lite.mmdb")
	if _, err := os.Stat(city); err != nil {
		t.Skipf("no city database at %s to mis-load", city)
	}
	tmp := t.TempDir()
	if err := os.Symlink(city, filepath.Join(tmp, mmdbName)); err != nil {
		t.Skipf("cannot stage the wrong database: %v", err)
	}
	_, err := Load(tmp)
	if err == nil {
		t.Fatal("a City database loaded as an ASN database; every lookup would " +
			"answer nothing and look like missing data")
	}
	if !contains(err.Error(), "ASN database is required") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

// ── AND THE REAL THING, WHEN THERE IS ONE ──────────────────────────────────
//
// Skipped rather than failed without a database, exactly as
// internal/geo/mmdb_test.go is: this file is republished monthly and a test
// that cannot run offline must not be a test that fails offline.
//
// It asserts the SHAPE the app depends on, not the vendor's answers: that a
// well-known address resolves to something, that the name has been tidied, and
// that an address in unrouted space misses. Asserting "8.8.8.8 is Google" would
// make a routine data refresh a red build.
func TestAgainstARealDatabase(t *testing.T) {
	dir := os.Getenv("MIKRODASH_GEO_DIR")
	if dir == "" {
		dir = "/app/geo"
	}
	db, err := Load(dir)
	if err != nil {
		t.Skipf("no ASN database in %s: %v", dir, err)
	}
	org, cat, ok := db.Lookup("8.8.8.8")
	if !ok || org == "" {
		t.Fatalf("8.8.8.8 resolved to nothing (%q/%q); the database is loaded but answering empty", org, cat)
	}
	// TIDIED, not raw. If this ever reads "Google LLC" the Name step has been
	// bypassed somewhere between the record and the payload.
	if contains(org, "LLC") || contains(org, "Inc") {
		t.Errorf("8.8.8.8 = %q; the legal suffix reached the badge", org)
	}
	if cat == "" {
		t.Error("a hit returned an empty category; the page renders a class name")
	}
	// An address in space reserved for documentation belongs to no AS.
	if org, _, ok := db.Lookup("198.51.100.1"); ok {
		t.Errorf("TEST-NET-2 resolved to %q; unrouted space must miss", org)
	}
	// A GUARD ON THE LIBRARY, not on this package. The reader resolves a zoned
	// address on its own, which is why internal/asn does not strip one — the
	// prefix-walking reader it replaced had to, because netip.Prefix.Contains
	// refuses a zoned address. A maxminddb that stops doing this fails here
	// rather than quietly turning every zoned address into a miss.
	if _, _, ok := db.Lookup("2001:4860:4860::8888%eth0"); !ok {
		t.Error("the reader no longer resolves a zoned address; internal/asn " +
			"relies on it doing so and must strip the zone itself again")
	}
	if _, _, ok := db.Lookup("not an address"); ok {
		t.Error("an unparseable address was reported as a hit")
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) &&
		(func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})()
}
