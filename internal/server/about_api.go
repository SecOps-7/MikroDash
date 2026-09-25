package server

// What the About page shows: what this build is, what it is running on, what it
// ships, and what changed in it.
//
// ── ONE REQUEST, BECAUSE IT IS ONE PAGE ────────────────────────────────────
//
// Version, runtime, database, dependencies and the release notes arrive
// together. They are five unrelated reads on the server and one screen to the
// operator, and five endpoints would mean five loading states on a page that is
// opened once and closed.
//
// ── NOTHING HERE IS A SECRET, AND THAT IS CHECKED NOT ASSUMED ──────────────
//
// `/healthz` deliberately withholds the version from an unauthenticated caller.
// This route is behind the same session gate as the rest of `/api`, so it may
// say more; what it must not do is grow a field carrying a credential, a router
// address or a path outside the container. Every field below is either a
// constant, a number the runtime reports about itself, or text from a file that
// ships in the image.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Build stamps, set with -ldflags at image build time.
//
// ── EMPTY IS THE NORMAL CASE FOR A LOCAL BUILD ─────────────────────────────
//
// `.git` is in `.dockerignore`, so `debug.ReadBuildInfo` carries no VCS stamp
// in the shipped image and these cannot be derived. The Dockerfile takes them
// as build arguments instead, which the release workflow passes and a plain
// `docker build` does not.
//
// So they are EMPTY by default and the page omits what it does not have, rather
// than rendering a blank where a commit should be. A line that reads as real
// and says nothing is worse than a line that is not there.
var (
	BuildCommit = ""
	BuildBranch = ""
	BuildDate   = ""
)

// changelogPath is where the release notes are read from.
//
// A FILE, NOT AN EMBED. `go:embed` cannot reach outside its own package
// directory and CHANGELOG.md lives at the repository root, so embedding would
// mean keeping a second copy inside internal/server. It ships beside the web
// assets instead and is read once, on the first request that needs it.
var changelogPath = "CHANGELOG.md"

func (s *Server) registerAbout(mux *http.ServeMux) {
	// RATE LIMITED like its neighbours, and for one reason particular to this
	// route: it can make an outbound request to GitHub. The cache means it
	// almost never does, and the limiter is what makes "almost never" true
	// under a page held on refresh.
	rw := newRateLimiter(30, time.Minute).limit
	mux.HandleFunc("GET /api/about", rw(s.aboutInfo))
}

type aboutRuntime struct {
	Go       string `json:"go"`
	Platform string `json:"platform"`
	MemoryMB int    `json:"memoryMb"`
	// Container is the image this runs as, when something says so. Docker does
	// not tell a process its own image name, so this is what the operator set
	// in compose; empty when nothing did.
	Container string `json:"container,omitempty"`
	UptimeSec int64  `json:"uptimeSec"`
}

type aboutUpdate struct {
	// Latest is the newest published release, or empty when the check has not
	// run or could not reach GitHub. The page shows NO badge for empty, rather
	// than claiming currency it did not verify.
	Latest  string `json:"latest,omitempty"`
	Current bool   `json:"current"`
	// CheckedAt is when the answer was obtained, so the page can say how old it
	// is instead of implying it is live.
	CheckedAt int64 `json:"checkedAt,omitempty"`
}

// Release is one version's notes.
type Release struct {
	Version string        `json:"version"`
	Title   string        `json:"title,omitempty"`
	Date    string        `json:"date,omitempty"`
	Entries []ReleaseNote `json:"entries"`
}

// ReleaseNote is one bullet, with the section it came under.
type ReleaseNote struct {
	// Kind is New, Fixed or Internal: the CHANGELOG's own headings, carried so
	// the page can badge a line rather than guess from its wording.
	Kind string `json:"kind"`
	Text string `json:"text"`
}

func (s *Server) aboutInfo(w http.ResponseWriter, r *http.Request) {
	// SIGNED IN ONLY. `/healthz` withholds the version from an anonymous caller
	// deliberately, and this says considerably more than the version.
	if sess, err := s.auth.Validate(r.Header.Get("Cookie")); err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	schema := 0
	if s.auditDB != nil {
		if v, err := s.auditDB.SchemaVersion(); err == nil {
			schema = v
		}
	}

	writeJSON(w, map[string]any{
		"ok":      true,
		"version": AppVersion,
		"commit":  BuildCommit,
		"branch":  BuildBranch,
		"built":   BuildDate,
		"runtime": aboutRuntime{
			Go:        runtime.Version(),
			Platform:  runtime.GOOS + "/" + runtime.GOARCH,
			MemoryMB:  int(mem.Sys / 1024 / 1024),
			Container: os.Getenv("MIKRODASH_IMAGE"),
			UptimeSec: int64(time.Since(s.startedAt).Seconds()),
		},
		"database": map[string]any{"engine": "SQLite", "schema": schema},
		"update":   s.updateStatus(),
		"releases": loadReleases(),
		"deps":     Dependencies(),
	})
}

// ── THE UPDATE CHECK ───────────────────────────────────────────────────────

var (
	updateCache struct {
		sync.Mutex
		at  time.Time
		res aboutUpdate
	}
	// updateEvery is how long an answer is kept. A release lands every few days
	// at most, so asking more often spends somebody's rate limit to learn
	// nothing.
	updateEvery = 24 * time.Hour
)

