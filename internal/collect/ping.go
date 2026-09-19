package collect

// Ping collector — the port of src/collectors/ping.js.
//
//	/tool/ping   streamed with interval=N, one !re per result
//
// ── LOSS IS A ROLLING WINDOW, NOT A RATIO OF EVERYTHING ─────────────────────
//
// Ten results wide. That is what stops a single timeout jumping the card to
// 100% and a single reply dropping it straight back to 0% — the number a viewer
// watches is "how bad is it right now", and a lifetime average answers a
// different question badly.
//
// ── WHAT COUNTS AS A REPLY IS NARROWER THAN IT LOOKS ────────────────────────
//
// `replied` is "no status, or exactly `replied`". RouterOS also answers
// `echo reply` — the documentation shows it for a multicast ping — and that
// string is NOT `replied`, so it counts as LOST. Reproduced rather than
// widened: the live card has always counted it that way, and a port that
// quietly accepted it would show a different loss figure than the app it
// replaces. Recorded here because no fixture will ever contain it: it needs a
// multicast target.
//
// ── AND A DURATION CAN CARRY TWO UNITS ──────────────────────────────────────
//
// The docs show `max-rtt=1ms438us`. The original's regex takes the FIRST number
// and the FIRST unit, so `1ms438us` parses as 1 and the 438µs is dropped. Also
// reproduced. A tidier parser would report 1.438 and disagree with the live
// card on every sub-millisecond hop.
//
// ── PERMISSION DENIED LATCHES ───────────────────────────────────────────────
//
// `/tool/ping` needs the `test` policy. Without it every retry fails the same
// way, so the refusal is recorded once, emitted so the card can say so, and not
// retried until the router reconnects.

