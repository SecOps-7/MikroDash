package collect

// Wifi collector — whichever wireless stack this router has, plus what a legacy
// CAPsMAN manager presents on top of it.
//
// A router runs EITHER `/interface/wifi` (modern) or `/interface/wireless`
// (legacy), never both, and which one is not knowable without asking. The stack
// is probed once and latched, and RESET on reconnect, because a package can be
// installed and the router rebooted underneath us.
//
// `/caps-man` is a THIRD source and not a third stack: its interfaces are remote
// CAPs' radios, presented on the manager, and they appear in neither local menu.
// A manager showed a page listing none of them until 2026-09-13. See
// readLegacyCaps, and BuildCapsLegacyNetworks next door.
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

	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
)

// The modern stack's reads. The proplists are copied from src/routeros/wifiMenus.js
// rather than re-derived, so the shared ones cannot drift.
var (
	// `cap` IS IN IT SINCE THE PAGE LEARNED TO GROUP BY ACCESS POINT. It reads
	// `identity@base-mac%id` on a CAP-provisioned interface and is absent on a
	// local one, which is exactly the distinction "which AP is this network on"
	// needs — `capField` in capsman.go already parses it, and deriving the
	// identity from the interface NAME instead would depend on the manager's
	// `name-format`, which is the operator's to change.
	//
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
			"cap,configuration,configuration.ssid,configuration.mode,configuration.hide-ssid," +
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
	// Available says there is something on this page: a wireless menu answered,
	// or a legacy CAPsMAN manager has interfaces to show. Not the same question
	// as "does this router have radios" since v1 networks joined the table.
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

	// cache coalesces reads shared with another collector; nil outside a live
	// session, which is every test. See collect/cache.go.
	cache *roscache.Cache

	mu sync.Mutex
	// stack is "" while unprobed, then "wifi" | "wireless" | "none".
	stack  string
	view   wifiView
	ticks  int
	dirty  bool
	lastFP string
	last   *WifiPayload

	// v1Avail latches the LEGACY CAPsMAN tree: nil while unprobed, false on a
	// router that has no `/caps-man` menu. It is a third source of networks and
	// orthogonal to `stack` — a manager can present a dozen CAP interfaces while
	// running either local stack, or neither.
	v1Avail *bool

	// MECHANISM B: the menu this wants is whichever STACK the router turned out
	// to have, which is not known until it answers. `load` latches it and then
	// moves the subscription -- see alignSubscription.
	sched scheduled
}

func NewWifi(ros Reader, emit Emit, pollMs int) *Wifi {
	// The Node call was clampPoll(pollMs, 10000, 600000, 30000) in its (raw,
	// def, hi, lo) order: a 30 s floor, above both the default and the store's
	// 10 s minimum. The floor is now the store's (2026-09-18; pollbounds_test.go).
	ms := clampPoll(pollMs, 10000, 10000, 600000)
	w := &Wifi{ros: ros, emit: emit, pollMs: newPollInterval(ms), dirty: true}
	w.poll = newPollLoop(func() { w.Tick() }, w.pollMs.duration)
	// THE SUBSCRIPTION'S CADENCE IS THE LOAD'S, NOT THE TICK'S. The polled path
	// ticks at pollMs and only READS every wifiConfigEvery ticks; the other nine
	// rebuild an unchanged payload and are suppressed by the fingerprint. A
	// subscription is a read, so it wants the product -- the same effective rate,
	// with the nine no-op ticks gone.
	w.sched = scheduled{
		loop: w.poll, menu: wifiIfaceCmd.Path, fields: fieldsOf(wifiIfaceCmd),
		cadence: func() time.Duration { return w.pollMs.duration() * wifiConfigEvery },
		apply:   w.applyLoad,
	}
	return w
}

// applyLoad is the scheduled path's tick: read, then emit.
//
// The delivered rows are not used. `load` re-reads the same menu through the
// cache, which the scheduler has just refreshed, so it costs nothing -- and it
// keeps ONE statement of how a stack is read instead of a second one here that
// would have to learn the fallback rules all over again.
func (w *Wifi) applyLoad([]routeros.Reply, error) {
	w.load()
	w.mu.Lock()
	w.dirty = false
	w.mu.Unlock()
	w.emitPayload()
}

// alignSubscription points the subscription at the stack this router has.
//
// MECHANISM B. Called from `load`, which is the only thing that decides the
// answer. Without it a legacy router would have the scheduler reading
// `/interface/wifi/print` forever -- a menu that is not there, once per cadence.
func (w *Wifi) alignSubscription() {
	w.mu.Lock()
	stack := w.stack
	w.mu.Unlock()
	menu := wifiIfaceCmd
	if stack == "wireless" {
		menu = wlIfaceCmd
	}
	w.sched.resubscribe(menu.Path, w.applyLoad)
}

