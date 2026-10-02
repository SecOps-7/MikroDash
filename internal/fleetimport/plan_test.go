package fleetimport

import (
	"encoding/json"
	"strings"
	"testing"
)

var fleet = Fleet{
	Endpoints: []Endpoint{{Host: "192.0.2.1", Port: 8729}, {Host: "router.example", Port: 0}},
	Profiles:  []Named{{ID: "p1", Name: "MikroDash login"}},
	Sites:     []Named{{ID: "s1", Name: "Depot"}},
}

func one(t *testing.T, r Row) Verdict {
	t.Helper()
	res := Plan([]Row{r}, fleet)
	if len(res.Rows) != 1 {
		t.Fatalf("got %d verdicts", len(res.Rows))
	}
	return res.Rows[0]
}

func TestARowNeedsAHostAndALogin(t *testing.T) {
	if v := one(t, Row{Line: 2, Username: "admin", Password: "x"}); v.Status != "error" || v.Reason != "host is required" {
		t.Errorf("no host: %+v", v)
	}
	if v := one(t, Row{Line: 2, Host: "192.0.2.9"}); v.Status != "error" || !strings.Contains(v.Reason, "No login") {
		t.Errorf("no login: %+v", v)
	}
	if v := one(t, Row{Line: 2, Host: "192.0.2.9", Username: "admin", Password: "x"}); v.Status != "ready" || v.Mode != ModePlain {
		t.Errorf("plain: %+v", v)
	}
}

// The checks are AddRouter's own, so the messages are too.
func TestTheRouterChecksAreAddRoutersOwn(t *testing.T) {
	cases := map[string]Row{
		"Invalid host":          {Host: "bad host!", Username: "a"},
		"Invalid port":          {Host: "192.0.2.9", Port: "70000", Username: "a"},
		`Invalid port "x"`:      {Host: "192.0.2.9", Port: "x", Username: "a"},
		"Invalid defaultIf":     {Host: "192.0.2.9", Uplink: "eth 1", Username: "a"},
		"Invalid pingTarget":    {Host: "192.0.2.9", PingTarget: "nope", Username: "a"},
		"tls must be yes or no": {Host: "192.0.2.9", TLS: "maybe", Username: "a"},
	}
	for want, r := range cases {
		if v := one(t, r); v.Status != "error" || !strings.HasPrefix(v.Reason, want) {
			t.Errorf("%s: got %+v", want, v)
		}
	}
}

func TestDuplicatesAreTheFleetAndEarlierRows(t *testing.T) {
	res := Plan([]Row{
		{Line: 2, Host: "192.0.2.1", Username: "a"},                    // in the fleet on the default port
		{Line: 3, Host: "ROUTER.example", Port: "8729", Username: "a"}, // stored port 0 is 8729
		{Line: 4, Host: "192.0.2.1", Port: "8728", Username: "a"},      // another port is another device
		{Line: 5, Host: "192.0.2.1", Port: "8728", Username: "a"},      // ...once
		{Line: 6, Host: "192.0.2.50", Port: "x", Username: "a"},        // an error does not claim the endpoint
		{Line: 7, Host: "192.0.2.50", Username: "a"},
	}, fleet)
	want := []string{"duplicate", "duplicate", "ready", "duplicate", "error", "ready"}
	for i, v := range res.Rows {
		if v.Status != want[i] {
			t.Errorf("line %d: %s, want %s (%s)", v.Line, v.Status, want[i], v.Reason)
		}
	}
	if res.Ready != 2 || res.Duplicates != 3 || res.Errors != 1 {
		t.Errorf("counts %+v", res)
	}
}

func TestAProfileRowSignsInOrLinks(t *testing.T) {
	v := one(t, Row{Host: "192.0.2.9", Profile: "mikrodash LOGIN"})
	if v.Status != "ready" || v.Mode != ModeVerify || v.ProfileID() != "p1" || v.Profile != "MikroDash login" {
		t.Errorf("verify: %+v", v)
	}
	if b := v.Body(nil); b["password"] != nil || b["username"] != nil {
		t.Errorf("a verify row must carry no login of its own: %v", b)
	}
	v = one(t, Row{Host: "192.0.2.9", Profile: "MikroDash login", Username: "admin", Password: "pw"})
	if v.Mode != ModeLink {
		t.Errorf("link: %+v", v)
	}
	if v := one(t, Row{Host: "192.0.2.9", Profile: "MikroDash login", Username: "admin"}); v.Status != "error" {
		t.Errorf("a username with no password is a mistake, not a verify: %+v", v)
	}
	if v := one(t, Row{Host: "192.0.2.9", Profile: "Nope"}); v.Status != "error" {
		t.Errorf("unknown profile: %+v", v)
	}
	res := Plan([]Row{{Host: "192.0.2.9", Profile: "MikroDash login", Username: "a", Password: "b"},
		{Host: "192.0.2.10", Profile: "MikroDash login"}}, fleet)
	if res.Accounts != 1 {
		t.Errorf("only the link row creates an account: %d", res.Accounts)
	}
}

func TestSitesMatchIgnoringCaseAndNewOnesAreCountedOnce(t *testing.T) {
	res := Plan([]Row{
		{Line: 2, Host: "192.0.2.9", Username: "a", Sites: " depot | North |north"},
		{Line: 3, Host: "192.0.2.10", Username: "a", Sites: "NORTH|South"},
	}, fleet)
	if got := res.Rows[0].Sites; len(got) != 2 || got[0] != "Depot" || got[1] != "North" {
		t.Errorf("sites %v", got)
	}
	if got := res.NewSites; len(got) != 2 || got[0] != "North" || got[1] != "South" {
		t.Errorf("new sites %v", got)
	}
	b := res.Rows[0].Body(map[string]string{"depot": "s1", "north": "s9"})
	ids, _ := b["siteIds"].([]any)
	if len(ids) != 2 || ids[0] != "s1" || ids[1] != "s9" {
		t.Errorf("site ids %v", ids)
	}
	if v := one(t, Row{Host: "192.0.2.9", Username: "a", Sites: strings.Repeat("x", 65)}); v.Status != "error" {
		t.Errorf("a long site name: %+v", v)
	}
}

// The verdicts go to the browser; the passwords must not.
func TestNoVerdictCarriesAPassword(t *testing.T) {
	res := Plan([]Row{{Host: "192.0.2.9", Username: "admin", Password: "s3cret-in-a-sheet"},
		{Host: "192.0.2.10", Profile: "MikroDash login", Username: "a", Password: "s3cret-in-a-sheet"}}, fleet)
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), "s3cret") {
		t.Fatalf("a verdict echoed the password: %s", b)
	}
	if res.Rows[0].Body(nil)["password"] != "s3cret-in-a-sheet" {
		t.Error("the import still needs it")
	}
}
