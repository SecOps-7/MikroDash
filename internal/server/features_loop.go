package server

// The background half of the optional features (Settings -> Features): one
// goroutine, once a minute, and each feature reads its switch on every pass, so
// turning one off stops it at the next minute without a restart.
//
//	Wi-Fi client history   the wireless payload a session already holds, written
//	Apps Usage history     the bytes of each minute the apps collectors close, written
//	                       (not a feature with a switch: it follows each router's
//	                       Reporting, and only its write runs here)
//	watch list             presence in Wi-Fi and ARP, judged against a grace
//	domain routing         the list, its mangle rules and their routes, read
//	domain lists           a list whose interval has passed, synced
//
// Only domain routing and the domain lists read a router themselves; see
// dnsrouting_api.go and dnslists_api.go. The client features read the payloads
// of the collectors their holds keep running (fleet_holds.go), on the routers
// that use them.
//
// ── ONE LIFETIME FOR EVERYTHING IT STARTS ───────────────────────────────────
//
// `ctx` is cancelled by Stop, and everything the features run in the
// background takes it: the pass in progress, a list's sync and a bulk job
// waiting for the write limit. A Server that is shut down leaves none of them
// running.
//
// ── A SLOW ROUTER DELAYS NOTHING ELSE ───────────────────────────────────────
//
// Domain routing reads routers, and a router can take its whole timeout. Its
// pass runs on a goroutine of its own, every router in it at once, and a pass
// still running when the minute comes round is not started again. The watch
// list and the lists' syncs are on the tick and wait for nobody.

import (
	"context"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mikrodash/internal/alert"
	"mikrodash/internal/collect"
	"mikrodash/internal/db"
	"mikrodash/internal/session"
	"mikrodash/internal/store"
)

// featureHoldsEvery is how often the loop re-applies the client features' holds,
// which is how a router that gained its first watched device comes to be held
// even if the change was made on another instance of the page.
const featureHoldsEvery = 10 * time.Minute

type featureLoop struct {
	ctx    context.Context
	cancel context.CancelFunc

	// watchMu orders a check of the watch list against the deletion of a device
	// from it: a device is never reported absent after it was deleted.
	watchMu sync.Mutex
	// routingBusy is whether a domain routing pass is still running.
	routingBusy atomic.Bool

	mu sync.Mutex
	// listsRunning is the domain lists being synced now, by id.
	listsRunning map[int64]bool
	// wifiTS is the payload each router's samples were last written from, so a
	// payload the collector has not replaced is not written twice.
	wifiTS map[string]int64
	// seen is when each watched device (router|MAC) was last present, in ms.
	seen map[string]int64
	// routing is each router's last domain routing reading, for the card.
	routing map[string]DomainRoutingStatus
	// fed is how many static entries fed the list on each router, and when and
	// for which list that was asked.
	fed map[string]routeFed
	// downStreak is how many readings in a row said a router's routing is down.
	downStreak map[string]int
	// unfed is the routers whose open alert was withdrawn because nothing feeds
	// the list there, so it is done once and not every minute.
	unfed map[string]bool
	// routingQuiet and watchQuiet are true once a switched-off feature's alerts
	// have been withdrawn, so that is done once and not every minute.
	routingQuiet, watchQuiet bool
	lastHolds                time.Time
}

type routeFed struct {
	list  string
	count int
	at    time.Time
}

func (s *Server) buildFeatureLoop() *featureLoop {
	ctx, cancel := context.WithCancel(context.Background())
	f := &featureLoop{ctx: ctx, cancel: cancel, listsRunning: map[int64]bool{}, wifiTS: map[string]int64{},
		seen: map[string]int64{}, routing: map[string]DomainRoutingStatus{}, fed: map[string]routeFed{},
		downStreak: map[string]int{}, unfed: map[string]bool{}}
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				s.featureTick(ctx, now)
			}
		}
	}()
	return f
}

// Stop ends the loop and everything it started.
func (f *featureLoop) Stop() { f.cancel() }

// featuresDone is closed when the server stops; nil, never closed, for a
// Server a test built without the loop.
func (s *Server) featuresDone() <-chan struct{} {
	if s.features == nil {
		return nil
	}
	return s.features.ctx.Done()
}

