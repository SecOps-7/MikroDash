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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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

	mu sync.Mutex
	// watching is a REFCOUNT per interface, not a set: two viewers on one
	// interface must not have the first to leave stop the stream for the second.
	watching  map[string]int
	available map[string]bool
	hist      map[string][]TrafficPoint
	lastWan   *WanStatus
	streamKey string
	stop      func()
}

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
	return &Traffic{
		ros: ros, emit: emit, defaultIf: defaultIf, maxPoints: points,
		watching: map[string]int{}, available: map[string]bool{},
		hist: map[string][]TrafficPoint{},
	}
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

func (t *Traffic) Start() { t.syncStream() }

func (t *Traffic) Stop() {
	t.mu.Lock()
	stop := t.stop
	t.stop, t.streamKey = nil, ""
	t.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// Reconnected drops the stream and the history. A reconnect may be a different
// router, and a chart that carried the previous one's samples across the gap
// would draw a line nobody's network ever produced.
func (t *Traffic) Reconnected() {
	t.Stop()
	t.mu.Lock()
	t.hist = map[string][]TrafficPoint{}
	t.lastWan = nil
	t.mu.Unlock()
	t.syncStream()
}

func (t *Traffic) Suspend() { t.Stop() }

func (t *Traffic) Resume() { t.syncStream() }

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
func (t *Traffic) ifaceList() []string {
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
	if !t.ros.Connected() {
		return
	}
	s, ok := t.ros.(Streamer)
	if !ok {
		return
	}

	t.mu.Lock()
	names := t.ifaceList()
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

	stop, err := s.Stream(routeros.Cmd{Path: "/interface/monitor-traffic", Args: []string{
		"=interface=" + key, "=interval=1",
		"=.proplist=name,rx-bits-per-second,tx-bits-per-second,running,disabled",
	}}, t.onPacket)
	if err != nil {
		return
	}
	t.mu.Lock()
	t.stop, t.streamKey = stop, key
	t.mu.Unlock()
}

// onPacket is the whole delivery path for one reading.
func (t *Traffic) onPacket(row routeros.Reply) {
	if row["name"] == "" {
		return
	}
	sample := parseTrafficSample(row, time.Now().UnixMilli())

	t.mu.Lock()
	// The ring is filled whether anyone is watching or not, so a browser that
	// connects gets history immediately instead of a blank chart.
	h := append(t.hist[sample.IfName], TrafficPoint{
		TS: sample.TS, RxMbps: sample.RxMbps, TxMbps: sample.TxMbps})
	if len(h) > t.maxPoints {
		h = h[len(h)-t.maxPoints:]
	}
	t.hist[sample.IfName] = h
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
	t.emit(TrafficSub(sample.IfName), "traffic:update", &sample)
	if isWan && wan != nil {
		// ROUTER-WIDE: the WAN badge is chrome on every page.
		t.emit("", "wan:status", wan)
	}
}
