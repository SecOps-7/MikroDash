package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/routeros"
)

// TestADomainListReadsEveryShapeItIsPublishedIn: one a line, a hosts file,
// wildcards and adblock rules, with comments, in one paste.
func TestADomainListReadsEveryShapeItIsPublishedIn(t *testing.T) {
	got, skipped := ParseDomains(strings.Join([]string{
		"# a comment",
		"Example.COM",
		"0.0.0.0 ads.example.net  # trailing",
		"*.cdn.example.org",
		".leading.example.io",
		"||tracker.example.com^",
		"! adblock comment",
		"not a domain!",
		"localhost",
		"example.com",
		"-bad-.example.com",
	}, "\n"))
	want := []string{"ads.example.net", "cdn.example.org", "example.com", "leading.example.io",
		"tracker.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("domains = %v\nwant      %v", got, want)
	}
	if skipped != 3 {
		t.Errorf("skipped %d, want 3: the sentence, localhost and the label with a dash at its edge", skipped)
	}
	if got := WithoutCovered([]string{"a.example.com", "example.com", "b.c.example.com", "other.org"}); !reflect.DeepEqual(got, []string{"example.com", "other.org"}) {
		t.Errorf("WithoutCovered = %v", got)
	}
}

// TestAListSyncTouchesOnlyWhatItMade. The operator's own entry for a listed
// domain is left alone and the domain skipped; the list's entry for a domain
// that left the list goes; one whose address list moved is rewritten.
func TestAListSyncTouchesOnlyWhatItMade(t *testing.T) {
	l := db.DNSList{ID: 7, Name: "vpn", AddressList: "to_vpn", ForwardTo: "corp", MatchSubdomain: true}
	tag := listTag(l)
	rows := []routeros.Reply{
		{".id": "*1", "name": "kept.example", "type": "FWD", "forward-to": "corp", "address-list": "to_vpn",
			"match-subdomain": "true", "comment": tag},
		{".id": "*2", "name": "moved.example", "type": "FWD", "forward-to": "corp", "address-list": "old",
			"match-subdomain": "true", "comment": tag},
		{".id": "*3", "name": "gone.example", "type": "FWD", "comment": tag},
		{".id": "*4", "name": "mine.example", "type": "FWD", "address-list": "other", "comment": "by hand"},
	}
	in, skipped := PlanDNSList(l, []string{"kept.example", "moved.example", "mine.example", "new.example"}, rows)
	if skipped != 1 {
		t.Errorf("skipped %d, want the operator's own entry", skipped)
	}
	if len(in.Add) != 1 || in.Add[0]["name"] != "new.example" || in.Add[0]["comment"] != tag {
		t.Errorf("add = %v", in.Add)
	}
	if len(in.Set) != 1 || in.Set[0].ID != "*2" {
		t.Errorf("set = %+v", in.Set)
	}
	if !reflect.DeepEqual(in.Remove, []string{"*3"}) {
		t.Errorf("remove = %v; the hand-made *4 must survive", in.Remove)
	}
	// And the plan passes the same checks a job applies.
	if _, errs := planDNSBulk(in, rows); len(errs) > 0 {
		t.Errorf("the plan would be refused: %v", errs)
	}
}

// TestABadFetchRemovesNothing. An error page, an empty file or a captive
// portal parses to no domains or very few; synced, it would remove everything
// the list made. Nothing is refused that has not been accepted before.
func TestABadFetchRemovesNothing(t *testing.T) {
	for _, c := range []struct {
		count, last int
		refused     bool
	}{
		{0, 0, true}, {0, 500, true}, {200, 500, true},
		{250, 500, false}, {499, 500, false}, {1200, 500, false}, {3, 0, false},
	} {
		if got := ListShrank(c.count, c.last) != ""; got != c.refused {
			t.Errorf("%d domains after %d: refused=%v, want %v", c.count, c.last, got, c.refused)
		}
	}
}

// TestAListIsFetchedOnlyFromAPublicAddress. The fetch dials through
// internal/netguard, so a source on this machine is refused. Which ranges count
// as public is that package's test.
func TestAListIsFetchedOnlyFromAPublicAddress(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("example.com\n"))
	}))
	defer local.Close()
	_, err := listDomains(context.Background(), db.DNSList{SourceURL: local.URL})
	if err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Errorf("a source on loopback was fetched: %v", err)
	}
}

