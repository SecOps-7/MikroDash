package collect

// Wifi collector — whichever wireless stack this router has.
//
// A router runs EITHER `/interface/wifi` (modern) or `/interface/wireless`
// (legacy), never both, and which one is not knowable without asking. The stack
// is probed once and latched, and RESET on reconnect, because a package can be
// installed and the router rebooted underneath us.
//
// ── WHAT IS ABSENT FROM EVERY PROPLIST IS THE SECURITY PROPERTY ─────────────
//
// `security.passphrase`, `wpa-pre-shared-key` and `wpa2-pre-shared-key` appear
// in none of them. Adding a field here puts it in front of every browser
// holding read on this page. The profile menus are shared with the CAPsMAN
// collector for the same reason: one definition of the safe proplist, so the two
// callers cannot drift apart.
//
// The view builders and their helpers live in wifiview.go — pure, and gated by
// a generator rather than a fixture, because no router here runs the legacy
// stack. See that file's header.

import (
	"encoding/json"
	"regexp"
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

// The modern stack's reads. The proplists are copied from src/routeros/wifiMenus.js
// rather than re-derived, so the shared ones cannot drift.
var (
	// `master` IS DELIBERATELY NOT IN THIS PROPLIST, and the round trip is worth
	// recording. It was added on 2026-08-29 because the Frequency Analyser's
	// catalogue was built here and `wifiscan.ParseCatalogue` reads `master` —
	// without it every radio parsed as not-a-radio and the dialog offered none.
	// The catalogue then MOVED to `wireless.go`, where the page that draws the
	// button actually runs, and that collector issues `/interface/wifi/print`
	// with no proplist at all. So nothing here reads `master` any more —
	// `wifiview.Master` is `master-interface`, a different field — and it came
	// back out rather than being left as a property nobody consumes.
	wifiIfaceCmd = routeros.Cmd{Path: "/interface/wifi/print", Args: []string{
		"=.proplist=.id,name,default-name,disabled,running,master-interface,radio-mac,mac-address," +
			"configuration,configuration.ssid,configuration.mode,configuration.hide-ssid," +
			"configuration.country,configuration.manager,security,security.authentication-types," +
			"channel,channel.band,channel.frequency,channel.width,datapath,datapath.bridge," +
			"datapath.vlan-id,comment,dynamic"}}
	wifiConfigCmd = routeros.Cmd{Path: "/interface/wifi/configuration/print", Args: []string{
		"=.proplist=.id,name,ssid,mode,country,hide-ssid,security,channel,datapath,manager,disabled,comment"}}
	wifiSecurityCmd = routeros.Cmd{Path: "/interface/wifi/security/print", Args: []string{
		"=.proplist=.id,name,authentication-types,wps,ft,ft-over-ds,connect-priority,disabled,comment"}}
	wifiChannelCmd = routeros.Cmd{Path: "/interface/wifi/channel/print", Args: []string{
		"=.proplist=.id,name,band,frequency,width,secondary-frequency,skip-dfs-channels,disabled,comment"}}
	wifiRegCmd = routeros.Cmd{Path: "/interface/wifi/registration-table/print", Args: []string{
		"=.proplist=interface,ssid"}}

	wlIfaceCmd = routeros.Cmd{Path: "/interface/wireless/print", Args: []string{
		"=.proplist=.id,name,default-name,disabled,running,ssid,mode,band,frequency,channel-width," +
			"security-profile,master-interface,hide-ssid,vlan-id,vlan-mode,mac-address,comment,dynamic"}}
	wlProfileCmd = routeros.Cmd{Path: "/interface/wireless/security-profiles/print", Args: []string{
		"=.proplist=.id,name,mode,authentication-types,default"}}
	wlRegCmd = routeros.Cmd{Path: "/interface/wireless/registration-table/print", Args: []string{
		"=.proplist=interface,ssid"}}
)

// wifiConfigEvery: re-read on this multiple of the emit tick when streaming.
// Wireless config changes when somebody edits the router, so the listen channel
// does the real work and this is the safety net for an event that never arrived.
const wifiConfigEvery = 10

// absentMenu is the router saying "that menu is not on this build".
var absentMenu = regexp.MustCompile(`(?i)no such command|unknown command|not supported`)

func isAbsentMenu(err error) bool {
	return err != nil && absentMenu.MatchString(err.Error())
}

type WifiTotals struct {
	Radios      int `json:"radios"`
	Networks    int `json:"networks"`
	Clients     int `json:"clients"`
	CapsManaged int `json:"capsManaged"`
	// Counted separately from CapsManaged: a router provisioning its own radios
	// reports neither a manager nor an editable row, and the page has to be able
	// to say which of the two it is looking at.
	ReadOnly int `json:"readOnly"`
}

type WifiPayload struct {
	TS     int64  `json:"ts"`
	PollMs int    `json:"pollMs"`
	Stack  string `json:"stack"`
	// Available says a wireless menu answered at all, as opposed to this being
	// a router with no radios.
	Available   bool             `json:"available"`
	Radios      []WifiRadio      `json:"radios"`
	Networks    []WifiNetwork    `json:"networks"`
	SecProfiles []WifiSecProfile `json:"secProfiles"`
	Totals      WifiTotals       `json:"totals"`
}

type wifiView struct {
	networks    []WifiNetwork
	radios      []WifiRadio
	secProfiles []WifiSecProfile

	// THE FREQUENCY ANALYSER'S CATALOGUE WAS HERE AND IS NOW IN `wireless.go`.
	//
	// Moved 2026-08-29, not duplicated: this collector is resumed by
	// `case "wifi"` in `ws.go`'s focus switch, and `faOpenBtn` is on the
	// WIRELESS page, whose focus resumes the Wireless collector instead. The
	// catalogue was therefore empty for anyone who had not visited the `wifi`
	// page, and the button — which is drawn only when the catalogue is
	// non-empty — never appeared. Live keeps it in the wireless collector for
	// the same reason.
	//
	// It reads the same rows either way, so nothing was gained by keeping a
	// second copy and the "one rule, two copies" hazard was real: that is
	// exactly the shape of `a4ac96e`, where three paths withheld a WAN address
	// and the fourth copy did not.
}

type Wifi struct {
	ros    Reader
	emit   Emit
	poll   *pollLoop
	pollMs *pollInterval

	mu sync.Mutex
	// stack is "" while unprobed, then "wifi" | "wireless" | "none".
	stack  string
	view   wifiView
	ticks  int
	dirty  bool
	lastFP string
	last   *WifiPayload
}

func NewWifi(ros Reader, emit Emit, pollMs int) *Wifi {
	// The Node signature is clampPoll(raw, def, hi, lo) and the call is
	// (pollMs, 10000, 600000, 30000). Reordered for this side's (raw, def, lo, hi).
	ms := clampPoll(pollMs, 10000, 30000, 600000)
	w := &Wifi{ros: ros, emit: emit, pollMs: newPollInterval(ms), dirty: true}
	w.poll = newPollLoop(func() { w.Tick() },
		func() time.Duration { return time.Duration(ms) * time.Millisecond })
	return w
}

// soft reads an ENRICHMENT menu. A build without it, or an API user who cannot
// see it, costs a badge — never the page.
func (w *Wifi) soft(cmd routeros.Cmd) []routeros.Reply {
	rows, err := w.ros.Do(cmd)
	if err != nil {
		return nil
	}
	return rows
}

func (w *Wifi) readWifi() (wifiView, error) {
	ifaces, err := w.ros.Do(wifiIfaceCmd)
	if err != nil {
		return wifiView{}, err
	}
	reg := w.soft(wifiRegCmd)
	nets, radios := BuildWifiView(WifiViewInput{
		Ifaces:   ifaces,
		Configs:  w.soft(wifiConfigCmd),
		Security: w.soft(wifiSecurityCmd),
		Channels: w.soft(wifiChannelCmd),
		Reg:      reg,
	})
	return wifiView{networks: nets, radios: radios}, nil
}

// replies turns the adapter's string maps into the generic shape the scan
// package parses. That package takes `map[string]any` because its corpus is
// JSON; the conversion is here rather than there so the parser has one input
// shape and one set of coercions.
func replies(rows []routeros.Reply) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		m := make(map[string]any, len(r))
		for k, v := range r {
			m[k] = v
		}
		out = append(out, m)
	}
	return out
}

