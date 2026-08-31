// GENERATED from testdata/pages-table.json — do not edit.
// Regenerate with `node tools/pages-table.js` then `node tools/pages-table-ts.js`.

/**
 * Digit -> page: pressing 3 opens PAGE_KEYS[2].
 *
 * ORDER IS THE ENTIRE MEANING, which is why it is generated rather than typed —
 * one transposition sends two shortcuts to each other's pages and reads as
 * completely normal.
 *
 * Only the first 9 are reachable: the handler parses a SINGLE keypress,
 * and no keypress produces "10". The rest are kept because the list is the live
 * app's, and a tenth becoming reachable would be a change there, not here.
 */
export const PAGE_KEYS: readonly string[] = [
  "dashboard",
  "wan",
  "wifi",
  "wireless",
  "capsman",
  "interfaces",
  "dhcp",
  "dns",
  "vlans",
  "bridges",
  "vpn",
  "ppp",
  "connections",
  "routing",
  "bandwidth",
  "firewall",
  "logs",
  "packages",
  "queues",
  "rosusers",
  "audit"
];

/**
 * Every page the visibility sweep considers, in the order it considers them.
 *
 * THE ORDER IS THE FALLBACK. When the page someone is standing on is taken away
 * from them, they are sent to the first page still visible — so reordering this
 * list silently changes where a demoted user lands. Generated for that reason,
 * and pinned to the nav items that carry the same keys: an entry with no nav
 * item is a page the sweep believes it hid and did not.
 */
export const ALL_NAV_PAGES: readonly string[] = [
  "dashboard",
  "wan",
  "interfaces",
  "vlans",
  "bridges",
  "topology",
  "wifi",
  "wireless",
  "capsman",
  "dhcp",
  "dns",
  "routing",
  "ppp",
  "vpn",
  "bandwidth",
  "queues",
  "connections",
  "firewall",
  "rosusers",
  "logs",
  "packages",
  "devices",
  "reports",
  "audit",
  "backups",
  "settings"
];
