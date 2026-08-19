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
 *                               // null for the three pages that have no toggle and are
 *                               // governed by role alone: dashboard, reports, settings.
 *   streamRooms: string[],      // rooms whose occupancy suspends/resumes this page's
 *                               // counter stream. Empty for pages with no suspendable stream.
 *   category:    string|null,   // nav category key, or null to sit at the top level
 * }
 *
 * Ordered as the nav renders them — grouped, category by category — so a reader
 * can check this against the sidebar. test/page-registry.test.js enforces that.
 */

const { COLLECTORS } = require('./collection');

/**
 * Nav categories, in render order.
 *
 * The sidebar is a 52px icon rail that widens on hover, and at 23 pages it ran
 * out of vertical room and clipped. Grouping is what buys that room back: 12
 * rows collapsed instead of 23.
 *
 * Loosely WinBox's own menu, deliberately not exactly it — MikroDash has pages
 * WinBox has no equivalent for (Audit, Reports, Routers), and WinBox has whole
 * menus MikroDash does not.
 */
const CATEGORIES = Object.freeze([
  { key: 'network',  title: 'Network'     },
  { key: 'wireless', title: 'Wireless'    },
  { key: 'ipsvc',    title: 'IP Services' },
  { key: 'tunnels',  title: 'Tunnels'     },
  { key: 'traffic',  title: 'Traffic'     },
  { key: 'security', title: 'Security'    },
  { key: 'system',   title: 'System'      },
]);

const CATEGORY_KEYS = Object.freeze(CATEGORIES.map(c => c.key));

const PAGES = Object.freeze([
  // Top level, and deliberately so: the one page everything starts from.
  { key: 'dashboard',   title: 'Dashboard',        settingsKey: null,              streamRooms: [],                                  category: null },

  // First in Network because it answers the question people open the sidebar to
  // ask — is the uplink up, and which one is carrying traffic.
  { key: 'wan',         title: 'WAN',              settingsKey: 'pageWan',         streamRooms: ['page-wan'],                                  category: 'network' },
  { key: 'interfaces',  title: 'Interfaces',       settingsKey: 'pageInterfaces',  streamRooms: [],                                  category: 'network' },
  { key: 'vlans',       title: 'VLANs',            settingsKey: 'pageVlans',       streamRooms: ['page-vlans'],                                  category: 'network' },
  { key: 'bridges',     title: 'Bridges',          settingsKey: 'pageBridges',     streamRooms: ['page-bridges'],                                  category: 'network' },
  // Topology maps what is connected, not what is flowing, which is why it sits
  // with the interfaces rather than with Traffic.
  { key: 'topology',    title: 'Network Topology', settingsKey: 'pageTopology',    streamRooms: ['page-topology'],                    category: 'network' },

  { key: 'wireless',    title: 'Wireless',         settingsKey: 'pageWireless',    streamRooms: ['page-wireless'],                    category: 'wireless' },
  { key: 'capsman',     title: 'CAPsMAN',          settingsKey: 'pageCapsman',     streamRooms: ['page-capsman'],                                  category: 'wireless' },

  { key: 'dhcp',        title: 'DHCP',             settingsKey: 'pageDhcp',        streamRooms: [],                                  category: 'ipsvc' },
  { key: 'dns',         title: 'DNS',              settingsKey: 'pageDns',         streamRooms: ['page-dns'],                                  category: 'ipsvc' },
  { key: 'routing',     title: 'Routing',          settingsKey: 'pageRouting',     streamRooms: ['page-routing'],                     category: 'ipsvc' },

  // streamRooms means "suspend this collector when nobody occupies these rooms".
  //
  // It once held only the five pages with an =interval=N counter stream, and
  // every page added afterwards declared []. That was fair while those
  // collectors were a slow poll and nothing else — but they grew /listen
  // channels and 5-second ticks, and the result was four collectors polling
  // every 5s and six idle /listen channels held open against the router while
  // somebody sat on the Dashboard. AI_CONTEXT is explicit that concurrent open
  // channels, not data volume, are what overwhelm small hardware.
  //
  // So a page-scoped collector names its own room. The idle gate still stops
  // everything when the last viewer of the router disconnects; this is the finer
  // gate for when somebody is here but looking at another page.
  { key: 'ppp',         title: 'PPP',              settingsKey: 'pagePpp',         streamRooms: ['page-ppp'],                                  category: 'tunnels' },
  { key: 'vpn',         title: 'VPN',              settingsKey: 'pageVpn',         streamRooms: ['page-vpn', 'dash-card-vpn'],        category: 'tunnels' },

  { key: 'bandwidth',   title: 'Bandwidth',        settingsKey: 'pageBandwidth',   streamRooms: [],                                  category: 'traffic' },
  { key: 'queues',      title: 'Queues',           settingsKey: 'pageQueues',      streamRooms: ['page-queues'],                                  category: 'traffic' },
  { key: 'connections', title: 'Connections',      settingsKey: 'pageConnections', streamRooms: [],                                  category: 'traffic' },

  // Firewall and Router Users are both access control — one for traffic, one for
  // people — which is a more useful neighbourhood than filing them under IP and
  // System respectively.
  { key: 'firewall',    title: 'Firewall',         settingsKey: 'pageFirewall',    streamRooms: ['page-firewall', 'dash-card-firewall'], category: 'security' },
  { key: 'rosusers',    title: 'Router Users',     settingsKey: 'pageRosusers',    streamRooms: ['page-rosusers'],                                  category: 'security' },

  { key: 'logs',        title: 'Logs',             settingsKey: 'pageLogs',        streamRooms: [],                                  category: 'system' },
  { key: 'packages',    title: 'Packages',         settingsKey: 'pagePackages',    streamRooms: ['page-packages'],                                  category: 'system' },

  // The last four stay at the top level whatever else moves. Audit and Reports
  // are both history but answer different questions — Reports is per-router
  // telemetry gated on router:history, and half the audit rows have no router at
  // all — so neither belongs inside the other, nor under System.
  { key: 'routers',     title: 'Routers',          settingsKey: 'pageRouters',     streamRooms: [],                                  category: null },
  { key: 'reports',     title: 'Reports',          settingsKey: null,              streamRooms: [],                                  category: null },
  { key: 'audit',       title: 'Audit',            settingsKey: 'pageAudit',       streamRooms: [],                                  category: null },
  { key: 'settings',    title: 'Settings',         settingsKey: null,              streamRooms: [],                                  category: null },
]);

