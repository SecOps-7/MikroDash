import type { City } from './city-picker';
import { t } from '../i18n';
// The Add/Edit Router dialog's pure halves.
//
// ── PART ONE OF THE MODAL: THE VALUE MAPPING ────────────────────────────────
//
// The dialog is a form, a geo picker and a live connection test. This file is the part that has no DOM in it at all: how
// a stored router becomes form values, and how form values become a stored
// router. Both directions are pure, so both are comparable against the original
// without a browser.
//
// The rest of the modal — the picker and the test button — comes after,
// and will call these.

/** A bandwidth figure as the form holds it: a number and a unit. */
export interface BwField { value: number; unit: 'mbps' | 'gbps' }

/**
 * Split a stored Mbps figure into the number and unit the form shows.
 *
 * `% 1000 === 0` IS THE RULE, and it is chosen rather than a magnitude test: a
 * round thousand is what an operator typed as Gbps, so it comes back as Gbps.
 * 1500 stays 1500 Mbps rather than becoming 1.5 Gbps, because the form's number
 * input holds integers and 1.5 would not survive the round trip.
 *
 * ZERO IS A ROUND THOUSAND, arithmetically — `0 % 1000 === 0` — so a stored 0
 * would display as "0 Gbps". It cannot arrive: `splitBw` is only ever called on
 * a value the caller has already defaulted with `|| 1000`, and 0 is falsy. The
 * behaviour is reproduced rather than guarded, because adding a guard the
 * original lacks is how a port drifts.
 */
export function splitBw(mbps: number): BwField {
  if (mbps % 1000 === 0) return { value: mbps / 1000, unit: 'gbps' };
  return { value: mbps, unit: 'mbps' };
}

/**
 * The inverse: what the form's number and unit mean in Mbps.
 *
 * `parseInt(…) || 1` is the original's, and the `|| 1` is doing real work here
 * unlike some of its siblings: an empty or unparseable box becomes 1 Mbps, not
 * 0. A zero would make every utilisation bar on the Routers page divide by it.
 */
export function joinBw(raw: string, unit: string): number {
  const v = parseInt(raw, 10) || 1;
  return unit === 'gbps' ? v * 1000 : v;
}

/** A router as the store holds it, in the fields this dialog edits. */
export interface StoredRouter {
  id?: string; label?: string; siteId?: string; siteIds?: string[];
  geo?: { place?: GeoPlace | null; auto?: GeoPlace | null };
  host?: string; port?: number | string; username?: string;
  defaultIf?: string; pingTarget?: string;
  tls?: boolean; tlsInsecure?: boolean;
  alertsEnabled?: boolean; reportingEnabled?: boolean; connDownThresholdSec?: number;
  bwDownMbps?: number; bwUpMbps?: number;
  collection?: { mode?: string };
}

/** Every value the form shows, for one router or for a fresh Add. */
export interface RouterFormValues {
  title: string;
  id: string; label: string; siteIds?: string[]; primarySite?: string;
  host: string; port: string; username: string;
  passPlaceholder: string;
  defaultIf: string; pingTarget: string;
  tls: boolean; tlsInsecure: boolean;
  alertsEnabled: boolean; reportingEnabled: boolean; downThreshold: number;
  bwDown: BwField; bwUp: BwField;
  mode: string;
}

/**
 * Seed the form from a router, or from nothing for an Add.
 *
 * THE DEFAULTS ARE NOT DECORATION. Port 8729 is the API-over-TLS port and 8728
 * is plaintext, so defaulting to 8729 with `tls` true is a security default that
 * a port silently changing would weaken every router added afterwards. `admin`,
 * `ether1` and `1.1.1.1` are the original's too.
 *
 * A SITE THAT HAS SINCE BEEN DELETED falls back to none, rather than leaving the
 * picker showing whatever was selected before — `knownSites` is what the caller
 * has, and a stored id absent from it is treated as no site at all.
 *
 * `connDownThresholdSec` uses an `!== undefined` test and not `||`, because 0 is
 * a legitimate value meaning "no debounce" and `|| 30` would silently turn it
 * back into thirty seconds.
 */
