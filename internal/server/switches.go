package server

// SwOS switches in the server (#142): the poller's wiring, the switch half of
// the router create and edit routes, the probe, and what the Devices page and
// the device modal are sent about a switch.
//
// A switch shares a router's paths wherever it can: its record is in
// routers.json, its permissions resolve through the same RBAC (whose fleet
// source is store.Devices), its outages go through the connectivity tracker,
// its port changes through the interface up/down rule, its traffic through
// history. What is different is how it is READ, and that is internal/switchpoll.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"mikrodash/internal/alert"
	"mikrodash/internal/audit"
	"mikrodash/internal/collect"
	"mikrodash/internal/connstate"
	"mikrodash/internal/rbac"
	"mikrodash/internal/routeros"
	"mikrodash/internal/routers"
	"mikrodash/internal/safe"
	"mikrodash/internal/store"
	"mikrodash/internal/switchpoll"
	"mikrodash/internal/swos"
)

func (s *Server) registerSwitches(mux *http.ServeMux) {
	// TEN A MINUTE, as `POST /api/routers/test`: each probe opens a connection
	// to a caller-chosen host, so an ungated one is a port scanner.
	lim := newRateLimiter(10, time.Minute).limit
	mux.HandleFunc("POST /api/switches/probe", lim(s.switchProbe))
}

// switchPoller builds the poller on first use; nil without a store.
func (s *Server) switchPoller() *switchpoll.Poller {
	if s.store == nil {
		return nil
	}
	s.switchOnce.Do(func() {
		s.switchPoll = switchpoll.New(switchpoll.Deps{
			Dial: func(sw store.Switch) (switchpoll.Getter, error) {
				return swos.New(sw.Scheme, sw.Host, sw.Port, sw.Username, sw.Password, routeros.DialReach())
			},
			Online: func(id string, up bool, now time.Time) {
				if s.connTrack == nil {
					return
				}
				if up {
					s.connTrack.Connected(id, now.UnixMilli())
				} else {
					s.connTrack.Disconnected(id, now.UnixMilli())
				}
			},
			Traffic: func(id string, smp collect.TrafficSample) {
				s.historyWire.Record(id, "traffic:update", smp)
			},
			Ports: func(sw store.Switch, p collect.IfStatusPayload) {
				if s.alerts == nil {
					return
				}
				r, label := switchAlertTarget(sw)
				if fired := s.alerts.Evaluate(r, "ifstatus:update", p); len(fired) > 0 {
					s.dispatchFired(sw.ID, label, fired)
				}
			},
			Identity: s.switchIdentity,
		})
	})
	return s.switchPoll
}

// syncSwitches makes the poller match routers.json. Declared to history and
// the debounce BEFORE the poller starts: an undeclared device records.
func (s *Server) syncSwitches() {
	p := s.switchPoller()
	if p == nil {
		return
	}
	list, problems := s.store.Switches()
	for _, e := range problems {
		log.Printf("[switch] %v", e)
	}
	for _, sw := range list {
		s.historyWire.SetReporting(sw.ID, sw.ReportingEnabled && !sw.Disabled)
		if s.connTrack != nil {
			sec, ok := 0, sw.ConnDownThresholdSec != nil
			if ok {
				sec = *sw.ConnDownThresholdSec
			}
			s.connTrack.SetThreshold(sw.ID, connstate.ThresholdMs(sec, ok))
		}
	}
	p.Sync(list)
}

func switchAlertTarget(sw store.Switch) (alert.Router, string) {
	sec, ok := 0, sw.ConnDownThresholdSec != nil
	if ok {
		sec = *sw.ConnDownThresholdSec
	}
	return alert.Router{ID: sw.ID, AlertsEnabled: sw.AlertsEnabled, HoldMs: connstate.ThresholdMs(sec, ok)},
		firstNonEmpty(sw.Label, sw.Host)
}

// switchAlertRouter is connVerdict's fallback for an id the session pool does
// not hold: a switch's offline and recovery alerts.
func (s *Server) switchAlertRouter(id string) (alert.Router, string, bool) {
	p := s.switchPoller()
	if p == nil {
		return alert.Router{}, "", false
	}
	sw, ok := p.Switch(id)
	if !ok {
		return alert.Router{}, "", false
	}
	r, label := switchAlertTarget(sw)
	return r, label, true
}

