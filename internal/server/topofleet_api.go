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
// ── ON DEMAND ───────────────────────────────────────────────────────────────
//
// Same shape as the DNS fleet read, through the same helpers in fleet.go: a hold
// that WAITS for the connection, every router at once, one deadline. Nothing
// here is polled — the merge is something an operator turns on while looking at
// the map.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"mikrodash/internal/collect"
	"mikrodash/internal/routeros"
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
	targets, over := s.fleetTargets(sess, ids, "network-topology", "read")

	// THE VIEWED ROUTER'S OWN ADDRESSES, WITHOUT WHICH THE MERGE CANNOT RUN.
	//
	// A peer's neighbour table names the viewed router like any other device, by
	// MAC. The graph does not: its core node is keyed `core` and carries neither
	// a MAC nor an identity — `BuildTopology` has no source for either. So the
	// browser had no way to recognise the viewed router in a peer's answer, and
	// the rule that decides what is BEHIND a peer had nothing to work from.
	//
	// Read here rather than polled, because that is what the rest of this
	// endpoint does and it costs one command on a request nobody makes twice.
	selfID := r.URL.Query().Get("self")
	self := []string{}
	if mine, _ := s.fleetTargets(sess, []string{selfID}, "network-topology", "read"); len(mine) == 1 {
		self = s.routerMACs(r.Context(), mine[0].ID)
	}

	now := time.Now().UnixMilli()
	out := fleetEach(r.Context(), targets, func(ctx context.Context, t fleetTarget) topoPeer {
		return s.topoPeerOne(ctx, t, now)
	})
	for _, t := range over {
		out = append(out, topoPeer{ID: t.ID, Label: t.Label,
			Error: "not read: too many routers in one request",
			MACs:  []string{}, Neighbors: []collect.TopoNeighbor{}})
	}
	writeJSON(w, map[string]any{"peers": out, "self": self})
}

// routerMACs is every address one router answers to. Empty when it cannot be
// read, which the page treats as "do not merge" rather than as "no matches".
func (s *Server) routerMACs(ctx context.Context, routerID string) []string {
	sn, drop, ok := s.fleetSession(ctx, routerID, topoFleetHold)
	if !ok {
		return []string{}
	}
	defer drop()
	rows, err := sn.Exec(collect.PeerIfaceCmd())
	if err != nil {
		return []string{}
	}
	return macsOf(rows)
}

func (s *Server) topoPeerOne(ctx context.Context, t fleetTarget, now int64) topoPeer {
	peer := topoPeer{ID: t.ID, Label: t.Label,
		MACs: []string{}, Neighbors: []collect.TopoNeighbor{}}

	sn, drop, ok := s.fleetSession(ctx, t.ID, topoFleetHold)
	if !ok {
		peer.Error = "unreachable"
		return peer
	}
	defer drop()

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
		peer.MACs = macsOf(ifaces)
	}
	peer.OK = true
	return peer
}

// macsOf is the deduplicated addresses of one `/interface/print` answer.
func macsOf(rows []routeros.Reply) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, row := range rows {
		mac := strings.ToUpper(strings.TrimSpace(row["mac-address"]))
		if mac == "" || seen[mac] {
			continue
		}
		seen[mac] = true
		out = append(out, mac)
	}
	return out
}
