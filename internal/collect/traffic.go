package collect

// Traffic collector — live per-interface throughput.
//
//	/interface/monitor-traffic =interface=<list> =interval=1
//
// THE STREAM COVERS ONLY WHAT IS BEING WATCHED, plus the default interface,
// which is always included because the WAN status badge reads it on every page.
// A router with forty interfaces is not forty streams: the subscription set is
// refcounted and the stream is restarted when it actually changes.
//
// This is the SECOND streaming collector, after logs, and for the same reason:
// `monitor-traffic` with an interval is a push channel by design, and polling it
// would mean asking the router to start and stop a measurement every second.
//
// ── AND `=interval=` HERE IS NOT THE `=interval=` ON A PRINT ────────────────
//
// A `monitor` command is ONE OPEN RESULT SET whose sections increment; a
// `print =interval=N` is a REPEATED print, and each repetition completes with
// its own `!done`. That difference decides whether this stream survives.
//
// `internal/routeros.Stream` ends when go-routeros closes the listen channel,
// which it does on `!done` — so if this command sent one per cycle, the chart
// would stop after one second. It does not. Measured on the hAP AC2 under 7.24,
// while investigating something else entirely:
//
//	[   5.248ms] !re | =.section=0
//	[1009.879ms] !re | =.section=1
//	[2017.518ms] !re | =.section=2
//	[3019.330ms] !re | =.section=3
//	[3503.877ms] !done   <- the only one, and only after /cancel
//
// So the cycle marker here is `.section`, not `!done`.
//
// `.section` IS STAMPED ON BOTH SHAPES — confirmed on the AC2 with
// `/interface/print =interval=3`, where nine packets share a section and it
// increments per cycle. So it is the delimiter to build on if these reads are
// ever unified, and it is the only one that works for both: a boundary built on
// `!done` never fires for a monitor, and one built on a repeated key is wrong
// for `/ip/address/print`, where an interface legitimately appears more than
// once per cycle. The live app hit the mirror image of this as its issue
// #119 — a collector that needed a cycle boundary on a PRINT-shaped stream,
// where the `!done` exists and its vendored library discards it.
//
// ── SAMPLES ARE KEPT WHETHER ANYONE IS WATCHING OR NOT ──────────────────────
//
// The ring buffer is filled on every packet, so a browser that connects gets
// history immediately rather than a blank chart that fills over the next minute.
// Only the per-viewer emit is gated on somebody being there.

import (
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
)

// TrafficSample is one reading of one interface.
type TrafficSample struct {
	IfName string `json:"ifName"`
	TS     int64  `json:"ts"`
	// The JSON names are snake_case here and camelCase everywhere else in this
	// app. That is the live contract — the chart reads `rx_mbps` — and a port
	// that tidied it would silently draw nothing.
	RxMbps   float64 `json:"rx_mbps"`
	TxMbps   float64 `json:"tx_mbps"`
	Running  bool    `json:"running"`
	Disabled bool    `json:"disabled"`
}

// TrafficPoint is one history entry. Same field names, minus the interface,
// which the history payload carries once.
type TrafficPoint struct {
	TS     int64   `json:"ts"`
	RxMbps float64 `json:"rx_mbps"`
	TxMbps float64 `json:"tx_mbps"`
}

type TrafficHistory struct {
	IfName string `json:"ifName"`
	// WindowMinutes is how far back the buffer reaches, and it is in this payload
	// because the live one carries it — `socket.emit('traffic:history', {ifName,
	// windowMinutes, points})`.
	//
	// NOTHING READS IT TODAY. The live handler destructures `data.ifName` and
	// `data.points`; the port's `dashboard-traffic.ts` types the payload as those
	// two. It is carried anyway because the payload contract is the line a port
	// may not move, and "no consumer today" is a statement about today.
	//
	// Found missing by the live-socket-diff tool on 2026-08-28, which compares
	// the payload SHAPES both servers actually emit — no static audit could see
	// it, because the field is absent from a struct rather than from a list
	// anything checks.
	WindowMinutes int            `json:"windowMinutes"`
	Points        []TrafficPoint `json:"points"`
}