import (
	"fmt"
	"log"
	"math"
	"regexp"
	"strconv"
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

const (
	pingMaxHistory = 60
	pingLossWindow = 10
	pingDefaultTgt = "1.1.1.1"
)

// pingDenied matches the answers that mean "this API user may not run ping",
// as opposed to a transient failure worth retrying.
var pingDenied = regexp.MustCompile(`(?i)not enough privileges|permission denied|cannot run`)

// pingRTTRe is the original's regex, character for character: a number, then an
// OPTIONAL unit. Anything after the first unit is ignored — see the header.
var pingRTTRe = regexp.MustCompile(`([\d.]+)(us|ms)?`)

// ParsePingRTT turns a RouterOS duration into milliseconds.
//
// Returns nil for an absent or unparseable value, which is what the card renders
// as an em dash. Exported for the differential gate.
func ParsePingRTT(val string) *float64 {
	if val == "" {
		return nil
	}
	m := pingRTTRe.FindStringSubmatch(val)
	if m == nil {
		return nil
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil
	}
	if m[2] == "us" {
		// `+(v/1000).toFixed(3)` over there: three decimals, then back to a
		// number so a trailing zero does not reach the payload as a string.
		v = math.Round(v/1000*1000) / 1000
	}
	return &v
}

// PingPoint is one result in the history the card charts.
type PingPoint struct {
	TS   int64    `json:"ts"`
	RTT  *float64 `json:"rtt"`
	Loss *int     `json:"loss"`
	// PermissionDenied rides on the point the refusal produced, so a history
	// replayed to a new viewer still explains itself.
	PermissionDenied bool `json:"permissionDenied,omitempty"`
}

// PingPayload is `ping:update`.
type PingPayload struct {
	Target           string   `json:"target"`
	RTT              *float64 `json:"rtt"`
	Loss             *int     `json:"loss"`
	MinRTT           *float64 `json:"minRtt,omitempty"`
	MaxRTT           *float64 `json:"maxRtt,omitempty"`
	PermissionDenied bool     `json:"permissionDenied,omitempty"`
	TS               int64    `json:"ts"`
	PollMs           int      `json:"pollMs"`
}

// PingHistory is `ping:history`.
type PingHistory struct {
	Target  string      `json:"target"`
	History []PingPoint `json:"history"`
}

type Ping struct {
	// STREAMER, the same one-method interface logs.go defines: a fixture cannot
	// record a stream, so a Reader that does not implement it simply gets no
	// ping — the honest degradation, and the same one the log tail takes.
	ros    Streamer
	emit   Emit
	target string
	pollMs *pollInterval

	// streamMu SERIALISES OPENING AND CLOSING THE STREAM. The watchdog, a
	// re-tune, a retarget and a reconnect each read "is one open" and then
	// opened one; two together left two /tool/ping channels on one target.
	// Held across Stream and a stop, never while holding mu.
	streamMu sync.Mutex

	mu      sync.Mutex
	history []PingPoint
	window  []bool // true = replied
	lastFP  string
	last    *PingPayload
	denied  bool
	stop    func()
	// loop drives the polled path. Nil while streaming, which is every install
	// whose ping interval is five seconds or less. See Start.
	loop *pollLoop

	// ── THE STREAM WATCHDOG ─────────────────────────────────────────────────
	//
	// `/tool/ping` streams one row per interval, and a LOST ping is a row too, so
	// a stream that has gone several intervals without one is dead. On the hAP
	// AX3 the stream ended at 2026-09-13 04:19:40 with no error and no
	// disconnect: `Stream` reports no end, `startStream` refuses while an old
	// handle is set, and nothing else was looking. The Dashboard's Networks card,
	// which ping keeps fresh, went stale minutes after every page load, and a
	// refresh only replayed the hours-old last reading.
	//
	// `streaming` is whether ping SHOULD be streaming — set by Start, cleared by
	// Suspend and Stop — so a tick can never reopen what the session stopped.
	// `lastRow` is when a row last arrived, `streamStart` when an open was last
	// attempted; the watchdog measures from whichever is later.
	streaming   bool
	lastRow     int64
	streamStart int64
	wd          *pollLoop
	// wdEvery and wdStaleMs are fields rather than constants so a test can drive
	// them without waiting out real time. A zero wdStaleMs means "derived from
	// the interval" — see staleMsLocked.
	wdEvery   time.Duration
	wdStaleMs int64
}

func NewPing(ros Streamer, emit Emit, pollMs int, target string) *Ping {
	if target == "" {
		target = pingDefaultTgt
	}
	if pollMs <= 0 {
		pollMs = 5000
	}
	p := &Ping{ros: ros, emit: emit, target: target, pollMs: newPollInterval(pollMs),
		wdEvery: 5 * time.Second}
	// BUILT HERE, STARTED WITH THE STREAM. `pollLoop` is inert until `start()`, so
	// a ping that polls, or is never started, holds no timer.
	p.wd = newPollLoop(p.watchdogTick, func() time.Duration { return p.wdEvery })
	return p
}

// pingIntervalSec is the interval RouterOS is asked for.
//
// CLAMPED TO [1,5]: RouterOS caps /tool/ping's interval at five seconds, and a
// larger one is not rejected — it is silently accepted and ignored, which would
// leave this side believing it had configured something it had not.
func pingIntervalSec(pollMs int) int {
	s := int(math.Round(float64(pollMs) / 1000))
	if s < 1 {
		s = 1
	}
	if s > 5 {
		s = 5
	}
	return s
}

// pingIsResult separates a ping RESULT from the summary RouterOS sends at the
// end of a run.
//
// A summary carries neither a time nor a status — it is sent/received/packet-loss
// and the min/avg/max — and feeding one to ProcessRow would push a LOST result
// into the rolling window, because a row with no status reads as replied and a
// row with no time has no rtt. So the card would show a phantom timeout after
// every burst.
//
// Named rather than left inline in the stream callback so it can be tested: as
// an inline condition, a mutation removing it SURVIVED, since neither corpus
// reaches the callback.
func pingIsResult(row routeros.Reply) bool {
	return row["time"] != "" || row["response-time"] != "" || row["status"] != ""
}

// PingFold is the carried state of a ping series: the loss window and the
// history ring.
//
// ── PHASE 4.1: A SEQUENCE DERIVATION IS A FOLD, NOT A MAP ──────────────────
//
// The five set-A derivations are `func(prior, ROWS) (payload, prior)` -- they map
// over a whole table, because a table IS the current state. `ping`, `traffic` and
// `logs` are not tables: their rows are a SEQUENCE, each element of which matters
// once, and the plan left them "blocked behind set B" as though they needed a
// different layer.
//
// They do not. They need the same signature with a different arity:
//
//	table     func(prior, []Reply) (payload, prior)   map over the current state
//	sequence  func(prior,   Reply) (payload, prior)   fold one element in
//
// That is the whole difference, and `ProcessRow` was already the fold -- it just
// carried its state on a receiver, which is what made it untestable without a
// collector and what hid the fact that the shape was already right.
type PingFold struct {
	// Window is the rolling replied/lost record the loss percentage is computed
	// from. Bounded at pingLossWindow.
	Window []bool
	// History is the series the chart draws, bounded at pingMaxHistory.
	History []PingPoint
	// LastFP suppresses an unchanged result. Carried because the decision to
	// emit belongs to the series, not to one reading.
	LastFP string
}

// FoldPing folds one /tool/ping reply into the series and returns the payload it
// produced, the next state, and whether anything should be emitted.
//
// PURE: no receiver, no lock, no clock. `now` arrives as an argument for the
// reason every other builder here takes one -- a derivation that reads the wall
// clock cannot be replayed against a fixture, and the fingerprint that suppresses
// redundant emits would never match twice.
func FoldPing(prior PingFold, target string, pollMs int, row routeros.Reply, now int64) (*PingPayload, PingFold, bool) {
	status := row["status"]
	replied := status == "" || status == "replied"
	var rtt *float64
	if replied {
		t := row["time"]
		if t == "" {
			t = row["response-time"]
		}
		rtt = ParsePingRTT(t)
	}
	minRTT := ParsePingRTT(row["min-rtt"])
	maxRTT := ParsePingRTT(row["max-rtt"])

	next := PingFold{
		Window:  append(append([]bool(nil), prior.Window...), replied),
		History: append([]PingPoint(nil), prior.History...),
		LastFP:  prior.LastFP,
	}
	if len(next.Window) > pingLossWindow {
		next.Window = next.Window[1:]
	}
	lost := 0
	for _, ok := range next.Window {
		if !ok {
			lost++
		}
	}
	// The original's `length > 0 ? ... : 100` — unreachable, since a value was
	// just pushed, and reproduced so the two read the same.
	loss := 100
	if len(next.Window) > 0 {
		loss = int(math.Round(float64(lost) / float64(len(next.Window)) * 100))
	}

	next.History = append(next.History, PingPoint{TS: now, RTT: rtt, Loss: &loss})
	if len(next.History) > pingMaxHistory {
		next.History = next.History[1:]
	}
	payload := &PingPayload{
		Target: target, RTT: rtt, Loss: &loss,
		MinRTT: minRTT, MaxRTT: maxRTT, TS: now, PollMs: pollMs,
	}
	// FINGERPRINTED on target, rtt and loss — not on min/max, which drift on
	// their own and would make every result an update.
	fp := fmt.Sprintf("%s|%s|%d", target, fmtPingRTT(rtt), loss)
	changed := fp != prior.LastFP
	next.LastFP = fp
	return payload, next, changed
}

// ProcessRow folds one /tool/ping reply into the history and returns the
// payload it produced, or nil when nothing should be emitted.
//
// The collector's half: take the lock, hand the carried state to the fold, put
// what comes back. Everything that can be got wrong lives in FoldPing.
func (p *Ping) ProcessRow(row routeros.Reply, now int64) *PingPayload {
	p.mu.Lock()
	payload, next, changed := FoldPing(
		PingFold{Window: p.window, History: p.history, LastFP: p.lastFP},
		p.target, p.pollMs.ms(), row, now)
	p.window, p.history, p.lastFP = next.Window, next.History, next.LastFP
	p.last = payload
	p.mu.Unlock()

	if !changed {
		return nil
	}
	return payload
}

// fmtPingRTT renders a nullable RTT the way the original's template literal
// does, so the fingerprints agree: `null` for absent.
func fmtPingRTT(v *float64) string {
	if v == nil {
		return "null"
	}
	return strconv.FormatFloat(*v, 'g', -1, 64)
}

// Last is the payload a newly-focused viewer is replayed.
func (p *Ping) Last() *PingPayload {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

// History is `ping:history`, sent when the Dashboard opens.
func (p *Ping) History() PingHistory {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PingPoint, len(p.history))
	copy(out, p.history)
	return PingHistory{Target: p.target, History: out}
}

// noteDenied records the refusal and produces the payload that tells the card.
func (p *Ping) noteDenied(now int64) *PingPayload {
	p.mu.Lock()
	p.denied = true
	p.history = append(p.history, PingPoint{TS: now, PermissionDenied: true})
	if len(p.history) > pingMaxHistory {
		p.history = p.history[1:]
	}
	payload := &PingPayload{
		Target: p.target, PermissionDenied: true, TS: now, PollMs: p.pollMs.ms(),
	}
	p.last = payload
	p.mu.Unlock()
	return payload
}

func (p *Ping) Denied() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.denied
}

