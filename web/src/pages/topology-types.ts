// The topology payload, as internal/collect/topology.go sends it.
//
// Three node shapes on the wire, so three interfaces and a union here: the core
// carries gauges, a neighbour carries discovery fields, a client carries its
// association. The renderer narrows on `kind`, exactly as the live app does.

export interface TopoNodeBase {
  key: string; kind: string; name: string; identity: string;
  mac: string; ip: string; ip6: string;
  type: string; typeSource: string;
  caps: string[]; capsEnabled: string[];
  platform: string; board: string; version: string;
  softwareId: string; description: string; uptime: string;
  ageSec: number | null;
  via: string[]; running: string[]; ifaces: string[];
  remoteIface: string; ipv6: boolean;
  gone: boolean; firstSeen: number; lastSeen: number;
  rtt: number | null; loss: number | null; pingTs: number | null;
  status: string;
}

export interface TopoCoreNode extends TopoNodeBase {
  kind: 'core';
  port: string; parent: null;
  cpuLoad: number | null; memPct: number | null;
  clientCount: number;
}

export interface TopoNeighborNode extends TopoNodeBase {
  kind: 'neighbor';
  port: string; parent: string | null;
  clientCount: number;
}

export interface TopoClientNode extends TopoNodeBase {
  kind: 'client';
  port: string; parent: string;
  attrib: string; vlans: number[]; vlanNames: string[];
  ssid: string; signal: string;
}

export type TopoNode = TopoCoreNode | TopoNeighborNode | TopoClientNode;

export interface TopoEdge {
  id: string; from: string; to: string;
  iface: string; viaPort: string; remoteIface: string;
  shared: boolean; inferred: boolean; client?: boolean; gone: boolean;
}

export interface TopoVlan { vid: number; name: string }

export interface TopoDiscovery {
  protocol: string[]; mode: string; interfaceList: string; interval: string;
}

export interface TopologyPayload {
  ts: number; pollMs: number;
  discovery: TopoDiscovery | null;
  permissionDenied: boolean; pingDenied: boolean;
  neighborCount: number;
  vlans: TopoVlan[];
  clientCount: number; clientsTruncated: number;
  nodes: TopoNode[];
  edges: TopoEdge[];
}
