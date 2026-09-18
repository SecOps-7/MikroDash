package collect

// The AREAS collector: one collector for every generated page.
//
// ── ONE, NOT ONE PER AREA ───────────────────────────────────────────────────
//
// `internal/areas` declares a RouterOS menu as a page. Sixty of those, each with
// its own collector, is sixty passes through the 21-row checklist in
// Collector-Architecture.md — sixty registry rows, sixty session fields, sixty
// dormancy targets. This is that checklist paid ONCE: the collector is in the
// registry as `areas`, and an area added tomorrow is a declaration and a fixture.
//
// It is the "a menu chosen at runtime" case the architecture already names for
// `firewall`, `wifi` and `wireless` — and more so: which menus it reads is not
// merely a tab the operator picked, it is the set of areas whose page somebody is
// looking at.
//
// ── POLL ONLY, NEVER A STREAM ───────────────────────────────────────────────
//
// Every menu here is CONFIGURATION: an address pool changes when somebody edits
// it, not on a tick. A stream holds an API channel open for a table that is
// identical between reads, and the channel cap is the scarce thing on a MikroTik.
// The interval is fixed in each area's declaration rather than being a settings
// key, so there is no per-area poll slider to keep in step with four clamps.
//
// ── IT READS ONLY WHAT SOMEBODY IS LOOKING AT ───────────────────────────────
//
// The session suspends the whole collector when no area's room is occupied, the
// same demand rule every collector gets. Within it, each area is read only while
// ITS room is occupied — otherwise opening one generated page would poll the
// menus of every other, which is the cost this mechanism exists to avoid.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/areas"
	"mikrodash/internal/resource"
	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
)

// AreaRow is one row of a generated table.
//
// `Values` is keyed by the resource's FIELD names, not RouterOS property names,
// because that is what the resource engine's edit dialog and `change_row` speak.
type AreaRow struct {
	ID       string            `json:"id"`
	Identity string            `json:"identity"`
	Values   map[string]string `json:"values"`
}

// AreaTable is one tab: a resource's rows, with the columns to show.
type AreaTable struct {
	Resource string    `json:"resource"`
	Title    string    `json:"title"`
	Columns  []string  `json:"columns"`
	Rows     []AreaRow `json:"rows"`
	// Unsupported is a menu this router does not have — a package that is not
	// installed, or a build without it. The page says so rather than rendering
	// an empty table, which reads as "you have none of these".
	Unsupported bool `json:"unsupported"`
	// Singleton is a settings menu's one row, which the page draws as a card of
	// label and value rather than as a one-row table.
	Singleton bool `json:"singleton"`
}

// AreaPayload is one generated page's state.
type AreaPayload struct {
	TS     int64       `json:"ts"`
	PollMs int         `json:"pollMs"`
	Area   string      `json:"area"`
	Title  string      `json:"title"`
	Tables []AreaTable `json:"tables"`
	// Denied is a menu this router's MikroDash account may not read, which is a
	// different sentence from "not installed".
	Denied bool `json:"denied"`
}

// BuildAreaRows turns one menu's rows into a table.
//
// Pure: rows in, table out. `res` says which fields exist and how a row is
// identified; `columns` is the order to show, empty meaning every non-secret
// field in declaration order. A secret is never carried — `RowValues` drops it,
// and a column naming one would be a header over a blank cell for ever.
func BuildAreaRows(res *resource.Resource, title string, columns []string, rows []routeros.Reply) AreaTable {
	if res == nil {
		return AreaTable{Columns: []string{}, Rows: []AreaRow{}}
	}
	cols := append([]string(nil), columns...)
	if len(cols) == 0 {
		for _, f := range res.Fields {
			if f.Type != resource.TypeSecret {
				cols = append(cols, f.Name)
			}
		}
	}
	if title == "" {
		title = res.Label
	}
	out := AreaTable{Resource: res.Key, Title: title, Columns: cols, Rows: []AreaRow{}, Singleton: res.Singleton}
	for _, r := range rows {
		// A settings menu's one row has no `.id`: StampID gives it SingletonID,
		// as the write path does, so the row the page clicks is the row it edits.
		r = routeros.Reply(res.StampID(r))
		// The empty row RouterOS returns for an empty menu carries no id.
		if r[".id"] == "" {
			continue
		}
		values := map[string]string{}
		for k, v := range res.RowValues(map[string]string(r)) {
			switch t := v.(type) {
			case string:
				values[k] = t
			case bool:
				values[k] = "false"
				if t {
					values[k] = "true"
				}
			default:
				values[k] = fmt.Sprint(t)
			}
		}
		out.Rows = append(out.Rows, AreaRow{
			ID: r[".id"], Identity: res.IdentityOf(map[string]string(r)), Values: values,
		})
	}
	return out
}

