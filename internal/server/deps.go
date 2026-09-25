package server

// What is actually inside this binary, and under what licence.
//
// ── THE VERSIONS COME FROM THE BINARY, THE LICENCES FROM A LEDGER ──────────
//
// `debug.ReadBuildInfo()` reports every module the linker put in, with the
// version it resolved. That half cannot drift: it is the build describing
// itself, so a dependency added, removed or bumped shows here with no list to
// remember.
//
// It carries no licence, so the other half is the table below. Every entry was
// read from that module's OWN LICENSE file in the module cache, not from
// memory, which is the rule `THIRD_PARTY_NOTICES.md` already states about
// itself. `TestEveryLinkedModuleHasARecordedLicence` fails in both directions:
// a module with no entry, and an entry naming no module.
//
// ── WHY BOTH DIRECTIONS MATTER ─────────────────────────────────────────────
//
// A missing entry means the About page shows a dependency with a blank licence,
// which is the thing this exists to prevent. A stale entry is worse in a quieter
// way: it looks like a considered attribution for something no longer shipped,
// and the next person adds to the list rather than checking it.

import (
	"runtime/debug"
	"sort"
	"strings"
)

// Dependency is one thing this build ships.
type Dependency struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Licence string `json:"licence"`
	// URL is where the project lives, for the link beside a row. Empty for a
	// module path that is not one.
	URL string `json:"url,omitempty"`
	// Kind separates what the Go binary links from what the browser loads, so
	// the page can say which is which rather than mixing them into one list.
	Kind string `json:"kind"`
}

// KindGo and KindWeb are the two halves of what ships.
const (
	KindGo  = "go"
	KindWeb = "web"
)

// moduleLicences is every Go module linked into `cmd/mikrodash`, with the
// licence read from its own LICENSE file on 2026-09-25.
//
// BSD-3-Clause rather than BSD-2-Clause throughout: each BSD entry was checked
// for the third clause ("may be used to endorse or promote"), because the two
// are different licences and naming the wrong one is a false statement about
// somebody else's terms.
var moduleLicences = map[string]string{
	"github.com/coder/websocket":              "ISC",
	"github.com/dustin/go-humanize":           "MIT",
	"github.com/go-pdf/fpdf":                  "MIT",
	"github.com/go-routeros/routeros/v3":      "MIT",
	"github.com/google/btree":                 "Apache-2.0",
	"github.com/google/uuid":                  "BSD-3-Clause",
	"github.com/oschwald/maxminddb-golang/v2": "ISC",
	"github.com/remyoudompheng/bigfft":        "BSD-3-Clause",
	"golang.org/x/crypto":                     "BSD-3-Clause",
	"golang.org/x/net":                        "BSD-3-Clause",
	"golang.org/x/sys":                        "BSD-3-Clause",
	"golang.org/x/time":                       "BSD-3-Clause",
	"golang.zx2c4.com/wireguard":              "MIT",
	"gvisor.dev/gvisor":                       "Apache-2.0",
	"modernc.org/libc":                        "BSD-3-Clause",
	"modernc.org/mathutil":                    "BSD-3-Clause",
	"modernc.org/memory":                      "BSD-3-Clause",
	"modernc.org/sqlite":                      "BSD-3-Clause",
}

// webLibraries is what the BROWSER loads and the Go build info knows nothing
// about: vendored files under `web/public`, credited in
// `THIRD_PARTY_NOTICES.md` and listed here so the About page can show them
// beside the modules.
//
// HAND-MAINTAINED, and that is the difference from the map above. Nothing in
// the build reports these, so the ledger checks them against
// `THIRD_PARTY_NOTICES.md`, which is the file that has to be right anyway.
var webLibraries = []Dependency{
	{Name: "Chart.js", Version: "4.4.2", Licence: "MIT",
		URL: "https://github.com/chartjs/Chart.js", Kind: KindWeb},
	{Name: "Tabler", Version: "2.47.0", Licence: "MIT",
		URL: "https://github.com/tabler/tabler-icons", Kind: KindWeb},
	{Name: "world-atlas", Version: "2.0.2", Licence: "ISC",
		URL: "https://github.com/topojson/world-atlas", Kind: KindWeb},
	{Name: "Fonts", Version: "JetBrains Mono, Oxanium", Licence: "SIL Open Font License 1.1",
		URL: "https://github.com/JetBrains/JetBrainsMono", Kind: KindWeb},
	{Name: "IP geolocation data", Version: "DB-IP City and ASN Lite", Licence: "CC BY 4.0",
		URL: "https://db-ip.com/db/download/ip-to-city-lite", Kind: KindWeb},
}

// Dependencies is everything this build ships, Go modules first, each with the
// licence it is under.
//
// SORTED BY NAME WITHIN EACH HALF, because the page lists them and the linker's
// own order means nothing to a reader.
func Dependencies() []Dependency {
	out := []Dependency{}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, m := range info.Deps {
			// A REPLACED MODULE IS REPORTED UNDER ITS ORIGINAL PATH, which is
			// what `go.mod` and the licence table both name. `go-routeros` is
			// replaced by the patched copy in third_party and would otherwise
			// show a filesystem path and no version at all.
			version := m.Version
			if m.Replace != nil && m.Replace.Version != "" {
				version = m.Replace.Version
			}
			out = append(out, Dependency{
				Name: m.Path, Version: version,
				Licence: moduleLicences[m.Path],
				URL:     moduleURL(m.Path),
				Kind:    KindGo,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return append(out, webLibraries...)
}

// moduleURL turns a module path into a link, for the paths that are one.
//
// A Go module path is a URL by convention rather than by rule, so this handles
// the hosts this build actually uses and leaves anything else unlinked rather
// than guessing a page that answers 404.
func moduleURL(path string) string {
	switch {
	case strings.HasPrefix(path, "github.com/"):
		// A major-version suffix is part of the MODULE path and not part of the
		// repository: `github.com/x/y/v3` lives at `github.com/x/y`.
		if i := strings.LastIndex(path, "/v"); i > 0 && isAllDigits(path[i+2:]) {
			path = path[:i]
		}
		return "https://" + path
	case strings.HasPrefix(path, "golang.org/x/"),
		strings.HasPrefix(path, "modernc.org/"),
		strings.HasPrefix(path, "gvisor.dev/"),
		strings.HasPrefix(path, "golang.zx2c4.com/"):
		return "https://pkg.go.dev/" + path
	}
	return ""
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