// switchIdentity keeps what the switch says about itself on its record, so a
// card can show the model while the switch is unreachable. Written only when
// it changed: sys.b is read every five minutes.
func (s *Server) switchIdentity(id string, sw swos.Switch) {
	rec, ok := s.switchPoller().Switch(id)
	if !ok || rec.Model == sw.Model && rec.Version == sw.Version && rec.Serial == sw.Serial && rec.MAC == sw.MAC {
		return
	}
	if err := s.store.UpdateRouter(id, map[string]any{
		"model": sw.Model, "osVersion": sw.Version, "serial": sw.Serial, "mac": sw.MAC,
	}); err != nil {
		log.Printf("[switch] %s identity: %v", id, err)
		return
	}
	go s.syncSwitches()
}

// switchStats is the poller's state per switch, for the Devices rows.
func (s *Server) switchStats(list []store.Router) map[string]routers.SwitchStats {
	out := map[string]routers.SwitchStats{}
	p := s.switchPoller()
	if p == nil {
		return out
	}
	for _, rec := range list {
		st, ok := p.State(rec.ID)
		if !ok || !st.Known {
			continue
		}
		row := routers.SwitchStats{Known: true, Connected: st.Online, LastError: st.LastError, Hosts: st.Hosts}
		if sw := st.Switch; sw != nil {
			row.Model, row.Version = sw.Model, sw.Version
			up := sw.UptimeSec
			row.UptimeSec = &up
			row.PortsTotal = len(sw.Ports)
			for _, p := range sw.Ports {
				if p.Up {
					row.PortsUp++
				}
			}
		} else {
			row.Model, row.Version = rec.Model, rec.OSVersion
		}
		out[rec.ID] = row
	}
	return out
}

// isSwitch is whether an id names a switch record.
func (s *Server) isSwitch(id string) bool {
	rec := s.deviceRecord(id)
	return rec != nil && store.IsSwitch(*rec)
}

// deviceRecord is a record of either kind, without its credential.
func (s *Server) deviceRecord(id string) *store.Router {
	if s.store == nil || id == "" {
		return nil
	}
	all, _ := s.store.Devices()
	for i := range all {
		if all[i].ID == id {
			return &all[i]
		}
	}
	return nil
}

// switchLive is a switch's device:live frame.
func (s *Server) switchLive(id string) routers.Live {
	out := routers.Live{Kind: "swos", RouterID: id, Points: []collect.TrafficPoint{},
		Ports: []routers.LivePort{}, Clients: []routers.LiveClient{}}
	p := s.switchPoller()
	if p == nil {
		return out
	}
	st, ok := p.State(id)
	if !ok {
		return out
	}
	out.Connected = st.Online
	live := &routers.SwitchLive{Hosts: st.Hosts, LastError: st.LastError, Ports: []routers.SwitchPortLive{}}
	if sw := st.Switch; sw != nil {
		up := sw.UptimeSec
		live.Model, live.Version, live.Scheme, live.UptimeSec, live.TempC = sw.Model, sw.Version, string(sw.Scheme), &up, sw.TempC
		for i, p := range sw.Ports {
			lp := routers.SwitchPortLive{Name: p.Name, Up: p.Up, Enabled: p.Enabled, Speed: p.Speed,
				FullDuplex: p.FullDuplex, Cage: p.Cage, SFP: p.SFP, PoE: p.PoE}
			if i < len(st.Rates) {
				rx, tx := st.Rates[i].RxBps, st.Rates[i].TxBps
				lp.RxBps, lp.TxBps = &rx, &tx
			}
			if i < len(st.Counters) {
				lp.RxErrors, lp.TxErrors = st.Counters[i].RxErrors, st.Counters[i].TxErrors
			}
			live.Ports = append(live.Ports, lp)
		}
	}
	out.Switch = live
	return out
}