// TestARenamedListKeepsItsEntries. The entries belong to the list's id, not to
// its name: after a rename they are still the list's, they are not added a
// second time, their comment follows the new name, and one the list no longer
// holds still goes.
func TestARenamedListKeepsItsEntries(t *testing.T) {
	was := db.DNSList{ID: 7, Name: "vpn", AddressList: "to_vpn", ForwardTo: "corp", MatchSubdomain: true}
	now := was
	now.Name = "vpn-sites"
	rows := []routeros.Reply{
		{".id": "*1", "name": "kept.example", "type": "FWD", "forward-to": "corp", "address-list": "to_vpn",
			"match-subdomain": "true", "comment": listTag(was)},
		{".id": "*2", "name": "gone.example", "type": "FWD", "comment": listTag(was)},
		// Another list's entry, whose id merely starts with the same digit, is not ours.
		{".id": "*3", "name": "other.example", "type": "FWD", "comment": "MikroDash list #70 vpn"},
		{".id": "*4", "name": "mine.example", "type": "FWD", "comment": "by hand"},
	}
	in, _ := PlanDNSList(now, []string{"kept.example", "other.example"}, rows)
	if len(in.Add) != 0 {
		t.Errorf("add = %v; the renamed list's own entry was added a second time", in.Add)
	}
	if len(in.Set) != 1 || in.Set[0].ID != "*1" || in.Set[0].Values["comment"] != "MikroDash list #7 vpn-sites" {
		t.Errorf("set = %+v; want the kept entry's comment brought to the new name", in.Set)
	}
	if !reflect.DeepEqual(in.Remove, []string{"*2"}) {
		t.Errorf("remove = %v; want the entry the list dropped, and neither of the others", in.Remove)
	}
}

// TestAListRemovesASecondCopyOfAName. A name the list holds twice (an earlier
// failed run, or a hand copy of its comment) keeps one entry; the extra goes.
func TestAListRemovesASecondCopyOfAName(t *testing.T) {
	l := db.DNSList{ID: 3, Name: "vpn", AddressList: "to_vpn", MatchSubdomain: true}
	tag := listTag(l)
	rows := []routeros.Reply{
		{".id": "*1", "name": "dup.example", "type": "FWD", "address-list": "to_vpn", "match-subdomain": "true", "comment": tag},
		{".id": "*2", "name": "DUP.example", "type": "FWD", "address-list": "to_vpn", "match-subdomain": "true", "comment": tag},
	}
	in, _ := PlanDNSList(l, []string{"dup.example"}, rows)
	if len(in.Add) != 0 || len(in.Set) != 0 || !reflect.DeepEqual(in.Remove, []string{"*2"}) {
		t.Errorf("add %v set %+v remove %v; want only the second copy removed", in.Add, in.Set, in.Remove)
	}
}

// TestForgettingAListRemovesOnlyWhatItMade. A purge holds no domains, so the
// plan removes every entry of the list and nothing else.
func TestForgettingAListRemovesOnlyWhatItMade(t *testing.T) {
	l := db.DNSList{ID: 7, Name: "vpn", AddressList: "to_vpn", MatchSubdomain: true}
	rows := []routeros.Reply{
		{".id": "*1", "name": "a.example", "type": "FWD", "comment": listTag(l)},
		{".id": "*2", "name": "b.example", "type": "FWD", "comment": "MikroDash list #7 an old name"},
		{".id": "*3", "name": "c.example", "type": "FWD", "comment": "by hand"},
	}
	in, _ := PlanDNSList(l, nil, rows)
	if len(in.Add)+len(in.Set) != 0 || !reflect.DeepEqual(in.Remove, []string{"*1", "*2"}) {
		t.Errorf("add %v set %+v remove %v; want *1 and *2 removed and *3 left", in.Add, in.Set, in.Remove)
	}
}

const listsDDL = `CREATE TABLE dns_lists (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE,
  source_url TEXT NOT NULL DEFAULT '', domains TEXT NOT NULL DEFAULT '', address_list TEXT NOT NULL DEFAULT '',
  forward_to TEXT NOT NULL DEFAULT '', match_subdomain INTEGER NOT NULL DEFAULT 1,
  router_ids TEXT NOT NULL DEFAULT '[]', interval_hours INTEGER NOT NULL DEFAULT 0,
  last_sync_at INTEGER NOT NULL DEFAULT 0, last_result TEXT NOT NULL DEFAULT '',
  last_count INTEGER NOT NULL DEFAULT 0, dropped_routers TEXT NOT NULL DEFAULT '[]');`

const listRouters = `[{"id":"r-off","label":"Off","host":"h1","disabled":true},{"id":"r-on","label":"On","host":"h2"}]`

// TestAPurgeBlocksOnADisabledRouterAndNotOnAGoneOne. A sync leaves a disabled
// router out and visits it when it is enabled. A purge, whose point is that no
// entry is left behind, reports a disabled router as failed, and counts one
// that MikroDash no longer has as done: there is nothing left to clean.
func TestAPurgeBlocksOnADisabledRouterAndNotOnAGoneOne(t *testing.T) {
	s := gateServerWith(t, `{}`, "", listRouters)
	l := db.DNSList{ID: 4, Name: "vpn", AddressList: "to_vpn", RouterIDs: []string{"r-off", "gone"}}
	if got := s.planDNSListFleet(context.Background(), l, []string{"a.example"}, false); len(got) != 0 {
		t.Errorf("a sync planned %+v for a disabled and a gone router", got)
	}
	got := s.planDNSListFleet(context.Background(), l, nil, true)
	if len(got) != 1 || got[0].RouterID != "r-off" || got[0].Error == "" {
		t.Errorf("a purge planned %+v; want only the disabled router, with an error", got)
	}
}

