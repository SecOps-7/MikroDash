// Package geo answers "where is this address", by reading the same data the
// live app reads.
//
// ── IT READS geoip-lite's OWN FILES, DELIBERATELY ───────────────────────────
//
// The Node app uses `geoip-lite`, an npm package carrying its own binary data.
// A Go port cannot require that package, and the obvious alternative — a Go
// MaxMind reader with an .mmdb — would be a DIFFERENT DATA SOURCE giving
// different answers. Every disagreement would then need triaging as "port
// defect, or different database?", which is exactly the question the whole
// differential approach exists to remove.
//
// So this reads `geoip-city.dat` and `geoip-city-names.dat` directly, with the
// same binary search geoip-lite performs. Same bytes in, same answers out, and
// tools/geo-cases.js is what proves it.
//
// ── THE FORMAT IS UNDOCUMENTED, SO FAILURE IS A VALUE ───────────────────────
//
// These offsets are geoip-lite's internal layout, read from its lib/geoip.js.
// The package may change them in a patch release without saying so. Every load
// is therefore validated and every failure is reported as "unavailable" rather
// than thrown: a router page with no country flags is a degraded page, while a
// panic in a collector is a broken one. The live app makes the same choice for
// the same reason.
package geo

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"

	"github.com/oschwald/maxminddb-golang"
)

// The record geometry, from geoip-lite's `conf4` and `conf6`.
const (
	rangeRecordSize  = 24 // geoip-city.dat
	rangeRecordSize6 = 48 // geoip-city6.dat
	locRecordSize    = 88 // geoip-city-names.dat
	// TYPED uint32, not untyped. It is read from a big-endian uint32 field and
	// compared against one. Left untyped it defaulted to int, which overflows on
	// a 32-bit build -- ARMv7 could not compile this file at all.
	noLocation uint32 = 4294967295 // (-1 >>> 0), the "no location" marker

	// Field offsets inside a location record (geoip-city-names.dat).
	locCountry = 0  // 2 bytes
	locRegion  = 2  // 3 bytes
	locCity    = 42 // to the end of the record

	// ── COORDINATES LIVE IN THE *RANGE* RECORD, NOT THE NAMES RECORD ───────
	//
	// Easy to get backwards, because country, region and city all come out of
	// the names file and it reads as though the whole location did. geoip-lite
	// takes latitude, longitude and the accuracy radius off the RANGE record it
	// already has in hand — `buffer.readInt32BE((line * recordSize) + 12)` and
	// friends — while reading the text out of `locBuffer`. Two files, one
	// record.
	//
	// The coordinates are stored as signed integers at ten-thousandths of a
	// degree; the radius is an unsigned count of kilometres.
	rangeLat  = 12 // int32, /10000
	rangeLon  = 16 // int32, /10000
	rangeArea = 20 // uint32, km
	// The same three in a v6 range record, which is 48 bytes rather than 24.
	range6Lat  = 36
	range6Lon  = 40
	range6Area = 44

	// Inside a v6 range record: floor at 0, ceil at 16, the location id at 32.
	// THE NAMES COME FROM THE v4 FILE — geoip-lite's lookup6 reads `locId` out
	// of the v6 record and then indexes `cache4.locationBuffer` with it. There
	// is no separate v6 names file and looking for one would be a wrong turn.
	loc6Offset = 32
)

// Location is what the collectors read, plus what the Routers map needs.
//
// It used to carry Country and City alone, on the reasoning that nothing else
// was consumed. `autoGeoAction` in the Routers page changed that: it reads the
// coordinate pair and the accuracy radius off this same lookup, and the region
// reaches the rendered label. geoip-lite's timezone, metro code and EU flag are
// still not decoded, because nothing renders those.
//
// ── Lat AND Lon ARE POINTERS, AND THAT IS THE WHOLE CARE OF THIS TYPE ──────
//
// geoip-lite initialises `ll` to `[null, null]` and fills it ONLY when the
// range resolves to a location record. A range can be a hit with no location —
// see the noLocation sentinel below — and 0,0 is a real place in the Gulf of
// Guinea. Returning zeros for "unknown" would put every such router in the sea
// off west Africa, which is precisely the failure internal/geoplace exists to
// prevent; it would be undone here if this were two float64s.
type Location struct {
	Country string
	Region  string
	City    string
	// Lat and Lon are nil when the record carried no location. Never zero for
	// absence — see above.
	Lat *float64
	Lon *float64
	// Area is the accuracy radius in kilometres: about 5 for a real city fix,
	// 1000 for a country centroid. Zero when absent, which is faithful — the
	// original leaves the field undefined and every consumer spells it
	// `Number(g.area) || 0`, so absent and zero are already the same value.
	Area uint32
}

