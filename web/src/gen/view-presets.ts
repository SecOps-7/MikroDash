// GENERATED from testdata/view-presets.json — do not edit.
// Rebuild with `node tools/view-presets-ts.js` from the committed JSON.
// The JSON it reads is a FROZEN artefact: the generator that produced it read the
// Node app and was deleted with the port-parity harness on 2026-09-01. This
// transform still runs, so the .ts can be rebuilt from the committed JSON --
// but the JSON itself can only change by hand, or from `v0.7.40` in git history.

/** settings key -> page key. `pageWifi` is the checkbox; `wifi` is what a preset names. */
export const PAGE_NAV_MAP: Record<string, string> = {
  "pageWifi": "wifi",
  "pageWireless": "wireless",
  "pageInterfaces": "interfaces",
  "pageDhcp": "dhcp",
  "pageVpn": "vpn",
  "pageConnections": "connections",
  "pageFirewall": "firewall",
  "pageLogs": "logs",
  "pageBandwidth": "bandwidth",
  "pageRouting": "routing",
  "pageTopology": "topology",
  "pageVlans": "vlans",
  "pagePpp": "ppp",
  "pageCapsman": "capsman",
  "pageBridges": "bridges",
  "pageDns": "dns",
  "pagePackages": "packages",
  "pageRosusers": "rosusers",
  "pageQueues": "queues",
  "pageWan": "wan",
  "pageDevices": "devices",
  "pageAudit": "audit",
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
