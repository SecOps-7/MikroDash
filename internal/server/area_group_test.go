package server

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/collect"
	"mikrodash/internal/routeros"
)

func addrRow(i int, addr, comment string) collect.AreaRow {
	return collect.AreaRow{ID: fmt.Sprintf("*%X", i), Identity: addr,
		Values: map[string]string{"list": "blocklist", "address": addr, "comment": comment}}
}

// A group larger than the cap is sent capped, with the true total, so "500 of
// 37,111" is what the page can say rather than implying 500 is all of them.
func TestAGroupIsCappedAndCountsEveryMatch(t *testing.T) {
	var rows []collect.AreaRow
	for i := 0; i < areaGroupCap+250; i++ {
		rows = append(rows, addrRow(i, fmt.Sprintf("198.51.100.%d", i%250), ""))
	}
	p := areaGroupPayload(areaGroupRequest{Area: "address-lists", Resource: "addressList", Group: "blocklist"},
		collect.AreaTable{Columns: []string{"list", "address"}, Rows: rows}, nil)
	if len(p.Rows) != areaGroupCap || p.Total != areaGroupCap+250 {
		t.Errorf("sent %d rows with total %d; want %d with total %d", len(p.Rows), p.Total, areaGroupCap, areaGroupCap+250)
	}
	if p.Rows[0].ID != "*0" {
		t.Errorf("the first row sent is %s; the router's order is kept", p.Rows[0].ID)
	}
}

// The search runs on the server over every value the row shows, case-blind, and
// the total is the matches, not the group.
func TestAGroupSearchNarrowsOnTheServer(t *testing.T) {
	rows := []collect.AreaRow{
		addrRow(1, "198.51.100.7", "scanner"),
		addrRow(2, "198.51.100.8", "Botnet C2"),
		addrRow(3, "203.0.113.9", ""),
	}
	req := areaGroupRequest{Area: "address-lists", Resource: "addressList", Group: "blocklist"}
	for _, tc := range []struct {
		search string
		want   []string
	}{
		{"198.51.100", []string{"*1", "*2"}},
		{"botnet", []string{"*2"}},
		{"  SCANNER ", []string{"*1"}},
		{"", []string{"*1", "*2", "*3"}},
		{"no-such", nil},
	} {
		req.Search = tc.search
		p := areaGroupPayload(req, collect.AreaTable{Rows: rows}, nil)
		var got []string
		for _, r := range p.Rows {
			got = append(got, r.ID)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") || p.Total != len(tc.want) {
			t.Errorf("search %q: rows %v total %d, want %v", tc.search, got, p.Total, tc.want)
		}
	}
}

// A failed read says so, with no rows, rather than an empty group.
func TestAGroupReadFailureIsSaid(t *testing.T) {
	p := areaGroupPayload(areaGroupRequest{}, collect.AreaTable{}, errors.New("timeout"))
	if p.Error == "" || len(p.Rows) != 0 || p.Rows == nil {
		t.Errorf("payload %+v; want an error and an empty, non-nil list", p)
	}
}

// ONLY A DECLARED GROUPED TABLE CAN BE ASKED FOR. The request names an area and
// a resource; anything but a grouped table of that area reads nothing, so a
// browser cannot use this to read an arbitrary menu.
func TestOnlyADeclaredGroupedTableCanBeOpened(t *testing.T) {
	if _, res := groupedTable("address-lists", "addressList"); res == nil {
		t.Fatal("Address Lists' grouped table is not found; the control fails")
	}
	for _, tc := range [][2]string{
		{"ip-pools", "ipPool"},      // an area table that is not grouped
		{"address-lists", "ipPool"}, // a resource of another area
		{"ip-pools", "addressList"}, // the grouped resource under the wrong area
		{"address-lists", "user"},   // not in any area
		{"no-such-area", "addressList"},
	} {
		if _, res := groupedTable(tc[0], tc[1]); res != nil {
			t.Errorf("%s/%s resolved to a readable table", tc[0], tc[1])
		}
	}
}

// A SEARCH FILTERS THE LAST READ; A REFRESH, ANOTHER GROUP OR A MINUTE READS
// AGAIN. Each read of the operator's 36,899-entry list is 6 s of router time.
func TestAGroupSearchReusesTheLastReadAndARefreshDoesNot(t *testing.T) {
	clock := time.Unix(1000, 0)
	m := &groupMemo{now: func() time.Time { return clock }}
	reads := 0
	read := func() ([]routeros.Reply, error) { reads++; return []routeros.Reply{{".id": "*1"}}, nil }

	m.rowsFor("r1|blocklist", true, read)
	m.rowsFor("r1|blocklist", false, read) // a search
	m.rowsFor("r1|blocklist", false, read) // another search
	if reads != 1 {
		t.Fatalf("%d router reads for one open and two searches; want 1", reads)
	}
	m.rowsFor("r1|blocklist", true, read) // after a write
	if reads != 2 {
		t.Errorf("a refresh was served from the kept read")
	}
	m.rowsFor("r1|admins", false, read) // another group
	if reads != 3 {
		t.Errorf("another group was served the kept rows")
	}
	clock = clock.Add(areaGroupReuse)
	m.rowsFor("r1|admins", false, read) // a minute later
	if reads != 4 {
		t.Errorf("a read older than %v was reused", areaGroupReuse)
	}
	// A FAILED READ IS NOT KEPT: the next search tries the router again.
	fail := func() ([]routeros.Reply, error) { reads++; return nil, errors.New("timeout") }
	if _, err := m.rowsFor("r2|x", true, fail); err == nil {
		t.Fatal("the failure was swallowed")
	}
	m.rowsFor("r2|x", false, read)
	if reads != 6 {
		t.Errorf("a failed read was kept and served to the next search")
	}
}
