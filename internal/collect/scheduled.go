package collect

// A collector's subscription to one menu, with the poll-loop fallback.
//
// ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
//
// Phase 3.2 moves collectors off their own timers: instead of owning a loop, a
// collector declares that it WANTS a menu at a cadence, and the router's one
// scheduler decides when to read it. `netwatch` and `talkers` were migrated by
// hand first, and doing two was enough to see that the interesting part is the
// same every time and the fiddly part is the same every time.
//
// The fiddly part is the fallback. BOTH BACKGROUND POOLS BUILD COLLECTORS WITH
// NO CACHE -- a router nobody is watching still needs netwatch for its alerts --
// so every migrated collector must keep working unchanged when `cache == nil`.
// Written out per collector that is four branches to get right in each of Start,
// Suspend, Resume, Reconnected and Stop, twenty times over. Written once it is
// this file, and a migration becomes a field plus five one-line delegations.
//
// ── WHAT IT DELIBERATELY DOES NOT DO ────────────────────────────────────────
//
// One menu AT A TIME. A collector reading several -- and most read several -- subscribes
// to the one whose cadence drives it, and reads the rest inside its own `apply`,
// through the cache, exactly as it did before. Modelling "derive when all of
// these are fresh" is a real question and it belongs to phase 4.2's views, not
// to a helper that exists to stop five lifecycle methods being copied.
//
// WHICH menu may change while running -- see `resubscribe` -- but there is still
// only ever one.

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
)

// scheduled is embedded by a collector. The zero value is a collector that has
// not been given a cache, which is the polled path.
type scheduled struct {
	cache *roscache.Cache
	// loop is the fallback. Nil is allowed: a collector with neither a cache nor
	// a loop simply never reads, which is what a misconfigured one should do
	// rather than panicking on a nil dereference in a timer goroutine.
	loop *pollLoop

	// residual makes the loop the OTHER HALF rather than the fallback.
	//
	// Set it when part of a collector cannot be scheduled: `begin` then
	// subscribes AND starts the loop, and the loop drives only what the
	// subscription does not. `ifStatus` is the case it exists for -- three
	// metadata menus that schedule cleanly and a rates measurement that never
	// can.
	//
	// ── THE RULE THAT MAKES IT SAFE ─────────────────────────────────────────
	//
	// THE LOOP MUST NOT READ A MENU THE SUBSCRIPTION READS. Two clocks driving
	// different reads is fine; two clocks driving the SAME read is the shape that
	// produced a real bug in 3.2, where the scheduler and Get each judged
	// freshness and a menu refreshed at half its cadence.
	//
	// It is not enforceable from here -- this type cannot see what the loop's
	// body does -- so it is enforced from outside, by
	// TestResidualLoopsDoNotReadSubscribedMenus.
	residual bool

	menu    string
	fields  []string
	cadence func() time.Duration
	apply   func([]routeros.Reply, error)

	// ── B.2/B.3: THE DELIVERY HALF ──────────────────────────────────────────
	//
	// WHETHER to stream is NOT a field. It is asked of the cache, which was told
	// once per session by the only thing that knows which collector owns a menu
	// and what the router's config says -- see roscache.StreamWhen. A field here
	// would be the same answer in twenty-two places.
	//
	// These two are the HOW, and both default:
	//
	//	streamKey   names a row for the rolling map. Defaults to RouterOS `.id`,
	//	            which every `/print` row carries. `monitor-traffic` is the
	//	            one that needs its own -- it keys by interface `name`.
	//	streamArgs  the arguments the channel is opened with. Defaults to the
	//	            cadence as `=interval=` plus this subscription's proplist, so
	//	            a streamed menu asks for exactly what the polled one did.
	//
	// The defaults are what make B.4 one line per collector rather than an edit
	// to each.
	streamKey  func(routeros.Reply) string
	streamArgs func() []string

	mu      sync.Mutex
	release func()
	// unfill closes the stream backing this menu, when there is one.
	unfill func()
}

