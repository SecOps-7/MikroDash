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
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
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
	// Note qualifies the version when the version alone would mislead: the one
	// case is `patched`, for a module `go.mod` replaces with a local copy.
	Note string `json:"note,omitempty"`
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

// directModules is what `go.mod` REQUIRES BY NAME, as opposed to what those
// requirements drag in behind them.
//
// ── WHY THE PAGE SHOWS THESE AND NOT THE FULL GRAPH ────────────────────────
//
// The binary links 18 modules. Seven of them are choices this project made and
// `CLAUDE.md` argues for one by one; the other eleven are `modernc.org/sqlite`
// and `wireguard` bringing their own transitive closure, and nobody chose them.
// Listing all 18 answers "what is in the binary", which is a licence question
// and is what `THIRD_PARTY_NOTICES.md` is for. The About page answers "what
// does MikroDash depend on", and eleven modules the operator has never heard of
// are noise in front of that.
//
// `github.com/evanw/esbuild` is in this list and NOT on the page: it is a
// direct requirement that `cmd/webbuild` uses to build the front end and the
// server binary never links. The filter falls out of `Dependencies()` reading
// the BUILD's own module list rather than this one, so nothing has to remember
// it.
//
// LEDGERED against go.mod's own require block, both ways, by
// `TestEveryDirectRequirementIsRecordedAsDirect`. A new direct dependency that
// is not here would silently never appear on the page.
var directModules = map[string]bool{
	"github.com/coder/websocket":              true,
	"github.com/evanw/esbuild":                true,
	"github.com/go-pdf/fpdf":                  true,
	"github.com/go-routeros/routeros/v3":      true,
	"github.com/oschwald/maxminddb-golang/v2": true,
	"golang.org/x/crypto":                     true,
	"golang.zx2c4.com/wireguard":              true,
	"modernc.org/sqlite":                      true,
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
}

// geoLibraries is the two DB-IP databases, SEPARATELY.
//
// They were one row reading "IP geolocation data / DB-IP City and ASN Lite",
// which put a description where every other row has a version and hid the fact
// that they are two files, fetched independently, that can be from different
// months.
//
// ── THE VERSION IS READ OUT OF THE FILE ────────────────────────────────────
//
// DB-IP publishes monthly and the Dockerfile fetches whichever month is
// available at build time, trying the current one and then the one before. So
// no constant here could be right: the version is whatever was actually
// fetched, and every MMDB carries its own build date in its metadata. Reading
// it is the only answer that cannot drift, and it costs one open per request
// on a page opened once.
var geoLibraries = []Dependency{
	{Name: "DB-IP City Lite", Licence: "CC BY 4.0",
		URL: "https://db-ip.com/db/download/ip-to-city-lite", Kind: KindWeb},
	{Name: "DB-IP ASN Lite", Licence: "CC BY 4.0",
		URL: "https://db-ip.com/db/download/ip-to-asn-lite", Kind: KindWeb},
}

// geoFiles maps each database to the file it ships as, in `geoLibraries` order.
var geoFiles = []string{"dbip-city-lite.mmdb", "dbip-asn-lite.mmdb"}

// geoDatabases is `geoLibraries` with each version read from its own file.
//
// A database that is missing or unreadable is still LISTED, with no version.
// The geo features degrade rather than fail when a file is absent, and a page
// that silently dropped the row would disagree with the rest of the app about
// what shipped.
func geoDatabases(dir string) []Dependency {
	out := make([]Dependency, len(geoLibraries))
	copy(out, geoLibraries)
	for i := range out {
		r, err := maxminddb.Open(filepath.Join(dir, geoFiles[i]))
		if err != nil {
			continue
		}
		if t := r.Metadata.BuildTime(); !t.IsZero() {
			out[i].Version = t.UTC().Format("2006-01-02")
		}
		r.Close()
	}
	return out
}

// Dependencies is what this project DEPENDS ON: the modules `go.mod` names and
// the libraries the browser loads, each with the licence it is under.
//
// NOT THE WHOLE MODULE GRAPH. The transitive closure is a licence question and
// `THIRD_PARTY_NOTICES.md` answers it; this answers a different one, and the
// eleven modules `sqlite` and `wireguard` bring with them would bury the seven
// that were chosen.
//
// SORTED BY NAME WITHIN EACH HALF, because the page lists them and the linker's
// own order means nothing to a reader.
func Dependencies(geoDir string) []Dependency {
	out := []Dependency{}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, m := range info.Deps {
			if !directModules[m.Path] {
				continue
			}
			version, note := moduleVersion(m)
			out = append(out, Dependency{
				Name: m.Path, Version: version, Note: note,
				Licence: moduleLicences[m.Path],
				URL:     moduleURL(m.Path),
				Kind:    KindGo,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	out = append(out, webLibraries...)
	return append(out, geoDatabases(geoDir)...)
}

// moduleVersion is what to SHOW for a module, which is not always what the
// build info holds.
//
// ── A REPLACED MODULE REPORTS "(devel)", WHICH IS NOT A VERSION ────────────
//
// `go.mod` replaces `go-routeros/v3` with the patched copy in `third_party`,
// and a filesystem replacement has no version of its own, so the build info
// carries the literal string `(devel)`. Shown on the page that is not a
// version, it is a build mode, and it hides the thing the reader wants: WHICH
// upstream release this is a patch of. The required version is `m.Version` and
// is right there; the replacement becomes a note instead.
//
// ── A PSEUDO-VERSION IS A DATE AND A COMMIT WEARING A SEMVER COSTUME ───────
//
// `golang.zx2c4.com/wireguard` has no tagged releases, so the module system
// invents `v0.0.0-20260522210424-ecfc5a8d5446`. The `v0.0.0` is a placeholder
// meaning "untagged", the middle is a UTC timestamp and the tail is a commit.
// Printed whole it reads as a broken version string; printed as its date and
// short commit it reads as what it is.
func moduleVersion(m *debug.Module) (version, note string) {
	version = m.Version
	if m.Replace != nil {
		// The replacement's own version, when it has one (a module replaced by
		// another MODULE rather than a directory). A directory replacement has
		// none, and `m.Version` already holds what go.mod requires.
		if m.Replace.Version != "" && m.Replace.Version != devel {
			version = m.Replace.Version
		}
		note = "patched"
	}
	if version == devel {
		// Nothing better to show. Empty beats a build mode masquerading as a
		// release, and the page omits what is empty.
		version = ""
	}
	if p := prettyPseudoVersion(version); p != "" {
		version = p
	}
	return version, note
}

// devel is what the toolchain reports for a module with no released version.
const devel = "(devel)"

// rePseudo matches a module pseudo-version: a placeholder semver, a 14 digit
// UTC timestamp and a 12 character commit prefix.
var rePseudo = regexp.MustCompile(`^v.*-(\d{14})-([0-9a-f]{12})$`)

func prettyPseudoVersion(v string) string {
	m := rePseudo.FindStringSubmatch(v)
	if m == nil {
		return ""
	}
	t, err := time.Parse("20060102150405", m[1])
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02") + " " + m[2][:7]
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
