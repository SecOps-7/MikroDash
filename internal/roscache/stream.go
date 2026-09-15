package roscache

// B.1 of Collectors-Rewrite.md: a SECOND WAY OF FILLING AN ENTRY.
//
// ── THE ASSUMPTION THIS FILE EXISTS TO RETIRE ───────────────────────────────
//
// This cache was built for queries, and the shared-menu ledger recorded
// `/interface/monitor-traffic` as unroutable because "one side holds a stream,
// the other takes a bounded measurement; a by-menu cache would hand one the
// other's answer". True of the cache as it was. Not true of caching.
//
// The operator's challenge, 2026-09-09: "any stream here is simply the router
// controlling the rate of data arriving using =interval=N; a slow poll to the
// same endpoint would yield the same fields at a slower rate. So why not use
// cache?"
//
// It survives. The scheduler is only ONE filler:
//
//	poll     Scheduler tick -> Invalidate -> Get -> deliver(rows) -> onRows
//	stream   the router pushes ->            store -> deliver(rows) -> onRows
//	                                                  ^ the same seam
//
// `Cache.deliver` and `Subscribe`'s `onRows` are already the fan-out. A stream
// is a filler for an entry collectors already subscribe to, so a collector does
// not change at all to gain a stream path.
//
// ── THE BOUNDARY IS NOT QUERY-VERSUS-STREAM. IT IS WHAT A ROW MEANS ─────────
//
// A stream can back an entry when its rows are successive READINGS of a keyed
// value. It cannot when each row is a distinct element that matters:
//
//	/interface/monitor-traffic   a fresh reading per interface     ROLLABLE
//	any /print =interval=N       a re-print of the whole table     ROLLABLE
//	/tool/ping                   a distinct measurement; the collector counts
//	                             EVERY row into min/max/avg/loss   NOT ROLLABLE
//	/log/listen                  a distinct event                  NOT ROLLABLE
//
// Getting that wrong fails SILENTLY -- 0% loss for ever, dropped log lines, and
// nothing red anywhere. `unrollable` below is the gate, and it refuses rather
// than warns.
//
// ── NO SWEEP BOUNDARY, WHICH IS THE TRAP AVOIDED RATHER THAN SOLVED ────────
//
// `monitor-traffic` with a comma list pushes one row per interface per interval
// and sends no `!done` between rounds, so "wait for a complete sweep" has no
// signal to wait on. A ROLLING MAP has no boundary to detect: each row replaces
// its own key and the entry is always current.
//
// `snapshot` returns the values SORTED BY KEY. That is not tidiness: collectors
// fingerprint their payloads to suppress redundant emits, and an unstable order
// would make every payload look changed and defeat the dirty check.

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

// Streamer is the half of a reader that can hold a channel open. The cache's
// `Reader` is Do-only, because until now it had no use for the other half.
type Streamer interface {
	Stream(routeros.Cmd, func(routeros.Reply)) (func(), error)
}

// unrollable are the menus a rolling map would silently corrupt. Keyed by menu
// path, valued by the reason, so a refusal can say why.
//
// A DENYLIST AND NOT A JUDGEMENT AT THE CALL SITE. The failure is invisible --
// a ping collector fed a rolling entry reports 0% loss for ever and every test
// still passes -- so the decision belongs somewhere a person has to edit
// deliberately, next to the reason.
var unrollable = map[string]string{
	// ── KIND ONE: THE ROWS ARE NOT READINGS OF ONE VALUE ────────────────────
	"/tool/ping": "every row is a distinct measurement counted into min/max/avg/loss; " +
		"a rolling entry keeps only the latest and would report 0% loss for ever",
	"/log/listen": "every row is a distinct event; a rolling entry drops lines",

	// ── KIND TWO WAS HERE AND IS NOW EMPTY ─────────────────────────────────
	//
	// It has been narrowed twice by measurement rather than by argument, and
	// both narrowings are worth keeping because each was a real obstacle:
	//
	//	FIRST it was CHURN. The entry could only accumulate, so a row that left
	//	the table never left the map -- closed connections, departed clients and
	//	expired leases piling up for the life of the session. B.6 found the round
	//	boundary (a repeated key, or a gap longer than the cadence) and that
	//	reason went. The connection table came off the list.
	//
	//	THEN it was EMPTINESS. A table with no rows sends nothing, and nothing is
	//	indistinguishable from a dead channel, so an emptied table held its last
	//	contents. The watchdog turned out to already run the distinguishing
	//	experiment: it reopens a quiet channel, and silence that survives a
	//	deliberate reopen is evidence of an empty table rather than a broken one.
	//	See the rule in `watch`.
	//
	// So no menu is refused for either reason now. The list above -- rows that
	// are not readings of one value -- is the one that remains, and it is
	// permanent: it is a fact about what a ping result and a log line ARE.
}

