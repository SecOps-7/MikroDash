'use strict';
/**
 * The safe way to read the /interface/wifi profile menus.
 *
 * THIS FILE EXISTS FOR ONE REASON: `/interface/wifi/security/print` and
 * `/interface/wifi/configuration/print` return the passphrase IN CLEAR TEXT, and
 * two collectors now read those menus — wifi.js for the Wifi Networks page, and
 * capsman.js for the CAPsMAN configuration card. A proplist is the only thing
 * standing between that value and every browser holding read on either page.
 *
 * A constants module for two consumers would normally be over-abstraction. What
 * is being shared here is not a convenience, it is a security decision, and the
 * cost of the two copies drifting apart is a leaked pre-shared key. One
 * definition, and one test (`no menu asks for a secret`) that covers both
 * callers at once.
 *
 * THE RULE: no proplist below may name `passphrase`, `pre-shared-key`, or any
 * other credential. Adding one puts it in front of every browser on the page.
 * The test enforces this; do not silence it.
 */

/** Every field the security profile can safely expose. Note what is absent. */
const SECURITY = '=.proplist=.id,name,authentication-types,wps,ft,ft-over-ds,' +
                 'connect-priority,disabled,comment';

/**
 * The configuration profile. `security`, `channel` and `datapath` here are
 * profile NAMES, not values — the reference is safe; what it points at is what
 * has to be read carefully.
 */
const CONFIGURATION = '=.proplist=.id,name,ssid,mode,country,hide-ssid,security,' +
                      'channel,datapath,manager,disabled,comment';

const CHANNEL  = '=.proplist=.id,name,band,frequency,width,secondary-frequency,' +
                 'skip-dfs-channels,disabled,comment';

const DATAPATH = '=.proplist=.id,name,bridge,vlan-id,client-isolation,' +
                 'local-forwarding,traffic-processing,disabled,comment';

const PROVISIONING = '=.proplist=.id,supported-bands,action,master-configuration,' +
                     'slave-configurations,name-format,radio-mac,identity-regexp,' +
                     'comment,disabled';

/**
 * menu key -> [command, proplist]. Keyed by menu so a caller writes
 * `MENUS.security` and cannot accidentally pair one menu's path with another's
 * proplist.
 */
const MENUS = Object.freeze({
  configuration: Object.freeze(['/interface/wifi/configuration/print', CONFIGURATION]),
  security:      Object.freeze(['/interface/wifi/security/print',      SECURITY]),
  channel:       Object.freeze(['/interface/wifi/channel/print',       CHANNEL]),
  datapath:      Object.freeze(['/interface/wifi/datapath/print',      DATAPATH]),
  provisioning:  Object.freeze(['/interface/wifi/provisioning/print',  PROVISIONING]),
});

/**
 * Rows of a menu, minus the junk.
 *
 * An EMPTY RouterOS menu answers with one nameless row — `[{"undefined":""}]` is
 * what /interface/wifi/channel/print returns on a router with no channel
 * profiles, and a populated menu can carry that key on its first row too. Keyed
 * by name it would become an entry under '', which is exactly what an interface
 * naming no profile looks up.
 */
function named(rows) {
  return (rows || []).filter(r => r && String(r.name || '').trim());
}

/** The same, for menus whose rows have no name — provisioning addresses by id. */
function identified(rows) {
  return (rows || []).filter(r => r && r['.id']);
}

module.exports = { MENUS, named, identified };