// WanStatus is the badge every page shows.
type WanStatus struct {
	IfName   string `json:"ifName"`
	TS       int64  `json:"ts"`
	Running  bool   `json:"running"`
	Disabled bool   `json:"disabled"`
}

// parseBps reads RouterOS's bits-per-second, which may carry a unit.
//
// The plain integer is what `monitor-traffic` sends; the suffixed forms turn up
// on other menus and are accepted because the original accepts them. `parseFloat`
// semantics are deliberate: "1.5Mbps" is 1,500,000, not 1.
func parseBps(val string) float64 {
	if val == "" || val == "0" {
		return 0
	}
	lower := strings.ToLower(val)
	mult := 0.0
	switch {
	case strings.HasSuffix(lower, "kbps"):
		mult = 1000
	case strings.HasSuffix(lower, "mbps"):
		mult = 1e6
	case strings.HasSuffix(lower, "gbps"):
		mult = 1e9
	case strings.HasSuffix(lower, "bps"):
		mult = 1
	}
	if mult != 0 {
		return jsParseFloat(val) * mult
	}
	if v := jsParseInt(val); v != nil {
		return float64(*v)
	}
	return 0
}

// jsParseFloat is parseFloat: a LEADING number wins and trailing text is
// ignored, which is what makes "1.5Mbps" readable at all.
func jsParseFloat(s string) float64 {
	t := strings.TrimSpace(s)
	end := 0
	if end < len(t) && (t[end] == '-' || t[end] == '+') {
		end++
	}
	seenDot := false
	for end < len(t) {
		c := t[end]
		if c >= '0' && c <= '9' {
			end++
			continue
		}
		if c == '.' && !seenDot {
			seenDot = true
			end++
			continue
		}
		break
	}
	f, err := strconv.ParseFloat(t[:end], 64)
	if err != nil {
		return 0
	}
	return f
}

// trafficMbps is bps to Mbps at THREE decimals, which is the precision the
// payload has always carried and therefore the precision the chart draws.
func trafficMbps(bps float64) float64 {
	r, _ := strconv.ParseFloat(strconv.FormatFloat(bps/1e6, 'f', 3, 64), 64)
	return r
}

// parseTrafficSample turns one stream packet into a sample.
//
// `running` DEFAULTS TO TRUE and `disabled` to false: the row is a measurement,
// and a missing flag means the router did not mention it, not that the link is
// down. Getting this backwards would blank the WAN badge on every router whose
// monitor rows omit the fields.
func parseTrafficSample(row routeros.Reply, now int64) TrafficSample {
	return TrafficSample{
		IfName:   row["name"],
		TS:       now,
		RxMbps:   trafficMbps(parseBps(row["rx-bits-per-second"])),
		TxMbps:   trafficMbps(parseBps(row["tx-bits-per-second"])),
		Running:  row["running"] != "false",
		Disabled: row["disabled"] == "true",
	}
}

// maxIfNameLength bounds what a browser may ask to watch. The name goes into a
// command sent to the router, so it is validated against the interfaces this
// collector knows about rather than merely escaped.
const maxIfNameLength = 128

// Traffic is the collector.
type Traffic struct {
	ros       Reader
	emit      Emit
	defaultIf string
	maxPoints int

	// cache is where the channel lives. `traffic` used to open a raw stream of
	// its own; it is one holder of a SHARED fill now — see
	// internal/collect/monitortraffic.go.
	cache *roscache.Cache

	mu sync.Mutex
	// watching is a REFCOUNT per interface, not a set: two viewers on one
	// interface must not have the first to leave stop the stream for the second.
	watching  map[string]int
	available map[string]bool
	hist      map[string][]TrafficPoint
	lastWan   *WanStatus
	streamKey string
	stop      func()

	// ── THE WATCHDOG'S STATE ────────────────────────────────────────────────
	//
	// `lastData` is when a row last arrived; `streamStart` is when the stream
	// was opened. The watchdog compares against whichever is later, so a stream
	// that has just been opened is not immediately judged stale for never having
	// produced anything.
	lastData    int64
	streamStart int64
	health      StreamHealth
	wd          *pollLoop
	// wdEvery and wdStaleMs are the live watchdog's 5s tick and 10s staleness.
	// Fields rather than constants so a test can drive them in milliseconds
	// instead of waiting out real time.
	wdEvery   time.Duration
	wdStaleMs int64
	// seenRestarts is the fill's restart count as of the last health tick. The
	// restarting is `roscache`'s now, so a restart is DETECTED here rather than
	// performed, and this is what turns a rising counter into one transition.
	seenRestarts int
}

