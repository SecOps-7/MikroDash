'use strict';
/**
 * Places — validation, display and priority resolution (issue #96).
 *
 * A "place" is a city or town chosen from the gazetteer (src/cityIndex.js) or
 * derived from a WAN IP (src/geo.js). Both sources produce the same five fields,
 * which is the point: the manual picker and the automatic fix read the same
 * database, so they cannot disagree about where "Berlin, BE, DE" is.
 *
 * This module is separate from src/index.js on purpose. index.js calls
 * server.listen() at require time and cannot be loaded by a test, so validation
 * living only inside _parseSiteBody would be untestable. Everything here is pure.
 */

const NAME_MAX = 64;

/**
 * A coordinate, or null if the input is not one.
 *
 * Number(null), Number(undefined) and Number('') are 0, -0 and 0 — all finite, so
 * a bare Number.isFinite() check accepts a missing coordinate as the equator.
 * That is exactly the "0,0 in the Gulf of Guinea" failure migration 4 warned
 * about when it reserved the sites columns, so absence is rejected explicitly
 * before any coercion.
 */
function _coord(v) {
  if (v === null || v === undefined || v === '') return null;
  const n = Number(v);
  return Number.isFinite(n) ? n : null;
}

/**
 * Coerce untrusted input into a place, or null if it is not one.
 *
 * Returns null rather than throwing or partially accepting: a malformed place is
 * dropped exactly as _cleanSiteId drops a malformed site id, so a bad value can
 * never be written through to storage. Callers treat null as "no location".
 */
function normalizePlace(input) {
  if (!input || typeof input !== 'object' || Array.isArray(input)) return null;

  // The town is optional, because geoip genuinely does not always know one: an
  // address it can only place to a country comes back with an empty city and a
  // 1000 km accuracy radius. Requiring a name here would drop exactly those
  // fixes — the approximate ones the accuracy ring exists to show — and leave
  // the router unlocated instead of roughly located. The picker always supplies
  // a name, so this only ever relaxes the automatic path.
  const name = typeof input.name === 'string' ? input.name.trim() : '';
  if (name.length > NAME_MAX) return null;

  // geoip-lite reports country as ISO-3166-1 alpha-2. Upper-cased so a client
  // sending 'de' is accepted, but a three-letter code is not — that would be a
  // different standard and would not match anything the gazetteer returns.
  const cc = typeof input.cc === 'string' ? input.cc.trim().toUpperCase() : '';
  if (!/^[A-Z]{2}$/.test(cc)) return null;

  // Region is geoip's 3-byte subdivision field. Optional: several hundred places
  // in the database have none. It may be numeric (Japan's Hiroshima is '34') —
  // that is valid data, and formatPlace decides whether it is worth showing.
  const region = typeof input.region === 'string' ? input.region.trim() : '';
  if (!/^[A-Za-z0-9]{0,3}$/.test(region)) return null;

  const lat = _coord(input.lat);
  const lon = _coord(input.lon);
  if (lat === null || lat < -90 || lat > 90) return null;
  if (lon === null || lon < -180 || lon > 180) return null;

  return { name, region, cc, lat, lon };
}

/**
 * Human label for a place: "Berlin, BE, DE".
 *
 * A numeric region is dropped. Tens of thousands of places in the database carry
 * a numeric subdivision code, so keeping it would render "Motomachi, 34, JP" —
 * which reads as a typo rather than as information. An alphabetic code (BE, ENG,
 * IDF, MD) is what people recognise, so that is what survives.
 */
function formatPlace(place) {
  if (!place) return '';
  const parts = [];
  if (place.name) parts.push(place.name);
  // A region on its own says nothing useful ("NW"), so it only appears next to a
  // town it qualifies.
  if (place.name && place.region && /^[A-Za-z]/.test(place.region)) parts.push(place.region);
  if (place.cc) parts.push(place.cc);
  return parts.join(', ');
}