// switchListFor is the switch records this viewer may read, credential
// masked, for the Settings devices table. A separate list beside the routers,
// so nothing that reads the router list (the selector above all) meets one.
func (s *Server) switchListFor(sess *Session) []map[string]any {
	out := []map[string]any{}
	if s.store == nil {
		return out
	}
	all, err := s.store.PublicSwitches()
	if err != nil {
		log.Printf("[switch] list: %v", err)
		return out
	}
	visible := s.visibleRouters(sess)
	for _, r := range all {
		id, _ := r["id"].(string)
		if visible == nil || visible[id] {
			out = append(out, r)
		}
	}
	return out
}

// switchCreate is routerCreate's switch branch (the body's `kind` is "swos").
// The caller has already checked the session and administration.
func (s *Server) switchCreate(w http.ResponseWriter, r *http.Request, sess *Session, body map[string]any) {
	sw, err := s.store.AddSwitch(body)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "router.create", TargetType: "router", TargetID: sw.ID,
		TargetName: firstNonEmpty(sw.Label, sw.Host), RouterID: sw.ID, Note: "SwOS switch",
	})
	EvPermsChanged.BroadcastAll(s.hub, map[string]any{})
	s.broadcastRouterList()
	s.syncFleetHolds()
	writeJSON(w, map[string]any{"ok": true, "router": map[string]any{
		"id": sw.ID, "kind": store.KindSwOS, "label": sw.Label, "host": sw.Host, "port": sw.Port,
		"scheme": sw.Scheme, "username": sw.Username, "password": maskIfSet(sw.Password),
	}})
}

func maskIfSet(p string) string {
	if p == "" {
		return ""
	}
	return store.Mask
}

// switchWritable is every key a switch edit may set. Anything else in the body
// - `kind` above all, which would turn the record into a router - is dropped.
var switchWritable = map[string]bool{
	"label": true, "host": true, "port": true, "scheme": true, "username": true, "password": true,
	"siteIds": true, "siteId": true, "alertsEnabled": true, "reportingEnabled": true,
	"disabled": true, "connDownThresholdSec": true,
}

// switchUpdate is routerUpdate's switch branch. The caller has already checked
// the session and `router:manage` on this id.
func (s *Server) switchUpdate(w http.ResponseWriter, r *http.Request, sess *Session, id string, body map[string]any) {
	for k := range body {
		if !switchWritable[k] {
			delete(body, k)
		}
	}
	stripSwitchPrivileged(body, s.mayManagePrincipals(sess))
	_, h := body["host"]
	_, p := body["port"]
	_, sc := body["scheme"]
	if h || p || sc {
		check := map[string]any{}
		if rec := s.deviceRecord(id); rec != nil {
			check["host"], check["port"], check["scheme"] = rec.Host, rec.Port, rec.Scheme
		}
		for _, k := range []string{"host", "port", "scheme"} {
			if v, ok := body[k]; ok {
				check[k] = v
			}
		}
		host, port, scheme, err := store.CheckSwitchBody(check)
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
			return
		}
		body["host"], body["port"], body["scheme"] = host, port, scheme
	}
	body = store.CoerceRouterPatch(body)
	if raw, ok := body["password"]; ok {
		plain := strings.TrimSpace(jsStringOf(raw))
		if plain == "" || store.IsMasked(raw) {
			delete(body, "password")
		} else {
			sealed, err := s.store.Encrypt(plain)
			if err != nil {
				writeJSONErr(w, http.StatusInternalServerError, "could not save the switch")
				return
			}
			body["password"] = sealed
		}
	}
	if err := s.store.UpdateRouter(id, body); err != nil {
		log.Printf("[switch] update %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not save the switch")
		return
	}
	name := id
	if rec := s.deviceRecord(id); rec != nil {
		name = firstNonEmpty(rec.Label, rec.Host)
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "router.update", TargetType: "router", TargetID: id, TargetName: name, RouterID: id,
		Note: "SwOS switch",
	})
	EvPermsChanged.BroadcastAll(s.hub, map[string]any{})
	s.broadcastRouterList()
	s.syncFleetHolds()
	writeJSON(w, map[string]any{"ok": true})
}