// clientInterfaces reads the registration table for which interface each
// associated client is on.
//
// ONE ENTRY PER CLIENT, not per interface: the dialog counts them, and a scan of
// a radio drops every one of them plus everyone on the virtual APs riding on it.
func clientInterfaces(reg []routeros.Reply) []string {
	out := make([]string, 0, len(reg))
	for _, r := range reg {
		if iface := r["interface"]; iface != "" {
			out = append(out, iface)
		}
	}
	return out
}

func (w *Wifi) readWireless() (wifiView, error) {
	ifaces, err := w.ros.Do(wlIfaceCmd)
	if err != nil {
		return wifiView{}, err
	}
	nets, radios, secs := BuildWirelessView(WirelessViewInput{
		Ifaces:   ifaces,
		Profiles: w.soft(wlProfileCmd),
		Reg:      w.soft(wlRegCmd),
	})
	return wifiView{networks: nets, radios: radios, secProfiles: secs}, nil
}

// load reads whichever stack this router has, latching on the way.
//
// THE FALLBACK RUNS IN BOTH DIRECTIONS. A modern board whose radios are all
// disabled returns an EMPTY `/interface/wifi/print` rather than an error, which
// looks exactly like a legacy router until something asks the legacy menu and
// gets refused. So an empty first answer is not latched on; it falls through and
// the other stack is tried.
func (w *Wifi) load() {
	w.mu.Lock()
	order := []string{"wifi", "wireless"}
	if w.stack == "wireless" {
		order = []string{"wireless", "wifi"}
	}
	w.mu.Unlock()

	var lastErr error
	for _, which := range order {
		var view wifiView
		var err error
		if which == "wifi" {
			view, err = w.readWifi()
		} else {
			view, err = w.readWireless()
		}
		if err != nil {
			lastErr = err
			if !isAbsentMenu(err) {
				break
			}
			continue
		}
		// An empty answer from a menu that EXISTS is a real answer — a router
		// can genuinely have no wireless configured. Only latch on it when the
		// other stack has not been tried yet.
		if len(view.networks) == 0 && which == order[0] {
			lastErr = nil
			continue
		}
		w.mu.Lock()
		w.stack = which
		w.view = view
		w.mu.Unlock()
		return
	}

	// Both menus refused, or the first was empty and the second refused. The
	// first case is a router with no wireless at all; keep serving an empty view
	// rather than an error, because that is what it is.
	if lastErr != nil && !isAbsentMenu(lastErr) {
		return
	}
	w.mu.Lock()
	if w.stack == "" {
		w.stack = "none"
	}
	w.view = wifiView{}
	w.mu.Unlock()
}