// streamStale is how long an open channel may deliver nothing before the
// watchdog restarts it.
//
// ── LIFTED FROM `traffic`, WHOSE PROBLEM THIS NOW IS FOR EVERYONE ──────────
//
// A polled entry fails LOUDLY: the read errors and the error is cached and
// surfaced. A pushed entry can go SILENT -- the router stops sending, or the
// channel wedges -- and without this the cache would serve its last value for
// ever with nothing noticing. `internal/collect/traffic.go` already carried
// exactly this recovery for its one stream (5s tick, 10s staleness); here it
// covers every stream instead of one.
const (
	streamStale = 10 * time.Second
	streamCheck = 5 * time.Second
)

// streamFill keeps one menu's entry current from an open channel.
type streamFill struct {
	cmd   routeros.Cmd
	keyOf func(routeros.Reply) string

	mu sync.Mutex
	// rows is the last COMPLETE round: what `snapshot` serves.
	rows map[string]routeros.Reply
	// round is the round being received. See absorb for how its end is found.
	round map[string]routeros.Reply
	// published is false until the first round has completed, and while it is
	// false `snapshot` serves the ACCUMULATING round instead. Without it a page
	// would be blank for a whole interval after the channel opens, which is the
	// hang B.1 exists to remove rather than introduce.
	published bool
	// boundary is how long a quiet gap must be to end a round. Derived from the
	// subscription's cadence by the caller.
	boundary time.Duration
	lastRow  time.Time
	// unkeyed counts rows the key function could not name. A menu that produces
	// any is one a rolling map cannot represent, and the count is the only way
	// to find that out from outside.
	unkeyed int
	stop    func()
	closed  bool
	// check and stale are the watchdog's tick and its silence bound. Fields
	// rather than the constants directly, so a test can drive the restart in
	// milliseconds instead of waiting out ten real seconds -- which is the
	// difference between this recovery being tested and being hoped for.
	check time.Duration
	stale time.Duration
	// restarts is how many times the watchdog has reopened this channel. Read by
	// a test, and worth having: a channel restarting steadily is a router
	// problem that would otherwise look like a slow page.
	restarts int
	// rounds is how many complete rounds have been published.
	rounds int
	// openedAt is when the channel was last opened, for `StreamStats`. A
	// collector that used to run its own watchdog needs to distinguish "silent
	// for ten seconds" from "opened one second ago and not yet producing".
	openedAt time.Time
	// ── SHARING ─────────────────────────────────────────────────────────────
	//
	// `shared` is set by `JoinStream` and never by `FillFromStream`, so the two
	// forms cannot be mixed on one menu by accident. That refusal is the guard
	// `FillFromStream` has always had — two collectors silently fighting over
	// one channel is a whole class of bug — and sharing has to be OPTED INTO at
	// the call site rather than acquired by being second.
	shared  bool
	nextID  int
	holders map[int]Join
	// quietSince is when the current run of silence began, reset by any row and
	// by an intentional reopen. See the empty-table rule in watch.
	quietSince time.Time
	// done stops the watchdog. Owned by whichever entry point opened the fill.
	done chan struct{}
}

