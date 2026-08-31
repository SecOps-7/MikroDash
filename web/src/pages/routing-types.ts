// The routing:update payload, as internal/collect/routing.go emits it.
//
// Separate from routing.ts so the shape has one definition that the page and
// anything else reading the payload agree on. The field names are the golden's,
// which is the contract — see the Go collector's package note.

export interface Route {
  dst: string; gateway: string; distance: number; active: boolean;
  comment: string; type: string; protocol: string;
  family: string; id: string;
}

export interface RouteCounts {
  total: number; connect: number; static: number;
  dynamic: number; bgp: number; ospf: number;
}

export interface Peer {
  key: string; peerType: string; name: string; description: string;
  remoteAddr: string; remoteAs: number; state: string;
  uptimeSec: number; prefixes: number; prefixHistory: number[];
  updatesSent: number; updatesRecv: number; lastError: string;
  holdTime: number; keepalive: number; flapping: boolean;
}

export interface PeerSummary { total: number; established: number; down: number }

export interface RoutingPayload {
  ts: number; pollMs: number;
  routeCounts: RouteCounts; peers: Peer[]; routes: Route[]; summary: PeerSummary;
}