/**
 * Where a router is, and how confidently we know it.
 *
 * The priority order is the whole feature, so it lives in one function rather
 * than being re-derived in the renderer:
 *
 *   1. the router's own picked place   — a person said so about this router
 *   2. the cached fix from its WAN IP  — inferred, possibly a country centroid
 *   3. its site's picked place         — a person said so about the group
 *   4. null                            — the map's "no location" tray
 *
 * `site` is the sites row (or null). Returns null when nothing places the router.
 *
 * The returned `wanIp` is disclosure-controlled: /api/localcc withholds the WAN
 * address from callers without system:settings, so every caller of this function
 * must strip `wanIp` under the same condition. It is returned here rather than
 * omitted so the decision stays at the boundary that knows who is asking.
 */
function resolveLocation(router, site) {
  const geo = (router && router.geo) || null;

  const manual = geo ? normalizePlace(geo.place) : null;
  if (manual) {
    return { lat: manual.lat, lon: manual.lon, source: 'manual', label: formatPlace(manual) };
  }

  const auto = geo ? normalizePlace(geo.auto) : null;
  if (auto) {
    const km = Number(geo.auto.accuracyKm);
    return {
      lat: auto.lat,
      lon: auto.lon,
      source: 'auto',
      label: formatPlace(auto),
      accuracyKm: Number.isFinite(km) && km > 0 ? km : null,
      wanIp: typeof geo.auto.ip === 'string' ? geo.auto.ip : '',
    };
  }

  if (site) {
    const lat = _coord(site.lat);
    const lon = _coord(site.lon);
    // Both or neither. A row with one coordinate set is not a location, and must
    // not be read as the other one being zero.
    if (lat !== null && lon !== null
        && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180) {
      const sitePlace = normalizePlace({
        name: site.place_name, region: site.place_region, cc: site.place_cc, lat, lon,
      });
      // Migration 4 reserved lat/lon before there was a picker, so a row may
      // carry coordinates with no place name. Fall back to the site's own name
      // rather than showing an empty label.
      return {
        lat,
        lon,
        source: 'site',
        label: sitePlace ? formatPlace(sitePlace) : String(site.name || ''),
      };
    }
  }

  return null;
}

/**
 * What to do with a router's cached automatic location, given the WAN address we
 * can currently see and what geoip made of it.
 *
 * This lives here, as a pure function, because getting it wrong is invisible.
 * The first implementation folded two different situations together and cleared
 * the cache whenever there was no address to look at — which emptied the map of
 * every offline router, the ones the view exists to show. It took driving a
 * browser to notice. A three-way answer makes the distinction explicit, and
 * testable:
 *
 *   'keep'   no address to work from — the router is offline, or its interface
 *            status has not arrived. We have not learned anything new, so we
 *            must not forget what we already knew.
 *   'clear'  there IS an address and it cannot be placed (RFC1918, CGNAT,
 *            unallocated). The router has moved somewhere we cannot resolve, so
 *            a fix from its previous address is now a lie.
 *   'set'    a usable fix.
 *
 * `g` is a geoip-lite lookup result, or null.
 */
function autoGeoAction(wanIp, g, now) {
  if (!wanIp) return { action: 'keep' };
  if (!g || !g.ll || g.ll[0] === null || g.ll[1] === null
      || !Number.isFinite(Number(g.ll[0])) || !Number.isFinite(Number(g.ll[1]))) {
    return { action: 'clear' };
  }
  return {
    action: 'set',
    auto: {
      name:       g.city || '',
      region:     g.region || '',
      cc:         g.country || '',
      lat:        Number(g.ll[0]),
      lon:        Number(g.ll[1]),
      ip:         wanIp,
      // geoip's accuracy radius in km: about 5 for a real city fix, 1000 for a
      // country centroid. The map draws it so a guess never reads as a survey.
      accuracyKm: Number(g.area) || 0,
      ts:         now,
    },
  };
}

module.exports = { normalizePlace, formatPlace, resolveLocation, autoGeoAction, NAME_MAX };
