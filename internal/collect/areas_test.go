package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/areas"
	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
)

// poolReader replays the captured /ip/pool reply, and counts what was asked.
type poolReader struct {
	rows  []routeros.Reply
	asked []string
	fail  error
}

func (p *poolReader) Connected() bool { return true }
func (p *poolReader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	p.asked = append(p.asked, cmd.Path)
	if p.fail != nil {
		return nil, p.fail
	}
	return p.rows, nil
}

func loadPoolFixture(t *testing.T) *poolReader {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "CHR Test", "ipPool.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Exchanges []struct {
			Cmd  string           `json:"cmd"`
			Rows []routeros.Reply `json:"rows"`
		} `json:"exchanges"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Exchanges) == 0 || len(f.Exchanges[0].Rows) == 0 {
		t.Fatal("the ipPool fixture has no rows — this test would measure nothing")
	}
	return &poolReader{rows: f.Exchanges[0].Rows}
}

// TestBuildAreaRowsReplaysTheCapture.
//
// The captured reply is what a RouterOS 7.24 router returns for /ip/pool,
// including the read-only total/used/available it volunteers. The derivation must
// carry every declared field through under the FORM NAME — which is what the
// edit dialog and change_row speak — and drop what the resource does not declare.
func TestBuildAreaRowsReplaysTheCapture(t *testing.T) {
	r := loadPoolFixture(t)
	table := BuildAreaRows(resource.IPPool, "", []string{"name", "ranges", "used", "total"}, r.rows)

	if table.Resource != "ipPool" || table.Title != "IP Pool" {
		t.Errorf("table is %q titled %q", table.Resource, table.Title)
	}
	if len(table.Rows) != len(r.rows) {
		t.Fatalf("%d rows built from %d captured", len(table.Rows), len(r.rows))
	}
	first := table.Rows[0]
	if first.ID != "*1" || first.Identity != "dhcp-pool" {
		t.Errorf("row addressed by %q and identified by %q", first.ID, first.Identity)
	}
	for k, want := range map[string]string{
		"name": "dhcp-pool", "ranges": "198.51.100.50-198.51.100.254",
		"used": "0", "total": "205", "comment": "fixture capture",
	} {
		if first.Values[k] != want {
			t.Errorf("value %s = %q, want %q", k, first.Values[k], want)
		}
	}
	// A property the resource does not declare is NOT carried: the payload is
	// the form's vocabulary, not the router's.
	if _, ok := first.Values["available"]; !ok {
		t.Error("`available` is a declared display field and was dropped")
	}
	if _, ok := first.Values[".id"]; ok {
		t.Error("the raw .id was carried into the values as well as the row id")
	}
	// The second row's next-pool becomes `nextPool`: a hyphenated RouterOS
	// property under its form name.
	if got := table.Rows[1].Values["nextPool"]; got != "dhcp-pool" {
		t.Errorf("nextPool = %q, want the captured dhcp-pool", got)
	}
	// Columns are what was asked for, in order; the empty case is every
	// non-secret field.
	if strings.Join(table.Columns, ",") != "name,ranges,used,total" {
		t.Errorf("columns = %v", table.Columns)
	}
	all := BuildAreaRows(resource.IPPool, "", nil, r.rows)
	if len(all.Columns) != len(resource.IPPool.Fields) {
		t.Errorf("an unspecified column list gave %d columns for %d fields",
			len(all.Columns), len(resource.IPPool.Fields))
	}
	// EMPTY INPUT IS AN EMPTY TABLE, not a nil one: the browser is typed `T[]`.
	empty := BuildAreaRows(resource.IPPool, "", nil, nil)
	if empty.Rows == nil || len(empty.Rows) != 0 {
		t.Errorf("empty input built %v", empty.Rows)
	}
}

// TestTheAreasCollectorReadsOnlyWhatIsBeingLookedAt.
//
// The whole point of one collector for many pages: opening one generated page
// must not poll the menus of every other. With no area's room occupied it reads
// NOTHING, which is also what makes the demand rule safe to apply to it.
func TestTheAreasCollectorReadsOnlyWhatIsBeingLookedAt(t *testing.T) {
	if len(areas.All()) == 0 {
		t.Skip("no areas declared")
	}
	first := areas.All()[0]

	r := loadPoolFixture(t)
	nobody := NewAreas(r, Emit{}).WithOccupancy(func(string) bool { return false })
	nobody.Tick()
	if len(r.asked) != 0 {
		t.Errorf("with no page open the collector still read %v", r.asked)
	}

	r = loadPoolFixture(t)
	watched := NewAreas(r, Emit{}).WithOccupancy(func(room string) bool {
		return room == AreaRoomFor(first.Key)
	})
	watched.Tick()
	if len(r.asked) == 0 {
		t.Fatal("with the page open the collector read nothing")
	}
	if p := watched.Last(first.Key); p == nil || len(p.Tables) == 0 {
		t.Fatalf("no payload for the area being looked at: %+v", p)
	}

	// AND IT DOES NOT RE-READ BEFORE THE AREA IS DUE. A tick is five seconds and
	// an area's interval is its own; without this the loop would poll every
	// area's menus every tick, which is the cost this design avoids.
	asked := len(r.asked)
	watched.Tick()
	if len(r.asked) != asked {
		t.Errorf("a second tick re-read %d menu(s) before the area was due", len(r.asked)-asked)
	}
}

// TestAnUnreadableMenuIsSaidRatherThanShownEmpty. A menu this build lacks and one
// this account may not read are different sentences, and an empty table is a
// third thing entirely: "you have none of these".
func TestAnUnreadableMenuIsSaidRatherThanShownEmpty(t *testing.T) {
	if len(areas.All()) == 0 {
		t.Skip("no areas declared")
	}
	first := areas.All()[0]
	r := loadPoolFixture(t)
	r.fail = errDenied{}
	c := NewAreas(r, Emit{}).WithOccupancy(func(string) bool { return true })
	c.Tick()
	p := c.Last(first.Key)
	if p == nil || len(p.Tables) == 0 {
		t.Fatal("a refused read produced no payload at all")
	}
	if !p.Tables[0].Unsupported {
		t.Error("a refused read renders as an ordinary empty table")
	}
	if !p.Denied {
		t.Error("a permission refusal is not reported as one, so the page cannot say which it was")
	}
}

type errDenied struct{}

func (errDenied) Error() string { return "routeros: not enough permissions (9)" }

// TestTheAreaFingerprintCoversEveryRenderedField. An unchanged payload is not
// sent; a payload that differs anywhere the page draws MUST be. Each mutation
// below is a field somebody would see change on screen.
func TestTheAreaFingerprintCoversEveryRenderedField(t *testing.T) {
	base := AreaPayload{
		TS: 1, PollMs: 60000, Area: "ip-pools", Title: "IP Pools",
		Tables: []AreaTable{{Resource: "ipPool", Title: "IP Pool",
			Columns: []string{"name", "ranges"},
			Rows: []AreaRow{{ID: "*1", Identity: "dhcp-pool",
				Values: map[string]string{"name": "dhcp-pool", "ranges": "198.51.100.50-198.51.100.254"}}}}},
	}
	fp := areaFingerprint(base)

	// THE CLOCK IS NOT IN IT: a payload identical but for its timestamp must not
	// be re-sent, or the heartbeat becomes every tick.
	later := base
	later.TS = 99
	if areaFingerprint(later) != fp {
		t.Error("the timestamp moves the fingerprint, so every tick would re-send")
	}

	for name, mutate := range map[string]func(*AreaPayload){
		"a value":        func(p *AreaPayload) { p.Tables[0].Rows[0].Values["ranges"] = "198.51.100.1-198.51.100.9" },
		"an identity":    func(p *AreaPayload) { p.Tables[0].Rows[0].Identity = "renamed" },
		"a row id":       func(p *AreaPayload) { p.Tables[0].Rows[0].ID = "*7" },
		"a row going":    func(p *AreaPayload) { p.Tables[0].Rows = nil },
		"the columns":    func(p *AreaPayload) { p.Tables[0].Columns = []string{"name"} },
		"the tab title":  func(p *AreaPayload) { p.Tables[0].Title = "Pools" },
		"unsupported":    func(p *AreaPayload) { p.Tables[0].Unsupported = true },
		"denied":         func(p *AreaPayload) { p.Denied = true },
		"the area title": func(p *AreaPayload) { p.Title = "Pools" },
	} {
		// A DEEP COPY, so one mutation does not leak into the next.
		var copyOf AreaPayload
		b, _ := json.Marshal(base)
		_ = json.Unmarshal(b, &copyOf)
		mutate(&copyOf)
		if areaFingerprint(copyOf) == fp {
			t.Errorf("%s does not move the fingerprint, so the page would not be told", name)
		}
	}
}

// TestAreaRoomsFollowTheDeclarations, both ways: a room per area and no others.
// A sender and a waiter that disagree about the name is a page that stays empty.
func TestAreaRoomsFollowTheDeclarations(t *testing.T) {
	rooms := RoomsOf("areas")
	if len(rooms) != len(areas.Keys()) {
		t.Fatalf("%d rooms for %d areas", len(rooms), len(areas.Keys()))
	}
	for _, key := range areas.Keys() {
		want := AreaRoomFor(key)
		found := false
		for _, r := range rooms {
			if r == want {
				found = true
			}
		}
		if !found {
			t.Errorf("area %q has no room %q", key, want)
		}
	}
	for _, r := range rooms {
		if !strings.HasPrefix(string(r), "page-") {
			t.Errorf("room %q is not a page room", r)
		}
	}
	// The poll interval reaches the payload, so the page's staleness rule has
	// the interval the collector is actually using.
	r := loadPoolFixture(t)
	c := NewAreas(r, Emit{}).WithOccupancy(func(string) bool { return true })
	c.Tick()
	for _, a := range areas.All() {
		p := c.Last(a.Key)
		if p == nil {
			t.Fatalf("no payload for %q", a.Key)
		}
		if want := int(a.Poll / time.Millisecond); p.PollMs != want {
			t.Errorf("area %q reports pollMs %d, declared %d", a.Key, p.PollMs, want)
		}
	}
}