// UseCache feeds BOTH halves: the 1.4 shared-read cache and the subscription.
// Set once, before Start; nil leaves every read direct and the collector polled.
func (w *Wifi) UseCache(c *roscache.Cache) {
	w.cache = c
	w.sched.useCache(c)
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
	// THROUGH THE CACHE, along with the registration table below.
	ifaces, err := readVia(w.cache, w.ros, wifiIfaceCmd, w.pollMs.duration())
	if err != nil {
		return wifiView{}, err
	}
	reg, _ := readVia(w.cache, w.ros, wifiRegCmd, w.pollMs.duration())
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

// readLegacyCaps is the `/caps-man` tree's contribution to this page.
//
// LATCHED LIKE A STACK, not asked every load: a modern board has none of these
// menus, and a refusal per menu per config cycle on every router in a fleet is
// the kind of cost nothing ever reports.
//
// THE INTERFACE MENU IS THE PROBE, and it comes first so a board without it
// costs one refusal rather than six. The other five enrich its rows, and a build
// missing one costs a column — the same trade `soft` makes for the modern
// profile menus.
func (w *Wifi) readLegacyCaps() ([]WifiNetwork, []WifiRadio) {
	// UNDER THE LOCK, for the reason the same latch in topology.go carries: this
	// runs before `Tick` takes `w.mu`, and `Reconnected` clears the latch while
	// holding it.
	w.mu.Lock()
	avail := w.v1Avail
	w.mu.Unlock()
	if !MenuAvailable(avail) {
		return nil, nil
	}
	ifaces, err := readVia(w.cache, w.ros, capsV1IfaceCmd, w.pollMs.duration())
	if err != nil {
		if isAbsentMenu(err) {
			no := false
			w.mu.Lock()
			w.v1Avail = &no
			w.mu.Unlock()
		}
		return nil, nil
	}
	yes := true
	w.mu.Lock()
	w.v1Avail = &yes
	w.mu.Unlock()
	configs, _ := readVia(w.cache, w.ros, capsV1ConfigCmd, w.pollMs.duration())
	channels, _ := readVia(w.cache, w.ros, capsV1ChannelCmd, w.pollMs.duration())
	reg, _ := readVia(w.cache, w.ros, capsV1RegCmd, w.pollMs.duration())
	return BuildCapsLegacyNetworks(CapsLegacyViewInput{
		Ifaces: ifaces, Configs: configs, Channels: channels,
		Security: w.soft(capsV1SecurityCmd), Datapaths: w.soft(capsV1DatapathCmd),
		Radios: w.soft(capsV1RadioCmd), Reg: reg,
	})
}

// withLegacyCaps appends the legacy manager's networks to whichever local stack
// answered — including none of them, which is a real configuration: a router
// with no radios of its own can still be a v1 manager.
//
// BY NAME, AND THE LOCAL ROW WINS. A manager that also runs its own radios as a
// CAP sees them twice: once in `/interface/wireless` as dynamic interfaces and
// once in `/caps-man/interface`. The local row is the one that carries an `.id`
// the write path could use, so it is the one kept.
func withLegacyCaps(v wifiView, nets []WifiNetwork, radios []WifiRadio) wifiView {
	local := map[string]bool{}
	for _, n := range v.networks {
		local[n.Name] = true
	}
	for _, n := range nets {
		if !local[n.Name] {
			v.networks = append(v.networks, n)
		}
	}
	for _, r := range radios {
		if !local[r.Name] {
			v.radios = append(v.radios, r)
		}
	}
	return v
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
	defer w.alignSubscription()
	w.mu.Lock()
	order := []string{"wifi", "wireless"}
	if w.stack == "wireless" {
		order = []string{"wireless", "wifi"}
	}
	w.mu.Unlock()

	// Read ONCE, before the stack probe, and folded into whichever view wins.
	// Doing it per branch would read the same five menus two or three times on a
	// load that falls through.
	capsNets, capsRadios := w.readLegacyCaps()

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
		w.view = withLegacyCaps(view, capsNets, capsRadios)
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
	w.view = withLegacyCaps(wifiView{}, capsNets, capsRadios)
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
		Stack: stack,
		// A v1 manager with no radios of its own has networks to show and no
		// stack at all, so "did a wireless menu answer" is no longer the same
		// question as "is there anything on this page".
		Available: stack == "wifi" || stack == "wireless" || len(networks) > 0,
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
		EvWifiUpdate.Emit(w.emit, wifiRooms.Join(), *payload)
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

func (w *Wifi) Start() { w.Tick(); w.sched.begin() }

func (w *Wifi) Reconnected() {
	w.sched.end()
	w.mu.Lock()
	// The STACK is reset too, not just the fingerprint: a package can be
	// installed and the router rebooted under us, and a latched answer would
	// keep this reading the menu that used to be there.
	w.stack = ""
	w.v1Avail = nil
	w.lastFP = ""
	w.dirty = true
	w.view = wifiView{}
	w.mu.Unlock()
	w.Tick()
	w.sched.begin()
}

func (w *Wifi) Suspend() { w.sched.end() }
func (w *Wifi) Resume()  { w.sched.begin() }

func (w *Wifi) Stop() {
	w.sched.end()
	w.mu.Lock()
	w.lastFP = ""
	w.mu.Unlock()
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (w *Wifi) SetPollMs(ms int) {
	w.pollMs.set(ms)
	w.poll.retime()
	// And the scheduler: a streamed menu reopens at the new interval.
	w.sched.retune()
}