func (p *Ping) Start() {
	// ── B.5: THE INTERVAL DECIDES, BECAUSE THE STREAM CANNOT EXPRESS IT ─────
	//
	// RouterOS caps `/tool/ping`'s interval at five seconds. A larger one is not
	// rejected -- it is silently accepted and ignored -- so an operator asking
	// for a ping every thirty seconds got one every five, six times as many as
	// they asked for, with nothing anywhere saying so.
	//
	// `pingIntervalSec` has clamped to [1,5] since the port and its comment says
	// exactly this. What was missing is the other half: a path that CAN honour
	// the setting.
	//
	// ── AND IT IS NOT A VIOLATION OF THE OPERATOR'S RULE ────────────────────
	//
	// "Mode switches delivery only and never touches intervals" cuts one way:
	// choosing Poll must not silently mean slower. This is the converse -- an
	// INTERVAL the chosen delivery cannot carry -- and there the interval wins,
	// because it is the thing the operator set explicitly while the mode is a
	// default they may never have seen.
	//
	// So: five seconds or less streams, which is every default and what every
	// install does today. Above that polls, and the setting is honoured for the
	// first time.
	if p.pollsRatherThanStreams() {
		p.startPolling()
		return
	}
	p.mu.Lock()
	p.streaming = true
	p.mu.Unlock()
	p.startStream()
	p.wd.start()
}