// coordsAt decodes the coordinate pair and the radius out of a range record.
//
// Called only once a location id has been resolved, because that is the
// condition under which geoip-lite fills them: a hit with no location record
// keeps the nulls.
func coordsAt(rec []byte, latOff, lonOff, areaOff int) (*float64, *float64, uint32) {
	lat := float64(int32(binary.BigEndian.Uint32(rec[latOff:]))) / 10000
	lon := float64(int32(binary.BigEndian.Uint32(rec[lonOff:]))) / 10000
	return &lat, &lon, binary.BigEndian.Uint32(rec[areaOff:])
}

// DB is a loaded geo database — DB-IP City Lite when one is present, otherwise
// geoip-lite's own files.
//
// TWO BACKENDS BEHIND ONE TYPE, so every caller — the collectors, the Devices
// page, the connections map — keeps the API it already had. `mmdb` being
// non-nil is what decides; everything below it is the legacy reader and is only
// reached when no .mmdb was found. See mmdb.go for why the swap happened.
type DB struct {
	// mmdb is the CURRENT backend. When it is set, the fields below are unused.
	mmdb *maxminddb.Reader

	main []byte
	loc  []byte
	// firstIP and lastIP bound the index. Outside them there is nothing to
	// find, and geoip-lite returns early rather than searching.
	firstIP, lastIP uint32
	lastLine        int

	// The v6 half. `main6` is nil when geoip-city6.dat is absent, and v6Reason
	// says why — see the note on Lookup's v6 branch for what that costs.
	main6             []byte
	lastLine6         int
	firstIP6, lastIP6 ip6
	v6Reason          string
}

// ip6 is the HIGH 64 BITS of an IPv6 address, and that is not a shortcut.
// geoip-lite's `cmp6` compares two elements of a four-element array, and its
// `readip` reads two uint32s out of a sixteen-byte field — so both the index
// and every comparison against it are made on the first eight bytes only. A
// reader comparing all 128 bits would order addresses differently from the file
// it is searching and would find the wrong record at some boundaries.
type ip6 [2]uint32

