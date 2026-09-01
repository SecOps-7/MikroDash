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

	mu      sync.Mutex
	history []PingPoint
	window  []bool // true = replied
	lastFP  string
	last    *PingPayload
	denied  bool
	stop    func()
}

func NewPing(ros Streamer, emit Emit, pollMs int, target string) *Ping {
	if target == "" {
		target = pingDefaultTgt
	}
	if pollMs <= 0 {
		pollMs = 5000
	}
	return &Ping{ros: ros, emit: emit, target: target, pollMs: newPollInterval(pollMs)}
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

// ProcessRow folds one /tool/ping reply into the history and returns the
// payload it produced, or nil when nothing should be emitted.
//
// Exported and pure-ish for the gate: everything above it is plumbing, and this
// is where the port can disagree with the original.
func (p *Ping) ProcessRow(row routeros.Reply, now int64) *PingPayload {
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

	p.mu.Lock()
	p.window = append(p.window, replied)
	if len(p.window) > pingLossWindow {
		p.window = p.window[1:]
	}
	lost := 0
	for _, ok := range p.window {
		if !ok {
			lost++
		}
	}
	// The original's `length > 0 ? ... : 100` — unreachable, since a value was
	// just pushed, and reproduced so the two read the same.
	loss := 100
	if len(p.window) > 0 {
		loss = int(math.Round(float64(lost) / float64(len(p.window)) * 100))
	}

	p.history = append(p.history, PingPoint{TS: now, RTT: rtt, Loss: &loss})
	if len(p.history) > pingMaxHistory {
		p.history = p.history[1:]
	}
	payload := &PingPayload{
		Target: p.target, RTT: rtt, Loss: &loss,
		MinRTT: minRTT, MaxRTT: maxRTT, TS: now, PollMs: p.pollMs.ms(),
	}
	p.last = payload
	// FINGERPRINTED on target, rtt and loss — not on min/max, which drift on
	// their own and would make every result an update.
	fp := fmt.Sprintf("%s|%s|%d", p.target, fmtPingRTT(rtt), loss)
	changed := fp != p.lastFP
	p.lastFP = fp
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
	p.startStream()
}

func (p *Ping) startStream() {
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

	sec := pingIntervalSec(p.pollMs.ms())
	cmd := routeros.Cmd{Path: "/tool/ping", Args: []string{
		"=address=" + p.target,
		"=interval=" + strconv.Itoa(sec),
		"=.proplist=time,response-time,status,min-rtt,max-rtt",
	}}
	stop, err := p.ros.Stream(cmd, func(row routeros.Reply) {
		if !pingIsResult(row) {
			return
		}
		if payload := p.ProcessRow(row, time.Now().UnixMilli()); payload != nil {
			p.emit("page-home", "ping:update", payload)
		}
	})
	if err != nil {
		if pingDenied.MatchString(err.Error()) {
			log.Printf("[ping] test policy not granted — ping disabled. Add \"test\" to this API user's group to enable it.")
			p.emit("page-home", "ping:update", p.noteDenied(time.Now().UnixMilli()))
			return
		}
		log.Printf("[ping] stream error (target=%s): %v", p.target, err)
		return
	}
	p.mu.Lock()
	p.stop = stop
	p.mu.Unlock()
}

func (p *Ping) stopStream() {
	p.mu.Lock()
	stop := p.stop
	p.stop = nil
	p.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (p *Ping) Suspend() { p.stopStream() }

func (p *Ping) Resume() {
	if p.Denied() {
		return
	}
	p.startStream()
}

func (p *Ping) Stop() { p.stopStream() }

// Reconnected clears the latch: a reconnect may be to a router whose API user
// DOES have the test policy, and a permanent refusal earned on the last one
// would keep the card dark for ever.
func (p *Ping) Reconnected() {
	p.mu.Lock()
	p.denied = false
	p.lastFP = ""
	p.stop = nil
	p.mu.Unlock()
	p.startStream()
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
func (p *Ping) SetPollMs(ms int) {
	p.pollMs.set(ms)
	p.mu.Lock()
	running := p.stop != nil
	p.mu.Unlock()
	if !running {
		return
	}
	p.stopStream()
	p.startStream()
}
