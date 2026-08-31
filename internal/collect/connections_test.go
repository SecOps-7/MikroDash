package collect

// The connections differential gate.
//
// tools/conns-cases.js drives the LIVE `_processRows` over the captured 431
// rows and records three cases; this replays the same rows through the Go
// aggregation and requires the same answer. Geo and ASN are off on both sides —
// see the generator's header for why that is the honest starting point.

import (
	"path/filepath"
	"testing"

	"mikrodash/internal/routeros"
)

type connsCases struct {
	LanCidrs []string     `json:"lanCidrs"`
	TopN     int          `json:"topN"`
	Rows     int          `json:"rows"`
	Full     ConnsPayload `json:"full"`
	Capped   struct {
		MaxConns int `json:"maxConns"`
		ConnsPayload
	} `json:"capped"`
	NoDetail ConnsPayload `json:"noDetail"`
}

func connsFixtureRows(t *testing.T) []routeros.Reply {
	t.Helper()
	var fixture struct {
		Exchanges []struct {
			Rows []routeros.Reply `json:"rows"`
		} `json:"exchanges"`
	}
	readJSON(t, filepath.Join(testdata, "fixtures", "Mikrotik identity-0cc5 AX3", "conns.json"), &fixture)
	rows := []routeros.Reply{}
	for _, e := range fixture.Exchanges {
		rows = append(rows, e.Rows...)
	}
	return rows
}

func TestConnsAgainstCases(t *testing.T) {
	var cases connsCases
	readJSON(t, filepath.Join(testdata, "conns-cases.json"), &cases)
	rows := connsFixtureRows(t)
	if len(rows) != cases.Rows {
		t.Fatalf("the fixture holds %d rows, the cases were generated from %d", len(rows), cases.Rows)
	}

	base := ConnsInput{Rows: rows, LanCidrs: cases.LanCidrs, TopN: cases.TopN, PollMs: 3000}

	full := base
	full.MaxConns = 10000
	full.Detailed = true
	if diff := diffJSON(toAny(t, BuildConns(full)), toAny(t, cases.Full), ""); diff != "" {
		t.Errorf("the unrestricted case differs from the Node collector:\n%s", diff)
	}

	// The cap changes the answer, and the payload has to say so.
	capped := base
	capped.MaxConns = cases.Capped.MaxConns
	capped.Detailed = true
	got := BuildConns(capped)
	if diff := diffJSON(toAny(t, got), toAny(t, cases.Capped.ConnsPayload), ""); diff != "" {
		t.Errorf("the capped case differs from the Node collector:\n%s", diff)
	}
	if !got.ProcessingCapped || got.Processed != cases.Capped.MaxConns {
		t.Errorf("capped payload reports processed=%d capped=%v", got.Processed, got.ProcessingCapped)
	}

	// NOBODY LOOKING: the four heavy indexes must be empty rather than merely
	// unused, because the page distinguishes "not asked for" from "nothing
	// there" and only one of them is a bug.
	quiet := base
	quiet.MaxConns = 10000
	quiet.Detailed = false
	if diff := diffJSON(toAny(t, BuildConns(quiet)), toAny(t, cases.NoDetail), ""); diff != "" {
		t.Errorf("the no-detail case differs from the Node collector:\n%s", diff)
	}
}

// The destination key, which decides what counts as one destination.
//
// A host on two ports is TWO destinations, because that is two different
// conversations — and an IPv6 address is bracketed so the port separator is not
// one colon among many.
func TestConnDestKey(t *testing.T) {
	cases := []struct {
		row  routeros.Reply
		want string
	}{
		{routeros.Reply{"dst-address": "1.1.1.1", "dst-port": "443", "protocol": "tcp"}, "1.1.1.1:443/tcp"},
		{routeros.Reply{"dst-address": "1.1.1.1", "dst-port": "443"}, "1.1.1.1:443"},
		{routeros.Reply{"dst-address": "1.1.1.1"}, "1.1.1.1"},
		{routeros.Reply{"dst-address": "2001:db8::1", "dst-port": "443", "protocol": "tcp"},
			"[2001:db8::1]:443/tcp"},
		{routeros.Reply{}, "unknown"},
		// The protocol is lowercased, so TCP and tcp are one destination.
		{routeros.Reply{"dst-address": "1.1.1.1", "dst-port": "80", "protocol": "TCP"}, "1.1.1.1:80/tcp"},
	}
	for _, c := range cases {
		if got := connDestKey(c.row); got != c.want {
			t.Errorf("connDestKey(%v) = %q, want %q", c.row, got, c.want)
		}
	}
}

