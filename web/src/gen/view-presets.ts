// GENERATED from testdata/view-presets.json — do not edit.
// Rebuild with `node tools/view-presets-ts.js` from the committed JSON, which is frozen:
// the generator that produced it read the Node app and was deleted on 2026-09-01.

/** settings key -> page key. `pageWifi` is the checkbox; `wifi` is what a preset names. */
export const PAGE_NAV_MAP: Record<string, string> = {
  "pageWifi": "wifi-networks",
  "pageWireless": "wifi-clients",
  "pageInterfaces": "interfaces",
  "pageDhcp": "dhcp",
  "pageVpn": "vpn",
  "pageConnections": "connections",
  "pageFirewall": "firewall",
  "pageLogs": "logs",
  "pageBandwidth": "bandwidth",
  "pageRouting": "routing",
  "pageTopology": "network-topology",
  "pageVlans": "vlans",
  "pagePpp": "ppp",
  "pageCapsman": "capsman",
  "pageBridges": "bridges",
  "pageDns": "dns",
  "pagePackages": "packages",
  "pageRosusers": "router-users",
  "pageQueues": "queues",
  "pageWan": "wan",
  "pageDevices": "devices",
  "pageAudit": "audit-trail",
  "pageBackups": "backups"
};

/**
 * The two EXPLICIT presets. `advanced` is absent on purpose — it is derived from
 * PAGE_NAV_MAP at use, exactly as the original derives it, "so a page added to
 * the nav joins Advanced by existing". A frozen list here would drop the next
 * page added from the preset, silently.
 */
export const VIEW_PRESETS: Record<string, string[]> = {
  "home": [
    "wifi",
    "wireless",
    "interfaces",
    "dhcp",
    "connections",
    "bandwidth"
  ],
  "standard": [
    "wifi",
    "wireless",
    "interfaces",
    "dhcp",
    "connections",
    "bandwidth",
    "topology",
    "dns",
    "vlans",
    "vpn",
    "firewall",
    "logs"
  ]
};

/** Where the chosen preset is remembered. */
export const VIEW_PRESET_KEY = "mkd_view_preset";