// pollsRatherThanStreams reports whether the configured interval is one only a
// poll can deliver.
func (p *Ping) pollsRatherThanStreams() bool {
	return p.pollMs.ms() > pingMaxStreamMs
}

// pingMaxStreamMs is the longest interval `/tool/ping` will actually honour.
// See pingIntervalSec, whose clamp this is the other side of.
const pingMaxStreamMs = 5000

// startPolling issues one bounded ping per interval.
//
// ── `=count=1`, WHICH MAKES IT A MEASUREMENT AND NOT A CHANNEL ─────────────
//
// The same shape `topology` uses to ping a discovered device, and the same
// reason `acquisition.KindOf` reads the bound before the interval: a count is
// what makes this a reading that ends. It costs one command per interval and
// holds nothing open.
//
// ONE RESULT PER RUN, so the loss statistics count the same way they do on the
// streamed path -- every row is a distinct measurement, which is why `ping` can
// never back a rolling cache entry either.
func (p *Ping) startPolling() {
	p.mu.Lock()
	if p.loop != nil || p.denied {
		p.mu.Unlock()
		return
	}
	loop := newPollLoop(p.pollOnce, p.pollMs.duration)
	p.loop = loop
	p.mu.Unlock()
	loop.start()
}

// pollOnce takes one reading.
func (p *Ping) pollOnce() {
	if c, ok := p.ros.(interface{ Connected() bool }); ok && !c.Connected() {
		return
	}
	// ASSERTED, NOT REQUIRED, exactly as `Connected()` is above and for the same
	// reason: `ros` is a Streamer so a fixture can drive this collector, and a
	// reader that cannot issue a command simply takes no reading rather than
	// making the whole collector unconstructable.
	doer, ok := p.ros.(interface {
		Do(routeros.Cmd) ([]routeros.Reply, error)
	})
	if !ok {
		return
	}
	rows, err := doer.Do(routeros.Cmd{Path: "/tool/ping", Args: []string{
		"=address=" + p.currentTarget(),
		"=count=1",
		"=.proplist=time,response-time,status,min-rtt,max-rtt",
	}})
	if err != nil {
		if pingDenied.MatchString(err.Error()) {
			log.Printf("[ping] test policy not granted — ping disabled. Add \"test\" to this API user's group to enable it.")
			EvPingUpdate.Emit(p.emit, pingRooms.Join(), *p.noteDenied(time.Now().UnixMilli()))
		}
		return
	}
	// THE LAST ROW CARRIES THE RESULT. `/tool/ping` ends a run with a summary
	// sentence that has neither a time nor a status, and `pingIsResult` is what
	// separates them -- feeding a summary to ProcessRow would push a LOST result
	// into the history on every successful ping.
	for _, row := range rows {
		if !pingIsResult(row) {
			continue
		}
		if payload := p.ProcessRow(row, time.Now().UnixMilli()); payload != nil {
			EvPingUpdate.Emit(p.emit, pingRooms.Join(), *payload)
		}
	}
}

func (p *Ping) startStream() {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	p.openStreamLocked()
}