const BY_KEY = Object.freeze(Object.fromEntries(PAGES.map(p => [p.key, p])));
const KEYS   = Object.freeze(PAGES.map(p => p.key));

/** The install-wide visibility toggles, for the settings allow-list and broadcast. */
const SETTING_KEYS = Object.freeze(PAGES.map(p => p.settingsKey).filter(Boolean));

/**
 * Canned view presets — the Home / Standard / Advanced tiers.
 *
 * Lists of PAGE KEYS, not settings keys, because both consumers work in page
 * keys: the Visible Pages grid maps them through the settings key, and the role
 * editor's matrix is keyed on the page itself.
 *
 * Only the ON set is listed. Whatever a preset does not name is turned OFF,
 * derived from SETTING_KEYS at apply time rather than written out here — the
 * polling profiles listed both halves by hand and silently stopped covering
 * seven sliders as pages were added (public/app.js:4595). A list that can only
 * be incomplete in one direction cannot drift the same way.
 *
 * `dashboard` is in every tier implicitly: it has no toggle and is always
 * visible. `routers` is Advanced-only on purpose — fleet management is a
 * professional feature, and it is why that page gained a toggle at all.
 */
const VIEW_PRESETS = Object.freeze({
  home:     Object.freeze(['wireless', 'interfaces', 'dhcp', 'connections', 'bandwidth']),
  standard: Object.freeze(['wireless', 'interfaces', 'dhcp', 'connections', 'bandwidth',
                           'topology', 'dns', 'vlans', 'vpn', 'firewall', 'logs']),
  // Everything with a toggle. Derived so a new page joins Advanced by existing,
  // which is the tier a new page belongs in until somebody decides otherwise.
  advanced: Object.freeze(PAGES.filter(p => p.settingsKey).map(p => p.key)),
});

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

module.exports = {
  VIEW_PRESETS, PAGES, BY_KEY, KEYS, SETTING_KEYS, STREAM_ROOMS, collectorsFor, pageForCollector,
  CATEGORIES, CATEGORY_KEYS };