// Areas is the collector.
type Areas struct {
	ros   Reader
	emit  Emit
	cache *roscache.Cache
	poll  *pollLoop
	sched scheduled

	// occupied answers "is anybody on this area's page". Injected, because
	// occupancy is the hub's question and this package does not know the hub.
	// Nil reads every area, which is what a caller that did not wire it should
	// get: correct, and more work than necessary.
	occupied func(room string) bool

	mu     sync.Mutex
	last   map[string]*AreaPayload
	lastFp map[string]string
	nextAt map[string]time.Time
	now    func() time.Time
}

// areasTick is how often the loop wakes. Each area is read on its OWN declared
// interval; this is the resolution that schedule is measured at, and the floor
// on how promptly a page picks up a write.
const areasTick = 5 * time.Second

func NewAreas(ros Reader, emit Emit) *Areas {
	a := &Areas{
		ros: ros, emit: emit,
		last: map[string]*AreaPayload{}, lastFp: map[string]string{},
		nextAt: map[string]time.Time{}, now: time.Now,
	}
	a.poll = newPollLoop(a.Tick, func() time.Duration { return areasTick })
	// NOT SUBSCRIBED TO A MENU. The scheduler subscribes a collector to one menu
	// and drives it from the router's own cadence; this collector's menus are
	// chosen per tick from the declarations and the occupied rooms, so the loop
	// IS the schedule. `firewall` is the precedent.
	a.sched = scheduled{loop: a.poll}
	return a
}

// WithOccupancy wires the room oracle. See `occupied`.
func (a *Areas) WithOccupancy(fn func(room string) bool) *Areas {
	a.occupied = fn
	return a
}

// RoomFor is the room an area's payload goes to, and the room whose occupancy
// decides whether it is read at all. One place, because a sender and a waiter
// that disagree about the name is a page that stays empty.
func AreaRoomFor(key string) string { return "page-" + key }

// Tick reads every area that is due and being looked at.
func (a *Areas) Tick() {
	if !a.ros.Connected() {
		return
	}
	now := a.now()
	for _, area := range areas.All() {
		if a.occupied != nil && !a.occupied(AreaRoomFor(area.Key)) {
			continue
		}
		a.mu.Lock()
		due := a.nextAt[area.Key]
		a.mu.Unlock()
		if !due.IsZero() && now.Before(due) {
			continue
		}
		a.readArea(area, now)
	}
}

// RefreshNow re-reads one area at once, after a write.
//
// ── PAST THE CACHE, WHICH IS THE WHOLE POINT OF CALLING IT ──────────────────
//
// The ordinary read is served from the per-router cache for the area's own
// interval, so a forced refresh straight after a write was answered with the
// rows from BEFORE it — measured on the CHR: the dialog closed, the router held
// the new comment, and the table went on showing the old one for a minute. Each
// of the area's menus is invalidated first, exactly as `tableCore.RefreshNow`
// invalidates its own.
func (a *Areas) RefreshNow(key string) {
	if !a.ros.Connected() {
		return
	}
	area, ok := areas.ByKey(key)
	if !ok {
		return
	}
	if a.cache != nil {
		for _, t := range area.Tables {
			if res := resource.ByKey(t.Resource); res != nil {
				a.cache.Invalidate(res.Menu + "/print")
			}
		}
	}
	a.readArea(area, a.now())
}

func (a *Areas) readArea(area areas.Area, now time.Time) {
	payload := AreaPayload{
		TS: now.UnixMilli(), PollMs: int(area.Poll / time.Millisecond),
		Area: area.Key, Title: area.Title, Tables: []AreaTable{},
	}
	next := area.Poll
	for _, t := range area.Tables {
		res := resource.ByKey(t.Resource)
		if res == nil {
			continue
		}
		// THROUGH THE CACHE. These menus are shared: /ip/pool is read by
		// dhcpNetworks too, and whichever asks first should pay for both.
		rows, err := readVia(a.cache, a.ros, areaReadCmd(res), area.Poll)
		table := BuildAreaRows(res, t.Title, t.Columns, rows)
		// FOUR ANSWERS, FOUR MEANINGS (#97, carried over from the IP Addresses
		// collector this replaced). Rows are the router's rows. A refusal and a
		// menu this build lacks are different sentences, and the page says which.
		// Anything else — a timeout, a reset — taught nothing: the last rows stay
		// on the page and the area is re-read at the next tick, because blanking
		// it would say "you have none" and "no such menu" would be a lie.
		switch {
		case err == nil:
		case isDenied(err):
			table.Unsupported = true
			payload.Denied = true
		case isAbsentMenu(err):
			table.Unsupported = true
		default:
			prev := a.lastTable(area.Key, res.Key)
			if prev == nil {
				// Nothing to keep: send nothing, so the page goes on saying it is
				// waiting rather than showing an empty table.
				a.mu.Lock()
				a.nextAt[area.Key] = now.Add(areasTick)
				a.mu.Unlock()
				return
			}
			table = *prev
			next = areasTick
		}
		payload.Tables = append(payload.Tables, table)
	}

	a.mu.Lock()
	a.nextAt[area.Key] = now.Add(next)
	fp := areaFingerprint(payload)
	changed := a.lastFp[area.Key] != fp
	a.lastFp[area.Key] = fp
	a.last[area.Key] = &payload
	a.mu.Unlock()
	if changed {
		EvAreaUpdate.Emit(a.emit, AreaRoomFor(area.Key), payload)
	}
}