// UseCache hands this collector the channel it shares with `ifStatus`.
//
// REQUIRED, not optional. `traffic` opened its own raw stream until the B.7
// merge, and keeping that as a fallback would leave two delivery paths alive —
// the exact thing the merge removes — with the fallback being the one every unit
// test exercised. So a `traffic` with no cache holds no channel and says so,
// rather than quietly working a second way.
func (t *Traffic) UseCache(c *roscache.Cache) { t.cache = c }

// TrafficSub is the room suffix one interface's samples are delivered to.
//
// ── ONE DEFINITION, USED BY BOTH SIDES ──────────────────────────────────────
//
// The emitter and the joiner used to build this string independently — here as
// `"traffic-"+sample.IfName` and in `ws.go` as
// `"router-"+routerID+"-traffic-"+ifName`. They agreed, and nothing would have
// noticed if they stopped: a viewer would simply sit in a room nobody sends to
// and see an empty chart, which is precisely the failure mode that shipped for
// a different reason and took a live router to find. Sharing the definition
// makes the mismatch unrepresentable rather than detectable.
func TrafficSub(ifName string) string { return "traffic-" + ifName }

// DefaultIf is the interface every viewer watches until it picks another. It is
// what `traffic.js:bindSocket` subscribes a socket to on connect, and the
// Bandwidth and Dashboard charts show nothing at all without it — see
// `(*conn).attachRouter`.
func (t *Traffic) DefaultIf() string { return t.defaultIf }

func NewTraffic(ros Reader, emit Emit, defaultIf string, historyMinutes int) *Traffic {
	points := historyMinutes * 60
	if points < 60 {
		points = 60
	}
	t := &Traffic{
		ros: ros, emit: emit, defaultIf: defaultIf, maxPoints: points,
		watching: map[string]int{}, available: map[string]bool{},
		hist:    map[string][]TrafficPoint{},
		wdEvery: 5 * time.Second, wdStaleMs: 10_000,
	}
	// BUILT HERE, STARTED BY Start. `pollLoop` is inert until `start()`, so a
	// collector that is constructed and never started holds no timer.
	t.wd = newPollLoop(t.watchdogTick, func() time.Duration { return t.wdEvery })
	return t
}

// SetAvailable records which interfaces exist, for validating what a browser
// asks to watch. Fed from the interface collector rather than read again here.
func (t *Traffic) SetAvailable(names []string) {
	t.mu.Lock()
	t.available = map[string]bool{}
	for _, n := range names {
		t.available[n] = true
	}
	t.mu.Unlock()
}

// NormalizeIfName is the validation a `traffic:select` goes through.
//
// An unknown name is REFUSED rather than passed to the router: this value ends
// up inside a command, and the interface list is the only authority on what is
// real. Before that list exists nothing is accepted, which is the honest answer
// — the alternative is trusting the browser for one poll interval.
func (t *Traffic) NormalizeIfName(name string) (string, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || len(trimmed) > maxIfNameLength {
		return "", false
	}
	if strings.ContainsAny(trimmed, "\r\n\x00") {
		return "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.available) == 0 || !t.available[trimmed] {
		return "", false
	}
	return trimmed, true
}