export function routerFormValues(
  router: StoredRouter | null, knownSites: Record<string, unknown>,
): RouterFormValues {
  const r = router;
  const coll = (r && r.collection) || {};
  return {
    // 'Device', not 'Router': #117 renamed the page and the dialog with it —
    // a fleet holds switches too.
    title: r ? t('Edit Device') : t('Add Device'),
    id: r ? (r.id || '') : '',
    label: r ? (r.label || '') : '',
    // #117: MEMBERSHIP, not one site. The singular `rtrModalSite` select is gone
    // from the dialog — upstream replaced it with a multi-select — so what the
    // form seeds is the set of ids to mark selected, plus which one is primary.
    //
    // A site the viewer's cache does not know is NOT selected, exactly as the
    // live seeding tests `_have.indexOf(o.value) !== -1 && !!_sitesById[o.value]`.
    // That means a device in a DELETED site opens with that membership unshown —
    // and saving from there drops it. Reproduced, not corrected.
    siteIds: siteIdsForSeed(r, knownSites),
    primarySite: primarySiteForSeed(r, knownSites),
    host: r ? (r.host || '') : '',
    port: r ? String(r.port ?? '') : '8729',
    username: r ? (r.username || '') : 'admin',
    // An edit never shows the stored password and says so; an Add has nothing
    // to explain.
    passPlaceholder: r ? t('leave blank to keep current') : '',
    defaultIf: r ? (r.defaultIf || '') : 'ether1',
    pingTarget: r ? (r.pingTarget || '') : '1.1.1.1',
    tls: r ? !!r.tls : true,
    tlsInsecure: r ? !!r.tlsInsecure : false,
    alertsEnabled: r ? !!r.alertsEnabled : false,
    // ── ON BY DEFAULT WHEN ADDING, OFF FOR A RECORD THAT SAYS NOTHING ──────
    //
    // `r` is null on Add, and a device somebody is adding almost certainly wants
    // its history kept — the Reports page is empty without it.
    //
    // For an EDIT the record decides, and absent is off: that is what
    // `store.ReportingOn` answers server-side, and the startup migration is what
    // gives an upgrading install the value it had before the flag existed. A
    // `!== undefined` dance here would disagree with the server about a router
    // the migration has not reached.
    reportingEnabled: r ? !!r.reportingEnabled : true,
    downThreshold: r ? (r.connDownThresholdSec !== undefined ? r.connDownThresholdSec : 30) : 30,
    bwDown: splitBw(r ? (r.bwDownMbps || 1000) : 1000),
    bwUp: splitBw(r ? (r.bwUpMbps || 1000) : 1000),
    mode: coll.mode || 'stream',
  };
}


/** The raw form values the save reads back. Strings, as the DOM holds them. */
export interface RouterFormInput {
  // siteIds is the device's STORED membership, already normalised by
  // `storedSiteIds` — an array (possibly empty) for a known device, and absent
  // ONLY when this browser has no record for it. The two are different
  // statements; see `siteIdsForSave`.
  id: string; label: string; siteIds?: string[]; primarySite?: string;
  geoPlace: unknown;
  host: string; port: string; username: string; password: string;
  defaultIf: string; pingTarget: string;
  tls: boolean; tlsInsecure: boolean;
  bwDownRaw: string; bwDownUnit: string;
  bwUpRaw: string; bwUpUnit: string;
  alertsEnabled: boolean; reportingEnabled: boolean; downThresholdRaw: string;
  mode: string;
}

/**
 * The body a save sends.
 *
 * ── THREE RULES HERE ARE LOAD-BEARING, AND ONE PROTECTS STORED DATA ─────────
 *
 * 1. **`geo` carries `place` and NEVER `auto`.** The store reads an absent
 *    `auto` as "keep what you learned", so sending one would let a save race the
 *    background geo refresh and discard what it found.
 *
 * 2. **`collection` carries the delivery mode only.** Per-router collector
 *    switching was removed on 2026-09-15, so a save sends no `off` list, and one
 *    an older install stored is ignored by the server.
 *
 * 3. **`connDownThresholdSec` is clamped to 0..300, falling back to 30.** An
 *    empty box parses to NaN and `NaN >= 0` is false, so it takes the default —
 *    while an explicit 0 passes, because 0 means "no debounce" and is a value an
 *    operator can legitimately want.
 *
 * The password is the one string NOT trimmed: leading or trailing whitespace in
 * a password is part of the password.
 *
 * `mode` is passed through verbatim — see the assignment for why the original's
 * `'stream'` fallback is not an empty-value default.
 */