// switchProbe is `POST /api/switches/probe`: sign in to a switch and read
// sys.b, before it is saved. Answers what it found, or why not.
//
// ONLY SwOS IS ACCEPTED: an answer with a board name and a version in one of
// the SwOS schemes. A RouterOS device is told to be added as a router. No
// model-name check: a CRS booted into SwOS is a switch here (the operator,
// 2026-11-19).
//
// GLOBAL ADMINISTRATOR, as `POST /api/routers/test`. The host is the caller's
// text, so the probe reaches wherever it is pointed (within the dial reach);
// a delegate may not move a switch's endpoint, so has nothing to probe, and
// an edit saves without one. An edit may leave the password blank, and then
// the stored one is used, only at the endpoint it is stored with.
func (s *Server) switchProbe(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.routerWriteSession(w, r)
	if !ok {
		return
	}
	var body map[string]any
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&body)
	if body == nil {
		body = map[string]any{}
	}
	if !s.mayManagePrincipals(sess) {
		writeJSONErr(w, http.StatusForbidden, "Administrator access required")
		return
	}
	id := strings.TrimSpace(jsStringOf(body["id"]))
	if id != "" && !s.isSwitch(id) {
		writeJSONErr(w, http.StatusBadRequest, "No such switch")
		return
	}
	host, port, scheme, err := store.CheckSwitchBody(body)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	user := strings.TrimSpace(orStr(jsStringOf(body["username"]), "admin"))
	pass := jsStringOf(body["password"])
	if (pass == "" || store.IsMasked(body["password"])) && id != "" {
		list, _ := s.store.Switches()
		pass = storedSwitchPassword(list, id, host, port, scheme, user)
	}
	c, err := swos.New(scheme, host, port, user, pass, routeros.DialReach())
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), swos.Timeout)
	defer cancel()
	sys, err := c.Get(ctx, "sys.b")
	switch {
	case errors.Is(err, swos.ErrAuth):
		writeJSON(w, map[string]any{"ok": false, "error": "The switch refused the username or password."})
		return
	case err != nil:
		if swos.LooksLikeRouterOS(ctx, scheme, host, port, routeros.DialReach()) {
			writeJSON(w, map[string]any{"ok": false, "error": "This is a RouterOS device: add it as a router instead."})
			return
		}
		writeJSON(w, map[string]any{"ok": false, "error": "This device did not answer as a SwOS switch: " + safe.Message(err.Error())})
		return
	}
	family, ok := swos.DetectScheme(sys)
	if !ok {
		writeJSON(w, map[string]any{"ok": false, "error": "This device did not answer as a SwOS switch."})
		return
	}
	sw := swos.BuildSwitch(family, sys, swos.Value{}, swos.Value{}, swos.Value{})
	writeJSON(w, map[string]any{"ok": true, "model": sw.Model, "version": sw.Version,
		"identity": sw.Identity, "family": string(family)})
}

// stripSwitchPrivileged is the strip a router edit gets, plus `scheme`: without
// administration, a switch's endpoint, credential and sites stay as they are.
// `scheme` is part of the endpoint, and a delegate who could move it from
// https to http could expose the login exchange; it is not a router field, so
// it is stripped here rather than added to the router list.
func stripSwitchPrivileged(body map[string]any, mayManagePrincipals bool) {
	rbac.StripPrivilegedRouterFields(body, mayManagePrincipals)
	if !mayManagePrincipals {
		delete(body, "scheme")
	}
}

// storedSwitchPassword is the stored password for a probe that left the box
// blank, and ONLY when the probe points at the address and login it is stored
// with. Otherwise somebody allowed to edit one switch could aim the probe at a
// host of their own and receive that switch's admin login: the rule the
// router connection test follows (storedPasswordFor).
func storedSwitchPassword(list []store.Switch, id, host string, port int, scheme, user string) string {
	for _, sw := range list {
		if sw.ID != id {
			continue
		}
		if !strings.EqualFold(sw.Host, host) || sw.Port != port || sw.Scheme != scheme || sw.Username != user {
			return ""
		}
		return sw.Password
	}
	return ""
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