// Geo is OPTIONAL, and the shape of the payload does not change when it is
// absent — only its contents. No fixture can show this, because the corpus is
// generated with geo off on both sides.
func TestConnsGeoOptional(t *testing.T) {
	// The captured table carries BARE addresses with the port in its own field,
	// which is what these rows imitate. An earlier version of this test put the
	// port in the address and produced keys like `1.1.1.1:443:443/tcp` — the
	// test was wrong, not the builder.
	rows := []routeros.Reply{
		{".id": "*1", "src-address": "10.0.0.5", "dst-address": "1.1.1.1",
			"dst-port": "443", "protocol": "tcp"},
		{".id": "*2", "src-address": "10.0.0.6", "dst-address": "1.1.1.1",
			"dst-port": "443", "protocol": "tcp"},
	}
	in := ConnsInput{Rows: rows, LanCidrs: []string{"10.0.0.0/8"}, TopN: 10,
		MaxConns: 100, Detailed: true, PollMs: 3000}

	off := BuildConns(in)
	if len(off.TopCountries) != 0 {
		t.Errorf("countries reported with no geo database: %+v", off.TopCountries)
	}
	if len(off.TopDestinations) != 1 || off.TopDestinations[0].Country != "" {
		t.Errorf("destination carries a country with no geo database: %+v", off.TopDestinations)
	}

	in.Geo = func(string) (string, string) { return "NL", "Amsterdam" }
	in.Org = func(string) (string, string, bool) { return "Cloudflare", "cdn", true }
	on := BuildConns(in)
	if len(on.TopCountries) != 1 || on.TopCountries[0].CC != "NL" {
		t.Fatalf("countries = %+v", on.TopCountries)
	}
	if on.TopCountries[0].Count != 2 || on.TopCountries[0].Proto.TCP != 2 {
		t.Errorf("country counts = %+v", on.TopCountries[0])
	}
	if len(on.TopCountries[0].Orgs) != 1 || on.TopCountries[0].Orgs[0].Org != "Cloudflare" {
		t.Errorf("country orgs = %+v", on.TopCountries[0].Orgs)
	}
	if on.TopDestinations[0].Country != "NL" || on.TopDestinations[0].Org == nil {
		t.Errorf("destination = %+v", on.TopDestinations[0])
	}
}

// A destination INSIDE the LAN is not a destination at all: this half of the
// collector is about traffic leaving the network.
func TestConnsIgnoresInternalDestinations(t *testing.T) {
	rows := []routeros.Reply{
		{".id": "*1", "src-address": "10.0.0.5", "dst-address": "10.0.0.9",
			"dst-port": "445", "protocol": "tcp"},
	}
	got := BuildConns(ConnsInput{Rows: rows, LanCidrs: []string{"10.0.0.0/8"}, TopN: 10,
		MaxConns: 100, Detailed: true, PollMs: 3000})
	if len(got.TopDestinations) != 0 || len(got.TopPorts) != 0 {
		t.Errorf("an internal destination was counted: %+v / %+v", got.TopDestinations, got.TopPorts)
	}
	// The SOURCE is still counted — it is a LAN host with a connection.
	if len(got.TopSources) != 1 || got.TopSources[0].Count != 1 {
		t.Errorf("the source was not counted: %+v", got.TopSources)
	}
	// And the protocol split counts every row, inside or out.
	if got.ProtoCounts.TCP != 1 {
		t.Errorf("proto counts = %+v", got.ProtoCounts)
	}
}

// THE COLLECTOR'S OWN DEFAULT IS THE LIVE ONE.
//
// It is reached only when nobody calls `WithTopN` with a positive value, which
// in the running server never happens — `topSetting` always supplies the
// generated default. That made a mutation reverting this from 5 to 10 survive
// every other test, so it is pinned where it is stated rather than left as a
// constant nothing asserts.
//
// TEN was what it said until 2026-08-29, and ten was never a live default: the
// live app's `topN` is 5 (`src/settings.js:126`). A wrong default is only
// invisible while something else always overrides it, and "something else always
// overrides it" is precisely the assumption that broke when the operator changed
// the setting and nothing happened.
func TestTheConnectionsTopNDefaultMatchesLive(t *testing.T) {
	c := NewConnections(fakeReader{}, func(string, string, any) {}, nil, nil, nil, 3000)
	if c.topN != 5 {
		t.Errorf("the default topN is %d, want 5 — the live default from src/settings.js", c.topN)
	}
	if c.WithTopN(0).topN != 5 {
		t.Error("a non-positive count replaced the default; it must fall through, as NewTalkers does")
	}
	if c.WithTopN(12).topN != 12 {
		t.Error("a positive count was ignored — this is the wiring the operator's report was about")
	}
}
