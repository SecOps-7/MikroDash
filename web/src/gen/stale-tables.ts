// GENERATED from testdata/stale-tables.json — do not edit.
// Rebuild with `node tools/stale-tables-ts.js` from the committed JSON.
// The JSON it reads is a FROZEN artefact: the generator that produced it read the
// Node app and was deleted with the port-parity harness on 2026-09-01. This
// transform still runs, so the .ts can be rebuilt from the committed JSON --
// but the JSON itself can only change by hand, or from `v0.7.40` in git history.

/** Grace added on top of a collector's reported poll interval. */
export const STALE_GRACE = 20000;

export interface StaleCard { cardId: string; event: string; threshold: number }

/**
 * Which event proves each card is alive, and how long it may go without one.
 *
 * THE THRESHOLDS ARE STARTING POINTS. A payload carrying `pollMs` rewrites its
 * own card's threshold at runtime, so a collector that reports a slower interval
 * stops being called stale for keeping to it.
 */
export const STALE_CARDS: readonly StaleCard[] = [
  {
    "cardId": "systemCard",
    "event": "system:update",
    "threshold": 15000
  },
  {
    "cardId": "connCard",
    "event": "conn:update",
    "threshold": 20000
  },
  {
    "cardId": "talkersCard",
    "event": "talkers:update",
    "threshold": 90000
  },
  {
    "cardId": "wirelessCard",
    "event": "wireless:update",
    "threshold": 25000
  },
  {
    "cardId": "vpnCard",
    "event": "vpn:update",
    "threshold": 90000
  },
  {
    "cardId": "netwatchCard",
    "event": "netwatch:update",
    "threshold": 90000
  },
  {
    "cardId": "firewallCard",
    "event": "firewall:update",
    "threshold": 90000
  },
  {
    "cardId": "ifStatusCard",
    "event": "ifstatus:update",
    "threshold": 90000
  },
  {
    "cardId": "networksCard",
    "event": "lan:overview",
    "threshold": 345000
  },
  {
    "cardId": "bandwidthCard",
    "event": "bandwidth:update",
    "threshold": 20000
  },
  {
    "cardId": "routingProtoCard",
    "event": "routing:update",
    "threshold": 90000
  },
  {
    "cardId": "routingBgpCard",
    "event": "routing:update",
    "threshold": 90000
  },
  {
    "cardId": "routingPeersCard",
    "event": "routing:update",
    "threshold": 90000
  },
  {
    "cardId": "routingRoutesCard",
    "event": "routing:update",
    "threshold": 90000
  },
  {
    "cardId": "topologyCard",
    "event": "topology:update",
    "threshold": 90000
  },
  {
    "cardId": "vlansCard",
    "event": "vlans:update",
    "threshold": 30000
  },
  {
    "cardId": "capsmanCard",
    "event": "capsman:update",
    "threshold": 40000
  },
  {
    "cardId": "bridgesCard",
    "event": "bridges:update",
    "threshold": 30000
  },
  {
    "cardId": "dnsCard",
    "event": "dns:update",
    "threshold": 40000
  },
  {
    "cardId": "packagesCard",
    "event": "packages:update",
    "threshold": 90000
  },
  {
    "cardId": "pppCard",
    "event": "ppp:update",
    "threshold": 30000
  }
];

/** Collector key -> the cards it feeds. */
export const COLLECTOR_CARDS: Record<string, string[]> = {
  "conns": [
    "connCard"
  ],
  "bandwidth": [
    "bandwidthCard"
  ],
  "talkers": [
    "talkersCard"
  ],
  "ifStatus": [
    "ifStatusCard"
  ],
  "wireless": [
    "wirelessCard"
  ],
  "vpn": [
    "vpnCard"
  ],
  "firewall": [
    "firewallCard"
  ],
  "routing": [
    "routingProtoCard",
    "routingBgpCard",
    "routingPeersCard",
    "routingRoutesCard"
  ],
  "netwatch": [
    "netwatchCard"
  ],
  "topology": [
    "topologyCard"
  ]
};

/**
 * Card -> the tbody holding its rows.
 *
 * Nothing to do with staleness: this is what gets emptied on a router switch.
 * Upstream this list exists because switching used to clear each card's
 * in-memory guard and never the rendered rows, so a card kept showing the
 * PREVIOUS router's data until the new one produced a payload — indefinitely if
 * that collector is disabled or slow.
 */
export const DASH_CARD_TABLES: Record<string, string> = {
  "bandwidthCard": "bwTbody",
  "talkersCard": "talkersTable",
  "ifStatusCard": "ifaceListBody",
  "wirelessCard": "wirelessTable",
  "vpnCard": "vpnTable",
  "firewallCard": "firewallTable",
  "routingPeersCard": "rtTbody",
  "routingRoutesCard": "rtRoutesTbody",
  "netwatchCard": "netwatchTable"
};
