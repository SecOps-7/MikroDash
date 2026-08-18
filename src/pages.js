'use strict';
/**
 * Page registry — the one place a page is defined (issue #108).
 *
 * A page used to be defined implicitly, in four lists that had to agree and
 * nothing checked: the nav markup, `PAGE_NAV_MAP` in app.js, `_PAGE_SETTING_KEYS`
 * and `boolFields` in index.js, and `_PAGE_STREAM_ROOMS`. `pageTopology` was in
 * some but not others, so the Topology toggle silently did nothing for a whole
 * release. Custom roles add a fifth consumer — the per-page permission matrix —
 * so the lists are derived from here instead of restated.
 *
 * Shape of an entry:
 * {
 *   key:         string,        // 'wireless' — matches data-page, #page-<key>, and the room suffix
 *   title:       string,        // display name, mirrored by PAGE_TITLES in app.js
 *   settingsKey: string|null,   // 'pageWireless' — the install-wide visibility toggle.
 *                               // null for the four pages that have no toggle and are
 *                               // governed by role alone: dashboard, reports, routers, settings.
 *   streamRooms: string[],      // rooms whose occupancy suspends/resumes this page's
 *                               // counter stream. Empty for pages with no suspendable stream.
 * }
 *
 * Ordered as the nav renders them, so a reader can check this against the UI.
 */

const { COLLECTORS } = require('./collection');

const PAGES = Object.freeze([
  { key: 'dashboard',   title: 'Dashboard',        settingsKey: null,              streamRooms: [] },
  { key: 'topology',    title: 'Network Topology', settingsKey: 'pageTopology',    streamRooms: ['page-topology'] },
  { key: 'wireless',    title: 'Wireless',         settingsKey: 'pageWireless',    streamRooms: ['page-wireless'] },
  { key: 'capsman',     title: 'CAPsMAN',          settingsKey: 'pageCapsman',     streamRooms: [] },
  { key: 'interfaces',  title: 'Interfaces',       settingsKey: 'pageInterfaces',  streamRooms: [] },
  { key: 'dhcp',        title: 'DHCP',             settingsKey: 'pageDhcp',        streamRooms: [] },
  { key: 'dns',         title: 'DNS',              settingsKey: 'pageDns',         streamRooms: [] },
  { key: 'vlans',       title: 'VLANs',            settingsKey: 'pageVlans',       streamRooms: [] },
  { key: 'bridges',     title: 'Bridges',          settingsKey: 'pageBridges',     streamRooms: [] },
  { key: 'vpn',         title: 'VPN',              settingsKey: 'pageVpn',         streamRooms: ['page-vpn', 'dash-card-vpn'] },
  // streamRooms is empty for ppp, vlans, capsman, bridges, dns, packages and
  // none of those collectors holds a /listen, and this list means "pages with a
  // suspendable counter stream". They are suspended by the idle gate instead,
  // and page:focus replays them explicitly.
  { key: 'ppp',         title: 'PPP',              settingsKey: 'pagePpp',         streamRooms: [] },
  { key: 'connections', title: 'Connections',      settingsKey: 'pageConnections', streamRooms: [] },
  { key: 'routing',     title: 'Routing',          settingsKey: 'pageRouting',     streamRooms: ['page-routing'] },
  { key: 'bandwidth',   title: 'Bandwidth',        settingsKey: 'pageBandwidth',   streamRooms: [] },
  { key: 'firewall',    title: 'Firewall',         settingsKey: 'pageFirewall',    streamRooms: ['page-firewall', 'dash-card-firewall'] },
  { key: 'logs',        title: 'Logs',             settingsKey: 'pageLogs',        streamRooms: [] },
  { key: 'packages',    title: 'Packages',         settingsKey: 'pagePackages',    streamRooms: [] },
  { key: 'reports',     title: 'Reports',          settingsKey: null,              streamRooms: [] },
  { key: 'routers',     title: 'Routers',          settingsKey: null,              streamRooms: [] },
  { key: 'settings',    title: 'Settings',         settingsKey: null,              streamRooms: [] },
]);

const BY_KEY = Object.freeze(Object.fromEntries(PAGES.map(p => [p.key, p])));
const KEYS   = Object.freeze(PAGES.map(p => p.key));

/** The install-wide visibility toggles, for the settings allow-list and broadcast. */
const SETTING_KEYS = Object.freeze(PAGES.map(p => p.settingsKey).filter(Boolean));

/**
 * page → rooms whose occupancy drives stream suspend/resume. Only the pages that
 * actually have a suspendable counter stream appear, which is what the caller
 * tests for before doing any work.
 */
const STREAM_ROOMS = Object.freeze(Object.fromEntries(
  PAGES.filter(p => p.streamRooms.length).map(p => [p.key, p.streamRooms])
));

/** Collector keys whose payload this page displays. Derived, never restated. */
function collectorsFor(pageKey) {
  return COLLECTORS.filter(c => c.page === pageKey).map(c => c.key);
}

/** The page a collector feeds, or null if it belongs to no single page. */
function pageForCollector(collectorKey) {
  const c = COLLECTORS.find(x => x.key === collectorKey);
  return c ? c.page : null;
}

module.exports = { PAGES, BY_KEY, KEYS, SETTING_KEYS, STREAM_ROOMS, collectorsFor, pageForCollector };