// openStreamLocked is startStream's work; the caller holds streamMu.
func (p *Ping) openStreamLocked() {
	p.mu.Lock()
	if p.stop != nil || p.denied {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	// Connectivity is asked of the client only if it can answer. The replay
	// harness implements neither Streamer nor this, and a collector that
	// insisted on both could not be driven by a fixture at all.
	if c, ok := p.ros.(interface{ Connected() bool }); ok && !c.Connected() {
		return
	}

	// ── SET B: A STREAM ─────────────────────────────────────────────────────
	// See acquisition.go. Parameterised by ADDRESS, so even the collector below
	// that pings the same menu is not asking this question -- it asks about a
	// different host. The menu alone was never the key.
	sec := pingIntervalSec(p.pollMs.ms())
	cmd := routeros.Cmd{Path: "/tool/ping", Args: []string{
		"=address=" + p.currentTarget(),
		"=interval=" + strconv.Itoa(sec),
		"=.proplist=time,response-time,status,min-rtt,max-rtt",
	}}
	// Stamped per ATTEMPT, success or not: the watchdog measures a failed open's
	// retry from here too, so a persistent error is retried once a window rather
	// than on every tick.
	p.mu.Lock()
	p.streamStart = time.Now().UnixMilli()
	p.mu.Unlock()
	stop, err := p.ros.Stream(cmd, func(row routeros.Reply) {
		// ANY row is a sign of life, the summary sentence included.
		p.noteRow()
		if !pingIsResult(row) {
			return
		}
		if payload := p.ProcessRow(row, time.Now().UnixMilli()); payload != nil {
			EvPingUpdate.Emit(p.emit, pingRooms.Join(), *payload)
		}
	})
	if err != nil {
		if pingDenied.MatchString(err.Error()) {
			log.Printf("[ping] test policy not granted — ping disabled. Add \"test\" to this API user's group to enable it.")
			EvPingUpdate.Emit(p.emit, pingRooms.Join(), *p.noteDenied(time.Now().UnixMilli()))
			return
		}
		log.Printf("[ping] stream error (target=%s): %v", p.currentTarget(), err)
		return
	}
	p.mu.Lock()
	p.stop = stop
	p.mu.Unlock()
}

func (p *Ping) stopStream() {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	p.closeStreamLocked()
}

// closeStreamLocked is stopStream's work; the caller holds streamMu.
func (p *Ping) closeStreamLocked() {
	p.mu.Lock()
	stop := p.stop
	p.stop = nil
	p.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// Suspend stops BOTH halves, and until 2026-09-10 it stopped one.
//
// ── THE ASYMMETRY, AND WHY IT WAS DORMANT ──────────────────────────────────
//
// `Start` chooses between a stream and a poll loop -- B.5, because RouterOS
// silently ignores a `/tool/ping` interval above five seconds, so a slower
// cadence can only be delivered by polling. `Suspend` stopped the stream only,
// and `Resume` started the stream only. So on an install whose ping interval is
// above five seconds, a suspend stopped nothing and a resume would have started
// the very stream the interval says cannot carry it.
//
// It was harmless because NOTHING CALLED EITHER: `ping` was not in the dormancy
// target table, so no gate could reach it. Adding it there is what turns a
// dormant asymmetry into a live bug, which is why this is fixed in the same
// commit and not after it.
func (p *Ping) Suspend() {
	// THE WATCHDOG GOES FIRST, as traffic's does: stopping the stream and
	// leaving the watchdog running would have it reopen what was just closed.
	p.stopWatchdog()
	p.stopStream()
	p.stopPolling()
}

// Resume mirrors Start, rather than reimplementing half of it.
func (p *Ping) Resume() {
	if p.Denied() {
		return
	}
	p.Start()
}

func (p *Ping) Stop() {
	p.stopWatchdog()
	p.stopStream()
	p.stopPolling()
}

func (p *Ping) stopWatchdog() {
	p.mu.Lock()
	p.streaming = false
	p.mu.Unlock()
	p.wd.stop()
}

func (p *Ping) noteRow() {
	p.mu.Lock()
	p.lastRow = time.Now().UnixMilli()
	p.mu.Unlock()
}

// staleMsLocked is how long the stream may go without a row before it is
// judged dead: three intervals, plus five seconds for a slow reply. Twenty
// seconds at the default five-second interval. The caller holds p.mu.
func (p *Ping) staleMsLocked() int64 {
	if p.wdStaleMs > 0 {
		return p.wdStaleMs
	}
	return int64(pingIntervalSec(p.pollMs.ms()))*3000 + 5000
}

// watchdogTick reopens a ping stream that has stopped producing rows, and
// retries one that failed to open.
func (p *Ping) watchdogTick() {
	if c, ok := p.ros.(interface{ Connected() bool }); ok && !c.Connected() {
		return
	}
	// JUDGED UNDER streamMu, so a stream a re-tune has just opened is seen as
	// fresh rather than reopened a second time.
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	now := time.Now().UnixMilli()
	p.mu.Lock()
	streaming, running, denied := p.streaming, p.stop != nil, p.denied
	last := max(p.lastRow, p.streamStart)
	stale := p.staleMsLocked()
	p.mu.Unlock()
	if !streaming || denied || now-last < stale {
		return
	}
	if running {
		log.Printf("[ping] no reading from the stream to %s for %ds; reopening it",
			p.currentTarget(), (now-last)/1000)
		p.closeStreamLocked()
	}
	p.openStreamLocked()
}

func (p *Ping) stopPolling() {
	p.mu.Lock()
	loop := p.loop
	p.loop = nil
	p.mu.Unlock()
	if loop != nil {
		loop.stop()
	}
}

// Reconnected clears the latch: a reconnect may be to a router whose API user
// DOES have the test policy, and a permanent refusal earned on the last one
// would keep the card dark for ever.
func (p *Ping) Reconnected() {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	p.mu.Lock()
	p.denied = false
	p.lastFP = ""
	p.stop = nil
	streams := !p.pollsRatherThanStreams()
	if streams {
		p.streaming = true
	}
	p.mu.Unlock()
	p.openStreamLocked()
	if streams {
		p.wd.start()
	}
}

// SetPollMs applies a new poll period to a running collector.
//
// ── PING HAS NO POLL LOOP, SO THIS IS NOT `retime` ──────────────────────────
//
// The interval is not a timer here: it is sent to the ROUTER, as
// `/tool/ping ... =interval=N`, and the router emits one `!re` per result. So
// changing the period means restarting the stream with a new argument, which is
// exactly what the live route does — `s.ping.pollMs = ...; s.ping._restartStream()`
// — rather than the `_restartTimer` its poll-mode siblings get.
//
// A stopped stream stays stopped: `stopStream` is a no-op when there is none,
// and `startStream` returns early if the client is not connected or the menu was
// denied. Both matter, because a settings save re-tunes every collector
// including ones nobody is watching.
// SetTarget points ping at a different host while it runs.
//
// ── WHY A RUNNING PING NEEDS THIS ────────────────────────────────────────────
//
// The router form edits `pingTarget`, and a Dashboard session is held for as
// long as its router has alerting or recording on — the hAP AX3's, for good —
// so a target read only when the session was built would never change on it.
//
// THE HISTORY GOES WITH THE OLD HOST. Its readings are not the new host's, and
// keeping them would chart one host's latency and loss under another's name.
// An empty target means the default, as NewPing's does.
func (p *Ping) SetTarget(target string) {
	if target == "" {
		target = pingDefaultTgt
	}
	p.mu.Lock()
	if target == p.target {
		p.mu.Unlock()
		return
	}
	p.target = target
	p.history, p.window, p.lastFP, p.last = nil, nil, "", nil
	p.mu.Unlock()
	p.restartRunning() // a polling ping reads the new target on its next tick
}

// restartRunning reopens the stream if one is open, deciding and acting under
// streamMu so a stream opened meanwhile is not doubled.
func (p *Ping) restartRunning() {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	p.mu.Lock()
	running := p.stop != nil
	p.mu.Unlock()
	if running {
		p.closeStreamLocked()
		p.openStreamLocked()
	}
}

// currentTarget is the host being pinged, read under the lock: SetTarget can
// change it while a stream or a poll is being set up.
func (p *Ping) currentTarget() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.target
}

func (p *Ping) SetPollMs(ms int) {
	p.pollMs.set(ms)
	p.restartRunning()
}

// Seed fills the RTT history from somewhere that already has it. See
// `Traffic.Seed` for why the background pool is the source and the history
// database is not.
//
// NEVER OVERWRITES: a non-empty history is this collector's own, and better.
func (p *Ping) Seed(points []PingPoint) {
	if len(points) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.history) > 0 {
		return
	}
	if len(points) > pingMaxHistory {
		points = points[len(points)-pingMaxHistory:]
	}
	p.history = append([]PingPoint{}, points...)
}
