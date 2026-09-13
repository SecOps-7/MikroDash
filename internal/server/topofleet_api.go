package server

// `/api/topology/peers` — what the OTHER routers can see, for one graph.
//
// ── WHAT A SINGLE ROUTER'S MAP CANNOT SHOW ──────────────────────────────────
//
// The topology collector builds the graph from the ACTIVE router's tables, so
// its horizon is that router's broadcast domain: a device on an access point's
// second port, or on a segment the core never hears, is simply not there. And
// where a switch forwards the discovery protocols, everything behind it arrives
// on one port and the graph draws it flat.
//
// Every one of those devices is visible to SOME router, and this dashboard
// already has the credentials for the ones the operator added. This endpoint
// reads their neighbour tables so the page can merge them into one map.
//
// ── WHAT IT STILL CANNOT DO, STATED ONCE ────────────────────────────────────
//
// A SwOS switch answers no RouterOS API — SwOS has a web interface and SNMP and
// nothing this app speaks — so it can never contribute a neighbour table. It
// appears as a NODE (it announces itself over MNDP, and the core hears that) and
// what hangs off it is a declaration: `topology-links` in internal/sitedoc.
//
// Two managed routers on one flat segment see each other and everything else on
// their uplink port, so the merge moves nothing there either, and that is not a
// bug to fix: a discovery protocol that is forwarded carries no hop information
// to recover.
//
// ── ON DEMAND, ONE HOLD AT A TIME ───────────────────────────────────────────
//
// Same shape as the DNS fleet read: `Retain` builds a session if there is not
// one, two reads, `Drop`. Nothing here is polled — the merge is something an
// operator turns on while looking at the map.

import (
	"net/http"
	"strings"
	"time"

	"mikrodash/internal/collect"
	"mikrodash/internal/safe"
)

// topoFleetHold is the reason this endpoint's session holds carry, so a stuck
// one can be explained by name — see session.Manager.Retain.
const topoFleetHold = "topology-fleet"

// topoPeer is one router's answer.
type topoPeer struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	OK    bool   `json:"ok"`
	// Error is why this router contributed nothing. Present rather than dropped:
	// a router silently missing from the merge reads as a router that sees
	// nothing, which is the opposite of the truth.
	Error string `json:"error"`
	// MACs is every address this router answers to, which is how the page finds
	// the node it ALREADY IS on the map. A router is discovered by whichever port
	// faces the discoverer, so one address would miss most of the time.
	MACs      []string               `json:"macs"`
	Neighbors []collect.TopoNeighbor `json:"neighbors"`
}

func (s *Server) registerTopologyFleet(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/topology/peers", s.topoPeersGet)
}

func (s *Server) topoPeersGet(w http.ResponseWriter, r *http.Request) {
	sess := s.layoutSession(w, r)
	if sess == nil {
		return
	}
	ids := strings.Split(r.URL.Query().Get("routers"), ",")
	targets := s.fleetTargets(sess, ids, "network-topology", "read")

	now := time.Now().UnixMilli()
	out := make([]topoPeer, 0, len(targets))
	for _, t := range targets {
		out = append(out, s.topoPeerOne(t.ID, t.Label, now))
	}
	writeJSON(w, map[string]any{"peers": out})
}

func (s *Server) topoPeerOne(routerID, label string, now int64) topoPeer {
	peer := topoPeer{ID: routerID, Label: label,
		MACs: []string{}, Neighbors: []collect.TopoNeighbor{}}

	sn, err := s.sessions.Retain(routerID, topoFleetHold)
	if err != nil || sn == nil {
		peer.Error = "unreachable"
		return peer
	}
	defer s.sessions.Drop(routerID, topoFleetHold)

	rows, rerr := sn.Exec(collect.NeighborCmd())
	if rerr != nil {
		peer.Error = safe.Message(rerr.Error())
		return peer
	}
	peer.Neighbors = collect.PeerNeighbors(rows, now)

	// BEST EFFORT, and the read order says which half matters. Without the
	// neighbour table this router contributes nothing; without its own addresses
	// it contributes nodes that cannot be attached to it, which is still more
	// than the map had.
	if ifaces, ierr := sn.Exec(collect.PeerIfaceCmd()); ierr == nil {
		seen := map[string]bool{}
		for _, row := range ifaces {
			mac := strings.ToUpper(strings.TrimSpace(row["mac-address"]))
			if mac == "" || seen[mac] {
				continue
			}
			seen[mac] = true
			peer.MACs = append(peer.MACs, mac)
		}
	}
	peer.OK = true
	return peer
}