// useCache moves the collector onto the scheduler. Set once, before start.
func (s *scheduled) useCache(c *roscache.Cache) { s.cache = c }

// scheduling reports whether this collector is on the scheduler rather than its
// own loop, so a caller can keep a behaviour that only makes sense on one path --
// the immediate first read in Start, for instance.
func (s *scheduled) scheduling() bool { return s.cache != nil }

// begin declares demand, or starts the loop when there is no cache.
//
// IDEMPOTENT. Resume on a collector that is already running must not add a
// second subscription: the demand set counts subscribers, so a duplicate would
// keep the menu alive after the real one released it.
func (s *scheduled) begin() {
	if s.cache == nil {
		if s.loop != nil {
			s.loop.start()
		}
		return
	}
	// The other half, when there is one. Started before the subscription so a
	// collector is never briefly scheduled-but-not-ticking.
	if s.residual && s.loop != nil {
		s.loop.start()
	}
	s.mu.Lock()
	already := s.release != nil
	// Read under the lock, because `resubscribe` may move them. Everything
	// after this point works on THESE values and re-checks them before
	// committing, so a menu change that lands mid-subscribe loses rather than
	// half-applies.
	menu, fields, apply := s.menu, s.fields, s.apply
	cadence := s.cadence
	s.mu.Unlock()
	if already {
		return
	}

	d := time.Duration(0)
	if cadence != nil {
		d = cadence()
	}
	// NOT UNDER s.mu. Subscribe takes the cache's own lock, and the scheduler
	// takes that lock before calling `apply`, which takes the collector's. Doing
	// both here in the other order is how a deadlock gets built.
	rel := s.cache.Subscribe(menu, fields, d, apply)

	s.mu.Lock()
	if s.release != nil || s.menu != menu {
		// Two begins raced, or a resubscribe moved the menu underneath us. Keep
		// whatever won and give this one up rather than leaking it, so the count
		// still reaches zero when the collector stops.
		s.mu.Unlock()
		rel()
		return
	}
	s.release = rel
	s.mu.Unlock()

	s.fillIfStreaming(menu)
}

// keyByID is the default row key: RouterOS `.id`, which every `/print` row of a
// TABLE carries and which is stable across re-prints. A menu whose rows have no
// `.id` must supply its own, and `FillFromStream` counts the rows it could not
// name so that is discoverable rather than silent.
func keyByID(r routeros.Reply) string { return r[".id"] }

// keySingleton is the key for a SETTINGS menu: one row, no `.id`, and each
// reading replaces the last.
//
// ── A CONSTANT IS THE CORRECT SEMANTICS HERE, NOT A SHORTCUT ───────────────
//
// A rolling map keyed by a constant holds exactly one entry. For
// `/system/resource/print` that is precisely right -- the menu returns one row
// describing the router, and the next reading supersedes it entirely. Keying it
// by `.id` would drop every row into `FillFromStream`'s unkeyed counter and
// serve an EMPTY entry, which for `system` means the dashboard's gauges stop.
//
// It would be wrong on a table, where it would collapse every row into the last
// one. That is why it is a named, deliberate choice at each call site rather
// than a fallback `keyByID` quietly applies when it finds no `.id`: a silent
// fallback cannot tell a settings menu from a table whose rows lost their ids.
func keySingleton(routeros.Reply) string { return "one" }

// defaultStreamArgs opens the channel asking for exactly what the polled read
// asked for: this subscription's proplist, at this subscription's cadence.
//
// THE INTERVAL IS THE CADENCE, which is the operator's rule -- "mode switches
// delivery only and never touches intervals". A stream that pushed faster than
// the poll would have been a silent change of the setting.
//
// RouterOS takes `=interval=` in SECONDS, so a sub-second cadence floors to one:
// that is the fastest the protocol expresses, and rounding to zero would ask for
// a stream with no interval at all.
func defaultStreamArgs(cadence func() time.Duration, fields []string) []string {
	sec := 1
	if cadence != nil {
		if d := cadence(); d >= time.Second {
			sec = int(d / time.Second)
		}
	}
	args := []string{"=interval=" + strconv.Itoa(sec)}
	if len(fields) > 0 {
		args = append(args, "=.proplist="+strings.Join(fields, ","))
	}
	return args
}

