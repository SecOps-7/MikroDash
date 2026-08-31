package session

import (
	"os"
	"regexp"
	"testing"

	"mikrodash/internal/store"
)

// THE OPERATOR'S "Top Connections N" MUST REACH THE COLLECTOR.
//
// Reported 2026-08-29: the Connections card ignored the setting. It was
// hardcoded to 10 in `NewConnections` with no writer anywhere — and 10 was not
// even the live default, which is 5. Its sibling `topTalkersN` was passed as a
// literal 0 with a comment saying the port had no settings write yet; that had
// been untrue since 2026-08-28.
//
// ── THE VALUE TEST AND THE CALL-SITE TEST ARE SEPARATE CLAIMS ─────────────
//
// `topSetting` returning 12 proves nothing about whether anyone calls it — the
// defect was never a wrong number, it was a number nobody asked for. So the
// second test reads the construction site out of the source.
func TestTopSettingPrefersTheFileThenTheGeneratedDefault(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  store.Settings
		key  string
		want int
	}{
		{"from the file", store.Settings{"topN": float64(12)}, "topN", 12},
		{"absent takes the generated default", store.Settings{}, "topN", 5},
		{"nil settings take the generated default", nil, "topN", 5},
		// A zero is not a choice anybody can act on — a card showing no rows is
		// indistinguishable from a broken one — so it falls through, matching
		// `NewTalkers`, which has always treated `topN <= 0` as "use the default".
		{"zero falls through", store.Settings{"topN": float64(0)}, "topN", 5},
		{"a string is not a count", store.Settings{"topN": "12"}, "topN", 5},
		{"talkers has its own key", store.Settings{"topTalkersN": float64(7)}, "topTalkersN", 7},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := topSetting(c.cfg, c.key); got != c.want {
				t.Errorf("topSetting(%v, %q) = %d, want %d", c.cfg, c.key, got, c.want)
			}
		})
	}

	// AND THE DEFAULT IS THE LIVE ONE, not a number typed into this test.
	// `store.Defaults()` is generated from `src/settings.js`; if that changes,
	// this asserts the port followed rather than that somebody updated a literal.
	if d, ok := store.Defaults()["topN"].(float64); !ok || int(d) != 5 {
		t.Errorf("the generated default for topN is %v; this test's expectations "+
			"above are written against 5 and must be re-derived", store.Defaults()["topN"])
	}
}

// TestBothCountsAreActuallyWiredIn reads the construction site.
//
// "Test the call site, not the callee, when the defect is 'somebody forgot to
// call it'" — a rule this project has now hit often enough to write down. Every
// unit test of `topSetting` passes with both collectors still hardcoded.
func TestBothCountsAreActuallyWiredIn(t *testing.T) {
	src, err := os.ReadFile("session.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ ctor, key string }{
		{"NewConnections", "topN"},
		{"NewTalkers", "topTalkersN"},
	} {
		re := regexp.MustCompile(`(?s)` + c.ctor + `\(.{0,600}?topSetting\(cfgSettings, "` + c.key + `"\)`)
		if !re.Match(src) {
			t.Errorf("%s is not given topSetting(cfgSettings, %q). The setting is read "+
				"and thrown away, which is exactly the state the operator reported: "+
				"changing it in Limits does nothing.", c.ctor, c.key)
		}
	}
}
