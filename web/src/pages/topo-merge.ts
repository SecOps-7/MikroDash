// Merging the other routers' neighbour tables into one graph.
//
// ── WHAT THE MERGE CAN ADD, AND WHAT IT CANNOT ──────────────────────────────
//
// The topology payload is built from ONE router's tables, so its horizon is that
// router's broadcast domain. `/api/topology/peers` reads every other managed
// router's `/ip/neighbor` and this folds them in. Two things come out of it:
//
//	a device no single router could see  — it is on a peer's other port, or on a
//	                                       segment the core never hears
//	a device in the wrong place          — the core heard it, but a peer reports
//	                                       it on a port that is NOT the peer's
//	                                       uplink, which means it is behind that
//	                                       peer and not beside it
//
// THE UPLINK IS THE WHOLE RULE. A peer's uplink is the interface it sees THIS
// router on; anything it reports on another interface is downstream of it. That
// is a fact from the peer's own table rather than an inference about distance.
//
// ── AND IT NEEDS THE VIEWED ROUTER'S OWN ADDRESSES, WHICH THE GRAPH HAS NOT ──
//
// A peer names the viewed router by MAC, like any other device. The core node
// carries neither a MAC nor an identity — `BuildTopology` has no source for
// either — so `/api/topology/peers` answers with them and they arrive here as
// `selfMacs`. WITHOUT THEM THE MERGE DOES NOT RUN: the uplink cannot be found,
// every device would read as "behind" the peer, and the peer's row for the
// viewed router would be added to the map as a second copy of it.
//
// It moves NOTHING on a flat segment, and that is not a gap to close. Where a
// switch forwards the discovery protocols, every router sees every other on its
// uplink port and no hop information exists to recover — the operator's own
// pins are the answer there, and a SwOS box answers no API at all, so it can
// only ever be a node with a declaration behind it.
//
// PURE, so it is tested by replaying two payloads rather than by opening the
// page against a fleet.

import type { TopologyPayload, TopoNeighbor, TopoEdge } from '../gen/payloads';

/** One router's answer, as `/api/topology/peers` sends it. */
export interface TopoPeer {
  id: string;
  label: string;
  ok: boolean;
  error: string;
  macs: string[];
  neighbors: TopoNeighbor[];
}

type Node = TopologyPayload['nodes'][number];

export interface Merged {
  nodes: Node[];
  edges: TopoEdge[];
  /** Which peer contributed or re-attached each node, by node key. */
  owner: Record<string, string>;
  /** Devices no single router could see. */
  added: number;
  /** Devices the merge moved behind a peer. */
  moved: number;
  /** Peers that answered, not counting the one being viewed. */
  answered: number;
  /** Peers that could not be read. A router silently missing from the merge
   *  reads as a router that sees nothing, which is the opposite of the truth. */
  failed: number;
}

const up = (s: string): string => (s || '').toUpperCase();

function fleetEdge(from: string, to: string, iface: string): TopoEdge {
  return {
    id: 'fleet|' + from + '>' + to, from, to, iface, viaPort: iface,
    remoteIface: '', shared: false,
    // AN INFERENCE, because that is what it is: this router did not report the
    // link, another one did, and the page colours it the way it colours every
    // other link it reasoned out rather than read.
    inferred: true, pinned: false, gone: false,
  };
}

/** A managed router nothing on this map has heard of. */
function peerNode(key: string, peer: TopoPeer, now: number): TopoNeighbor {
  return {
    key, kind: 'neighbor', name: peer.label || key, identity: peer.label,
    mac: key, ip: '', ip6: '', type: 'router', typeSource: 'caps',
    caps: [], capsEnabled: [], platform: '', board: '', version: '',
    softwareId: '', description: '', uptime: '', ageSec: null,
    via: [], running: [], ifaces: [], remoteIface: '', ipv6: false,
    gone: false, firstSeen: now, lastSeen: now,
    rtt: null, loss: null, pingTs: null, status: 'unknown', port: '',
    parent: null, pinned: false, clientCount: 0,
  };
}

/**
 * Drop any parent that closes a loop.
 *
 * Two peers can each report the other's devices, and nothing in the rule above
 * stops A hanging off B while B hangs off A. The layout caps its recursion, so a
 * cycle does not hang the page — it just draws a lie. Breaking it here keeps the
 * graph a tree, which is what every reader of `parent` assumes.
 */