// Watch adds one viewer to an interface and returns its history.
func (t *Traffic) Watch(ifName string) TrafficHistory {
	t.mu.Lock()
	t.watching[ifName]++
	points := append([]TrafficPoint{}, t.hist[ifName]...)
	t.mu.Unlock()
	t.syncStream()
	// `maxPoints` is `historyMinutes * 60`, so the window is that back again. Held
	// as points rather than minutes because the buffer is bounded by samples; the
	// division is exact for every value the settings validator admits.
	return TrafficHistory{IfName: ifName, WindowMinutes: t.maxPoints / 60, Points: points}
}

// Unwatch removes one viewer. The stream shrinks only when the last one leaves.
func (t *Traffic) Unwatch(ifName string) {
	t.mu.Lock()
	if n := t.watching[ifName]; n <= 1 {
		delete(t.watching, ifName)
	} else {
		t.watching[ifName] = n - 1
	}
	t.mu.Unlock()
	t.syncStream()
}

func (t *Traffic) Start() {
	t.syncStream()
	t.wd.start()
}

func (t *Traffic) Stop() {
	// THE WATCHDOG GOES FIRST. Stopping the stream and leaving the watchdog
	// running would have it find no stream a moment later and start one, which
	// is the opposite of what Stop means — and `Suspend` is Stop, so a
	// suspended collector would resurrect its own stream every five seconds.
	t.wd.stop()
	t.stopStream()
}

// stopStream closes the stream and clears the key WITHOUT touching the
// watchdog, which is what makes it usable from the watchdog's own restart.
//
// The key is cleared as well as the stop func: `syncStream` returns early when
// the interface set is unchanged, so a restart that left the key in place would
// close the stream and decline to reopen it.
func (t *Traffic) stopStream() {
	t.mu.Lock()
	stop := t.stop
	t.stop, t.streamKey = nil, ""
	t.streamStart, t.lastData = 0, 0
	t.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// Reconnected drops the stream and the WAN badge's last value, and KEEPS the
// sample history.
//
// ── THE PREMISE THAT WAS WRONG ──────────────────────────────────────────────
//
// This used to empty `hist` too, on the stated grounds that "a reconnect may be
// a different router". It cannot be. `Reconnected` is called from one place --
// `connectLoop`, inside a Session -- and a Session is built per router ID and
// held in `Manager.live` under that key. A DIFFERENT router is a different
// Session, reached by releasing this one and acquiring that one, which discards
// this history by discarding the whole collector.
//
// So the clear never protected against the thing it named, and it cost the
// operator the chart: a router reconnect is routine (an upgrade, a brief drop,
// and `connectLoop` retries every 5s), and each one restarted the traffic graph
// from nothing. That is what "clears and starts drawing from scratch" was.
//
// ── WHAT IS GIVEN UP, DELIBERATELY ──────────────────────────────────────────
//
// The samples either side of the gap are now both in the ring, so the chart
// draws one straight segment across however long the router was away. That is a
// visible artefact and it is the better trade: it is honest about time, it is
// bounded by the ring's own window, and it costs one misleading segment instead
// of every point the operator was looking at.
//
// `lastWan` is still cleared. It is a STATUS, not a series -- a stale "up" for a
// router that just came back is a claim about right now, and wrong until the
// next read replaces it.
func (t *Traffic) Reconnected() {
	t.Stop()
	t.mu.Lock()
	t.lastWan = nil
	// THE RESTART COUNT DESCRIBED THE OLD CONNECTION. Carrying it across a
	// reconnect would let three restarts spread over three separate outages
	// report a degraded stream that is working perfectly well — the live helper
	// resets here for the same reason.
	t.health.Reset()
	t.mu.Unlock()
	t.syncStream()
	t.wd.start()
}

func (t *Traffic) Suspend() { t.Stop() }

func (t *Traffic) Resume() {
	t.syncStream()
	t.wd.start()
}

// LastWan is the badge's last value, for replay when a page opens.
func (t *Traffic) LastWan() *WanStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastWan
}

// History is one interface's ring, for replay.
func (t *Traffic) History(ifName string) TrafficHistory {
	t.mu.Lock()
	defer t.mu.Unlock()
	return TrafficHistory{IfName: ifName, Points: append([]TrafficPoint{}, t.hist[ifName]...)}
}