// TestTheRoutersASaveDropsStayPendingUntilClean. The routers a save takes off a
// list are kept with it until their entries are confirmed removed, and a
// narrowed purge never forgets the list, however clean it is.
func TestTheRoutersASaveDropsStayPendingUntilClean(t *testing.T) {
	s := gateServerWith(t, `{}`, listsDDL, listRouters)
	id, err := s.auditDB.SaveDNSList(db.DNSList{Name: "vpn", AddressList: "to_vpn", Domains: "a.example",
		RouterIDs: []string{"r-on"}, Dropped: []string{"gone"}})
	if err != nil {
		t.Fatal(err)
	}
	l, _ := s.dnsListByID(id)
	if got, _ := s.settleDropped(context.Background(), l, "someone", nil); got != "" {
		t.Errorf("a gone router left %q behind", got)
	}
	l, ok := s.dnsListByID(id)
	if !ok || len(l.Dropped) != 0 {
		t.Fatalf("list %v, kept %v; want it kept with nothing pending", l.Dropped, ok)
	}

	l.Dropped = []string{"r-off"}
	if got, _ := s.settleDropped(context.Background(), l, "someone", nil); got == "" {
		t.Error("a disabled router was counted clean")
	}
	if l, _ = s.dnsListByID(id); !slices.Equal(l.Dropped, []string{"r-off"}) {
		t.Errorf("pending = %v after a failed purge, want r-off kept for the next sync", l.Dropped)
	}
}

// TestARouterReturnedWhileItsRemovalRunsIsSyncedAgain. The first save starts
// removing both routers; the second puts CHR back before that run finishes.
// CHR must not stay pending after the old purge, while AC2 still is, and the
// changed target set needs the normal sync that restores CHR's entries.
func TestARouterReturnedWhileItsRemovalRunsIsSyncedAgain(t *testing.T) {
	before := db.DNSList{RouterIDs: []string{}, Dropped: []string{"chr", "ac2"}}
	now := db.DNSList{RouterIDs: []string{"chr"}, Dropped: []string{"ac2"}}
	dropped, resync := settledDropped(before, now, []string{"ac2"})
	if !resync {
		t.Fatal("putting CHR back on the list did not schedule its normal sync")
	}
	if !slices.Equal(dropped, []string{"ac2"}) {
		t.Errorf("pending = %v, want only AC2", dropped)
	}

	// A router put back cannot stay pending even when it was unclean or when
	// the re-read still had it in Dropped.
	nowBoth := db.DNSList{RouterIDs: []string{"ac2"}, Dropped: []string{"ac2"}}
	droppedBoth, resyncBoth := settledDropped(before, nowBoth, []string{"ac2"})
	if !resyncBoth || slices.Contains(droppedBoth, "ac2") {
		t.Errorf("ac2 put back ended up in pending: %v (resync=%v)", droppedBoth, resyncBoth)
	}
}

// TestForgettingAListNeedsEveryRouterClean.
func TestForgettingAListNeedsEveryRouterClean(t *testing.T) {
	s := gateServerWith(t, `{}`, listsDDL, listRouters)
	id, err := s.auditDB.SaveDNSList(db.DNSList{Name: "vpn", AddressList: "to_vpn", Domains: "a.example",
		RouterIDs: []string{"gone"}})
	if err != nil {
		t.Fatal(err)
	}
	l, _ := s.dnsListByID(id)
	s.finishForget(l, []string{"r-off"}, []string{"Off: disabled"})
	if l, ok := s.dnsListByID(id); !ok || !strings.Contains(l.LastResult, "not forgotten") {
		t.Errorf("list kept %v with result %q after an unclean purge", ok, l.LastResult)
	}
	s.finishForget(l, nil, nil)
	if _, ok := s.dnsListByID(id); ok {
		t.Error("a clean purge left the list")
	}
}

// TestASaveKeepsWhatIsStillPending.
func TestASaveKeepsWhatIsStillPending(t *testing.T) {
	was := db.DNSList{RouterIDs: []string{"a", "b"}, Dropped: []string{"c"}}
	if got := pendingAfterSave(was, []string{"a"}); !slices.Equal(got, []string{"c", "b"}) {
		t.Errorf("pending = %v, want c kept and b added", got)
	}
	if got := pendingAfterSave(was, []string{"a", "b", "c"}); len(got) != 0 {
		t.Errorf("pending = %v; a router put back on the list is not pending", got)
	}
	if got := pendingAfterSave(db.DNSList{}, []string{"a"}); got == nil || len(got) != 0 {
		t.Errorf("pending = %#v for a new list, want an empty slice", got)
	}
}