function breakCycles(nodes: Node[]): void {
  const byKey = new Map<string, Node>();
  nodes.forEach((n) => byKey.set(n.key, n));
  nodes.forEach((n) => {
    const seen = new Set<string>([n.key]);
    let cur: Node | undefined = n;
    while (cur && cur.parent) {
      if (seen.has(cur.parent)) { cur.parent = null; break; }
      seen.add(cur.parent);
      cur = byKey.get(cur.parent);
    }
  });
}

export function mergePeers(base: TopologyPayload, peers: TopoPeer[],
                           selfMacs: string[], now: number): Merged {
  const nodes: Node[] = base.nodes.map((n) => ({ ...n }));
  const byKey = new Map<string, Node>();
  nodes.forEach((n) => byKey.set(n.key, n));
  let edges: TopoEdge[] = base.edges.slice();
  const owner: Record<string, string> = {};
  let added = 0, moved = 0, answered = 0, failed = 0;

  const mine = new Set((selfMacs || []).map(up));
  // NOTHING TO MERGE AGAINST. See the header: with no way to recognise the
  // viewed router in a peer's table, every rule below would fire the wrong way.
  if (!mine.size) {
    return { nodes, edges, owner, added: 0, moved: 0, answered: 0, failed: peers.length };
  }

  peers.forEach((peer) => {
    if (!peer.ok) { failed++; return; }
    const own = new Set(peer.macs.map(up));
    // THE ROUTER BEING VIEWED IS ALREADY THE GRAPH. Merging its own answer in
    // would re-attach its neighbours to itself through a second path.
    if ([...own].some((m) => mine.has(m))) return;
    answered++;

    let selfKey = '';
    for (const m of own) {
      if (byKey.has(m)) { selfKey = m; break; }
    }
    if (!selfKey) {
      // Managed, and therefore real, but nothing on this router has heard it.
      // It goes on the map so its own devices have something to hang off.
      selfKey = [...own][0] || 'peer:' + peer.id;
      const n = peerNode(selfKey, peer, now);
      nodes.push(n);
      byKey.set(selfKey, n);
      edges.push(fleetEdge('core', selfKey, ''));
      owner[selfKey] = peer.label;
      added++;
    }

    // The port this peer sees the viewed router on. Everything it reports
    // anywhere else is downstream of it.
    const uplink = new Set<string>();
    peer.neighbors.forEach((n) => {
      if (mine.has(up(n.mac))) (n.ifaces || []).forEach((i) => uplink.add(i));
    });
    // A PEER THAT CANNOT SEE THE VIEWED ROUTER has no uplink to measure against,
    // and calling everything it reports "behind it" is the flat-segment mistake
    // this module exists to avoid. It still contributes devices nothing else can
    // see, which is the half that needs no uplink.
    const blind = !uplink.size;

    peer.neighbors.forEach((n) => {
      const key = up(n.key || n.mac);
      if (!key || key === selfKey || mine.has(key) || own.has(key)) return;
      const behind = !(n.ifaces || []).some((i) => uplink.has(i));
      const have = byKey.get(key);

      if (!have) {
        const node: Node = { ...n, key, parent: selfKey };
        nodes.push(node);
        byKey.set(key, node);
        edges.push(fleetEdge(selfKey, key, (n.ifaces || [])[0] || ''));
        owner[key] = peer.label;
        added++;
        return;
      }
      // A CLIENT KEEPS ITS PARENT. The wireless attribution on this router is a
      // better answer than a neighbour row, and a client is a leaf either way.
      if (blind || !behind || have.kind !== 'neighbor' || have.parent === selfKey) return;
      // A PIN IS THE OPERATOR'S, and outranks anything read off a peer.
      // `'pinned' in n` is what narrows the node union; see topology.ts.
      if ('pinned' in have && have.pinned) return;
      have.parent = selfKey;
      edges = edges.filter((e) => !(e.to === key && !e.client));
      edges.push(fleetEdge(selfKey, key, (n.ifaces || [])[0] || ''));
      owner[key] = peer.label;
      moved++;
    });
  });

  breakCycles(nodes);
  return { nodes, edges, owner, added, moved, answered, failed };
}