// fillIfStreaming asks the cache to keep this menu current from an open channel
// instead of from a read.
//
// ── DELIVERY IS THE ONLY THING THAT CHANGES ─────────────────────────────────
//
// The subscription above is untouched: the menu is still in the demand set, the
// scheduler still delivers at the declared cadence, and `apply` still receives
// rows it cannot tell apart. Only the FILLER differs, which is the operator's
// own framing of the two modes -- "mode switches delivery only and never touches
// intervals".
//
// ── EVERY REFUSAL FALLS BACK TO POLLING, SILENTLY AND ON PURPOSE ────────────
//
// A menu that will not stream, a reader that cannot, a collector with no key
// function, a router that refuses `=interval=` -- each leaves the subscription
// exactly as it was, which is a working polled collector. The alternative is
// surfacing an error for a delivery mechanism the operator asked for and the
// hardware declined, on a page that is rendering correctly.
//
// It is logged nowhere YET, and that is a gap rather than a decision: B.4
// enables collectors one at a time and needs to see which ones took the stream.
// The instrument for that is a stream counter in `roslimit`, which does not
// exist.
func (s *scheduled) fillIfStreaming(menu string) {
	if s.cache == nil {
		return
	}
	s.mu.Lock()
	keyOf, args := s.streamKey, s.streamArgs
	cadence, fields := s.cadence, s.fields
	already := s.unfill != nil
	s.mu.Unlock()
	if already || !s.cache.StreamsMenu(menu) {
		return
	}
	// NO `keyOf == nil` CHECK, DELIBERATELY. `FillFromStream` refuses a nil key
	// and this falls back to polling on any refusal, so a check here would be
	// the same rule in a second place -- what `rooms.go` exists to stop, after
	// the room lists disagreed five times. `keyByID` is a DEFAULT rather than a
	// guard: it is the right key for every `/print` menu.
	if keyOf == nil {
		keyOf = keyByID
	}

	cmd := routeros.Cmd{Path: menu}
	if args != nil {
		cmd.Args = args()
	} else {
		cmd.Args = defaultStreamArgs(cadence, fields)
	}

	// THE BOUNDARY IS THE CADENCE. A round of a `/print =interval=N` ends when
	// the next one starts, and a quiet gap longer than the interval is what says
	// so for a table small enough to arrive in a burst. See absorb.
	boundary := time.Second
	if cadence != nil {
		if d := cadence(); d > 0 {
			boundary = d
		}
	}
	// NO `Merge`, WHICH IS THE DECLARATION THAT THIS MENU HAS ONE OWNER. A
	// second holder is refused rather than merged — seven of the eight
	// streamed menus have exactly one consumer, and two collectors quietly
	// sharing a channel is a real bug class. `/interface/monitor-traffic` is the
	// exception and declares a merge rule; see collect/monitortraffic.go.
	stop, err := s.cache.JoinStream(roscache.Join{
		Menu: menu, Cmd: cmd, KeyOf: keyOf, Boundary: boundary,
	})
	if err != nil {
		return // polling, which is what the subscription already does
	}

	s.mu.Lock()
	if s.unfill != nil || s.menu != menu {
		// Raced with another begin, or a resubscribe moved the menu. Give this
		// one up rather than leaking a channel the collector cannot reach.
		s.mu.Unlock()
		stop()
		return
	}
	s.unfill = stop
	s.mu.Unlock()
}