// Join is one holder's claim on a SHARED stream-filled menu.
//
// ── WHY SHARING NEEDED A SECOND ENTRY POINT ────────────────────────────────
//
// `FillFromStream` is single-owner and refuses a second caller, deliberately:
// two collectors quietly fighting over one channel is a whole class of bug, and
// seven of the eight streamed menus have exactly one consumer.
//
// `/interface/monitor-traffic` has two, and they want different things.
// `ifStatus` wants every enabled interface at its own cadence and reads a
// SNAPSHOT; `traffic` wants the interfaces somebody is watching — which may
// include a disabled one — at one second, and needs EVERY ROW as it arrives to
// fan out to per-interface rooms. Neither set contains the other, so this is a
// merge and not a subscription to somebody else's channel.
//
// ── THE MERGE IS THE CALLER'S, AND THAT IS THE POINT ───────────────────────
//
// `=interface=` is a comma list and `=interval=` is a minimum. Neither rule
// belongs in a cache that knows nothing about menus, and putting them here
// would make the next shared menu's rule the second special case. So holders
// declare what they want, this package keeps the set, and `Merge` — supplied by
// the caller and identical for every holder of one menu — turns the set into
// the command the channel is opened with.
type Join struct {
	Menu     string
	Cmd      routeros.Cmd
	KeyOf    func(routeros.Reply) string
	Boundary time.Duration
	// Merge combines every current holder's command into the one to open with.
	// Called on every join and every release, so a holder leaving NARROWS the
	// channel as well as a holder arriving widening it.
	//
	// ── NIL MEANS UNSHARED, AND THAT IS THE WHOLE DECLARATION ──────────────
	//
	// Seven of the eight streamed menus have exactly one consumer, and a
	// second one arriving is a real bug class: two collectors quietly fighting
	// over one channel. A nil Merge says "this menu has one owner", and a second
	// `JoinStream` on it is REFUSED rather than merged.
	//
	// That refusal used to be a whole second entry point, `FillFromStream`, kept
	// beside this one because migrating its single caller was work. It is a
	// property of a fill, not a reason for a second function — phase 6.2.
	Merge func([]routeros.Cmd) routeros.Cmd
	// OnRow, if set, receives every row as it arrives — before it is folded into
	// the round, and never under the fill's lock.
	OnRow func(routeros.Reply)
}

// JoinStream adds a holder to a shared stream-filled menu, opening the channel
// if this is the first.
//
// THE RETURNED RELEASE IS PER HOLDER and idempotent. The channel closes when the
// last holder releases; before that, a release re-merges and may narrow the
// command, which is the half a refcount alone would miss.
//
// `KeyOf` and `Boundary` come from the FIRST holder, except that the boundary is
// lowered to the finest any holder asks for — a merged channel delivers at the
// fastest interval requested, so the gap that ends a round is that interval and
// not a slower holder's.
func (c *Cache) JoinStream(j Join) (func(), error) {
	if j.KeyOf == nil {
		return nil, fmt.Errorf("roscache: %s needs a key function; a rolling map with "+
			"no key holds one row", j.Menu)
	}
	if why, no := unrollable[j.Menu]; no {
		return nil, fmt.Errorf("roscache: %s cannot be stream-filled: %s", j.Menu, why)
	}
	s, ok := c.ros.(Streamer)
	if !ok {
		return nil, fmt.Errorf("roscache: this reader cannot stream")
	}

	// BEFORE the lock: `streamTimings` takes `c.mu` too, and taking it twice on
	// one goroutine is a deadlock rather than a re-entrant read.
	check, stale := c.streamTimings()

	c.mu.Lock()
	if c.fills == nil {
		c.fills = map[string]*streamFill{}
	}
	f, existing := c.fills[j.Menu]
	if existing && !f.shared {
		c.mu.Unlock()
		return nil, fmt.Errorf("roscache: %s already has an owner and declared no "+
			"merge rule, so it is single-owner; both holders must supply a Merge to "+
			"share it", j.Menu)
	}
	if existing && j.Merge == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("roscache: %s is shared and this holder declared no "+
			"merge rule; every holder of one menu must agree how the command is built",
			j.Menu)
	}
	if !existing {
		f = newFill(j.Cmd, j.KeyOf, j.Boundary, check, stale)
		f.shared = j.Merge != nil
		f.holders = map[int]Join{}
		c.fills[j.Menu] = f
	}
	f.mu.Lock()
	f.nextID++
	id := f.nextID
	f.holders[id] = j
	cmd := f.mergeLocked()
	f.tightenBoundaryLocked(j.Boundary)
	f.mu.Unlock()
	c.mu.Unlock()

	if err := f.reopenFor(s, cmd); err != nil {
		// The first holder failing leaves nothing behind; a later one failing
		// leaves the channel as it was, which is right — the holders already
		// there are still being served.
		c.mu.Lock()
		f.mu.Lock()
		delete(f.holders, id)
		last := len(f.holders) == 0
		f.mu.Unlock()
		if last {
			delete(c.fills, j.Menu)
		}
		c.mu.Unlock()
		if last {
			f.close()
		}
		return nil, err
	}
	if !existing {
		f.done = make(chan struct{})
		go f.watch(s, f.done)
	}

	var once sync.Once
	return func() {
		once.Do(func() { c.releaseHolder(j.Menu, f, id, s) })
	}, nil
}

