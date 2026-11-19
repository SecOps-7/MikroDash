package fleetimport

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// goldenRows and goldenFleet are the rows testdata/fleetimport/
// router-rows-before-swos.json was recorded from, with the planner as it was
// BEFORE SwOS switches (#142): every way a router row can be ready, a
// duplicate or wrong. Copied verbatim from the recording program.
func goldenRows() []Row {
	long := strings.Repeat("x", 80)
	return []Row{
		{Line: 2, Host: "198.51.100.1", Name: "Edge", Username: "admin", Password: "p"},
		{Line: 3, Host: "198.51.100.2", Port: "8729", TLS: "yes", TLSInsecure: "yes", Username: "u", Password: "p", Sites: "Office|branch|office"},
		{Line: 4, Host: "198.51.100.3", Port: "8728", TLS: "no", Username: "u", Password: "p", Uplink: "ether2", PingTarget: "9.9.9.9"},
		{Line: 5, Host: "198.51.100.4", Profile: "Fleet"},
		{Line: 6, Host: "198.51.100.5", Profile: "fleet", Username: "admin", Password: "old"},
		{Line: 7, Host: "198.51.100.6", Profile: "Fleet", Username: "admin"},
		{Line: 8, Host: "198.51.100.7"},
		{Line: 9, Host: "198.51.100.8", Port: "abc", Username: "u", Password: "p"},
		{Line: 10, Host: "198.51.100.9", TLS: "maybe", Username: "u", Password: "p"},
		{Line: 11, Host: "198.51.100.1", Username: "u", Password: "p"},
		{Line: 12, Host: "198.51.100.10", Username: "u", Password: "p", Sites: long},
		{Line: 13, Host: "bad host!", Username: "u", Password: "p"},
		{Line: 14, Host: "", Username: "u", Password: "p"},
		{Line: 15, Host: "198.51.100.11", Profile: "Nope"},
		{Line: 16, Host: "198.51.100.12", Port: "70000", Username: "u", Password: "p"},
	}
}

func goldenFleet() Fleet {
	return Fleet{
		Endpoints: []Endpoint{{Host: "198.51.100.99", Port: 8729}},
		Profiles:  []Named{{ID: "p1", Name: "Fleet"}},
		Sites:     []Named{{ID: "s1", Name: "Office"}},
	}
}

var goldenSites = map[string]string{"office": "s1", "branch": "new-branch"}