/**
 * The site ids the dialog should show as selected.
 *
 * Normalises the record the way `_rtrSiteIds` does — an ARRAY wins outright,
 * even when empty — and then keeps only the ids the viewer's cache knows,
 * because an option that does not exist cannot be selected.
 */
export function siteIdsForSeed(
  r: { siteIds?: string[]; siteId?: string | null } | null | undefined,
  knownSites: Record<string, unknown>,
): string[] {
  if (!r) return [];
  const have = Array.isArray(r.siteIds) ? r.siteIds : (r.siteId ? [r.siteId] : []);
  return have.filter((id) => !!knownSites[id]);
}

/**
 * Which site the primary select should show.
 *
 * The FIRST of the device's ids, and only when the cache knows it — the live
 * seeding guards `_have.length && _sitesById[_have[0]]`. An unknown first site
 * leaves the control alone rather than falling through to the second, which
 * would silently move the geo tier to a different place.
 */
export function primarySiteForSeed(
  r: { siteIds?: string[]; siteId?: string | null } | null | undefined,
  knownSites: Record<string, unknown>,
): string {
  if (!r) return '';
  const have = Array.isArray(r.siteIds) ? r.siteIds : (r.siteId ? [r.siteId] : []);
  return have.length && knownSites[have[0]!] ? have[0]! : '';
}

/**
 * The ids to SAVE, ordered with the primary first.
 *
 * THE ORDER IS THE PRIMARY — there is no separate field. The server keeps the
 * scalar `siteId` in step as a rollback mirror by reading the first entry, so
 * putting the wrong id there moves the device's map location.
 *
 * A primary that is not among the chosen is IGNORED rather than prepended:
 * selecting a site and then deselecting it leaves the primary select showing a
 * stale value, and honouring it would save a membership the operator had just
 * removed.
 */
/**
 * The membership a save carries: the device's STORED list, with the chosen
 * primary moved to the head.
 *
 * ── IT REORDERS. IT NEVER REBUILDS ─────────────────────────────────────────
 *
 * Upstream 76afa49 took membership out of the device modal — it is an
 * authorization decision and lives in Access Management. What remains is a
 * primary picker offering only the sites the device is already in, so the list
 * saved must come from the RECORD and not from the control. A site deleted since
 * the device was filed has no name to show and is deliberately absent from the
 * picker; rebuilding from its options would silently un-file the device.
 *
 * ── AND AN UNKNOWN DEVICE SAVES `undefined`, NOT `[]` ──────────────────────
 *
 * The server reads ABSENT as "leave membership alone" and an EMPTY ARRAY as
 * "remove every site this device is in". This function used to take
 * `chosen: string[]` and its caller did `f.siteIds ?? []` — which turned "we do
 * not know this device" into "remove all its sites". The `??` was the whole bug:
 * the same falsy-versus-absent shape as `limit || 200`.
 *
 * `siteId` is the pre-multi-site scalar, normalised here so a record written
 * before #117 still saves its one site.
 */
export function siteIdsForSave(
  stored: string[] | undefined,
  primary: string,
): string[] | undefined {
  // ABSENT IN, ABSENT OUT. `undefined` here means "this browser has no record
  // for the device", which is the only case the server must read as "leave
  // membership alone".
  if (!stored) return undefined;
  // A KNOWN device with no sites saves an EMPTY ARRAY, which is a different
  // statement: it has none, and the server may act on that. Collapsing the two
  // is the destructive direction.
  const have = stored.slice();
  if (primary && have.indexOf(primary) !== -1) {
    return [primary].concat(have.filter((id) => id !== primary));
  }
  return have;
}

/**
 * A record's stored membership, normalised — or `undefined` when there is no
 * record at all.
 *
 * `siteId` is the pre-multi-site scalar, so a device filed before #117 still
 * saves its one site. A record that exists with NEITHER field yields an empty
 * array, not undefined: it is a device with no sites, which is a fact, where
 * undefined is an absence of knowledge.
 */