// resubscribe points the collector at a different menu, with the callback that
// knows how to read it.
//
// ── MECHANISM B: A MENU CHOSEN AT RUNTIME ───────────────────────────────────
//
// `begin`/`end` assume the collector knows its menu when it is constructed.
// Three do not. The firewall refreshes the counters of whichever TABLE the
// operator is looking at; `wifi` and `wireless` read whichever WIRELESS STACK
// the router turned out to have, which is not known until it answers. A
// subscription is per-menu, so those collectors need to be able to move one.
//
// ── WHY IT TAKES THE CALLBACK TOO, AND WHY THAT IS THE WHOLE POINT ──────────
//
// The delivered rows say nothing about which menu they came from. So a callback
// that resolves that itself -- reading the collector's "current table" field --
// can be handed the OLD menu's rows after the field has already moved, and merge
// nat counters into the filter table. The rows are keyed by RouterOS `.id`, and
// `*1` exists in every menu, so that merge SUCCEEDS and produces silently wrong
// numbers for one frame.
//
// Binding the callback to the menu at the moment of subscription removes the
// question. The caller passes a closure that already knows its table; nothing
// has to be resolved later, so nothing can be resolved late.
//
// ── ORDER ───────────────────────────────────────────────────────────────────
//
// Release first, then subscribe. The demand set counts subscribers per menu and
// stops reading a menu that has none, so releasing first is what makes the old
// menu actually go quiet.
//
// Releasing does NOT drain a delivery already under way. `Cache.deliver` copies
// the callbacks out and drops the lock before calling them, so an old-menu
// delivery can still land after the release returns. That is precisely why the
// callback is bound above rather than resolved later: the ordering cannot be
// relied on, so nothing is allowed to depend on it.
//
// A no-op when the menu has not changed, so a caller may pass its current
// selection unconditionally. Safe while suspended: it records the choice and
// `begin` picks it up.
func (s *scheduled) resubscribe(menu string, apply func([]routeros.Reply, error)) {
	s.mu.Lock()
	if s.menu == menu {
		s.mu.Unlock()
		return
	}
	s.menu, s.apply = menu, apply
	old := s.release
	// AND THE OLD MENU'S CHANNEL. A fill is per-MENU, so moving the menu without
	// releasing it leaves a stream open on a menu this collector no longer reads
	// -- and `FillFromStream` refuses a second fill of one menu, so the leak
	// would also stop the collector ever streaming that menu again if it moved
	// back. Mechanism B moves menus routinely: `firewall` follows the table the
	// operator is looking at, and `wifi`/`wireless` follow whichever stack the
	// router turned out to have.
	oldFill := s.unfill
	s.release, s.unfill = nil, nil
	fields, cadence := s.fields, s.cadence
	s.mu.Unlock()

	if old != nil {
		old()
	}
	if oldFill != nil {
		oldFill()
	}
	// Nothing to move: either this collector is polled, or it is suspended and
	// `begin` will subscribe to the menu just recorded.
	if s.cache == nil || old == nil {
		return
	}

	d := time.Duration(0)
	if cadence != nil {
		d = cadence()
	}
	rel := s.cache.Subscribe(menu, fields, d, apply)

	s.mu.Lock()
	if s.release != nil || s.menu != menu {
		s.mu.Unlock()
		rel()
		return
	}
	s.release = rel
	s.mu.Unlock()

	s.fillIfStreaming(menu)
}

// end gives up the demand, or stops the loop.
//
// This is what Suspend now means. Not "stop my timer" but "stop wanting this
// menu" -- and when the last subscriber goes, the scheduler stops reading it.
// That is the single answer to "should this run" that phase 3 is for.
func (s *scheduled) end() {
	if s.cache == nil {
		if s.loop != nil {
			s.loop.stop()
		}
		return
	}
	if s.residual && s.loop != nil {
		s.loop.stop()
	}
	s.mu.Lock()
	rel := s.release
	fill := s.unfill
	s.release, s.unfill = nil, nil
	s.mu.Unlock()
	if rel != nil {
		rel()
	}
	// THE CHANNEL GOES WITH THE SUBSCRIPTION. A fill outliving its collector is
	// an open channel on a router nobody is reading -- the exact cost this whole
	// rewrite is organised around, and invisible because the page it fed is gone.
	if fill != nil {
		fill()
	}
}