func (s *Server) featureTick(ctx context.Context, now time.Time) {
	cfg, err := s.mergedSettings()
	if err != nil || s.sessions == nil {
		return
	}
	live := s.sessions.Live()
	if store.FeatureOn(cfg, store.FeatureWifiHistory) && s.auditDB != nil {
		s.recordWifi(live, now)
	}
	// The Apps Usage history is summed per closed minute (apps_history.go);
	// this is its once-a-minute write.
	s.flushAppUsage()
	if store.FeatureOn(cfg, store.FeatureWatchList) && s.auditDB != nil {
		s.features.watchQuiet = false
		s.checkWatchList(live, now)
	} else if !s.features.watchQuiet && s.auditDB != nil {
		s.withdrawWatchAlerts()
	}
	if store.FeatureOn(cfg, store.FeatureDomainRouting) {
		s.features.routingQuiet = false
		if s.features.routingBusy.CompareAndSwap(false, true) {
			list := store.DomainRoutingList(cfg)
			go func() {
				defer s.features.routingBusy.Store(false)
				s.checkDomainRouting(ctx, live, list, now)
			}()
		}
	} else if !s.features.routingQuiet {
		s.withdrawRoutingAlerts()
	}
	if store.FeatureOn(cfg, store.FeatureDNSLists) && s.auditDB != nil {
		s.syncDueLists(ctx, now)
	}
	if now.Sub(s.features.lastHolds) >= featureHoldsEvery {
		s.features.lastHolds = now
		s.syncFeatureHolds()
	}
}

// ── Wi-Fi client history ────────────────────────────────────────────────────

// recordWifi writes every client of every router that is set to be recorded and
// whose wireless payload is newer than the last one written, stamped with the
// minute.
func (s *Server) recordWifi(live map[string]*session.Session, now time.Time) {
	on, err := s.auditDB.WifiHistoryRouters()
	if err != nil {
		log.Printf("[wifi-history] %v", err)
		return
	}
	minute := now.Truncate(time.Minute).UnixMilli()
	for rid, sn := range live {
		if !on[rid] {
			continue
		}
		w := sn.Wireless()
		if w == nil || !sn.Connected() {
			continue
		}
		p := w.Last()
		s.features.mu.Lock()
		stale := p == nil || p.TS <= s.features.wifiTS[rid]
		if !stale {
			s.features.wifiTS[rid] = p.TS
		}
		s.features.mu.Unlock()
		if stale {
			continue
		}
		apOf := map[string]string{}
		if wf := sn.Wifi(); wf != nil {
			if wp := wf.Last(); wp != nil {
				for _, r := range wp.Radios {
					apOf[r.Name] = r.AP
				}
				for _, n := range wp.Networks {
					apOf[n.Name] = n.AP
				}
			}
		}
		rows := make([]db.WifiSample, 0, len(p.Clients))
		for _, c := range p.Clients {
			sig := c.Signal
			rows = append(rows, db.WifiSample{TS: minute, MAC: strings.ToUpper(c.MAC), Iface: c.Iface,
				AP: apOf[c.Iface], SSID: c.SSID, Signal: &sig, TxRate: c.TxRate})
		}
		if err := s.auditDB.InsertWifiSamples(rid, rows); err != nil {
			log.Printf("[wifi-history] %s: %v", rid, err)
		}
	}
}

// ── the watch list ──────────────────────────────────────────────────────────

// devicePresent is whether a MAC is on the router's network now: a Wi-Fi client,
// or a live ARP entry. `known` is false while neither collector has answered
// yet, which is not the same as absent. `fixed` is a device that is not in
// Wi-Fi and is in the ARP table only through a static or DHCP-made entry, which
// says nothing about it either way.
//
// A DHCP lease is not evidence: it stays bound until it expires, so a phone with
// a day's lease would be reported a day late. Nor is an ARP entry that is
// static or added by a DHCP server, which stays however the device is and which
// RouterOS never probes. A dynamic entry that is reachable, stale, delay or
// probe is a device that answered lately (collect.BuildARP).
func devicePresent(sn *session.Session, mac string) (present, known, fixed bool) {
	var wifi *collect.WirelessPayload
	if w := sn.Wireless(); w != nil {
		wifi = w.Last()
	}
	var arp *collect.ARPIndex
	if a := sn.ARP(); a != nil {
		arp = a.Last()
	}
	return presence(wifi, arp, mac)
}

