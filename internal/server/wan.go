package server

// WAN DHCP lease actions — the port of the `wan:renew` / `wan:release` block in
// src/index.js.
//
// Renew and release both drop the uplink for a few seconds. On a router managed
// over its LAN that is harmless; on one managed THROUGH the WAN it drops the
// dashboard, and unlike a bad queue it cannot be undone from the row that caused
// it, because the row is no longer reachable. `internal/guard.CheckLeaseAction`
// decides which case applies. It WARNS and never refuses, and it FAILS OPEN —
// read its header before assuming otherwise.
//
// RENEW AND RELEASE ARE TREATED IDENTICALLY. Both interrupt the uplink, so
// there is no quieter path to the riskier one, and the original says so.

import (
	"encoding/json"

	"mikrodash/internal/audit"
	"mikrodash/internal/guard"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
)

type wanRequest struct {
	ID           string `json:"id"`
	ExpectedName string `json:"expectedName"`
	Ack          string `json:"ack"`
}

// wanVerbs is the menu path per verb. Two entries rather than string-building,
// so an unknown verb cannot become a router command.
var wanVerbs = map[string]string{
	"renew":   "/ip/dhcp-client/renew",
	"release": "/ip/dhcp-client/release",
}

func (cn *conn) wanErr(code string, extra map[string]any) {
	m := map[string]any{"code": code}
	for k, v := range extra {
		m[k] = v
	}
	EvWanError.Send(cn.srv.hub, cn.c, m)
}

func (cn *conn) wanMayWrite() bool { return cn.canPage("wan", "write") }

func (cn *conn) wanCaps() {
	if cn.routerID == "" || cn.rsession == nil {
		cn.wanErr("unavailable", nil)
		return
	}
	if !cn.canPage("wan", "read") {
		cn.wanErr("denied", nil)
		return
	}
	EvWanCaps.Send(cn.srv.hub, cn.c, map[string]any{
		"permitted":  cn.wanMayWrite(),
		"routerName": cn.rsession.Label,
	})
}

// wanRead takes everything the guard and the write both need, in one tick.
//
// THE CONNECTED SUBNETS COME FROM /ip/address, NOT FROM THE COLLECTOR PAYLOAD.
// This is the input that decides whether we are about to cut our own management
// path, so it must not be a cached answer that predates the change we are about
// to make.
//
// /user/active is allowed to fail: a read-only API user is denied it, which is
// the common case rather than an edge one, and the guard then fails open.
//
// The original issues these four reads with Promise.all. This side runs them in
// sequence, which is a mechanism change and not a behavioural one: the answer is
// identical, and the documented bottleneck here is CONCURRENT CHANNELS on the
// MikroTik rather than the latency of a handful of prints. Four sequential reads
// on the one channel this session already holds cost the router less than four
// at once, which is the direction this port is supposed to move in.
func (cn *conn) wanRead() (rows []routeros.Reply, path guard.WanPath, activeDefault string, err error) {
	clients, err := cn.rsession.Exec(routeros.Cmd{Path: "/ip/dhcp-client/print"})
	if err != nil {
		return nil, guard.WanPath{}, "", err
	}
	for _, r := range clients {
		if r[".id"] != "" {
			rows = append(rows, r)
		}
	}

	addrs, err := cn.rsession.Exec(routeros.Cmd{
		Path: "/ip/address/print", Args: []string{"=.proplist=address,interface,disabled"}})
	if err != nil {
		return nil, guard.WanPath{}, "", err
	}
	var connected []string
	for _, a := range addrs {
		// A DISABLED address is not a network we are attached to. Including it
		// would let a switched-off subnet make a remote session look local, and
		// silence the warning on exactly the router that needs it.
		if a["address"] != "" && a["disabled"] != "true" {
			connected = append(connected, a["address"])
		}
	}

	var active []routeros.Reply
	if a, aerr := cn.rsession.Exec(routeros.Cmd{Path: "/user/active/print"}); aerr == nil {
		for _, r := range a {
			if r["name"] != "" {
				active = append(active, r)
			}
		}
	}
	self, _ := guard.SelfAddresses(active, []string{cn.rsession.Username()})
	path = guard.ResolveWanPath(self, connected)

	routes, rerr := cn.rsession.Exec(routeros.Cmd{
		Path: "/ip/route/print", Args: []string{"=.proplist=dst-address,gateway,distance,active"}})
	if rerr != nil {
		return nil, guard.WanPath{}, "", rerr
	}
	activeDefault = guard.ActiveDefaultWan(asMaps(routes), asMaps(rows))
	return rows, path, activeDefault, nil
}

// asMaps hands routeros.Reply values to the guard, which takes plain maps so it
// carries no dependency on the wire package.
func asMaps(in []routeros.Reply) []map[string]string {
	out := make([]map[string]string, 0, len(in))
	for _, r := range in {
		out = append(out, map[string]string(r))
	}
	return out
}