func cmp6(a, b ip6) int {
	for i := 0; i < 2; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

var (
	once   sync.Once
	shared *DB
	reason string
)

// Load reads the database from dir.
//
// PREFERS DB-IP, FALLS BACK TO geoip-lite. The fallback is not politeness: an
// operator who points `-geo` at a volume of `.dat` files should keep working
// rather than lose their country flags to an upgrade, and the legacy reader is
// still the only thing the geo corpus can check against the Node app.
func Load(dir string) (*DB, error) {
	if p, ok := mmdbPath(dir); ok {
		r, err := openMMDB(p)
		if err != nil {
			return nil, err
		}
		return &DB{mmdb: r}, nil
	}
	main, err := os.ReadFile(filepath.Join(dir, "geoip-city.dat"))
	if err != nil {
		return nil, err
	}
	loc, err := os.ReadFile(filepath.Join(dir, "geoip-city-names.dat"))
	if err != nil {
		return nil, err
	}
	if len(main) == 0 || len(main)%rangeRecordSize != 0 {
		return nil, fmt.Errorf("geo: geoip-city.dat is %d bytes, not a multiple of %d",
			len(main), rangeRecordSize)
	}
	if len(loc) == 0 || len(loc)%locRecordSize != 0 {
		return nil, fmt.Errorf("geo: geoip-city-names.dat is %d bytes, not a multiple of %d",
			len(loc), locRecordSize)
	}
	lastLine := len(main)/rangeRecordSize - 1
	db := &DB{
		main: main, loc: loc, lastLine: lastLine,
		firstIP: binary.BigEndian.Uint32(main[0:4]),
		lastIP:  binary.BigEndian.Uint32(main[lastLine*rangeRecordSize+4:]),
	}
	if db.firstIP > db.lastIP {
		return nil, errors.New("geo: the index is not ordered — the on-disk format has changed")
	}
	loadV6(db, dir)
	return db, nil
}

// loadV6 attaches the IPv6 index, and DOES NOT FAIL THE LOAD if it cannot.
//
// A missing or malformed geoip-city6.dat leaves every IPv4 answer correct, and
// v4 is the overwhelming majority of what a home router sees. Refusing to load
// at all would turn a partial degradation into a total one.
//
// What is NOT reproduced, deliberately: geoip-lite falls back to
// geoip-country6.dat with a 34-byte record when city6 is missing. That is a
// different format for a configuration that does not occur here — the app
// container ships city6 — and writing a second decoder for it would be code no
// test could reach. If city6 ever does go missing, `v6Reason` says so and v6
// lookups report not-found, which is visible rather than silently wrong.
func loadV6(db *DB, dir string) {
	main6, err := os.ReadFile(filepath.Join(dir, "geoip-city6.dat"))
	if err != nil {
		db.v6Reason = err.Error()
		return
	}
	if len(main6) == 0 || len(main6)%rangeRecordSize6 != 0 {
		db.v6Reason = fmt.Sprintf("geoip-city6.dat is %d bytes, not a multiple of %d",
			len(main6), rangeRecordSize6)
		return
	}
	lastLine6 := len(main6)/rangeRecordSize6 - 1
	first := readIP6(main6, 0, 0)
	last := readIP6(main6, lastLine6, 1)
	if cmp6(first, last) > 0 {
		db.v6Reason = "the v6 index is not ordered — the on-disk format has changed"
		return
	}
	db.main6, db.lastLine6, db.firstIP6, db.lastIP6 = main6, lastLine6, first, last
}

// readIP6 reads the floor (offset 0) or ceil (offset 1) of a v6 range record.
func readIP6(buf []byte, line, which int) ip6 {
	off := line*rangeRecordSize6 + which*16
	return ip6{
		binary.BigEndian.Uint32(buf[off:]),
		binary.BigEndian.Uint32(buf[off+4:]),
	}
}

// Shared loads the database once from the usual place, and reports whether it
// is available. Every caller gates on the bool rather than on a non-nil handle,
// so "no database" is a state the code names.
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

// Current is the database Shared already loaded, for callers that have no
// business knowing where it lives.
//
// This mirrors the live app's `src/geo.js` deliberately. That module exists
// because geoip-lite had been required at three call sites, two of which
// swallowed the load failure — leaving every lookup silently returning nothing
// while the dashboard looked healthy. One load point, one place the failure is
// reported, and every caller gating on availability rather than on a handle.
func Current() (*DB, bool) { return shared, shared != nil }

// Lookuper adapts a database to the collectors' GeoLookup shape: country and
// city, empty when unknown.
//
// A HIT WITH NO COUNTRY IS TREATED AS A MISS, which is the live app's rule
// rather than this reader's — `_geo` in both collectors does
// `g && g.country ? … : {country:”, city:”}`. Lookup itself keeps the
// distinction, because it has to agree with geoip-lite; the collectors do not
// want it.
func (d *DB) Lookuper() func(ip string) (string, string) {
	return func(ip string) (string, string) {
		loc, ok := d.Lookup(ip)
		if !ok || loc.Country == "" {
			return "", ""
		}
		return loc.Country, loc.City
	}
}

// privateRanges are refused BEFORE the search, exactly as geoip-lite does, and
// are ITS three ranges rather than the full RFC 1918-and-friends set —
// `privateRange4` in lib/geoip.js. Adding 127/8 or 169.254/16 here would be an
// improvement over the reference, which is the one thing a port may not do.
//
// An earlier version of this comment claimed these addresses fall inside the
// index, so that searching anyway would place a LAN host in another country.
// THAT IS NOT TRUE OF THIS DATA and it was worth measuring rather than
// repeating: not one of the three million index records overlaps any private
// range, so a private address falls in a gap and misses on its own. See the
// call site in Lookup for why the refusal is kept regardless.
var privateRanges = [][2]uint32{
	{aton("10.0.0.0"), aton("10.255.255.255")},
	{aton("172.16.0.0"), aton("172.31.255.255")},
	{aton("192.168.0.0"), aton("192.168.255.255")},
}

func aton(s string) uint32 {
	addr, err := netip.ParseAddr(s)
	if err != nil || !addr.Is4() {
		return 0
	}
	b := addr.As4()
	return binary.BigEndian.Uint32(b[:])
}

// Lookup finds an address, or reports false when it is unknown.
//
// THE DISPATCH IS geoip-lite's, including the v4-mapped case. An address like
// `::ffff:203.0.113.7` is unwrapped and searched in the V4 index — it is a v4
// address wearing a v6 costume, and the v6 index does not contain it.
//
// An earlier version of this reader handled v4 only, on the reasoning that the
// collectors "only ever ask about v4 addresses". They do not: the live app
// gates its lookups on `ipaddr.isValid`, which accepts v6, so every v6
// destination a router sees is geo-located there. Refusing them here would have
// emptied the world map of exactly the traffic that is growing.
func (d *DB) Lookup(ip string) (Location, bool) {
	if d == nil {
		return Location{}, false
	}
	if d.mmdb != nil {
		// PARSED THE SAME WAY FIRST, deliberately. The rules below — no
		// TrimSpace, a zone ignored rather than refused — are behaviour this
		// package already committed to, and routing v4/v6 through net.ParseIP
		// here instead would quietly change which strings are answered.
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			return Location{}, false
		}
		return lookupMMDB(d.mmdb, net.IP(addr.Unmap().AsSlice()))
	}
	// NO TrimSpace. `net.isIP(' 8.8.8.8 ')` is 0, so geoip-lite returns null for
	// a padded address — trimming would answer where the live app does not. The
	// same well-meant call was found and removed in internal/asn on the day this
	// comment was written; both were written as a kindness and both were
	// behaviour changes.
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return Location{}, false
	}
	// A ZONE IS IGNORED, NOT REFUSED. `net.isIP('2001:4860::1%eth0')` is 6, and
	// geoip-lite's aton6 then parses the last group with parseInt, which stops at
	// the `%` — so the live app places that address exactly as if the zone were
	// absent. This side refused it, which was another well-meant strictness the
	// reference does not have. The zone needs no explicit strip here because the
	// search reads As16(), which does not carry one.
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	if !addr.Is4() {
		return d.lookup6(addr)
	}
	b := addr.As4()
	n := binary.BigEndian.Uint32(b[:])

	if n > d.lastIP || n < d.firstIP {
		return Location{}, false
	}
	// This refusal is BELT AND BRACES, not load-bearing, and the distinction was
	// measured rather than assumed: deleting it changed no answer, and scanning
	// all three million index records against 10/8, 172.16/12, 192.168/16,
	// 127/8, 169.254/16, 192.0.2/24 and 100.64/10 found not one record
	// overlapping any of them. A private address falls in a gap and misses on
	// its own. It is kept because geoip-lite has it, because it is the cheaper
	// answer for the addresses this dashboard sees most, and because "the data
	// happens not to contain one" is a property of a file that gets refreshed.
	for _, r := range privateRanges {
		if n >= r[0] && n <= r[1] {
			return Location{}, false
		}
	}

	// geoip-lite's own search, reproduced including its unusual midpoint —
	// `round((cline-fline)/2) + fline` rather than the usual floor.
	//
	// THE MIDPOINT IS NOT OBSERVABLE, and saying so here is the point. Swapping
	// in the ordinary floor was tried as a deliberate mutation and no test
	// noticed, which looked like a hole in the gate; running both variants over
	// the six boundary addresses of every 37th record — 516,275 lookups — showed
	// they agree on every one. They have to: the index is sorted and disjoint,
	// so the two probe different records on the way down and converge on the
	// same one, and the `fline == cline-1` case checks both endpoints either
	// way. The faithful form is kept because it costs nothing and matches the
	// reference, not because a test defends it. Do not add one that pretends to.
	fline, cline := 0, d.lastLine
	for {
		line := int((float64(cline-fline)/2)+0.5) + fline
		off := line * rangeRecordSize
		floor := binary.BigEndian.Uint32(d.main[off:])
		ceil := binary.BigEndian.Uint32(d.main[off+4:])

		if floor <= n && ceil >= n {
			locID := binary.BigEndian.Uint32(d.main[off+8:])
			if locID >= noLocation {
				// THE RANGE IS STILL A HIT. geoip-lite returns its record here
				// with every field empty — the sentinel means "this address is
				// in the index and nothing is known about it", which is not the
				// same as "not in the index". Reporting a miss instead was a
				// real difference, and the only thing that caught it was
				// recording `found` separately from `country` in the case set.
				return Location{}, true
			}
			lo := int(locID) * locRecordSize
			if lo+locRecordSize > len(d.loc) {
				return Location{}, false
			}
			rec := d.loc[lo : lo+locRecordSize]
			lat, lon, area := coordsAt(d.main[off:off+rangeRecordSize],
				rangeLat, rangeLon, rangeArea)
			return Location{
				Country: cstr(rec[locCountry : locCountry+2]),
				Region:  cstr(rec[locRegion : locRegion+3]),
				City:    cstr(rec[locCity:locRecordSize]),
				Lat:     lat, Lon: lon, Area: area,
			}, true
		}
		if fline == cline {
			return Location{}, false
		}
		if fline == cline-1 {
			if line == fline {
				fline = cline
			} else {
				cline = fline
			}
			continue
		}
		if floor > n {
			cline = line
		} else if ceil < n {
			fline = line
		} else {
			return Location{}, false
		}
	}
}

