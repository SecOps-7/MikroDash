'use strict';
/**
 * The fleet-push guard, for the CAPsMAN profile menus.
 *
 * Everything else this engine writes has a blast radius you can see: a firewall
 * rule affects one chain, a VLAN one interface. A CAPsMAN profile is different.
 * MikroTik's documentation is explicit — "if you adjust any configuration
 * profile that is linked to the provisioned interface, all changes will be
 * pushed as soon as you apply changes to the profile". Saving a passphrase here
 * reconnects every client on every CAP that follows it, and nothing on screen
 * would otherwise say so.
 *
 * WHAT IT DOES NOT DO. It stays silent when nothing ENABLED references the
 * profile. An unused profile is an unused profile, and a prompt on every save of
 * one is how a warning becomes furniture people click through — the same
 * reasoning resources.js applies to the VLAN guard and wifiGuard.js applies to
 * shared configuration profiles. It is also silent for the provisioning menu
 * itself, which is why that resource declares no guard at all: a provisioning
 * rule creates interfaces when a CAP joins, it does not push to the ones already
 * running.
 *
 * REFERENCES RESOLVE ONE OR TWO LEVELS. A configuration profile is named
 * directly by a provisioning rule. A security, channel or datapath profile is
 * named by a CONFIGURATION profile, which is then named by a rule — so those
 * three resolve transitively, and a profile referenced only by an unprovisioned
 * configuration is correctly silent.
 */

const _none = () => ({ level: 'none', code: null, detail: null, fingerprint: null });

const _bool = (v) => v === true || v === 'true' || v === 'yes';

/** RouterOS comma lists arrive as one string. */
const _split = (v) => String(v == null ? '' : v)
  .split(',').map(s => s.trim()).filter(Boolean);

/** Which resource key maps to which field of a configuration profile. */
const CONFIG_FIELD = Object.freeze({
  capsSecurity: 'security',
  capsChannel:  'channel',
  capsDatapath: 'datapath',
});

/**
 * The enabled provisioning rules that would push this profile.
 *
 * `configRows` and `provRows` are RAW RouterOS rows, read by the caller in the
 * same tick as the write is checked — not the collector's last tick, which may
 * be two minutes old.
 */
function referencingRules({ resourceKey, name, configRows, provRows }) {
  if (!name) return [];

  // Which configuration profiles are in play. For capsConfig it is the profile
  // itself; for the other three it is every configuration naming it.
  let configNames;
  if (resourceKey === 'capsConfig') {
    configNames = [name];
  } else {
    const field = CONFIG_FIELD[resourceKey];
    if (!field) return [];
    configNames = (configRows || [])
      .filter(c => c && String(c[field] || '') === name)
      .map(c => String(c.name || ''))
      .filter(Boolean);
  }
  if (!configNames.length) return [];

  const wanted = new Set(configNames);
  return (provRows || []).filter((p) => {
    // A disabled rule provisions nothing, so it cannot push anything either.
    if (!p || _bool(p.disabled)) return false;
    if (wanted.has(String(p['master-configuration'] || ''))) return true;
    return _split(p['slave-configurations']).some(s => wanted.has(s));
  });
}

function _fingerprint(name, ruleIds, fields) {
  return JSON.stringify(['capsman-push', String(name || ''),
                         ruleIds.slice().sort(), fields.slice().sort()]);
}

/**
 * Would saving this push to live CAPs?
 *
 * Returns the shared warn shape `{ level, code, detail, fingerprint }`.
 *
 * `capCount` is advisory and FAILS SOFT: it comes from the collector's payload,
 * and a missing count costs a number in the sentence, never the warning.
 */
function checkPush({ resourceKey, action, values, before, configRows, provRows, capCount }) {
  // A create references nothing yet — nothing is following it, so nothing moves.
  if (action !== 'update' && action !== 'delete') return _none();

  // The name as the ROUTER currently has it. A rename is still a push of the old
  // profile, and `before` is the freshly read row.
  const name = String((before && before.name) || (values && values.name) || '');
  const rules = referencingRules({ resourceKey, name, configRows, provRows });
  if (!rules.length) return _none();

  // Which submitted fields actually differ. A delete changes everything about it.
  const changed = [];
  if (action === 'delete') changed.push('(removed)');
  else {
    for (const k of Object.keys(values || {})) {
      // A secret never reads back, so any value submitted for one is a change.
      if (k === 'passphrase') { if (values[k]) changed.push(k); continue; }
      const next    = String(values[k] == null ? '' : values[k]);
      const current = before ? String(before[k] == null ? '' : before[k]) : '';
      if (next !== current) changed.push(k);
    }
  }

  const ruleIds = rules.map(r => String(r['.id'] || ''));
  return {
    level: 'warn',
    code: 'capsman-push',
    detail: {
      profile: name,
      rules: rules.map(r => String(r['name-format'] || r['master-configuration'] || r['.id'] || '')),
      ruleCount: rules.length,
      caps: Number.isFinite(capCount) ? capCount : null,
      action: action === 'delete' ? 'delete' : 'update',
    },
    fingerprint: _fingerprint(name, ruleIds, changed),
  };
}

module.exports = { checkPush, referencingRules, CONFIG_FIELD };