// releaseHolder drops one holder, narrowing or closing the channel.
func (c *Cache) releaseHolder(menu string, f *streamFill, id int, s Streamer) {
	c.mu.Lock()
	f.mu.Lock()
	delete(f.holders, id)
	last := len(f.holders) == 0
	var cmd routeros.Cmd
	if !last {
		cmd = f.mergeLocked()
	}
	f.mu.Unlock()
	if last {
		delete(c.fills, menu)
	}
	c.mu.Unlock()

	if last {
		close(f.done)
		f.close()
		return
	}
	// NARROWING IS NOT OPTIONAL. A holder leaving is exactly when the channel
	// should stop carrying interfaces nobody is watching, and a refcount that
	// only ever widens would keep the last viewer's selection open for the life
	// of the session.
	_ = f.reopenFor(s, cmd)
}

// mergeLocked applies the holders' merge rule. Caller holds f.mu.
//
// SORTED BY HOLDER ID, so the command a given set of holders produces is stable
// and `reopenFor` restarts on a real change rather than on map iteration order.
func (f *streamFill) mergeLocked() routeros.Cmd {
	ids := make([]int, 0, len(f.holders))
	for id := range f.holders {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	cmds := make([]routeros.Cmd, 0, len(ids))
	var merge func([]routeros.Cmd) routeros.Cmd
	for _, id := range ids {
		h := f.holders[id]
		cmds = append(cmds, h.Cmd)
		if merge == nil {
			merge = h.Merge
		}
	}
	if merge == nil {
		// Unshared: one holder, and its own command is the whole answer.
		if len(cmds) == 1 {
			return cmds[0]
		}
		return f.cmd
	}
	return merge(cmds)
}

// tightenBoundaryLocked lowers the round boundary to the finest any holder wants.
func (f *streamFill) tightenBoundaryLocked(b time.Duration) {
	if b <= 0 {
		return
	}
	if f.boundary <= 0 || b < f.boundary {
		f.boundary = b
	}
	if 2*f.boundary > f.stale {
		f.stale = 2 * f.boundary
	}
}

// reopenFor restarts the channel when the merged command has changed.
//
// A NO-OP WHEN IT HAS NOT, which is the common case: a second holder that wants
// a subset of what is already open costs nothing at all.
func (f *streamFill) reopenFor(s Streamer, cmd routeros.Cmd) error {
	f.mu.Lock()
	if f.stop != nil && sameCmd(f.cmd, cmd) {
		f.mu.Unlock()
		return nil
	}
	old := f.stop
	f.stop, f.cmd = nil, cmd
	f.mu.Unlock()
	if old != nil {
		old()
	}
	return f.open(s)
}

func sameCmd(a, b routeros.Cmd) bool {
	if a.Path != b.Path || len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Args {
		if a.Args[i] != b.Args[i] {
			return false
		}
	}
	return true
}

// newFill is the shared constructor for both entry points.
func newFill(cmd routeros.Cmd, keyOf func(routeros.Reply) string,
	boundary, check, stale time.Duration) *streamFill {
	if boundary > 0 && 2*boundary > stale {
		stale = 2 * boundary
	}
	return &streamFill{cmd: cmd, keyOf: keyOf,
		rows: map[string]routeros.Reply{}, round: map[string]routeros.Reply{},
		boundary: boundary, check: check, stale: stale}
}

// StreamStats is one fill's health, for a collector that used to run its own
// watchdog and still has a health signal to report.
//
// `traffic` tints its dashboard card and names a restart count — a real feature
// that must survive its watchdog being retired in favour of this package's. So
// the counters it kept move here rather than disappearing.
type StreamStats struct {
	Open     bool
	Restarts int
	// Silent is how long since the last row arrived.
	Silent time.Duration
	// SinceOpen is how long the channel has been up. A channel opened a moment
	// ago is not stale for having produced nothing yet, which is the distinction
	// `traffic`'s watchdog drew with `streamStart`.
	SinceOpen time.Duration
}

// StreamStats reports one menu's fill, and whether there is one.
func (c *Cache) StreamStats(menu string) (StreamStats, bool) {
	f := c.fillFor(menu)
	if f == nil {
		return StreamStats{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	st := StreamStats{Open: f.stop != nil, Restarts: f.restarts}
	if !f.lastRow.IsZero() {
		st.Silent = time.Since(f.lastRow)
	}
	if !f.openedAt.IsZero() {
		st.SinceOpen = time.Since(f.openedAt)
	}
	return st, true
}

// StreamTimings shortens the stream watchdog for every fill this cache opens.
//
// ── A TEST SEAM, AND THE FIELDS IT REACHES ALREADY SAID SO ─────────────────
//
// `streamFill.check` and `.stale` are fields rather than the constants directly,
// and the comment on them has always given the reason: "so a test can drive the
// restart in milliseconds instead of waiting out ten real seconds -- which is
// the difference between this recovery being tested and being hoped for".
// `fillEvery` is how `roscache`'s own tests reach them.
//
// A test in another package could not, and one now needs to: `traffic` retired
// its watchdog in favour of this one and still reports the restart count on the
// Dashboard card, so the property worth driving is END TO END — the channel
// stalls, this package restarts it, and `traffic` turns the rising count into a
// health transition. At the shipped 5s and 10s that test takes a minute a case.
//
// Zero means the shipped values. Nothing in the binary calls this.
func (c *Cache) StreamTimings(check, stale time.Duration) {
	c.mu.Lock()
	c.checkOver, c.staleOver = check, stale
	c.mu.Unlock()
}

// streamTimings returns the timings a new fill should use.
func (c *Cache) streamTimings() (check, stale time.Duration) {
	c.mu.Lock()
	check, stale = c.checkOver, c.staleOver
	c.mu.Unlock()
	if check <= 0 {
		check = streamCheck
	}
	if stale <= 0 {
		stale = streamStale
	}
	return check, stale
}

// `FillFromStream` and `fillEvery` lived here until phase 6.2.
//
// ── ONE ENTRY POINT, NOT TWO ───────────────────────────────────────────────
//
// `FillFromStream(menu, cmd, keyOf, boundary)` was the single-owner form and
// `JoinStream` the shared one. `JoinStream` did everything it did plus merging
// and fan-out, and thirteen menus stayed on the old form because migrating them
// was work — through exactly ONE call site, `scheduled.fillIfStreaming`.
//
// The single-owner REFUSAL was the thing worth keeping, and it is a property of a
// fill rather than a reason for a second function: a `Join` with no `Merge`
// declares "this menu has one owner", and a second holder is refused. See the
// note on `Join.Merge`.
//
// `fillEvery` was its timing-injection twin, and `Cache.StreamTimings` had already
// replaced what it was for.

// open starts the channel. The caller holds no lock.
func (f *streamFill) open(s Streamer) error {
	stop, err := s.Stream(f.cmd, f.absorb)
	if err != nil {
		return err
	}
	f.mu.Lock()
	if f.closed { // stopped while the stream was opening
		f.mu.Unlock()
		stop()
		return nil
	}
	f.stop, f.lastRow = stop, time.Now()
	f.openedAt = f.lastRow
	f.mu.Unlock()
	return nil
}

// absorb folds one pushed row into the round being received.
//
// ── FINDING THE END OF A ROUND, WHICH THE PROTOCOL DOES NOT MARK ────────────
//
// A `/print =interval=N` re-prints the WHOLE table every interval and sends no
// `!done` between rounds. Measured 2026-09-09: `/tool/netwatch/print` returned 9
// rows in 3s for 3 configured hosts, `/ip/dns/print` 4 rows for 1. Three
// re-prints, no separator.
//
// Without a boundary the entry can only ever accumulate, and a row that LEAVES
// the table never leaves the map -- closed connections, departed clients and
// expired leases pile up for the life of the session, on a page that looks
// populated. That is why every churning menu was refused.
//
// TWO SIGNALS, because neither is sufficient alone:
//
//	A KEY REPEATS   the round has restarted. Reliable precisely because the
//	                re-print is total: every row still present appears in every
//	                round, so a repeat is certain unless the entire membership
//	                turned over at once. This is the signal that works when the
//	                table is large enough that rounds arrive back to back with no
//	                gap between them.
//	A QUIET GAP     nothing for longer than the cadence. This is what ends the
//	                round for a small table, where the rows arrive in a burst and
//	                then silence, and a repeat would otherwise be a whole
//	                interval away.
//
// ── WHAT THIS STILL DOES NOT SOLVE, NAMED RATHER THAN GLOSSED ──────────────
//
// A table that becomes COMPLETELY EMPTY sends nothing at all, which is
// indistinguishable from a stream that has died -- and the watchdog, correctly,
// treats prolonged silence as death and reopens. So an emptied table holds its
// last contents rather than emptying.
//
// That is a far smaller error than the unbounded growth it replaces, and it is
// bounded by the next row rather than by the session. It is still an error, and
// it is why the registration tables stay refused: "no wireless clients" is an
// ordinary state, and ghost clients would be the visible result.
func (f *streamFill) absorb(r routeros.Reply) {
	// ── THE FAN-OUT COMES FIRST, AND NOT UNDER THE LOCK ────────────────────
	//
	// A holder's `OnRow` is another collector's delivery path: `traffic`'s takes
	// its own mutex and emits to the hub. Calling it while holding `f.mu` puts
	// two collector locks in one order here and invites the opposite order
	// somewhere else — which is exactly the deadlock `syncRateChannel` and
	// `Tick` produced on 2026-09-09, and it HUNG the suite rather than failing
	// it.
	//
	// Before the fold rather than after, so a row reaches its consumer at the
	// same moment it always did.
	for _, fn := range f.rowHooks() {
		fn(r)
	}
	k := f.keyOf(r)
	f.mu.Lock()
	defer f.mu.Unlock()

	// A GAP ENDS THE PREVIOUS ROUND, and this is checked before the repeat so a
	// small table's round closes on time rather than an interval late.
	if f.boundary > 0 && len(f.round) > 0 && !f.lastRow.IsZero() &&
		time.Since(f.lastRow) > f.boundary {
		f.finishRoundLocked()
	}
	f.lastRow = time.Now()
	// Any row ends the run of silence, which is what makes the empty-table rule
	// self-correcting rather than sticky.
	f.quietSince = time.Time{}

	if k == "" {
		// NOT DROPPED SILENTLY. A menu whose rows this cannot name is one a
		// rolling map cannot represent, and the count is what makes that
		// visible from outside instead of appearing as a page missing a row.
		f.unkeyed++
		return
	}
	if _, repeat := f.round[k]; repeat {
		f.finishRoundLocked()
	}
	if f.round == nil {
		f.round = map[string]routeros.Reply{}
	}
	f.round[k] = r
}

// rowHooks copies the holders' row callbacks so `absorb` can call them without
// holding the lock. Nil for every unshared fill, which is seven of eight.
func (f *streamFill) rowHooks() []func(routeros.Reply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.holders) == 0 {
		return nil
	}
	out := make([]func(routeros.Reply), 0, len(f.holders))
	for _, h := range f.holders {
		if h.OnRow != nil {
			out = append(out, h.OnRow)
		}
	}
	return out
}

// finishRoundLocked publishes the round just received. Caller holds f.mu.
func (f *streamFill) finishRoundLocked() {
	if len(f.round) == 0 {
		return
	}
	f.rows = f.round
	f.round = map[string]routeros.Reply{}
	f.published = true
	f.rounds++
}

// hasRows reports whether this fill has received anything at all yet.
//
// ── A STREAM THAT HAS NOT WARMED UP IS A MISS, NOT AN EMPTY ANSWER ─────────
//
// `FillFromStream` returns as soon as the channel is OPEN, and the first row
// arrives some milliseconds later. The scheduler can deliver in that window, and
// `Get` answering "no rows" there is not a cheap wrong answer -- it is published
// to the collector, which builds an empty payload, and the next delivery is a
// whole cadence away.
//
// MEASURED: the DHCP page read "0 leases" and "No DHCP networks on this device"
// while the router held 45 and 3. Both menus run at TEN MINUTES, so one empty
// answer at startup persisted for ten minutes. On a fast menu the same race
// exists and self-corrects in a second, which is exactly why it would have been
// found late and blamed on something else.
//
// So an unwarmed fill falls through to the ordinary read path: the page is
// answered from a poll, and the stream takes over the moment it has rows.
// ── "AUTHORITATIVE", NOT "HAS ROWS", AND THE DIFFERENCE IS NOT PEDANTIC ────
//
// Written as `len(rows) > 0` this sends an entry that has legitimately gone
// EMPTY back to the read path -- so a genuinely empty table would be polled for
// ever AND hold a channel, which is worse than either alone. An entry that has
// completed a round is authoritative about its own emptiness.
//
// So the question is whether a round has ever completed, not whether there is
// anything in it.
func (f *streamFill) authoritative() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.published || len(f.round) > 0
}

// snapshot is the current value of every key, sorted. See the header on why the
// order is load-bearing rather than tidy.
func (f *streamFill) snapshot() []routeros.Reply {
	f.mu.Lock()
	defer f.mu.Unlock()
	src := f.rows
	if !f.published {
		// The first round is still arriving. Serving it partially fills the page
		// a whole interval sooner than waiting, and the next round replaces it
		// wholesale.
		src = f.round
	}
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]routeros.Reply, 0, len(keys))
	for _, k := range keys {
		out = append(out, src[k])
	}
	return out
}

