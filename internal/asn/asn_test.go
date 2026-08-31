package asn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type asnCase struct {
	IP string `json:"ip"`
	// Found is SEPARATE from Org for the same reason the geo cases separate it
	// from Country: the original returns null for no match, and a single string
	// field cannot tell that from a match whose name happens to be empty.
	Found bool   `json:"found"`
	Org   string `json:"org"`
	Cat   string `json:"cat"`
}

// KNOWN_DIVERGENT — addresses where this port DELIBERATELY disagrees with
// asnLookup.js, each with the reason it is not worth closing.
//
// ipaddr.js implements the legacy inet_aton grammar: `0x08.8.8.8`, `134744072`,
// `8.8.2056` and `8.8.8.010` are all 8.8.8.8 to it, and therefore Google. Go's
// netip accepts dotted-quad decimal and nothing else, so the port answers
// nothing for them.
//
// NOT CLOSED, on purpose. The input to this function is an address out of a
// RouterOS connection table, and RouterOS emits canonical dotted-quad — none of
// these forms can occur. Reproducing the grammar means hand-rolling a second IP
// parser to be maintained for traffic that does not exist. What is NOT
// acceptable is closing the gap by deleting the cases, so the divergence is
// asserted instead: implement the grammar and this test fails, which forces the
// note to be removed rather than left lying.
var knownDivergent = map[string]string{
	"0x08.8.8.8":      "hex octet",
	"0x8.0x8.0x8.0x8": "hex octets",
	"134744072":       "one-part inet_aton",
	"8.526344":        "two-part inet_aton",
	"8.8.2056":        "three-part inet_aton",
	"8.8.8.010":       "octal octet",
}

// TestLookupMatchesAsnLookup replays every answer the live lookupOrg gave.
//
// The cases are generated FROM the range list — first, last, middle and the two
// addresses immediately outside each range — so an off-by-one in prefix handling
// fails here rather than being discovered on a page.
func TestLookupMatchesAsnLookup(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "asn-cases.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the cases: %v", err)
	}
	var cases struct {
		Total   int       `json:"total"`
		Matched int       `json:"matched"`
		Cases   []asnCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parsing the cases: %v", err)
	}
	if len(cases.Cases) == 0 {
		t.Fatal("no cases — regenerate with tools/asn-cases.js")
	}

	matched := 0
	for _, c := range cases.Cases {
		if _, skip := knownDivergent[c.IP]; skip {
			continue
		}
		org, ok := Org(c.IP)
		if ok != c.Found || org != c.Org {
			t.Errorf("%q: got (%q, %v), asnLookup says (%q, %v)", c.IP, org, ok, c.Org, c.Found)
			continue
		}
		if cat := Category(org); cat != c.Cat {
			t.Errorf("%q: category %q, asnLookup says %q", c.IP, cat, c.Cat)
		}
		if ok {
			matched++
		}
	}
	// The divergent addresses all MATCH on the live side, so they are added back
	// before the total is compared. Getting this wrong in the lenient direction
	// would make the count agree while the answers did not.
	if matched+len(knownDivergent) != cases.Matched {
		t.Errorf("matched %d addresses (+%d known-divergent), the case file records %d",
			matched, len(knownDivergent), cases.Matched)
	}
	t.Logf("%d addresses, %d matched, %d known divergences skipped",
		len(cases.Cases), matched, len(knownDivergent))
}

// TestKnownDivergencesStillDiverge asserts the gap DESCRIBED ABOVE still exists.
//
// This is the half that makes a documented gap honest. Without it, someone
// implementing inet_aton parsing would close the divergence and leave a comment
// claiming it is open — and the next reader would believe the comment. Closing
// it fails here, which is the only reliable way to make the note get deleted.
func TestKnownDivergencesStillDiverge(t *testing.T) {
	for ip, why := range knownDivergent {
		if org, ok := Org(ip); ok {
			t.Errorf("%q (%s) now resolves to %q — asnLookup accepts this form and the "+
				"port did not. If that was deliberate, remove it from knownDivergent "+
				"and from the note above it.", ip, why, org)
		}
	}
}

// TestCategoryFallsBackToOther pins the half of the contract the cases cannot
// reach: an org name that is not in the table at all. Every org the case file
// contains IS in the table, so without this the fallback goes unexercised.
func TestCategoryFallsBackToOther(t *testing.T) {
	for _, org := range []string{"", "Nobody", "cloudflare", "CLOUDFLARE"} {
		if got := Category(org); got != "other" {
			t.Errorf("Category(%q) = %q, want \"other\"", org, got)
		}
	}
	// And a known one, so the test cannot pass by returning "other" always.
	if got := Category("Cloudflare"); got != "cdn" {
		t.Errorf("Category(\"Cloudflare\") = %q, want \"cdn\"", got)
	}
}

// TestLookupShape checks the collectors' entry point returns both values
// together, since it is the only function they call.
func TestLookupShape(t *testing.T) {
	org, cat, ok := Lookup("8.8.8.8")
	if !ok || org != "Google" || cat != "cloud" {
		t.Errorf("Lookup(8.8.8.8) = (%q, %q, %v), want (Google, cloud, true)", org, cat, ok)
	}
	if org, cat, ok := Lookup("not-an-ip"); ok || org != "" || cat != "" {
		t.Errorf("Lookup(not-an-ip) = (%q, %q, %v), want empty and false", org, cat, ok)
	}
}
