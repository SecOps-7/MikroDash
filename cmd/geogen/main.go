// Command geogen builds the city gazetteer from a DB-IP City Lite database.
//
// ── WHY A GENERATOR AND NOT A LOOKUP AT RUNTIME ───────────────────────────
//
// The picker behind `GET /api/cities` needs a LIST of places to prefix-match.
// An .mmdb is a lookup structure, not a list: the only way to enumerate it is to
// walk every network, and MEASURED on the 2026-08 file that is 14,727,275
// networks taking ~35 seconds. `CityHolder` builds lazily on first search, so
// doing it there would hang the first keystroke for half a minute.
//
// So it is done once, at image build, into a file the runtime just reads.
//
// ── THE WEIGHT IS THE SAME QUANTITY THE OLD INDEX USED ────────────────────
//
// `BuildCityIndex` counts `weight[id]++` for every RANGE pointing at a location,
// and ranks the picker on it — a proxy for how much of the internet is there, so
// that "london" offers London before Londonderry. This counts networks per city
// for exactly that reason. Getting it wrong would not fail a test; it would just
// quietly make the picker worse, which is why it is worth stating.
//
//	go run ./cmd/geogen -mmdb <path> -out <path>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/oschwald/maxminddb-golang"
)

// entry is one gazetteer row as written to disk. The field names match
// `geo.Place`'s json tags so the loader decodes into it directly, plus `w`.
type entry struct {
	Name   string  `json:"name"`
	Region string  `json:"region"`
	CC     string  `json:"cc"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	W      uint32  `json:"w"`
}

type record struct {
	Country struct {
		ISO string `maxminddb:"iso_code"`
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

type key struct{ cc, region, city string }

func main() {
	mmdb := flag.String("mmdb", "", "the DB-IP City Lite database to read")
	out := flag.String("out", "", "the gazetteer to write")
	minRows := flag.Int("min", 10000, "fail if fewer places than this are found")
	flag.Parse()
	if *mmdb == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: geogen -mmdb <path> -out <path>")
		os.Exit(2)
	}

	db, err := maxminddb.Open(*mmdb)
	if err != nil {
		fmt.Fprintln(os.Stderr, "geogen:", err)
		os.Exit(1)
	}
	defer db.Close()

	seen := map[key]*entry{}
	networks := 0
	// SkipAliasedNetworks: without it the v6 tree's mapped-v4 aliases are walked
	// again, counting the same ranges twice and inflating every weight that has
	// a v4 presence.
	it := db.Networks(maxminddb.SkipAliasedNetworks)
	for it.Next() {
		var r record
		if _, err := it.Network(&r); err != nil {
			continue
		}
		networks++
		city := r.City.Names["en"]
		if city == "" || r.Country.ISO == "" {
			continue
		}
		region := ""
		if len(r.Subdivisions) > 0 {
			region = r.Subdivisions[0].Names["en"]
			if region == "" {
				region = r.Subdivisions[0].ISO
			}
		}
		k := key{r.Country.ISO, region, city}
		if e, ok := seen[k]; ok {
			e.W++
			continue
		}
		seen[k] = &entry{
			Name: city, Region: region, CC: r.Country.ISO,
			Lat: r.Location.Lat, Lon: r.Location.Lon, W: 1,
		}
	}
	if err := it.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "geogen: walking the database:", err)
		os.Exit(1)
	}

	rows := make([]entry, 0, len(seen))
	for _, e := range seen {
		rows = append(rows, *e)
	}
	// DETERMINISTIC ORDER, so two builds of the same database produce the same
	// bytes and a diff of the output means the DATA changed. Sorted by the
	// identifying fields rather than by weight, because weight is a tie-breaker
	// the index applies at search time and sorting on it here would bury that.
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.CC != b.CC {
			return a.CC < b.CC
		}
		if a.Region != b.Region {
			return a.Region < b.Region
		}
		return a.Name < b.Name
	})

	// A FLOOR, NOT AN EXPECTED SIZE — the same guard `cityindex.go` applies to
	// the legacy format. A database that opened but yielded almost nothing is a
	// format change, and writing the file anyway would ship a picker that
	// silently offers three cities.
	if len(rows) < *minRows {
		fmt.Fprintf(os.Stderr, "geogen: only %d places from %d networks (floor %d). "+
			"That is a format change or the wrong database, not a small world.\n",
			len(rows), networks, *minRows)
		os.Exit(1)
	}

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "geogen:", err)
		os.Exit(1)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(rows); err != nil {
		fmt.Fprintln(os.Stderr, "geogen: writing:", err)
		os.Exit(1)
	}
	fmt.Printf("geogen: %d places from %d networks -> %s\n", len(rows), networks, *out)
}