// presence is devicePresent over the two payloads, either of which is nil
// until its collector has answered.
func presence(wifi *collect.WirelessPayload, arp *collect.ARPIndex, mac string) (present, known, fixed bool) {
	mac = strings.ToUpper(mac)
	if wifi != nil {
		known = true
		for _, c := range wifi.Clients {
			if strings.EqualFold(c.MAC, mac) {
				return true, true, false
			}
		}
	}
	if arp != nil {
		known = true
		if e, ok := arp.ByMAC[mac]; ok {
			if e.Live {
				return true, true, false
			}
			if e.Fixed {
				return false, false, true
			}
		}
	}
	return false, known, false
}

// WatchState is a watched device with what the loop last saw.
type WatchState struct {
	db.WatchDevice
	Present bool `json:"present"`
	// LastSeen is when it was last present, in ms; 0 before the loop has seen it.
	LastSeen int64 `json:"lastSeen"`
	Known    bool  `json:"known"`
	// Fixed is a device that can be seen only through a static or DHCP-made ARP
	// entry, so its presence is not known and it is not watched.
	Fixed bool `json:"fixed"`
}

func (s *Server) checkWatchList(live map[string]*session.Session, now time.Time) {
	devs, err := s.auditDB.WatchDevices("")
	if err != nil {
		log.Printf("[watch] %v", err)
		return
	}
	ms := now.UnixMilli()
	for _, d := range devs {
		sn := live[d.RouterID]
		if sn == nil || !sn.Connected() {
			// A router that is down raises Router Offline; its devices are not
			// judged on readings it cannot give.
			continue
		}
		present, known, fixed := devicePresent(sn, d.MAC)
		if fixed {
			// An absence raised before the device was only known through such an
			// entry would never see its recovery.
			s.alerts.Withdraw(d.RouterID, "device_absent", d.MAC)
			continue
		}
		if !known {
			continue
		}
		key := d.RouterID + "|" + d.MAC
		s.features.mu.Lock()
		last, seen := s.features.seen[key]
		if present || !seen {
			// The first sighting is the baseline even when absent: a device
			// the loop has never seen is timed from when the loop started.
			s.features.seen[key] = ms
			last = ms
		}
		s.features.mu.Unlock()
		gone := !present && ms-last >= int64(d.GraceMinutes)*60_000
		if !present && !gone {
			continue
		}
		r, label, ok := s.sessions.AlertRouter(d.RouterID)
		if !ok || s.alerts == nil {
			continue
		}
		s.raiseWatch(d, r, label, present)
	}
}

// raiseWatch files a device's alert, or its recovery, unless the device was
// deleted from the watch list since the pass read it. The deletion holds
// watchMu while it removes the row and withdraws the alert, so this either runs
// before it, and the alert is withdrawn after, or after it, and finds no row.
func (s *Server) raiseWatch(d db.WatchDevice, r alert.Router, label string, present bool) {
	s.features.watchMu.Lock()
	defer s.features.watchMu.Unlock()
	if !present && !s.auditDB.HasWatchDevice(d.RouterID, d.MAC) {
		return
	}
	if fired := s.alerts.DeviceAbsent(r, d.MAC, d.Label, present); len(fired) > 0 {
		s.dispatchFired(d.RouterID, label, fired)
	}
}

// withdrawWatchAlerts closes every watched device's open alert, without a
// message, once the watch list is switched off.
func (s *Server) withdrawWatchAlerts() {
	devs, err := s.auditDB.WatchDevices("")
	if err != nil {
		return
	}
	for _, d := range devs {
		s.alerts.Withdraw(d.RouterID, "device_absent", d.MAC)
	}
	s.features.watchQuiet = true
}

// watchStates is the watch list of one router with what the loop knows.
func (s *Server) watchStates(routerID string) ([]WatchState, error) {
	devs, err := s.auditDB.WatchDevices(routerID)
	if err != nil {
		return nil, err
	}
	sn := s.sessions.Live()[routerID]
	out := make([]WatchState, 0, len(devs))
	for _, d := range devs {
		st := WatchState{WatchDevice: d}
		if sn != nil && sn.Connected() {
			st.Present, st.Known, st.Fixed = devicePresent(sn, d.MAC)
		}
		s.features.mu.Lock()
		st.LastSeen = s.features.seen[d.RouterID+"|"+d.MAC]
		s.features.mu.Unlock()
		out = append(out, st)
	}
	return out, nil
}