func (f *streamFill) close() {
	f.mu.Lock()
	stop := f.stop
	f.stop, f.closed = nil, true
	f.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// watch is the silent-death recovery. See streamStale.
func (f *streamFill) watch(s Streamer, done <-chan struct{}) {
	t := time.NewTicker(f.check)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			f.mu.Lock()
			// A ROUND THAT HAS GONE QUIET IS OVER. `absorb` can only notice a
			// gap when the NEXT row arrives, which for a table read once a
			// minute is a minute late; this closes it on time. Same rule, the
			// other side of the silence.
			if f.boundary > 0 && len(f.round) > 0 && !f.lastRow.IsZero() &&
				time.Since(f.lastRow) > f.boundary {
				f.finishRoundLocked()
			}

			// ── AN EMPTY TABLE SENDS NOTHING, AND SO DOES A DEAD STREAM ─────
			//
			// RouterOS emits no rows at all for a `/print =interval=N` on an
			// empty table -- measured, not assumed: the B.4 probe held
			// `/ppp/active/print` open for three seconds on four routers and
			// received nothing, and that menu is empty on all of them.
			//
			// So silence is ambiguous, and holding the last contents was the
			// safe reading: an emptied table went on showing rows that had gone.
			// That is why the registration tables, the lease table and
			// `/ppp/active` stayed on the polled path after B.6.
			//
			// THE WATCHDOG RESOLVES IT, because it already does the experiment.
			// It reopens a channel that has gone quiet, and a REOPENED channel
			// on a router that is answering delivers at once if the table has
			// rows. Silence that survives a deliberate restart is therefore
			// evidence of an empty table rather than of a broken one.
			//
			// The rule: once restarted for silence, a further full staleness
			// window with nothing publishes an EMPTY round.
			//
			// IT CAN STILL BE WRONG, and the direction matters. A router that
			// has wedged in a way a reconnect does not clear would be reported
			// as having an empty table rather than a stale one. That is the
			// better error for these menus -- "no clients associated" invites a
			// look, while three clients that left an hour ago look entirely
			// plausible -- and it self-corrects on the first row that arrives.
			if f.restarts > 0 && f.published && len(f.rows) > 0 &&
				!f.quietSince.IsZero() && time.Since(f.quietSince) > 2*f.stale {
				f.rows = map[string]routeros.Reply{}
				f.round = map[string]routeros.Reply{}
				f.rounds++
				f.quietSince = time.Now()
			}

			quiet := time.Since(f.lastRow)
			shut := f.closed
			stop := f.stop
			f.mu.Unlock()
			if shut || quiet < f.stale {
				continue
			}

			// STOPPED AND REOPENED, not closed: `close` sets `closed` and this
			// fill must survive its own restart.
			if stop != nil {
				stop()
			}
			f.mu.Lock()
			f.stop = nil
			f.restarts++
			// ── SET ONCE, NOT ON EVERY RESTART ──────────────────────────
			//
			// The silence run starts at the FIRST reopen and is not restarted by
			// later ones. Written as an unconditional assignment it could never
			// fire: the watchdog reopens every staleness window, so the run was
			// reset every window and never reached the two windows the
			// empty-table rule asks for. Caught by the test, which is the only
			// thing that could have caught it -- a rule that never fires looks
			// exactly like a rule that is not needed.
			if f.quietSince.IsZero() {
				f.quietSince = time.Now()
			}
			// The rolling map is KEPT across a restart. Its rows are the last
			// readings the router gave and they are what a page renders while
			// the channel comes back; dropping them would blank every card for
			// the length of a reconnect, which is the opposite of the point.
			f.lastRow = time.Now()
			f.mu.Unlock()
			_ = f.open(s)
		}
	}
}