// ifaceList is the interfaces the stream must cover: everything being watched,
// plus the default. Sorted, so the key it produces is stable and a restart is
// triggered by a real change rather than by map iteration order.
// ifaceListLocked is ifaceList's body for a caller already holding the lock.
func (t *Traffic) ifaceListLocked() []string {
	names := []string{}
	seen := map[string]bool{}
	if t.defaultIf != "" {
		names, seen[t.defaultIf] = append(names, t.defaultIf), true
	}
	for n := range t.watching {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// syncStream restarts the stream when the interface set has changed.
func (t *Traffic) syncStream() {
	if !t.ros.Connected() || t.cache == nil {
		return
	}

	t.mu.Lock()
	names := t.ifaceListLocked()
	key := strings.Join(names, ",")
	if key == t.streamKey {
		t.mu.Unlock()
		return
	}
	old := t.stop
	t.stop, t.streamKey = nil, ""
	t.mu.Unlock()

	if old != nil {
		old()
	}
	if len(names) == 0 {
		return
	}

	// ── SET B: A STREAM, AND SINCE B.7 A SHARED ONE ────────────────────────
	//
	// See acquisition.go. Same menu `ifStatus` reads its rates from, and a
	// different question of it: that one wants a snapshot of every enabled
	// interface, this one wants EVERY ROW for the interfaces somebody is
	// watching. Neither set contains the other, so the two are merged rather
	// than one subscribing to the other's channel.
	//
	// ONE SECOND, ALWAYS, and it is this holder that asks for it: the chart draws
	// a point a second and is the holder with resolution to lose. The merge takes
	// the finest interval any holder wants.
	stop, err := joinMonitorTraffic(t.cache, names, 1, t.onPacket)
	if err != nil {
		return
	}
	t.mu.Lock()
	t.stop, t.streamKey = stop, key
	t.streamStart = time.Now().UnixMilli()
	t.mu.Unlock()
}

// watchdogTick is what is LEFT of the silent-death watchdog after B.7, and what
// it no longer does is the point.
//
// ── WHY A STREAM NEEDED WATCHING AT ALL ─────────────────────────────────────
//
// `/interface/monitor-traffic` pushes a row a second and nothing acknowledges
// it. A router that stops sending — after an upgrade, a CPU spike, a menu
// briefly disappearing — leaves a connection that is open, a client that
// reports Connected, and a chart that simply stops moving. Nothing errors, so
// nothing retries, and the only cure was for the operator to force a reconnect.
// Reported on issue #126 as a router that "disconnects" and comes back only
// when the device is deleted and added again. This is `_startWatchdog` in
// src/collectors/traffic.js, added in PR #91 for issues #55 and #90.
//
// ── THE RESTARTING MOVED, THE REPORTING DID NOT ────────────────────────────
//
// The channel is `roscache`'s now and so is its recovery: `streamFill.watch`
// reopens a fill that has gone quiet, and it does it for all eight streamed
// menus instead of this one. Keeping a second watchdog here would mean two
// things racing to restart one channel, and the loser would find a stream it
// had not opened.
//
// What could NOT move is the health signal. `stream:health` tints the traffic
// card on the Dashboard and names a restart count — a real feature, and the
// restart count is now somebody else's counter. So this tick DETECTS a restart
// instead of performing one, and feeds the same `StreamHealth` state machine it
// always did.
//
// ── AND ONE THING THE FILL DOES NOT DO: RETRY A FAILED OPEN ────────────────
//
// `JoinStream` returns an error when the channel will not open, and leaves no
// fill behind — so there is nothing for its watchdog to watch. That retry was
// this loop's second job and it stays here, because it is about whether this
// collector HAS a holder, which is a question only this collector can ask.
//
//	not connected   do nothing; connectLoop owns that
//	no holder       try to join. This is how a failed open is retried at all
//	holder          read the fill's counters and report any change in health
func (t *Traffic) watchdogTick() {
	if !t.ros.Connected() || t.cache == nil {
		return
	}

	t.mu.Lock()
	running := t.stop != nil
	wanted := len(t.ifaceListLocked()) > 0
	t.mu.Unlock()

	if !running {
		if wanted {
			t.syncStream()
		}
		return
	}

	st, ok := t.cache.StreamStats(monitorTrafficMenu)
	if !ok {
		// The fill went away under us — another holder released last, or the
		// cache was rebuilt. Rejoining is the same action as a failed open.
		t.stopStream()
		if wanted {
			t.syncStream()
		}
		return
	}

	now := time.Now().UnixMilli()
	t.mu.Lock()
	seen := t.seenRestarts
	t.mu.Unlock()

	if st.Restarts > seen {
		// ONE TRANSITION PER RESTART, however many the fill did between ticks.
		// `RecordRestart` is the same call the old watchdog made after doing the
		// restart itself; all that changed is who did it.
		t.mu.Lock()
		t.seenRestarts = st.Restarts
		degraded, changed := t.health.RecordRestart(now)
		restarts := t.health.Restarts()
		t.mu.Unlock()
		log.Printf("[traffic] the shared monitor-traffic channel restarted (%d total)",
			st.Restarts)
		if changed {
			t.emitHealth(degraded, restarts)
		}
		return
	}

	// A stream that has been up a while is recovering. `SinceOpen` is the fill's
	// own clock, which is what stops a channel opened a moment ago from being
	// read as a long healthy run.
	if st.Open && st.SinceOpen > 0 {
		t.mu.Lock()
		degraded, changed := t.health.RecordHealthy(st.SinceOpen.Milliseconds())
		restarts := t.health.Restarts()
		t.mu.Unlock()
		if changed {
			t.emitHealth(degraded, restarts)
		}
	}
}

// emitHealth sends `stream:health`, which the Dashboard has always listened for
// — `renderStreamHealth` tints the traffic card and names the restart count.
//
// ROUTER-WIDE (an empty room), matching the live `io.emit`: the warning belongs
// to the card rather than to one interface's room, and a viewer watching some
// other interface still needs to know the data is incomplete.
//
// ONLY ON A TRANSITION. `StreamHealth` reports whether the flag changed, so a
// stream that stays degraded does not push a frame to every browser every five
// seconds.
func (t *Traffic) emitHealth(degraded bool, restarts int) {
	EvStreamHealth.Emit(t.emit, "", map[string]any{
		"collector": "traffic",
		"degraded":  degraded,
		"restarts":  restarts,
	})
}

// onPacket is the whole delivery path for one reading.
// FoldTraffic folds one monitor-traffic packet into the per-interface history
// and returns the sample it made and the ring that interface now holds.
//
// ── PHASE 4.1: THE THIRD SEQUENCE DERIVATION, AND THE STATE HAS A KEY ──────
//
//	table     func(prior, []Reply) (payload, prior)   map over the current state
//	sequence  func(prior,   Reply) (payload, prior)   fold one element in
//
// `ping` and `logs` fold into ONE ring. This folds into one ring PER INTERFACE,
// so the carried state is keyed -- but the signature is the same, because the
// fold only ever touches the ring the packet belongs to. Handing it the whole
// map would make the derivation responsible for storage it does not use.
//
// PURE, AND THE COPY IS LOAD-BEARING for the reason `FoldPing` and `FoldLog`
// record: `append` into a slice with spare capacity writes THROUGH to the
// caller's backing array, and a ring that has been trimmed always has spare
// capacity.
//
// A ROW WITH NO NAME YIELDS NOTHING. RouterOS sends one for an empty interface
// list, and folding it in would file a point against an interface called "".
func FoldTraffic(prior []TrafficPoint, maxPoints int, row routeros.Reply, now int64) (TrafficSample, []TrafficPoint, bool) {
	if row["name"] == "" {
		return TrafficSample{}, prior, false
	}
	sample := parseTrafficSample(row, now)

	next := append(append(make([]TrafficPoint, 0, len(prior)+1), prior...),
		TrafficPoint{TS: sample.TS, RxMbps: sample.RxMbps, TxMbps: sample.TxMbps})
	if maxPoints > 0 && len(next) > maxPoints {
		// THE LAST `maxPoints`: a chart that trimmed from the back would freeze
		// on its oldest window and never move, which is the same rule the log
		// ring follows and for the same reason.
		next = next[len(next)-maxPoints:]
	}
	return sample, next, true
}

func (t *Traffic) onPacket(row routeros.Reply) {
	t.mu.Lock()
	sample, next, ok := FoldTraffic(t.hist[row["name"]], t.maxPoints, row, time.Now().UnixMilli())
	if !ok {
		t.mu.Unlock()
		return
	}
	// THE WATCHDOG'S EVIDENCE THAT THE STREAM IS ALIVE. Set for every row, not
	// only the WAN interface's: the stream carries them all, and one interface
	// going quiet is not the stream stalling.
	t.lastData = sample.TS
	// The ring is filled whether anyone is watching or not, so a browser that
	// connects gets history immediately instead of a blank chart.
	t.hist[sample.IfName] = next
	isWan := sample.IfName == t.defaultIf
	if isWan {
		t.lastWan = &WanStatus{IfName: sample.IfName, TS: sample.TS,
			Running: sample.Running, Disabled: sample.Disabled}
	}
	wan := t.lastWan
	t.mu.Unlock()

	// One room per interface, so a viewer receives only the interface they
	// selected — the per-socket subscription list on the Node side, expressed as
	// rooms because that is what this hub already does well.
	EvTrafficUpdate.Emit(t.emit, TrafficSub(sample.IfName), sample)
	if isWan && wan != nil {
		// ROUTER-WIDE: the WAN badge is chrome on every page.
		EvWanStatus.Emit(t.emit, "", *wan)
	}
}

// Seed fills the history ring for one interface from somewhere that already has
// it, so a chart draws immediately instead of growing from one point.
//
// ── PHASE 5.1: WHERE THE POINTS COME FROM, AND WHY NOT THE DATABASE ─────────
//
// A Session is reference counted and built when the first viewer arrives, so its
// ring is EMPTY at that moment and no amount of priming fills it faster than real
// time. The points have to come from something that already holds them.
//
// The history DATABASE looks like that something and is not: `internal/history`
// buckets to one row per interface per MINUTE, carrying the minute's mean, while
// this ring is per-second with `maxPoints = historyMinutes * 60`. A five-minute
// window wants three hundred points and the database can offer five.
//
// The BACKGROUND POOL is the right source. It runs this same collector for every
// router with reporting enabled, streaming continuously on a connection it
// already holds, so its ring is per-second and current -- and a live Session
// displaces that pool, which is exactly when the copy should happen.
//
// ── IT NEVER OVERWRITES ─────────────────────────────────────────────────────
//
// A non-empty ring is live data this collector produced, and it is always better
// than a seed. Seeding is for the blank case only, so a late or duplicated call
// is a no-op rather than a rewind.
//
// `lastData` is deliberately NOT touched. That field is the STREAM WATCHDOG's,
// and telling it a seed was traffic would suppress the restart that recovers a
// stream which died silently -- a different question from whether the chart has
// anything to draw.
func (t *Traffic) Seed(ifName string, points []TrafficPoint) {
	if ifName == "" || len(points) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.hist[ifName]) > 0 {
		return
	}
	if len(points) > t.maxPoints {
		points = points[len(points)-t.maxPoints:]
	}
	t.hist[ifName] = append([]TrafficPoint{}, points...)
}

// SeedableInterfaces is what a seed source holds, so a caller can copy every
// interface without knowing which ones the pool happened to stream.
func (t *Traffic) SeedableInterfaces() map[string][]TrafficPoint {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string][]TrafficPoint, len(t.hist))
	for k, v := range t.hist {
		if len(v) > 0 {
			out[k] = append([]TrafficPoint{}, v...)
		}
	}
	return out
}
