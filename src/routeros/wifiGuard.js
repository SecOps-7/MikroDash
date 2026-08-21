'use strict';
/**
 * The inherited-profile guard, for /interface/wifi.
 *
 * On the modern wireless stack an interface can take its SSID, security and
 * channel from a shared `/interface/wifi/configuration` profile rather than
 * carrying them inline. Writing one of those values onto the INTERFACE does not
 * edit the profile — it creates a local override that shadows it. On the radio
 * you are looking at that is exactly what you asked for. On its sibling, which
 * is still following the profile, it is a silent divergence: the two SSIDs that
 * used to move together stop doing so, and nothing on screen said as much.
 *
 * So this warns, and only for the case that is actually surprising.
 *
 * IT DOES NOT FIRE FOR A PROFILE ONLY ONE INTERFACE USES. An override there
 * splits nothing — there is no sibling to diverge from — and a prompt on every
 * save of a defconf router is how a warning becomes furniture people learn to
 * click through. resources.js says the same thing about the VLAN guard, and
 * queueGuard.js says it at more length.
 *
 * Detecting inheritance at all is a comparison rather than a lookup: RouterOS's
 * `print detail config` (directly-set values only) has no dependable binary-API
 * equivalent, so src/collectors/wifi.js decides a field is inherited when the
 * profile defines it and the interface's effective value still equals it. That
 * fails toward "not inherited", which suppresses a warning rather than blocking
 * a write — the right direction for something that is only ever advisory.
 */

/**
 * The fields that can be inherited, mapped to the interface key carrying their
 * effective value. A dotted key is how RouterOS reports an inherited value, so
 * this is also the list of things an override would shadow.
 */
const INHERITABLE = Object.freeze({
  ssid:       'configuration.ssid',
  authTypes:  'security.authentication-types',
  passphrase: 'security.passphrase',
  band:       'channel.band',
  frequency:  'channel.frequency',
  width:      'channel.width',
});

const _none = () => ({ level: 'none', code: null, detail: null, fingerprint: null });

function _fingerprint(profile, fields, sharedBy) {
  return JSON.stringify(['wifi-inherit', String(profile || ''), fields.slice().sort(), sharedBy]);
}

/**
 * Would this write override a profile more than one interface shares?
 *
 * `before` is the RAW freshly-read RouterOS row, as every other guard receives
 * it. `values` are the validated submission. `siblings` is every row in the
 * menu, so the share count comes from the same read the write is checked
 * against rather than from the collector's last tick.
 *
 * Returns the shared warn shape: { level, code, detail, fingerprint }.
 */
function checkInherit({ values, before, siblings, action }) {
  // A create cannot override anything — there is no existing row whose values
  // came from a profile. A delete removes the interface, profile and all.
  if (action !== 'update' || !before) return _none();

  const profile = String(before.configuration || '');
  if (!profile) return _none();

  // How many interfaces follow this profile. One is not a divergence.
  const sharedBy = (siblings || [])
    .filter(r => r && String(r.configuration || '') === profile).length;
  if (sharedBy < 2) return _none();

  // Which submitted fields are both inherited today and actually changing.
  const changing = [];
  for (const [field, rosKey] of Object.entries(INHERITABLE)) {
    if (!Object.prototype.hasOwnProperty.call(values || {}, field)) continue;
    const next = String(values[field] == null ? '' : values[field]);
    // A passphrase is write-only: it is never read back, so "is it changing"
    // cannot be answered by comparison. A non-empty one is always a change, and
    // a blank one means leave it alone.
    if (field === 'passphrase') { if (next) changing.push(field); continue; }
    const current = String(before[rosKey] == null ? '' : before[rosKey]);
    if (next !== current) changing.push(field);
  }
  if (!changing.length) return _none();

  return {
    level: 'warn',
    code: 'wifi-inherit',
    detail: {
      profile,
      sharedBy,
      fields: changing,
      interface: String(before.name || ''),
    },
    fingerprint: _fingerprint(profile, changing, sharedBy),
  };
}

module.exports = { checkInherit, INHERITABLE };
