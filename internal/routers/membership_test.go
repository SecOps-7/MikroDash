package routers

// `SiteMembership` against the LIVE route's loop, lifted and run by
// The site-membership corpus.

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type membershipCorpus struct {
	Cases map[string]struct {
		All []struct {
			ID      string   `json:"id"`
			SiteIDs []string `json:"siteIds"`
			SiteID  string   `json:"siteId"`
		} `json:"all"`
		SiteID  string   `json:"siteId"`
		Wanted  []string `json:"wanted"`
		Changes []struct {
			RouterID string   `json:"routerId"`
			Before   []string `json:"before"`
			After    []string `json:"after"`
		} `json:"changes"`
	} `json:"cases"`
}

func loadMembershipCorpus(t *testing.T) membershipCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/site-membership-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c membershipCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("corpus is empty -- this test would pass against nothing")
	}
	return c
}

func TestSiteMembershipMatchesLive(t *testing.T) {
	c := loadMembershipCorpus(t)

	// Believability: at least one case must REMOVE something and one must ADD,
	// or an implementation that only ever did one of them would pass.
	var sawAdd, sawRemove bool
	for _, tc := range c.Cases {
		for _, ch := range tc.Changes {
			if len(ch.After) > len(ch.Before) {
				sawAdd = true
			} else {
				sawRemove = true
			}
		}
	}
	if !sawAdd || !sawRemove {
		t.Fatal("the corpus does not exercise both directions, so nothing here " +
			"distinguishes add from remove")
	}

	for name, tc := range c.Cases {
		t.Run(name, func(t *testing.T) {
			all := make([]SiteMemberRouter, 0, len(tc.All))
			for _, r := range tc.All {
				all = append(all, SiteMemberRouter{
					ID: r.ID, SiteIDs: r.SiteIDs, SiteID: r.SiteID,
				})
			}
			got := SiteMembership(all, tc.SiteID, tc.Wanted)

			if len(got) != len(tc.Changes) {
				t.Fatalf("%d changes, live made %d\n  got  %s\n  live %s",
					len(got), len(tc.Changes), showChanges(got), mustJSON(tc.Changes))
			}
			for i, want := range tc.Changes {
				// ORDER IS PART OF THE ANSWER: the live loop walks the fleet, and
				// the audit rows appear in that order. A port producing them in
				// `wanted` order writes a trail that reads differently for the
				// same action.
				if got[i].RouterID != want.RouterID {
					t.Errorf("change %d is %s, live %s (order: %s vs %s)",
						i, got[i].RouterID, want.RouterID,
						showChanges(got), mustJSON(tc.Changes))
					continue
				}
				if !sameIDs(got[i].Before, want.Before) {
					t.Errorf("%s before = %v, live %v", want.RouterID, got[i].Before, want.Before)
				}
				if !sameIDs(got[i].After, want.After) {
					t.Errorf("%s after = %v, live %v", want.RouterID, got[i].After, want.After)
				}
			}
		})
	}
}

// TestADeviceAlreadyInTheRightStateIsNotTouched.
//
// Stated separately because it is what keeps the audit trail readable: the live
// loop `continue`s, and a port that emitted a no-op would write a `router.site`
// row for every untouched device on every save.
func TestADeviceAlreadyInTheRightStateIsNotTouched(t *testing.T) {
	all := []SiteMemberRouter{
		{ID: "r1", SiteIDs: []string{"s1"}},
		{ID: "r2", SiteIDs: []string{"s2"}},
	}
	// Believability: this same fleet DOES produce a change when the wanted set
	// differs, so an empty result below is about the state rather than about the
	// function returning nothing.
	if got := SiteMembership(all, "s1", []string{"r2"}); len(got) != 2 {
		t.Fatalf("a real change produced %d entries, want 2", len(got))
	}
	if got := SiteMembership(all, "s1", []string{"r1"}); len(got) != 0 {
		t.Errorf("a fleet already in the wanted state produced %d changes", len(got))
	}
}

// TestAddingKeepsEveryOtherSite.
//
// The defect this whole shape exists to prevent: before #117 the write was an
// overwrite, so joining a second site silently left the first.
func TestAddingKeepsEveryOtherSite(t *testing.T) {
	got := SiteMembership(
		[]SiteMemberRouter{{ID: "r1", SiteIDs: []string{"s2", "s3"}}}, "s1", []string{"r1"})
	if len(got) != 1 {
		t.Fatalf("%d changes", len(got))
	}
	if !sameIDs(got[0].After, []string{"s2", "s3", "s1"}) {
		t.Errorf("after = %v; joining a site must keep the others and append this one",
			got[0].After)
	}
	if !got[0].Added {
		t.Error("the change is not marked as an addition")
	}
}

// TestTheInputIsNotMutated.
//
// The caller holds the fleet it passed and will write from it. A `Before` that
// aliases the record's own slice would let an `append` in one iteration show up
// in another's `Before`.
func TestTheInputIsNotMutated(t *testing.T) {
	// SPARE CAPACITY ON PURPOSE. With cap == len the aliasing append reallocates
	// and the scribble lands nowhere, so a slice literal here would let the bug
	// through -- which is exactly what happened the first time this was written.
	// A fleet that arrives through append or a decode has spare capacity.
	sites := make([]string, 0, 4)
	sites = append(sites, "s2")
	all := []SiteMemberRouter{{ID: "r1", SiteIDs: sites}}

	got := SiteMembership(all, "s1", []string{"r1"})
	if len(got) != 1 {
		t.Fatalf("%d changes", len(got))
	}
	if len(sites) != 1 || sites[0] != "s2" {
		t.Errorf("the caller's slice became %v", sites)
	}
	if len(all[0].SiteIDs) != 1 {
		t.Errorf("the record's SiteIDs became %v", all[0].SiteIDs)
	}
	// And Before must not alias it either. The caller holds MemberChange values
	// while it writes them; a Before backed by the record's own array turns one
	// device's bookkeeping into another's data.
	got[0].Before = append(got[0].Before, "scribble")
	if n := len(sites[:cap(sites)]); n > 1 && sites[:cap(sites)][1] == "scribble" {
		t.Errorf("Before aliases the caller's array -- an append through it wrote "+
			"into the fleet: %v", sites[:cap(sites)][:2])
	}
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func showChanges(cs []MemberChange) string {
	out := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		out = append(out, map[string]any{
			"routerId": c.RouterID, "before": c.Before, "after": c.After,
		})
	}
	return mustJSON(out)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

var _ = reflect.DeepEqual