// areaReadCmd is one resource's read: its menu, and ONLY the fields it declares.
//
// ── A PROPLIST, BECAUSE THE CACHE ENTRY IS SHARED ───────────────────────────
//
// The cache keeps one entry per menu, holding the UNION of every consumer's
// fields, and a read with no proplist means "all of them" — which drops the
// entry's proplist for good (`roscache` widen). Read that way, opening IP
// Addresses made every later `/ip/address` read by Interfaces, WAN and DHCP
// Networks fetch every property too, and IP Pools did the same to DHCP
// Networks' `/ip/pool`. No extra channel, but every one of those reads carried
// what nobody renders. Found on 2026-09-18, after the IP Addresses migration.
//
// The fields are the resource's: `.id` to address a row, and every field's
// RouterOS name, identity and Display fields included. A secret is never ASKED
// for — not merely dropped afterwards by BuildAreaRows — so none can reach this
// process, let alone the cache another collector reads.
func areaReadCmd(res *resource.Resource) routeros.Cmd {
	props := []string{".id"}
	seen := map[string]bool{".id": true}
	for _, f := range res.Fields {
		if f.Type == resource.TypeSecret || f.ROS == "" || seen[f.ROS] {
			continue
		}
		seen[f.ROS] = true
		props = append(props, f.ROS)
	}
	return routeros.Cmd{Path: res.Menu + "/print", Args: []string{"=.proplist=" + strings.Join(props, ",")}}
}

// lastTable is the table this area last sent for one resource, or nil.
func (a *Areas) lastTable(key, res string) *AreaTable {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p := a.last[key]; p != nil {
		for i := range p.Tables {
			if p.Tables[i].Resource == res {
				t := p.Tables[i]
				return &t
			}
		}
	}
	return nil
}

// isDenied separates "this account may not read that" from "this build has no
// such menu", which RouterOS answers differently and the page words differently.
func isDenied(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "not enough permissions") || strings.Contains(m, "permission denied")
}

// areaFingerprint covers every field the page renders, so an unchanged payload is
// not sent and a changed one always is.
func areaFingerprint(p AreaPayload) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%v|", p.Area, p.Title, p.Denied)
	for _, t := range p.Tables {
		fmt.Fprintf(&b, "%s:%s:%v:%s;", t.Resource, t.Title, t.Unsupported, strings.Join(t.Columns, ","))
		for _, r := range t.Rows {
			keys := make([]string, 0, len(r.Values))
			for k := range r.Values {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			fmt.Fprintf(&b, "%s=%s{", r.ID, r.Identity)
			for _, k := range keys {
				fmt.Fprintf(&b, "%s=%s,", k, r.Values[k])
			}
			b.WriteString("}")
		}
	}
	return b.String()
}

// Last is one area's most recent payload, replayed on page:focus.
func (a *Areas) Last(key string) *AreaPayload {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last[key]
}

func (a *Areas) Start() {
	if a.ros.Connected() {
		a.Tick()
	}
	a.sched.begin()
}

func (a *Areas) Reconnected() {
	a.sched.end()
	a.mu.Lock()
	a.lastFp = map[string]string{}
	a.nextAt = map[string]time.Time{}
	a.mu.Unlock()
	a.Tick()
	a.sched.begin()
}

func (a *Areas) Suspend() { a.sched.end() }

func (a *Areas) Resume() {
	if a.ros.Connected() {
		a.sched.begin()
	}
}

func (a *Areas) Stop() {
	a.sched.end()
	a.mu.Lock()
	a.lastFp = map[string]string{}
	a.mu.Unlock()
}

// UseCache routes the shared menu reads through the per-router cache.
//
// ── THE CACHE GOES TO THE READS, NOT TO THE SCHEDULER ───────────────────────
//
// `scheduled.begin()` takes the SUBSCRIPTION path as soon as it has a cache, and
// starts the loop only for a residual half. This collector subscribes to no menu
// — its menus are chosen per tick — so handing the cache to `sched` subscribed it
// to nothing and never started the loop: the collector ran, reported no error,
// and read the router exactly never. Measured on the CHR, where the IP Pools page
// waited ninety seconds for a payload that could not come.
func (a *Areas) UseCache(rc *roscache.Cache) { a.cache = rc }
