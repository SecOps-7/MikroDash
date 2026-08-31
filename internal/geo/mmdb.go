package geo

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/oschwald/maxminddb-golang"
)

// THE MMDB BACKEND — DB-IP City Lite, and why it replaced geoip-lite's files.
//
// ── THE REASON THE OLD READER EXISTED HAS BEEN SPENT ──────────────────────
//
// `geoip.go` reads geoip-lite's own `.dat` files, and its header explains why:
// a different database would give different answers, and every disagreement
// with the Node app would then need triaging as "port defect, or different
// data?" — the question the whole differential approach exists to remove.
//
// That reason held until cutover. It does not hold now. The Node app is no
// longer the product, and bug-for-bug agreement with a SNAPSHOT is worth less
// than data that is current: geoip-lite 2.0.3 ships whatever it shipped, and
// neither its Dockerfile nor its CI ever ran `updatedb`. The addresses move; the
// file does not.
//
// ── WHY DB-IP CITY LITE AND NOT MAXMIND GeoLite2 ──────────────────────────
//
// GeoLite2 is more accurate and updates twice weekly. It also requires an
// account, a signed EULA and a licence key that expires every 90 days unless
// reconfirmed by email. For a self-hosted app that people build from source,
// that is a build which cannot run without someone's credentials. DB-IP City
// Lite is a direct download, monthly, CC BY 4.0.
//
// MEASURED before it was chosen, on the 2026-08 file: every field this package
// needs is present on both IPv4 and IPv6 — country iso_code and name, city,
// subdivisions[0] for the region, and location lat/lon.
//
// ── SIZE IS NOT AN IMPROVEMENT, AND WAS CLAIMED AS ONE ────────────────────
//
// 130 MB uncompressed, against ~115 MB for the four geoip-lite `.dat` files it
// replaces. An earlier note in this migration said the swap made the image
// smaller, from a "~19 MB" figure that turned out to be the compressed size of a
// different variant. It does not. It makes the data CURRENT, which is the whole
// and only case for it.

// mmdbName is the file `Load` looks for. Fixed rather than globbed: a directory
// holding two vintages should fail loudly at the download step, not silently
// pick whichever sorted first.
const mmdbName = "dbip-city-lite.mmdb"

// mmdbRecord is the subset of a DB-IP record this package reads.
//
// DECODED INTO A STRUCT, NOT `any`. The full record carries continent and
// country names in ten languages; decoding it into a map allocates all of that
// on every connection row. The struct tells maxminddb to skip everything not
// named here.
type mmdbRecord struct {
	Country struct {
		ISO   string            `maxminddb:"iso_code"`
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	Subdivisions []struct {
		ISO   string            `maxminddb:"iso_code"`
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Location struct {
		Lat float64 `maxminddb:"latitude"`
		Lon float64 `maxminddb:"longitude"`
	} `maxminddb:"location"`
}

// mmdbPath returns the database path inside dir, and whether it is there.
func mmdbPath(dir string) (string, bool) {
	p := filepath.Join(dir, mmdbName)
	if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
		return p, true
	}
	return "", false
}

// openMMDB loads the database.
//
// A FAILURE IS A VALUE, exactly as it is for the `.dat` reader: a page with no
// country flags is degraded, a panic in a collector is broken.
func openMMDB(path string) (*maxminddb.Reader, error) {
	r, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	// VALIDATE THE TYPE, because the filename is a convention and a MaxMind
	// Country database would open cleanly here and then answer "" for every
	// city — which reads as "this address has no city" rather than as a
	// misconfiguration.
	if t := r.Metadata.DatabaseType; !strings.Contains(strings.ToLower(t), "city") {
		r.Close()
		return nil, fmt.Errorf("%s is a %q database; a City database is required "+
			"(a Country one answers with no city and no coordinates, which looks "+
			"like missing data rather than the wrong file)", path, t)
	}
	return r, nil
}

// lookupMMDB answers the same question `(*DB).Lookup` does.
//
// ── THE REGION IS THE FIRST SUBDIVISION, AND MAY BE ABSENT ────────────────
//
// geoip-lite gives a region CODE; DB-IP gives a subdivision list whose first
// entry is the broadest. The English name is preferred and the ISO code is the
// fallback, because "England" is what a person reading a connection row wants
// and "ENG" is what they get when the name is missing.
func lookupMMDB(r *maxminddb.Reader, ip net.IP) (Location, bool) {
	if r == nil || ip == nil {
		return Location{}, false
	}
	var rec mmdbRecord
	if err := r.Lookup(ip, &rec); err != nil {
		return Location{}, false
	}
	if rec.Country.ISO == "" {
		// An address the database does not place is NOT an error. Private,
		// reserved and unallocated space all land here, and they are the
		// majority of what a LAN dashboard asks about.
		return Location{}, false
	}
	region := ""
	if len(rec.Subdivisions) > 0 {
		region = rec.Subdivisions[0].Names["en"]
		if region == "" {
			region = rec.Subdivisions[0].ISO
		}
	}
	return Location{
		Country: rec.Country.ISO,
		Region:  region,
		City:    rec.City.Names["en"],
	}, true
}