func (w *Wifi) Tick() {
	w.mu.Lock()
	needLoad := w.dirty || w.ticks%wifiConfigEvery == 0
	w.mu.Unlock()
	if needLoad {
		w.load()
		w.mu.Lock()
		w.dirty = false
		w.mu.Unlock()
	}
	w.mu.Lock()
	w.ticks++
	w.mu.Unlock()
	w.emitPayload()
}

func (w *Wifi) emitPayload() {
	w.mu.Lock()
	networks := SortNetworks(w.view.networks)
	radios := w.view.radios
	if radios == nil {
		radios = []WifiRadio{}
	}
	secs := w.view.secProfiles
	if secs == nil {
		secs = []WifiSecProfile{}
	}
	stack := w.stack
	if stack == "" {
		stack = "none"
	}

	totals := WifiTotals{Radios: len(radios), Networks: len(networks)}
	for _, n := range networks {
		totals.Clients += n.Clients
		if n.CapsManaged {
			totals.CapsManaged++
		}
		if n.ReadOnlyReason != "" {
			totals.ReadOnly++
		}
	}

	payload := &WifiPayload{
		TS: time.Now().UnixMilli(), PollMs: w.pollMs.ms(),
		Stack:     stack,
		Available: stack == "wifi" || stack == "wireless",
		Radios:    radios, Networks: networks, SecProfiles: secs,
		Totals: totals,
	}
	// Assigned UNCONDITIONALLY: the focus replay serves it, so a socket that
	// connects during a quiet spell must still get the current view. Only the
	// emit is fingerprint-gated.
	w.last = payload
	fp := wifiFingerprintOf(payload)
	changed := fp != w.lastFP
	w.lastFP = fp
	w.mu.Unlock()

	if changed {
		w.emit("page-wifi-networks", "wifi:update", payload)
	}
}

func wifiFingerprintOf(p *WifiPayload) string {
	n := make([][]any, 0, len(p.Networks))
	for _, x := range p.Networks {
		n = append(n, []any{x.ID, x.Name, x.SSID, x.Band, x.Security, x.VlanID,
			x.Disabled, x.Running, x.Hidden, x.Clients, x.Profile})
	}
	r := make([][]any, 0, len(p.Radios))
	for _, x := range p.Radios {
		r = append(r, []any{x.Name, x.Band, x.Frequency, x.ChannelWidth, x.Disabled, x.Running})
	}
	s := make([][]any, 0, len(p.SecProfiles))
	for _, x := range p.SecProfiles {
		s = append(s, []any{x.ID, x.Name, x.Mode, x.AuthTypes})
	}
	b, _ := json.Marshal([]any{p.Stack, n, r, s})
	return string(b)
}

func (w *Wifi) Last() *WifiPayload {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}

// RefreshNow re-reads after a write, so the page shows what the router did
// rather than what it was asked to do.
func (w *Wifi) RefreshNow() {
	w.mu.Lock()
	w.dirty = true
	w.mu.Unlock()
	w.Tick()
}

func (w *Wifi) Start() { w.Tick(); w.poll.start() }

func (w *Wifi) Reconnected() {
	w.poll.stop()
	w.mu.Lock()
	// The STACK is reset too, not just the fingerprint: a package can be
	// installed and the router rebooted under us, and a latched answer would
	// keep this reading the menu that used to be there.
	w.stack = ""
	w.lastFP = ""
	w.dirty = true
	w.view = wifiView{}
	w.mu.Unlock()
	w.Tick()
	w.poll.start()
}

func (w *Wifi) Suspend() { w.poll.stop() }
func (w *Wifi) Resume()  { w.poll.start() }

func (w *Wifi) Stop() {
	w.poll.stop()
	w.mu.Lock()
	w.lastFP = ""
	w.mu.Unlock()
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (w *Wifi) SetPollMs(ms int) {
	w.pollMs.set(ms)
	w.poll.retime()
}
