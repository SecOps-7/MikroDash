package geo

// The location picker's gazetteer — the port of `src/cityIndex.js`.
//
// ── WHY IT EXISTS, AND WHY IT READS THE SAME FILES ──────────────────────────
//
// Locations are never typed as coordinates; they are chosen from a list, and
// that list cannot come from a bundled dataset (a new shipped file) or a
// geocoding API (an outbound request the CSP exists to prevent). So it is
// derived from geoip-lite's own data, which is already present — and the manual
// picker and the automatic WAN-IP fix then read the same database and cannot
// disagree about where a place is.
//
// ── THE RANKING IS THE CONTRACT ─────────────────────────────────────────────
//
// An exact match first; then WEIGHT descending — the number of address ranges
// pointing at the place, which is the only popularity signal this data carries;
// then the shorter name; then the country code.
//
// Ties beyond those four keep INSERTION ORDER. JavaScript's sort is stable, so
// this must use `sort.SliceStable` — `sort.Slice` is not, and real data ties on
// all four keys often enough that the difference is not theoretical.
//
// ── RECORD COUNTS ARE NOT STABLE ────────────────────────────────────────────
//
// The live header is explicit: counts "change with every geoip-lite data
// refresh. Nothing here, and no test, may assert one." `minRows` is a floor
// against a format change, not a count.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// A real database has ~110k places; this is a floor that catches a format
	// change, not an expected size.
	minRows  = 10000
	maxLimit = 50
	// The default when a caller passes no usable limit. `parseInt(x) || 20` on
	// the live side, which is why a limit of ZERO also lands here — 0 is falsy.
	defaultLimit = 20
)