// updateStatus is the newest published release, cached.
//
// ── IT FAILS QUIET, AND THAT IS THE WHOLE CONTRACT ─────────────────────────
//
// No network, a rate limit, a moved repository: every one of those returns an
// empty Latest, and the page then shows the version with no badge. The
// alternative is a badge reading "up to date" because the check failed, which
// is the one answer worse than no answer.
func (s *Server) updateStatus() aboutUpdate {
	updateCache.Lock()
	defer updateCache.Unlock()
	if time.Since(updateCache.at) < updateEvery && updateCache.res.Latest != "" {
		return updateCache.res
	}

	req, err := http.NewRequest("GET",
		"https://api.github.com/repos/SecOps-7/MikroDash/releases/latest", nil)
	if err != nil {
		return aboutUpdate{}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// A SHORT TIMEOUT. This runs while a page is rendering; an unreachable
	// GitHub must cost a missing badge, not a hanging tab.
	resp, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
	if err != nil {
		return aboutUpdate{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return aboutUpdate{}
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return aboutUpdate{}
	}
	latest := strings.TrimPrefix(strings.TrimSpace(body.TagName), "v")
	if latest == "" {
		return aboutUpdate{}
	}
	updateCache.at = time.Now()
	updateCache.res = aboutUpdate{
		Latest:    latest,
		Current:   latest == AppVersion,
		CheckedAt: time.Now().UnixMilli(),
	}
	return updateCache.res
}

// ── THE RELEASE NOTES ──────────────────────────────────────────────────────

var (
	releasesOnce sync.Once
	releasesVal  []Release
)

var (
	// reReleaseHead matches `## [0.8.68] - Traffic history per interface`, the
	// shape every entry in CHANGELOG.md has.
	reReleaseHead = regexp.MustCompile(`^##\s+\[([^\]]+)\]\s*-?\s*(.*)$`)
	reSection     = regexp.MustCompile(`^###\s+(.+)$`)
	reBullet      = regexp.MustCompile(`^-\s+(.+)$`)
	reBold        = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reMdLink      = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
)

// loadReleases parses CHANGELOG.md once.
//
// ── ONLY THE TOP-LEVEL BULLETS, AND ALL OF EACH ONE ────────────────────────
//
// The changelog nests detail under many of its entries, two and three levels
// deep. The page shows a scannable list of what changed, so a NESTED BULLET is
// skipped rather than flattened into a sibling of the thing it qualifies, which
// would read as a separate change that did not happen.
//
// It also WRAPS its bullets across source lines at column 96, and a wrapped
// continuation is indented exactly like a nested bullet. Telling them apart is
// what `-` after the indent is for: with the two conflated, every entry on the
// page ended mid-sentence ("Click an interface and see its traffic, in the"),
// which reads as a truncation bug in the browser rather than a parser dropping
// four fifths of the text. Found by looking at the rendered page; the payload
// was valid JSON and the endpoint dump was ten lines of plausible prose.
func loadReleases() []Release {
	releasesOnce.Do(func() { releasesVal = parseChangelog(readChangelog()) })
	return releasesVal
}

// readChangelog finds the file in the working directory, then one level up from
// a package running under `go test`, and returns "" when it is in neither.
//
// TWO PLACES BECAUSE THERE ARE TWO WAYS TO RUN: the image copies it to /app,
// which is the working directory, and a developer runs from the repository
// root. An empty string gives an empty list and a page with no release notes,
// which is the honest rendering of a file that is not there.
func readChangelog() string {
	for _, p := range []string{changelogPath, filepath.Join("..", "..", changelogPath)} {
		if b, err := os.ReadFile(p); err == nil {
			return string(b)
		}
	}
	return ""
}

func parseChangelog(src string) []Release {
	out := []Release{}
	var cur *Release
	section, open := "", ""

	// flush finishes the bullet being accumulated. Markdown is stripped HERE and
	// not per line, because the file wraps mid-emphasis: `**Live** (last 60
	// seconds)` begins on one line and `**1 hour** to **30 days**` spans two, and
	// a pattern applied to half a pair matches nothing and leaves the asterisks.
	flush := func() {
		if open == "" || cur == nil {
			return
		}
		cur.Entries = append(cur.Entries, ReleaseNote{Kind: section, Text: plainText(open)})
		open = ""
	}

	for _, line := range strings.Split(src, "\n") {
		if m := reReleaseHead.FindStringSubmatch(line); m != nil {
			flush()
			if cur != nil {
				out = append(out, *cur)
			}
			title, date := strings.TrimSpace(m[2]), ""
			// A bare ISO date is the other shape the file uses:
			// `## [0.8.1] - 2026-09-01`. Anything else is a title.
			if len(title) == 10 && strings.Count(title, "-") == 2 {
				title, date = "", title
			}
			cur = &Release{Version: m[1], Title: title, Date: date, Entries: []ReleaseNote{}}
			section = ""
			continue
		}
		if cur == nil {
			continue
		}
		if m := reSection.FindStringSubmatch(line); m != nil {
			flush()
			section = strings.TrimSpace(m[1])
			continue
		}
		if indented := strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t"); indented {
			trimmed := strings.TrimSpace(line)
			// A NESTED BULLET ends the entry above it and is itself dropped: it
			// is detail about that entry, not a change of its own.
			if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
				flush()
				continue
			}
			// A CONTINUATION is the rest of the sentence, joined with the single
			// space the line break stood for.
			if open != "" && trimmed != "" {
				open += " " + trimmed
			}
			continue
		}
		// A blank line, or any unindented prose, closes the entry.
		if m := reBullet.FindStringSubmatch(line); m != nil {
			flush()
			open = m[1]
			continue
		}
		flush()
	}
	flush()
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// plainText strips the markdown a bullet carries, so the page renders text
// rather than asterisks. Bold becomes plain and a link becomes its label: the
// page is a summary, and a URL mid-sentence is noise there.
func plainText(s string) string {
	s = reBold.ReplaceAllString(s, "$1")
	s = reMdLink.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, "`", "")
	return strings.TrimSpace(s)
}