// TestARouterCSVPlansExactlyAsBeforeSwitches: a file with no type column is
// every CSV written before #142, and must plan as it did, verdict for verdict
// and body for body. The only new field is the kind, and it says routeros.
func TestARouterCSVPlansExactlyAsBeforeSwitches(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/fleetimport/router-rows-before-swos.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Rows []struct {
			Verdict map[string]any `json:"verdict"`
			Body    map[string]any `json:"body"`
		} `json:"rows"`
		Totals map[string]any `json:"totals"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	res := Plan(goldenRows(), goldenFleet())
	if len(res.Rows) != len(golden.Rows) {
		t.Fatalf("%d verdicts, golden has %d", len(res.Rows), len(golden.Rows))
	}
	roundTrip := func(v any) map[string]any {
		b, _ := json.Marshal(v)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	for i, v := range res.Rows {
		got := roundTrip(v)
		if got["kind"] != "routeros" {
			t.Errorf("line %d: kind %v, want routeros", v.Line, got["kind"])
		}
		delete(got, "kind")
		if !reflect.DeepEqual(got, golden.Rows[i].Verdict) {
			t.Errorf("line %d verdict changed:\n got %v\nwant %v", v.Line, got, golden.Rows[i].Verdict)
		}
		var body map[string]any
		if v.Status == "ready" {
			body = roundTrip(v.Body(goldenSites))
		}
		if !reflect.DeepEqual(body, golden.Rows[i].Body) {
			t.Errorf("line %d body changed:\n got %v\nwant %v", v.Line, body, golden.Rows[i].Body)
		}
	}
	res.Rows = nil
	if totals := roundTrip(res); !reflect.DeepEqual(totals, golden.Totals) {
		t.Errorf("totals changed: %v, golden %v", totals, golden.Totals)
	}
}

func TestEachKindRefusesTheOthersColumns(t *testing.T) {
	for _, c := range []struct {
		row  Row
		want string
	}{
		{Row{Line: 2, Type: "swos", Host: "198.51.100.20", TLS: "yes"}, "tls is a RouterOS setting"},
		{Row{Line: 3, Type: "swos", Host: "198.51.100.20", Profile: "Fleet"}, "credential_profile is a RouterOS setting"},
		{Row{Line: 4, Type: "swos", Host: "198.51.100.20", Uplink: "ether1"}, "uplink_interface is a RouterOS setting"},
		{Row{Line: 5, Type: "swos", Host: "198.51.100.20", PingTarget: "1.1.1.1"}, "ping_target is a RouterOS setting"},
		{Row{Line: 6, Host: "198.51.100.21", Scheme: "https", Username: "u", Password: "p"}, "scheme is a SwOS switch setting"},
		{Row{Line: 7, Type: "switch", Host: "198.51.100.22"}, "type must be routeros or swos"},
		{Row{Line: 8, Type: "swos", Host: "198.51.100.23", Scheme: "ftp"}, "scheme must be http or https"},
	} {
		v := Plan([]Row{c.row}, goldenFleet()).Rows[0]
		if v.Status != "error" || !strings.Contains(v.Reason, c.want) {
			t.Errorf("line %d: %s %q, want an error saying %q", c.row.Line, v.Status, v.Reason, c.want)
		}
	}
}

// TestAMixedCSVAddsEachRowAsItsKind, and a switch row needs no password:
// SwOS ships with admin and an empty one.
func TestAMixedCSVAddsEachRowAsItsKind(t *testing.T) {
	res := Plan([]Row{
		{Line: 2, Type: "routeros", Host: "198.51.100.30", Username: "u", Password: "p"},
		{Line: 3, Type: "SwOS", Host: "198.51.100.31", Name: "Core switch", Sites: "Office"},
		{Line: 4, Host: "198.51.100.32", Username: "u", Password: "p"},
		{Line: 5, Type: "swos", Host: "198.51.100.33", Scheme: "https", Port: "8443", Username: "ops", Password: "s"},
		{Line: 6, Type: "swos", Host: "198.51.100.31"},
	}, goldenFleet())
	kinds := []string{}
	for _, v := range res.Rows {
		kinds = append(kinds, v.Kind+":"+v.Status)
	}
	if want := "routeros:ready swos:ready routeros:ready swos:ready swos:duplicate"; strings.Join(kinds, " ") != want {
		t.Fatalf("got %v, want %s", kinds, want)
	}
	sw := res.Rows[1].Body(map[string]string{"office": "s1"})
	if sw["kind"] != "swos" || sw["username"] != "admin" || sw["password"] != "" || sw["port"] != 80 || sw["scheme"] != "http" {
		t.Errorf("switch body %v", sw)
	}
	if ids, _ := sw["siteIds"].([]any); len(ids) != 1 || ids[0] != "s1" {
		t.Errorf("switch sites %v", sw["siteIds"])
	}
	if b := res.Rows[3].Body(nil); b["port"] != 8443 || b["scheme"] != "https" || b["username"] != "ops" {
		t.Errorf("https switch body %v", b)
	}
	if b := res.Rows[0].Body(nil); b["kind"] != nil {
		t.Errorf("a router row's body carries kind %v", b["kind"])
	}
	if res.Ready != 4 || res.Duplicates != 1 {
		t.Errorf("ready %d duplicates %d", res.Ready, res.Duplicates)
	}
}