export function storedSiteIds(
  rec: { siteIds?: string[]; siteId?: string } | null | undefined,
): string[] | undefined {
  if (!rec) return undefined;
  if (Array.isArray(rec.siteIds)) return rec.siteIds.slice();
  return rec.siteId ? [rec.siteId] : [];
}

export function collectRouterForm(f: RouterFormInput): Record<string, unknown> {
  const thresh = parseInt(f.downThresholdRaw, 10);
  const body: Record<string, unknown> = {
    id: f.id.trim(),
    label: f.label.trim(),
    // #117: the membership, ordered with the primary first — that order IS the
    // primary, and the server derives the scalar `siteId` mirror from the head.
    //
    // NO `?? []` HERE. An absent list must stay absent; see `siteIdsForSave`.
    siteIds: siteIdsForSave(f.siteIds, f.primarySite ?? ''),
    geo: { place: f.geoPlace ?? null },
    host: f.host.trim(),
    port: parseInt(f.port, 10),
    username: f.username.trim(),
    password: f.password,
    defaultIf: f.defaultIf.trim(),
    pingTarget: f.pingTarget.trim(),
    tls: f.tls,
    tlsInsecure: f.tlsInsecure,
    bwDownMbps: joinBw(f.bwDownRaw, f.bwDownUnit),
    bwUpMbps: joinBw(f.bwUpRaw, f.bwUpUnit),
    alertsEnabled: !!f.alertsEnabled,
    // SEEDED AND COLLECTED, both. A field seeded above and missing here is
    // shown to the operator, edited, and thrown away on save — the silent-loss
    // shape this file's own history records.
    reportingEnabled: !!f.reportingEnabled,
    connDownThresholdSec: (thresh >= 0 && thresh <= 300) ? thresh : 30,
  };
  body.collection = {
    // `f.mode` VERBATIM, with no `|| 'stream'`. The original's fallback is
    // `modalMode ? modalMode.value : 'stream'` — for a MISSING ELEMENT, not an
    // empty value — so an empty mode is sent as ''. The caller supplies
    // 'stream' when the element is absent, which is where the original puts that
    // decision.
    mode: f.mode,
  };
  return body;
}


/**
 * A place as the picker holds it — the SAME shape as a searched town.
 *
 * They were declared separately at first and disagreed about whether `name` is
 * optional, which the compiler caught the moment the two met at the modal's
 * seam. A stored place always has a name: it came either from the picker (which
 * only commits search results) or from the geo lookup (which supplies one).
 */
export type GeoPlace = City;

/** A site row, in the fields the picker reads. */
export interface SiteRow {
  // `name` is what the site is CALLED, and it was missing from this type until
  // the primary-site picker needed it (upstream 76afa49). The live
  // `window._sitesById` holds whole rows from `db.listSites()` — id, name,
  // description, coordinates and place fields together — so this type was
  // narrower than the object it describes, not different from it.
  //
  // Optional because every existing reader wants only the geo half and would
  // otherwise have to supply a name it does not use.
  name?: string;
  // NULL IS ALLOWED, and it is not laxness. These rows come from
  // `db.listSites()`, which is SQLite: a column nobody has filled in is NULL,
  // and `sites:update` puts that null on the wire verbatim. The type said
  // `string | undefined` and the object had always held `string | null`, which
  // is the same "narrower than the object it describes" problem the note above
  // records for `name` — it only surfaced when the Devices page's modal became
  // the second reader.
  //
  // Every reader here already tests truthiness (`place_name ? ... : ...`), so
  // null and undefined were being handled identically all along; what changes is
  // that TypeScript now agrees.
  place_name?: string | null; place_region?: string | null; place_cc?: string | null;
  lat?: number | null; lon?: number | null;
}

/**
 * What the geo picker shows when the dialog opens, and the hint beneath it.
 *
 * ── FOUR SOURCES, IN STRICT PRECEDENCE ──────────────────────────────────────
 *
 *   place   an override the operator set earlier          → SET
 *   auto    what the server worked out from the WAN IP    → PREVIEW
 *   site    the router's site, if it has a place          → PREVIEW
 *   none    nothing known                                 → CLEAR
 *
 * SET AND PREVIEW ARE DIFFERENT STATES, not two words for filled-in. A preview
 * shows what is already true without becoming an override — the original says it
 * "only becomes an override once something else is picked" — so a router already
 * on the map does not sit next to an empty box, and merely opening the dialog
 * does not convert an automatic location into a manual one.
 *
 * `place` BEATS `auto` and that ordering is the whole point: an operator who
 * corrected a bad geolocation must not have it silently replaced the next time
 * the background refresh finds something.
 *
 * The last hint explains WHY there is nothing rather than just saying so — a
 * private or CGNAT WAN address cannot be geolocated, and without that sentence
 * the empty box reads as a bug.
 */