// lookup6 is the same search over the v6 index, and the differences from the v4
// one are all in the reference rather than chosen here:
//
//   - NO PRIVATE-RANGE REFUSAL. geoip-lite has no `privateRange6`, so a ULA or
//     link-local address is searched like any other. It misses, because the
//     index does not carry those ranges — but it misses by searching.
//   - The comparison is on the HIGH 64 BITS ONLY. See the `ip6` type.
//   - The location id lives at offset 32 and indexes the SAME names file the v4
//     records index.
func (d *DB) lookup6(addr netip.Addr) (Location, bool) {
	if d.main6 == nil {
		return Location{}, false
	}
	b := addr.As16()
	n := ip6{binary.BigEndian.Uint32(b[0:]), binary.BigEndian.Uint32(b[4:])}

	if cmp6(n, d.lastIP6) > 0 || cmp6(n, d.firstIP6) < 0 {
		return Location{}, false
	}

	fline, cline := 0, d.lastLine6
	for {
		line := int((float64(cline-fline)/2)+0.5) + fline
		floor := readIP6(d.main6, line, 0)
		ceil := readIP6(d.main6, line, 1)

		if cmp6(floor, n) <= 0 && cmp6(ceil, n) >= 0 {
			locID := binary.BigEndian.Uint32(d.main6[line*rangeRecordSize6+loc6Offset:])
			if locID >= noLocation {
				return Location{}, true
			}
			lo := int(locID) * locRecordSize
			if lo+locRecordSize > len(d.loc) {
				return Location{}, false
			}
			rec := d.loc[lo : lo+locRecordSize]
			// THE CITY IS READ, THOUGH geoip-lite's OWN COMMENT SAYS IT HAS NO
			// v6 city data ("We do not currently have detailed region/city info
			// for IPv6"). It reads the field anyway, out of the same location
			// record, so whatever the field holds is what the live app shows.
			// Skipping it here on the strength of that comment would be this
			// port disagreeing with the app it is copying.
			r6 := d.main6[line*rangeRecordSize6 : (line+1)*rangeRecordSize6]
			lat, lon, area := coordsAt(r6, range6Lat, range6Lon, range6Area)
			return Location{
				Country: cstr(rec[locCountry : locCountry+2]),
				Region:  cstr(rec[locRegion : locRegion+3]),
				City:    cstr(rec[locCity:locRecordSize]),
				Lat:     lat, Lon: lon, Area: area,
			}, true
		}
		if fline == cline {
			return Location{}, false
		}
		if fline == cline-1 {
			if line == fline {
				fline = cline
			} else {
				cline = fline
			}
			continue
		}
		if cmp6(floor, n) > 0 {
			cline = line
		} else if cmp6(ceil, n) < 0 {
			fline = line
		} else {
			return Location{}, false
		}
	}
}

// cstr reads a NUL-terminated string out of a fixed-width field, which is how
// geoip-lite stores every text value — its own reader strips everything from
// the first NUL onward with a regex. The NUL is written as an escape here and
// never as a literal: a control character in source is invisible in a diff, and
// this project has lost time to that twice already.
func cstr(b []byte) string {
	if i := indexZero(b); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func indexZero(b []byte) int {
	for i, c := range b {
		if c == '\x00' {
			return i
		}
	}
	return -1
}