// wanRow addresses by id and identifies by interface name — an id survives a
// rename, a name does not.
func wanRow(rows []routeros.Reply, id, expectedName string) routeros.Reply {
	for _, r := range rows {
		if r[".id"] != id {
			continue
		}
		if expectedName != "" && r["interface"] != expectedName {
			return nil
		}
		return r
	}
	return nil
}

// runWanLease renews or releases one lease and REPORTS what happened, rather
// than sending it.
//
// ── SPLIT SO A SECOND CALLER CAN HAVE IT ────────────────────────────────────
//
// Slice 3 of the MikroMCP parity work gives the assistant `run_action`, and an
// action it can run has to be the same code the page runs, not a second copy of
// it: two paths to one router command is two places for the guard, the audit row
// and the refresh to drift. So the sequence lives here and returns a
// `writeOutcome`, exactly as the resource writes do, and the socket handler
// below is a thin adapter over it.
//
// `via` is the provenance the audit row carries: "" for a human at the page,
// "agent" for the assistant.
func (cn *conn) runWanLease(verb, id, expectedName, ack, via string) writeOutcome {
	menu, known := wanVerbs[verb]
	if !known || id == "" {
		return writeOutcome{Code: "bad-request"}
	}
	if cn.routerID == "" || cn.rsession == nil {
		return writeOutcome{Code: "unavailable"}
	}
	action := "wan." + verb
	if !cn.wanMayWrite() {
		cn.recorder().Denied(audit.Event{
			Action: action, TargetType: "wan", RouterID: cn.routerID,
			TargetName: expectedName,
		})
		return writeOutcome{Code: "denied"}
	}

	var out writeOutcome
	err := cn.inWriteQueue(func() error {
		rows, path, activeDefault, rerr := cn.wanRead()
		if rerr != nil {
			return rerr
		}
		target := wanRow(rows, id, expectedName)
		if target == nil {
			out = writeOutcome{Code: "stale-row"}
			return nil
		}
		name := target["interface"]

		v := guard.CheckLeaseAction(&path, name, activeDefault)
		if v.Level == "warn" {
			detail := map[string]any{
				"warning": v.Detail, "fingerprint": v.Fingerprint, "verb": verb,
			}
			if ack == "" {
				// NOTHING WRITTEN, NOTHING AUDITED. A prompt is not a refusal,
				// and a denied row here would misrepresent what was attempted.
				out = writeOutcome{Code: "self-cutoff", Name: name, Detail: detail}
				return nil
			}
			if ack != v.Fingerprint {
				// Acknowledged against different values, or our own path moved
				// between the prompt and the retry.
				out = writeOutcome{Code: "stale-warning", Name: name, Detail: detail}
				return nil
			}
		}

		if _, werr := cn.rsession.Exec(routeros.Cmd{
			Path: menu, Args: []string{"=.id=" + id}}); werr != nil {
			return werr
		}

		extra := []audit.KV{{Key: "status", Value: target["status"]}}
		if ack != "" {
			extra = append(extra, audit.KV{Key: "selfCutoffAcknowledged", Value: true})
		}
		if via != "" {
			extra = append(extra, audit.KV{Key: "via", Value: via})
		}
		note := "requested a DHCP lease renewal"
		if verb == "release" {
			note = "released the DHCP lease; the uplink is down until the client rebinds"
		}
		cn.recorder().Record(audit.Event{
			Action: action, TargetType: "wan", TargetID: id,
			TargetName: name, RouterID: cn.routerID,
			Extra: extra, Note: note,
		})
		// The lease state settles over the next second or two, so this re-read
		// may still show the old value. The page says "requested" rather than
		// claiming the new state, and the next tick tells the truth.
		if cn.rsession.CollectorEnabled("wan") {
			cn.rsession.Wan().RefreshNow()
		}
		out = writeOutcome{Action: verb, Name: name}
		return nil
	})
	if err != nil {
		return writeOutcome{Code: writeFailCode(err),
			Detail: map[string]any{"message": safe.Message(err.Error())}}
	}
	return out
}

// wanLeaseAction is the socket handler: decode, run, tell the browser.
func (cn *conn) wanLeaseAction(verb string, raw json.RawMessage) {
	var req wanRequest
	if json.Unmarshal(raw, &req) != nil {
		cn.wanErr("bad-request", nil)
		return
	}
	out := cn.runWanLease(verb, req.ID, req.ExpectedName, req.Ack, "")
	if out.Code == "" {
		EvWanOk.Send(cn.srv.hub, cn.c, map[string]any{"action": out.Action, "name": out.Name})
		return
	}
	extra := map[string]any{}
	for k, v := range out.Detail {
		extra[k] = v
	}
	if out.Name != "" {
		extra["name"] = out.Name
	}
	cn.wanErr(out.Code, extra)
}
