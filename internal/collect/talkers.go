package collect

// Top Talkers — per-device throughput, from Kid Control.
//
// ── IT IS NOT A TRAFFIC MENU ────────────────────────────────────────────────
//
// The source is `/ip/kid-control/device`, which RouterOS ships as a parental
// control feature and which happens to maintain a per-MAC rate counter. That is
// the only place RouterOS offers per-device throughput without running Torch, so
// the live app uses it and so does this. Two consequences follow and both are
// visible in the payload: a router with no kid-control menu reports
// `available: false` rather than an empty list, and the names are whatever the
// operator typed into Kid Control rather than DHCP hostnames.
//
// ── THE ROWS ARE KEYED BY MAC, AND THAT IS A DEDUPLICATION ──────────────────
//
// The live collector stores rows in a Map keyed by `mac-address`, so a menu
// listing the same MAC twice keeps the LAST row and reports one device. A row
// with no MAC is dropped entirely — there is nothing to key it by, and a device
// that cannot be identified cannot be shown as a talker.
//
// ── UNAVAILABILITY LATCHES, SUCCESS DOES NOT UNLATCH IT ─────────────────────
//
// "unknown command" or "no such item" means this router has no kid-control menu,
// which is a property of the RouterOS build rather than a transient failure. The
// live collector sets a latch and stops asking; a later successful tick clears
// the retry backoff but NOT the latch, and its comment says why — clearing it
// there "un-latched the feature probe almost immediately". Any other error is
// transient and simply leaves the previous payload standing.

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/routeros"
)

// TalkerDevice is one row of the card.
type TalkerDevice struct {
	Name string `json:"name"`
	MAC  string `json:"mac"`
	// Megabits per second, rounded to three decimals — `+(n/1e6).toFixed(3)` in
	// the original, which rounds and then returns a NUMBER, so 7248568 becomes
	// 7.249 and not "7.249".
	TxMbps float64 `json:"tx_mbps"`
	RxMbps float64 `json:"rx_mbps"`
}

// TalkersPayload is what the Dashboard's Top Talkers card renders.
type TalkersPayload struct {
	TS      int64          `json:"ts"`
	Devices []TalkerDevice `json:"devices"`
	// PollMs is 0 in stream mode and the poll interval otherwise — the original's
	// `get _reportedPollMs() { return this.streamMode ? 0 : this.pollMs.ms(); }`. The
	// browser's stale detection reads it, and 0 means "streamed, judge me by the
	// heartbeat instead".
	PollMs int `json:"pollMs"`
	// Available is false only for a router with no kid-control menu. An empty
	// list with `available: true` is a router where nobody is using bandwidth.
	Available bool `json:"available"`
}

var talkersCmd = routeros.Cmd{
	Path: "/ip/kid-control/device/print",
	Args: []string{"=.proplist=name,mac-address,rate-up,rate-down"},
}

// Talkers is the collector.
type Talkers struct {
	ros    Reader
	emit   Emit
	pollMs *pollInterval
	topN   int
	// streamMode only decides what PollMs reports. This port polls; the live
	// collector can also subscribe, and the two agree on everything else.
	streamMode bool

	// unavailable latches. See the header.
	unavailable bool
	lastFp      string
	last        *TalkersPayload
	loop        *pollLoop
	now         func() time.Time
}

// NewTalkers builds the collector. `topN` of 0 takes the original's default of
// five, which is `topN || 5` there.
func NewTalkers(ros Reader, emit Emit, pollMs, topN int) *Talkers {
	if topN <= 0 {
		topN = 5
	}
	t := &Talkers{
		ros:    ros,
		emit:   emit,
		pollMs: newPollInterval(clampPoll(pollMs, 3000, 1000, 300000)),
		topN:   topN,
		now:    time.Now,
	}
	t.loop = newPollLoop(func() { t.Tick() }, func() time.Duration {
		return t.pollMs.duration()
	})
	return t
}

// Start reads once and then polls, matching Netwatch.Start. The immediate tick
// is what stops the card sitting empty for a whole interval after a connect.
func (t *Talkers) Start() { t.Tick(); t.loop.start() }

func (t *Talkers) Suspend() { t.loop.stop() }

func (t *Talkers) Resume() {
	if t.ros.Connected() {
		t.loop.start()
	}
}

func (t *Talkers) Stop() { t.loop.stop() }

// Reconnected CLEARS the latch, and that is the opposite of what an earlier
// version of this comment claimed.
//
// The live collector separates two wake-ups: `resume()` is an idle or page-gate
// wake-up and deliberately respects the latch — "resume() is an idle wake-up,
// not a feature re-probe" — while `probe()` is the deliberate re-probe that
// clears it, called by the dormancy supervisor on backoff expiry and page focus.
//
// This port has no dormancy supervisor, and `Reconnected` is its re-probe. The
// convention is set by every other collector here and stated in session.go: "a
// reconnect must drop every 'this menu is absent' latch, because the usual
// reason a connection dropped is an upgrade, and the router that came back may
// not be the same build." A router that gained kid-control in that upgrade would
// otherwise stay latched off until the process restarted.
//
// The fingerprint is cleared too, so the first payload after a reconnect is
// always sent — a browser that reconnected has nothing on screen to compare it
// against.
func (t *Talkers) Reconnected() {
	t.loop.stop()
	t.unavailable = false
	t.lastFp = ""
	t.Tick()
	t.loop.start()
}