// Place is one gazetteer entry.
type Place struct {
	Name   string  `json:"name"`
	Region string  `json:"region"`
	CC     string  `json:"cc"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
}

// CityIndex is the searchable gazetteer.
type CityIndex struct {
	places []Place
	// keys are the lowercased names, so the hot loop does no case folding —
	// the same reason the live module keeps a parallel array.
	keys   []string
	weight []uint32
}

// gazetteerName is the file `cmd/geogen` writes at image build.
const gazetteerName = "cities.json"

// buildFromGazetteer loads the generated gazetteer.
//
// The rows arrive already sorted and de-duplicated, so this does none of the
// merging the legacy path does — geogen owns that, and doing it twice would let
// the two disagree.
func buildFromGazetteer(path string) (*CityIndex, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Place
		W uint32 `json:"w"`
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(rows) < minRows {
		// The same floor the legacy path applies, for the same reason: a file
		// that parsed but holds almost nothing is a format change, and a picker
		// offering three cities looks like a working picker.
		return nil, errTooFew{}
	}
	idx := &CityIndex{
		places: make([]Place, 0, len(rows)),
		keys:   make([]string, 0, len(rows)),
		weight: make([]uint32, 0, len(rows)),
	}
	for _, r := range rows {
		idx.places = append(idx.places, r.Place)
		idx.keys = append(idx.keys, strings.ToLower(r.Name))
		idx.weight = append(idx.weight, r.W)
	}
	return idx, nil
}

// BuildCityIndex reads the gazetteer.
//
// PREFERS THE GENERATED FILE, falls back to geoip-lite's `.dat`. The fallback
// keeps an operator who points `-geo` at an old data volume working, and keeps
// the legacy decoder — which the differential gate still checks — reachable.
func BuildCityIndex(dir string) (*CityIndex, error) {
	if p := filepath.Join(dir, gazetteerName); fileHasBytes(p) {
		return buildFromGazetteer(p)
	}
	return buildFromDat(dir)
}

// fileHasBytes reports whether path is a non-empty regular file.
func fileHasBytes(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}

func buildFromDat(dir string) (*CityIndex, error) {
	names, err := os.ReadFile(dir + "/geoip-city-names.dat")
	if err != nil {
		return nil, err
	}
	if len(names) == 0 || len(names)%locRecordSize != 0 {
		return nil, errFormat{"geoip-city-names.dat", len(names), locRecordSize}
	}
	locCount := len(names) / locRecordSize

	ranges, err := os.ReadFile(dir + "/geoip-city.dat")
	if err != nil {
		return nil, err
	}
	if len(ranges) == 0 || len(ranges)%rangeRecordSize != 0 {
		return nil, errFormat{"geoip-city.dat", len(ranges), rangeRecordSize}
	}

	lat := make([]float64, locCount)
	lon := make([]float64, locCount)
	weight := make([]uint32, locCount)
	seen := make([]bool, locCount)

	for o := 0; o+rangeRecordSize <= len(ranges); o += rangeRecordSize {
		// The location id sits at byte 8 of a range record.
		id := int(binary.BigEndian.Uint32(ranges[o+8:]))
		if id == noLocation || id >= locCount {
			continue
		}
		weight[id]++
		if seen[id] {
			continue
		}
		// LAT AND LON COME FROM THE FIRST RANGE SEEN for this location, not the
		// last and not an average — the live scanner returns early once `seen`.
		seen[id] = true
		lat[id] = float64(int32(binary.BigEndian.Uint32(ranges[o+rangeLat:]))) / 10000
		lon[id] = float64(int32(binary.BigEndian.Uint32(ranges[o+rangeLon:]))) / 10000
	}

	idx := &CityIndex{}
	// A few hundred location ids share a (name, region, cc) triple. They are
	// collapsed so the picker does not offer the same place twice, and their
	// weights are SUMMED so the merged row ranks on the combined evidence — but
	// the FIRST row's coordinates are kept, not the heaviest one's.
	byKey := make(map[string]int, locCount/2)
	for id := 0; id < locCount; id++ {
		if !seen[id] {
			continue // no range points here
		}
		o := id * locRecordSize
		name := strings.TrimSpace(cstr(names[o+locCity : o+locRecordSize]))
		if name == "" {
			continue // a country-level row, not a place
		}
		cc := strings.TrimSpace(cstr(names[o+locCountry : o+locCountry+2]))
		region := strings.TrimSpace(cstr(names[o+locRegion : o+locRegion+3]))

		k := name + "|" + region + "|" + cc
		if at, ok := byKey[k]; ok {
			idx.weight[at] += weight[id]
			continue
		}
		byKey[k] = len(idx.places)
		idx.places = append(idx.places, Place{
			Name: name, Region: region, CC: cc, Lat: lat[id], Lon: lon[id],
		})
		idx.keys = append(idx.keys, strings.ToLower(name))
		idx.weight = append(idx.weight, weight[id])
	}

	if len(idx.places) < minRows {
		return nil, errTooFew{len(idx.places), minRows}
	}
	return idx, nil
}

// Search returns the places whose name starts with the query, ranked.
//
// `limit` is the caller's raw value, kept as a string because that is what a
// query parameter is; see `clampLimit` for why zero and a negative are treated
// differently.
func (c *CityIndex) Search(q string, limit string) []Place {
	query := strings.ToLower(strings.TrimSpace(q))
	// One letter matches thousands of places and is never a real intent.
	if len(query) < 2 {
		return []Place{}
	}
	cap := clampLimit(limit)

	hits := make([]int, 0, 64)
	for i, k := range c.keys {
		if strings.HasPrefix(k, query) {
			hits = append(hits, i)
		}
	}

	// STABLE, so ties beyond the four keys keep insertion order — which is
	// location-id order, and is what the live sort preserves.
	sort.SliceStable(hits, func(x, y int) bool {
		a, b := hits[x], hits[y]
		ea, eb := c.keys[a] != query, c.keys[b] != query
		if ea != eb {
			return !ea // an exact match first
		}
		if c.weight[a] != c.weight[b] {
			return c.weight[a] > c.weight[b]
		}
		if len(c.keys[a]) != len(c.keys[b]) {
			return len(c.keys[a]) < len(c.keys[b])
		}
		return c.places[a].CC < c.places[b].CC
	})

	if len(hits) > cap {
		hits = hits[:cap]
	}
	out := make([]Place, 0, len(hits))
	for _, i := range hits {
		out = append(out, c.places[i])
	}
	return out
}

// clampLimit is `Math.min(Math.max(parseInt(limit, 10) || 20, 1), MAX_LIMIT)`.
//
// THE ZERO CASE IS THE ONE TO GET RIGHT. `parseInt("0") || 20` is 20, because 0
// is falsy — so a caller asking for none gets the DEFAULT, not one and not none.
// A negative parses truthy and is then clamped up to 1. Both are pinned by the
// corpus rather than derived from the expression, which reads as though 0 and -3
// would behave alike.
func clampLimit(raw string) int {
	n, ok := jsParseInt(raw)
	if !ok || n == 0 {
		n = defaultLimit
	}
	if n < 1 {
		n = 1
	}
	if n > maxLimit {
		n = maxLimit
	}
	return n
}

// jsParseInt is `parseInt(s, 10)`: leading whitespace and an optional sign, then
// as many digits as it can take, and NaN when there are none. "20.9" is 20 and
// "abc" is nothing.
func jsParseInt(s string) (int, bool) {
	s = strings.TrimSpace(s)
	i, neg := 0, false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	n := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		n = n*10 + int(s[i]-'0')
		i++
	}
	if i == start {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

type errFormat struct {
	file string
	size int
	unit int
}

func (e errFormat) Error() string {
	return e.file + " is not a multiple of the record size"
}

type errTooFew struct{ got, want int }

func (e errTooFew) Error() string { return "too few places decoded" }
