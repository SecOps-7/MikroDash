// Package asn answers "who owns this address", from DB-IP ASN Lite.
//
// ── WHAT IT REPLACED, AND WHY ──────────────────────────────────────────────
//
// This was a hand-curated table of 339 published prefixes covering thirteen
// services, ported from the Node app and frozen the day it was ported. It was
// measured against one router's live connection table on 2026-09-20:
//
//	named by the curated table:  40/70  (57%)
//	named by DB-IP ASN Lite:     70/70  (100%)
//
// The thirty misses were not exotic — OVH, Linode, Deutsche Telekom, Proximus,
// Verizon, Vox Telecom, AdGuard, Quad9. Worse, three of the forty hits were
// WRONG: the table listed 52.144.0.0/12 under Amazon, which swallows Microsoft
// Azure space, so Azure traffic had been labelled Amazon since the cutover.
// That is the failure a curated list cannot catch about itself, and it is why
// this is a database now.
//
// ── THE ANSWER CHANGED MEANING, AND THAT IS WORTH KNOWING ──────────────────
//
// A range list says WHOSE SERVICE this is. An AS says WHOSE NETWORK carries it.
// They differ wherever a service rents someone else's network: Twitch now reads
// Amazon and Spotify reads Google, because that is who actually routes the
// packets. Measured, not assumed — every probe address for both resolved to the
// host's AS. The old answer was a guess about intent; this one is a fact about
// routing, and the page says what the router is really talking to.
//
// ── DB-IP AND NOT MAXMIND, FOR THE REASON internal/geo GIVES ───────────────
//
// GeoLite2 ASN is updated daily and is probably better data. It also needs an
// account, a licence key and a signed EULA, which makes a build nobody who
// clones this repo can run. DB-IP ASN Lite is one keyless HTTPS GET, monthly,
// CC BY 4.0 — the same terms, the same vendor and the same Dockerfile shape as
// the city database beside it. IPinfo and IP2Location were checked and both
// require an account too.
//
// CC BY 4.0 REQUIRES THE CREDIT TO BE VISIBLE. "IP Geolocation by DB-IP",
// linked to db-ip.com, on the pages that show the data. See `web/src/ui/` —
// it is a licence term, not a courtesy, and `internal/verify` pins it.
//
// ── FAILURE IS A VALUE, AS IT IS FOR GEO ───────────────────────────────────
//
// No database is a DEGRADED state, never a fatal one: a Connections page with
// no organisation badges still shows every address, rate and country. Callers
// gate on the bool rather than on a non-nil handle, so "no database" is a state
// the code names rather than one it stumbles into.
package asn

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/oschwald/maxminddb-golang/v2"
)

// mmdbName is the file Load looks for. Fixed rather than globbed, for the
// reason internal/geo fixes its own: a directory holding two vintages should
// fail loudly at the download step, not silently pick whichever sorted first.
const mmdbName = "dbip-asn-lite.mmdb"

// record is the subset of a DB-IP ASN row this package reads. Decoded into a
// struct rather than `any` so maxminddb skips everything not named here; this
// runs once per distinct destination per tick.
type record struct {
	Num uint   `maxminddb:"autonomous_system_number"`
	Org string `maxminddb:"autonomous_system_organization"`
}

// DB is a loaded ASN database.
type DB struct{ r *maxminddb.Reader }

var (
	once   sync.Once
	shared *DB
	reason string
)

// Load opens the database in dir.
func Load(dir string) (*DB, error) {
	p := filepath.Join(dir, mmdbName)
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if st.IsDir() || st.Size() == 0 {
		return nil, fmt.Errorf("asn: %s is empty or a directory", p)
	}
	r, err := maxminddb.Open(p)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", p, err)
	}
	// VALIDATE THE TYPE. A City database opens cleanly here and then answers
	// with no organisation for every address — which reads as "nothing owns
	// this" rather than as the wrong file in the wrong place. internal/geo
	// makes the same check for the mirror-image mistake.
	if t := r.Metadata.DatabaseType; !strings.Contains(strings.ToLower(t), "asn") {
		r.Close()
		return nil, fmt.Errorf("%s is a %q database; an ASN database is required "+
			"(any other kind answers with no organisation, which looks like missing "+
			"data rather than the wrong file)", p, t)
	}
	return &DB{r: r}, nil
}

// Shared loads the database once from the usual place, and reports whether it
// is available.
func Shared(dir string) (*DB, bool) {
	once.Do(func() {
		db, err := Load(dir)
		if err != nil {
			reason = err.Error()
			return
		}
		shared = db
	})
	return shared, shared != nil
}

// Reason is why the database is unavailable, for a log line or a status page.
func Reason() string { return reason }

// Current is the database Shared already loaded, for callers with no business
// knowing where it lives.
func Current() (*DB, bool) { return shared, shared != nil }

// Lookup returns the owning organisation and its colour category.
//
// The bool distinguishes "looked up and found nothing" from "not looked up",
// which is what a null org in the payload means. An address in no AS — unrouted
// space, or a network this Lite edition does not carry — is a miss, not an
// organisation with an empty name.
func (d *DB) Lookup(ip string) (string, string, bool) {
	if d == nil || d.r == nil {
		return "", "", false
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "", "", false
	}
	// NO ZONE HANDLING HERE, and that is measured rather than assumed. The
	// prefix-walking reader this replaced HAD to strip a zone, because
	// netip.Prefix.Contains returns false for any address carrying one. The
	// mmdb reader resolves `…::8888%eth0` correctly on its own, so stripping it
	// here would be a line with no instances. TestAgainstARealDatabase asserts
	// the library still does it, which is the guard that used to be this code.
	var rec record
	if err := d.r.Lookup(addr).Decode(&rec); err != nil {
		return "", "", false
	}
	if rec.Org == "" {
		return "", "", false
	}
	return Name(rec.Org), Category(rec.Num), true
}

// Lookuper adapts a database to the collectors' OrgLookup shape, as
// (*geo.DB).Lookuper does for theirs.
func (d *DB) Lookuper() func(ip string) (string, string, bool) {
	return d.Lookup
}