func (t *Talkers) Last() *TalkersPayload { return t.last }

func (t *Talkers) reportedPollMs() int {
	if t.streamMode {
		return 0
	}
	return t.pollMs.ms()
}

// mbps is `+(n / 1_000_000).toFixed(3)`.
//
// `toFixed` rounds half away from zero and Go's `math.Round` does the same, so
// the two agree — including on the halfway cases a naive `float64` truncation
// would get wrong in the other direction.
func mbps(bits int) float64 {
	return math.Round(float64(bits)/1_000_000*1000) / 1000
}

// intOf is `parseInt(v || '0', 10)`.
//
// A missing or empty value is zero. A value with trailing rubbish takes the
// LEADING digits, because that is what parseInt does — "12abc" is 12, and a
// value that starts with no digit at all is NaN there, which this reports as
// zero. That last one is the only divergence and it is unreachable: RouterOS
// answers these two keys with a decimal integer or not at all.
func intOf(v string) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	end := 0
	if end < len(v) && (v[end] == '-' || v[end] == '+') {
		end++
	}
	for end < len(v) && v[end] >= '0' && v[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(v[:end])
	if err != nil {
		return 0
	}
	return n
}

// Tick reads the menu once and emits if the reading changed.
func (t *Talkers) Tick() {
	if !t.ros.Connected() || t.unavailable {
		return
	}
	rows, err := t.ros.Do(talkersCmd)
	if err != nil {
		m := strings.ToLower(err.Error())
		if strings.Contains(m, "unknown command") || strings.Contains(m, "no such") {
			t.markUnavailable()
			return
		}
		// Transient: leave the previous payload standing rather than replacing it
		// with an empty one, which would read as "nobody is using bandwidth".
		return
	}
	t.commit(rows)
}

func (t *Talkers) markUnavailable() {
	if t.unavailable {
		return
	}
	t.unavailable = true
	t.loop.stop()
	p := &TalkersPayload{
		TS: t.now().UnixMilli(), Devices: []TalkerDevice{},
		PollMs: t.reportedPollMs(), Available: false,
	}
	t.last = p
	t.emit(talkersRoom, "talkers:update", p)
}

const talkersRoom = "page-dashboard"

// commit turns rows into the payload. Split from Tick so the differential gate
// can drive it from a fixture without a router.
func (t *Talkers) commit(rows []routeros.Reply) {
	// ORDER-PRESERVING DEDUPLICATION BY MAC, matching the Map: a repeated MAC
	// keeps the LAST row's values but the FIRST row's position, because a JS Map
	// overwrites in place rather than moving the key to the end. That matters
	// only for ties in the sort below, where insertion order decides.
	order := make([]string, 0, len(rows))
	byMAC := make(map[string]TalkerDevice, len(rows))
	for _, r := range rows {
		mac := r["mac-address"]
		if mac == "" {
			continue // nothing to key it by
		}
		if _, seen := byMAC[mac]; !seen {
			order = append(order, mac)
		}
		byMAC[mac] = TalkerDevice{
			Name:   r["name"],
			MAC:    mac,
			TxMbps: mbps(intOf(r["rate-up"])),
			RxMbps: mbps(intOf(r["rate-down"])),
		}
	}

	devices := make([]TalkerDevice, 0, len(order))
	for _, mac := range order {
		devices = append(devices, byMAC[mac])
	}

	// Descending by the SUM of both directions, and STABLE: `Array.prototype.sort`
	// has been stable since ES2019, so two devices with equal totals keep their
	// insertion order. `sort.SliceStable` is the same guarantee.
	sort.SliceStable(devices, func(i, j int) bool {
		return devices[i].TxMbps+devices[i].RxMbps > devices[j].TxMbps+devices[j].RxMbps
	})
	if len(devices) > t.topN {
		devices = devices[:t.topN]
	}

	p := &TalkersPayload{
		TS: t.now().UnixMilli(), Devices: devices,
		PollMs: t.reportedPollMs(), Available: true,
	}
	t.last = p

	// The fingerprint covers MAC and both rates but NOT the name, exactly as the
	// original's does — a device renamed in Kid Control does not by itself
	// justify a repaint, and the next real change carries the new name with it.
	fp := talkersFingerprint(devices)
	if fp == t.lastFp {
		return
	}
	t.lastFp = fp
	t.emit(talkersRoom, "talkers:update", p)
}

func talkersFingerprint(devices []TalkerDevice) string {
	type fpRow struct {
		MAC string  `json:"mac"`
		Tx  float64 `json:"tx"`
		Rx  float64 `json:"rx"`
	}
	out := make([]fpRow, 0, len(devices))
	for _, d := range devices {
		out = append(out, fpRow{MAC: d.MAC, Tx: d.TxMbps, Rx: d.RxMbps})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (t *Talkers) SetPollMs(ms int) {
	t.pollMs.set(ms)
	t.loop.retime()
}
