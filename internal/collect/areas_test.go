package collect

import (
	"encoding/json"
	"errors"
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

// TestATransientFailureKeepsTheLastRows (#97, carried over from the IP Addresses
// collector the area replaced). Three answers from one menu mean three things:
//
//	rows              the router's rows
//	a transient error  nothing learned: the last rows stay on the page
//	no such command    the router has no such package: there is nothing to show
//
// Treating the second like the third blanked every IPv6 row for a poll on any
// hiccup, and treating the third like the second would show addresses from a
// package the router no longer has.
func TestATransientFailureKeepsTheLastRows(t *testing.T) {
	area, ok := areas.ByKey("ip-addresses")
	if !ok || len(area.Tables) != 2 {
		t.Fatal("the IP Addresses area, with its IPv4 and IPv6 tabs, is what this pins")
	}
	r := &menuScript{rows: map[string][]routeros.Reply{
		"/ip/address/print":   {{".id": "*1", "address": "198.51.100.1/24", "interface": "bridge"}},
		"/ipv6/address/print": {{".id": "*A", "address": "2001:db8::1/64", "interface": "bridge"}},
	}, fail: map[string]error{}}
	c := NewAreas(r, Emit{}).WithOccupancy(func(string) bool { return true })
	now := time.Unix(1_000_000, 0)
	c.now = func() time.Time { return now }
	v6 := func() (int, bool) {
		p := c.Last(area.Key)
		if p == nil || len(p.Tables) != 2 {
			t.Fatalf("payload %+v: want both tabs", p)
		}
		return len(p.Tables[1].Rows), p.Tables[1].Unsupported
	}

	c.Tick()
	if n, _ := v6(); n != 1 {
		t.Fatalf("first read: %d IPv6 rows, want 1", n)
	}

	r.fail["/ipv6/address/print"] = errors.New("connection reset by peer")
	now = now.Add(area.Poll)
	c.Tick()
	if n, unsupported := v6(); n != 1 || unsupported {
		t.Errorf("after a transient failure: %d IPv6 rows (unsupported %v), want the last 1 kept", n, unsupported)
	}
	// AND IT IS RE-READ AT THE NEXT TICK, not a whole interval later.
	r.fail["/ipv6/address/print"] = errors.New("no such command prefix")
	now = now.Add(areasTick)
	c.Tick()
	if n, unsupported := v6(); n != 0 || !unsupported {
		t.Errorf("after the router said the menu is not there: %d IPv6 rows (unsupported %v), want 0 and unsupported", n, unsupported)
	}
}

// TestAFirstReadThatFailsSendsNothing. With no rows to keep, a transient failure
// must not become an empty table: that reads as "you have none of these".
func TestAFirstReadThatFailsSendsNothing(t *testing.T) {
	area, ok := areas.ByKey("ip-addresses")
	if !ok {
		t.Fatal("no ip-addresses area")
	}
	r := &menuScript{rows: map[string][]routeros.Reply{}, fail: map[string]error{
		"/ip/address/print": errors.New("i/o timeout"),
	}}
	c := NewAreas(r, Emit{}).WithOccupancy(func(k string) bool { return k == AreaRoomFor(area.Key) })
	c.Tick()
	if p := c.Last(area.Key); p != nil {
		t.Errorf("a failed first read produced a payload: %+v", p)
	}
}

// menuScript answers each menu from a table, or with that menu's error.
type menuScript struct {
	rows map[string][]routeros.Reply
	fail map[string]error
}

func (m *menuScript) Connected() bool { return true }
func (m *menuScript) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	if err := m.fail[cmd.Path]; err != nil {
		return nil, err
	}
	return m.rows[cmd.Path], nil
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

// TestEveryAreaRendersItsCapture: the generic replay, over every declared area.
//
// ── WHY GENERIC AND NOT ONE TEST PER AREA ───────────────────────────────────
//
// An area is a declaration, so the thing that can be wrong is the same for all
// of them: a column that names nothing the router returns, a row that renders
// with no identity to round-trip, a secret carried into a payload that goes to
// the browser. A test per area would be that list retyped forty times, and the
// fortieth would be the one nobody wrote.
//
// The fixture is found by the RESOURCE key, which is what
// `internal/verify`'s ledger requires each area's resource to have. A missing
// one fails there; here it fails loudly rather than skipping, because a replay
// that quietly tests nothing is the failure this whole file exists to prevent.
func TestEveryAreaRendersItsCapture(t *testing.T) {
	if len(areas.All()) == 0 {
		t.Fatal("no areas declared — this replay would measure nothing")
	}
	checked := 0
	for _, area := range areas.All() {
		for _, decl := range area.Tables {
			res := resource.ByKey(decl.Resource)
			if res == nil {
				t.Errorf("area %q names resource %q, which does not exist", area.Key, decl.Resource)
				continue
			}
			rows, from := captureFor(t, decl.Resource)
			table := BuildAreaRows(res, decl.Title, decl.Columns, rows)
			checked++

			// EVERY CAPTURED ROW IS RENDERED, except the id-less one RouterOS
			// returns for an empty menu.
			want := 0
			for _, r := range rows {
				if r[".id"] != "" {
					want++
				}
			}
			if len(table.Rows) != want {
				t.Errorf("%s/%s: %d rows from %d captured (%s)",
					area.Key, decl.Resource, len(table.Rows), want, from)
			}

			for _, row := range table.Rows {
				if row.ID == "" {
					t.Errorf("%s/%s: a row has no id, so it cannot be addressed", area.Key, decl.Resource)
				}
				// THE IDENTITY IS WHAT STOPS A WRITE HITTING THE WRONG ROW:
				// RouterOS reuses `*N` after a delete, so a row with no identity
				// is one an edit cannot confirm it is still looking at.
				if row.Identity == "" {
					t.Errorf("%s/%s: row %s has no identity to round-trip",
						area.Key, decl.Resource, row.ID)
				}
				for _, f := range res.Fields {
					if f.Type == resource.TypeSecret {
						if _, leaked := row.Values[f.Name]; leaked {
							t.Errorf("%s/%s: the secret field %q reached a payload the browser reads",
								area.Key, decl.Resource, f.Name)
						}
					}
				}
			}

			// EVERY DECLARED COLUMN MUST RESOLVE FOR AT LEAST ONE ROW. A column
			// naming a property no router returns is a header over a column of
			// dashes, and it renders perfectly happily.
			for _, col := range table.Columns {
				seen := false
				for _, row := range table.Rows {
					if row.Values[col] != "" {
						seen = true
						break
					}
				}
				if !seen && len(table.Rows) > 0 && !optionalColumn(res, col) {
					t.Errorf("%s/%s: column %q is empty for every captured row (%s) — either the "+
						"capture does not exercise it or the field names a property the router "+
						"does not return", area.Key, decl.Resource, col, from)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no area tables were replayed")
	}
	t.Logf("%d area table(s) replayed against their captures", checked)
}

// optionalColumn: a column whose emptiness is ordinary rather than suspicious.
// A comment and a clearable field are legitimately blank on a row nobody
// commented on, and requiring a capture to exercise every one of them would mean
// writing fixtures to satisfy a test rather than to record a router.
func optionalColumn(res *resource.Resource, col string) bool {
	for _, f := range res.Fields {
		if f.Name == col {
			return f.Clearable || !f.Required
		}
	}
	return false
}

// captureFor finds a resource's fixture by key, and returns its rows and where
// they came from. Fails rather than skips: `internal/verify` requires the
// fixture to exist, so a missing one here is a broken lookup, not an absence.
func captureFor(t *testing.T, key string) ([]routeros.Reply, string) {
	t.Helper()
	dirs, err := os.ReadDir(filepath.Join("..", "..", "testdata", "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		path := filepath.Join("..", "..", "testdata", "fixtures", d.Name(), key+".json")
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var f struct {
			Exchanges []struct {
				Rows []routeros.Reply `json:"rows"`
			} `json:"exchanges"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var rows []routeros.Reply
		for _, ex := range f.Exchanges {
			rows = append(rows, ex.Rows...)
		}
		if len(rows) == 0 {
			t.Fatalf("%s holds no rows, so replaying it proves nothing", path)
		}
		return rows, d.Name()
	}
	t.Fatalf("no fixture for resource %q under testdata/fixtures", key)
	return nil, ""
}
