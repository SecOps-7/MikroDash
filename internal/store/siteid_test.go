package store

// `cleanSiteIDs` against the live `_cleanSiteIds`.
//
// `tools/siteid-cases.js` slices `_SITE_ID_RE`, `_cleanSiteId` and
// `_cleanSiteIds` out of `src/routers.js` and runs them.
//
// ── COMPARED AS A SEQUENCE ──────────────────────────────────────────────────
//
// The FIRST surviving id is the primary — it supplies the map's site geo tier
// and is what the `siteId` mirror stores — so dropping an invalid first entry
// PROMOTES the second. A set comparison would miss that entirely, and the corpus
// carries the case for it.

import (
	"encoding/json"
	"os"
	"testing"
)

type siteIDCase struct {
	Why               string          `json:"why"`
	Input             json.RawMessage `json:"input"`
	InputWasUndefined bool            `json:"inputWasUndefined"`
	IDs               []string        `json:"ids"`
}

func loadSiteIDCases(t *testing.T) []siteIDCase {
	t.Helper()
	b, err := os.ReadFile("../../testdata/siteid-cases.json")
	if err != nil {
		t.Fatalf("corpus missing: %v — run tools/siteid-cases.js", err)
	}
	var doc struct {
		Cases []siteIDCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("corpus is empty")
	}
	return doc.Cases
}

// anyOf decodes the recorded input into what cleanSiteIDs actually receives: a
// value straight out of a decoded JSON record.
func anyOf(t *testing.T, raw json.RawMessage, wasUndefined bool) any {
	t.Helper()
	if wasUndefined || len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCleanSiteIDsMatchesLive(t *testing.T) {
	dropped := 0
	for _, c := range loadSiteIDCases(t) {
		c := c
		t.Run(c.Why, func(t *testing.T) {
			got := cleanSiteIDs(anyOf(t, c.Input, c.InputWasUndefined))
			if len(got) != len(c.IDs) {
				t.Fatalf("got %v, live returned %v", got, c.IDs)
			}
			for i := range got {
				if got[i] != c.IDs[i] {
					t.Fatalf("got %v, live returned %v — the ORDER differs, and the first "+
						"surviving id is the primary", got, c.IDs)
				}
			}
		})
		if len(c.IDs) == 0 {
			dropped++
		}
	}
	// The corpus must still be mostly REFUSALS. One that accepted everything
	// would agree with the helper as it was before the regex was applied.
	if dropped < 8 {
		t.Errorf("only %d cases yield no membership; the corpus had 14 when written, and they "+
			"are what the regex is for", dropped)
	}
}

// TestAnInvalidFirstEntryPromotesTheSecond — the property a set comparison
// cannot see, and the reason `siteId` (the primary mirror) is affected by a
// dropped id rather than merely the list.
func TestAnInvalidFirstEntryPromotesTheSecond(t *testing.T) {
	got := cleanSiteIDs([]any{"bad id", "site-b"})
	if len(got) != 1 || got[0] != "site-b" {
		t.Fatalf("got %v, want [site-b] — dropping the invalid first entry must promote the "+
			"second to primary", got)
	}
}

// TestTheRuleLivesInOnePlace.
//
// `normalizeSites` used to filter on top of `cleanSiteIDs` because the helper
// did not apply the regex. It no longer does, and this is what would notice if
// the helper's filter were removed and the read path silently covered for it
// again: a record written with an invalid id must come back with NO membership,
// which can only happen if the WRITE path dropped it.
func TestTheRuleLivesInOnePlace(t *testing.T) {
	s, _ := routerStore(t, nil)
	pub := s.PublicRouter(map[string]any{
		"id": "r1", "siteIds": []any{"../../etc/passwd", "site-b"},
	})
	ids, _ := pub["siteIds"].([]string)
	if len(ids) != 1 || ids[0] != "site-b" {
		t.Errorf("siteIds = %v, want [site-b]", ids)
	}
	if pub["siteId"] != "site-b" {
		t.Errorf("siteId = %v, want site-b — the mirror follows the surviving primary", pub["siteId"])
	}
}