export interface GeoSeed {
  mode: 'set' | 'preview' | 'clear';
  value: GeoPlace | null;
  hint: string;
}

export function seedGeoPicker(
  geo: { place?: GeoPlace | null; auto?: GeoPlace | null } | null | undefined,
  site: SiteRow | null | undefined,
  esc: (s: string) => string,
): GeoSeed {
  const g = geo || {};
  if (g.place) {
    return {
      mode: 'set', value: g.place,
      hint: t('Set here.') + ' <span class="text-muted">' + t('Clear it to go back to the automatic location.') + '</span>',
    };
  }
  if (g.auto) {
    return {
      mode: 'preview', value: g.auto,
      hint: '<span class="text-muted">' + t('Found automatically')
        + (g.auto.ip ? ' from ' + esc(g.auto.ip) : '')
        + '. Pick a different town to override it.</span>',
    };
  }
  if (site && site.place_name) {
    return {
      mode: 'preview',
      value: {
        name: site.place_name, region: site.place_region || '',
        // `?? undefined` because a site row's coordinates are SQLite columns and
        // arrive as null when unset, while the preview's own shape uses
        // undefined for absent. The two behaved identically here already —
        // everything downstream tests truthiness — so this narrows the type
        // without moving a value.
        cc: site.place_cc || '', lat: site.lat ?? undefined, lon: site.lon ?? undefined,
      },
      hint: '<span class="text-muted">' + t('From this router\u2019s site, {place}. Pick a town to override it.', { place: esc(site.place_name) }) + '</span>',
    };
  }
  return {
    mode: 'clear', value: null,
    hint: '<span class="text-muted">' + t('No location yet. A private or CGNAT WAN address cannot be geolocated \u2014 pick a town instead.') + '</span>',
  };
}


/**
 * The connection-test result message.
 *
 * The board name is appended only when the router reported one — a bare
 * "Connected" is what a device that did not is entitled to, and an empty dash
 * would read as a missing value rather than an absent one.
 */
export function testResultMessage(
  ok: boolean, boardName: string | undefined, error: string | undefined,
): string {
  if (ok) return t('\u2713 Connected') + (boardName ? ' \u2014 ' + boardName : '');
  return '\u2717 ' + (error || t('Connection failed'));
}

/**
 * The label after a successful test.
 *
 * Auto-fill ONLY into an empty box. A label the operator typed is theirs; the
 * board name is a convenience for the common case of adding a router and not
 * caring what it is called. `trim()` is what makes a box of spaces count as
 * empty.
 */
export function labelAfterTest(current: string, boardName: string | undefined): string {
  if (boardName && !current.trim()) return boardName;
  return current;
}

/**
 * Whether Save may write, or must test first.
 *
 * ── EDITING ANY FIELD INVALIDATES A PASSING TEST ────────────────────────────
 *
 * This is the rule worth having. Without it an operator could test against one
 * host, change the host — or the username, or the password, or the TLS
 * checkbox — and save credentials that were never tried against the router they
 * now name. The live app binds `input` on every text field and `change` on both
 * TLS boxes to exactly this reset.
 *
 * `invalidate` is deliberately a no-op when nothing had passed: the live guard
 * is `if (_testPassed)`, so typing in a form that has not been tested does not
 * hide a result that is not there.
 */
export class TestGate {
  private passed = false;

  /** A successful test. */
  pass(): void { this.passed = true; }

  /**
   * A field changed. Returns whether anything was invalidated, which is what
   * tells the caller to hide the result banner.
   */
  invalidate(): boolean {
    if (!this.passed) return false;
    this.passed = false;
    return true;
  }

  /** True when Save can write without testing again. */
  maySaveDirectly(): boolean { return this.passed; }
}