// Unrollable reports why a menu may not back a rolling entry, if it may not.
//
// EXPORTED SO THE CALLER'S OWN TABLE CAN BE CHECKED AGAINST IT. Without this a
// line added to `session.streamableMenus` naming a refused menu is SAFE but
// MISLEADING: `fillIfStreaming` falls back to polling on any refusal, so the
// commit claims a delivery change, makes none, and nothing fails. The two lists
// disagreeing quietly is the exact shape `rooms.go` exists to stop.
func Unrollable(menu string) (string, bool) {
	why, no := unrollable[menu]
	return why, no
}

// StreamWhen installs the decision: for a menu about to be subscribed, may it be
// kept current by a channel instead of a read?
//
// ── ONE TABLE, NOT TWENTY-TWO COLLECTOR EDITS ───────────────────────────────
//
// The obvious plumbing was to give every collector a "should I stream" function
// at construction. That is twenty-two edits to say one thing, and twenty-two
// places for it to drift -- the defect `rooms.go` exists to record.
//
// The decision is keyed by MENU, which is what this package already speaks, and
// the caller resolves the rest: it knows which collector owns a menu and what
// `eff.Stream` says about it. So this is set ONCE per session and `scheduled`
// asks rather than being told.
//
// Nil means "poll everything", which is the state of every session until the
// caller says otherwise.
func (c *Cache) StreamWhen(fn func(menu string) bool) {
	c.mu.Lock()
	c.streamWhen = fn
	c.mu.Unlock()
}

// StreamsMenu is the decision for one menu. Exported because `scheduled` in
// internal/collect is the caller.
func (c *Cache) StreamsMenu(menu string) bool {
	c.mu.Lock()
	fn := c.streamWhen
	c.mu.Unlock()
	return fn != nil && fn(menu)
}

// fillFor returns the stream backing a menu, or nil.
func (c *Cache) fillFor(menu string) *streamFill {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fills[menu]
}

// Streaming reports whether a menu is backed by a channel that has answered.
//
// AUTHORITATIVE, NOT MERELY OPEN. A caller asking this is deciding whether to
// take its own measurement instead, and an open-but-unwarmed channel would send
// it away with nothing -- the same race that emptied the DHCP page, arriving
// from the other direction.
func (c *Cache) Streaming(menu string) bool {
	f := c.fillFor(menu)
	return f != nil && f.authoritative()
}

// StreamedMenus is which menus are currently stream-filled. For a test, and for
// anything that needs to know a menu is push-backed rather than polled.
func (c *Cache) StreamedMenus() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.fills))
	for m := range c.fills {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