// ── domain routing ──────────────────────────────────────────────────────────

// checkDomainRouting reads every connected router at once, each bounded by its
// own timeouts, and waits for them all.
func (s *Server) checkDomainRouting(ctx context.Context, live map[string]*session.Session, list string, now time.Time) {
	var wg sync.WaitGroup
	for rid, sn := range live {
		if !sn.Connected() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.checkDomainRoutingOn(ctx, rid, sn, list, now)
		}()
	}
	wg.Wait()
}

func (s *Server) checkDomainRoutingOn(ctx context.Context, rid string, sn *session.Session, list string, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	st := s.readDomainRouting(ctx, rid, sn, list, now)
	// The feature may have been switched off while the router was read.
	if !s.featureOn(store.FeatureDomainRouting) {
		return
	}
	s.features.mu.Lock()
	s.features.routing[rid] = st
	s.features.mu.Unlock()
	s.sendDomainRoute(st)
	if st.Error != "" {
		return
	}
	r, label, ok := s.sessions.AlertRouter(rid)
	if !ok || s.alerts == nil {
		return
	}
	if st.List == "" || st.Feeding == 0 {
		// Not routing by this list: what it raised before is withdrawn, once.
		s.features.mu.Lock()
		first := !s.features.unfed[rid]
		s.features.unfed[rid] = true
		s.features.downStreak[rid] = 0
		s.features.mu.Unlock()
		if first {
			s.alerts.Withdraw(rid, "domain_routing_down", "")
		}
		return
	}
	s.features.mu.Lock()
	delete(s.features.unfed, rid)
	s.features.mu.Unlock()
	if !s.features.routingDue(rid, st.OK) {
		return
	}
	if !s.featureOn(store.FeatureDomainRouting) {
		return
	}
	if fired := s.alerts.DomainRouting(r, st.OK, st.Why); len(fired) > 0 {
		s.dispatchFired(rid, label, fired)
	}
}

// routingDue is the debounce: whether this reading is handed to the rule. A
// good one always is, and resets the count; a bad one only once it is the
// domainRouteDownAfter-th in a row.
func (f *featureLoop) routingDue(rid string, ok bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ok {
		f.downStreak[rid] = 0
		return true
	}
	f.downStreak[rid]++
	return f.downStreak[rid] >= domainRouteDownAfter
}

// readDomainRouting is one router's reading: nothing when no list is named,
// "not used here" when nothing feeds it, the chain otherwise.
func (s *Server) readDomainRouting(ctx context.Context, rid string, sn *session.Session, list string,
	now time.Time) DomainRoutingStatus {

	st := newDomainRoutingStatus(rid, list)
	if list == "" {
		st.Error = "Name the address list to watch in Settings -> Features"
		return st
	}
	s.features.mu.Lock()
	f := s.features.fed[rid]
	s.features.mu.Unlock()
	if f.list != list || now.Sub(f.at) >= domainRouteFedEvery {
		n, err := domainRoutingFed(sn.Exec, list)
		if err != nil {
			st.Error = "could not read the static DNS entries"
			return st
		}
		f = routeFed{list: list, count: n, at: now}
		s.features.mu.Lock()
		s.features.fed[rid] = f
		s.features.mu.Unlock()
	}
	if f.count == 0 {
		return st
	}
	return domainRoutingStatus(ctx, sn.Exec, rid, list, f.count)
}

// withdrawRoutingAlerts closes every router's Domain Routing Down, without a
// message, once the feature is switched off, and forgets the readings.
func (s *Server) withdrawRoutingAlerts() {
	if all, _ := s.store.Routers(); all != nil {
		for _, r := range all {
			s.alerts.Withdraw(r.ID, "domain_routing_down", "")
		}
	}
	s.features.mu.Lock()
	s.features.routing = map[string]DomainRoutingStatus{}
	s.features.fed = map[string]routeFed{}
	s.features.downStreak = map[string]int{}
	s.features.unfed = map[string]bool{}
	s.features.mu.Unlock()
	s.features.routingQuiet = true
}
