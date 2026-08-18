'use strict';
/**
 * Router users collector — RouterOS `/user`, not MikroDash accounts.
 *
 *   /user           who may log into the router
 *   /user/group     what each group may do (the policy list)
 *   /user/active    who is logged in right now
 *   /user/settings  the router's own password policy, which the create form needs
 *
 * THIS COLLECTOR ONLY READS. Every write — add, edit, remove, and ending a
 * session — lives in the socket actions in index.js, gated on router:write and
 * on the page. A collector runs unattended on a timer for every connected
 * router, so a write reachable from here would be a write nobody asked for. A
 * test asserts those command paths never appear in this file.
 *
 * `/user/print` DOES NOT RETURN PASSWORDS — verified against a live router — so
 * the read path carries no secret and needs no redaction. Nothing here should
 * ever be changed in a way that makes that untrue.
 *
 * THE PROTECTED SET. MikroDash logs into each router as one of these users, and
 * editing that account or its group is how an operator locks the dashboard out
 * of the device it manages. The payload therefore carries a `self` block naming
 * the accounts and groups that must not be touched, and marks the matching rows
 * `protected: true`.
 *
 * Those marks are a CONVENIENCE for the page. They are NOT the guard: the guard
 * is server-side in the action handlers, which re-read from the router in the
 * same tick as the write, because a page can be stale or crafted.
 */

const { clampPoll, createPollLoop } = require('./util');
// resolveSelf lives with the guard that acts on it, not here: the page's marks
// and the handlers' refusals must never be able to disagree about what "ours"
// means, and two copies of that rule is how they would.
const { resolveSelf } = require('../routeros/selfGuard');

const USER_CMD    = ['/user/print',
                     '=.proplist=.id,name,group,address,comment,disabled,expired,last-logged-in,inactivity-timeout,inactivity-policy'];
const GROUP_CMD   = ['/user/group/print',   '=.proplist=.id,name,policy,skin,comment'];
const ACTIVE_CMD  = ['/user/active/print',  '=.proplist=.id,when,name,address,via,group,radius'];
const SETTINGS_CMD = ['/user/settings/print', ''];

// A user list changes when somebody edits it, not on a tick. Actions call
// refreshNow(), so a slow cadence costs nothing in responsiveness.
const CONFIG_EVERY = 6;

/**
 * The full RouterOS policy vocabulary, in the order WinBox shows it.
 *
 * Exported because the group editor renders exactly this list: a policy the UI
 * does not know about is one an operator cannot see they are removing. The
 * router normalises what it is sent — send `read,api` and it stores all 17 with
 * the rest negated — so the editor works in terms of which are POSITIVE.
 */
const POLICIES = Object.freeze([
  'local', 'telnet', 'ssh', 'ftp', 'reboot', 'read', 'write', 'policy', 'test',
  'winbox', 'password', 'web', 'sniff', 'sensitive', 'api', 'romon', 'rest-api',
]);

const _bool = (v) => v === true || v === 'true' || v === 'yes';
const _key  = (v) => String(v == null ? '' : v).trim().toLowerCase();

/**
 * Split a stored policy string into the set that is granted.
 *
 * RouterOS answers with every policy listed, negated ones prefixed `!`, so the
 * granted set is what survives filtering. Returning both halves keeps "this
 * group does not mention rest-api at all" distinguishable from "it denies it",
 * which matters on an older RouterOS that lacks a policy this build knows.
 */
function parsePolicy(raw) {
  const granted = [], denied = [];
  for (const part of String(raw || '').split(',')) {
    const p = part.trim();
    if (!p) continue;
    if (p.startsWith('!')) denied.push(p.slice(1));
    else granted.push(p);
  }
  return { granted, denied };
}

/**
 * The inverse: the string to send, with every ungranted policy explicitly
 * negated.
 *
 * THE NEGATIONS ARE LOAD-BEARING, and only on `set`. Verified against a live
 * router: `/user/group/set =policy=read` against a group holding `read,test,api`
 * changes NOTHING — a positive-only list is purely additive, and RouterOS
 * removes a policy only when it is named with a `!`. Sending the full list with
 * explicit negations narrows the group correctly.
 *
 *   set =policy=read                         -> read,test,api   (silently unchanged)
 *   set =policy=!local,...,read,...,!api     -> read
 *
 * `add` is the misleading case: there RouterOS fills the negations in itself, so
 * a positive-only list works and the create path looks fine while every edit
 * quietly fails to remove anything. One form is correct for both, so this always
 * emits the full seventeen.
 *
 * A policy the vocabulary does not contain is dropped rather than relayed: the
 * editor renders exactly POLICIES, so anything else is a newer RouterOS or a
 * crafted request.
 */
function buildPolicy(granted) {
  const set = new Set((granted || []).filter(p => POLICIES.includes(p)));
  return POLICIES.map(p => (set.has(p) ? p : '!' + p)).join(',');
}

/** Join the four reads into one view. */
function buildUsersView(userRows, groupRows, activeRows, settingsRow, usernames) {
  const self  = resolveSelf(userRows, activeRows, usernames);
  const isMe  = (n) => self.names.indexOf(_key(n)) !== -1;
  const isMyG = (g) => self.groups.indexOf(_key(g)) !== -1;

  const users = [];
  for (const r of userRows || []) {
    if (!r || !r.name) continue;                       // also drops {undefined:''}
    users.push({
      id:        r['.id'] || '',
      name:      String(r.name),
      group:     r.group || '',
      address:   r.address || '',
      comment:   r.comment || '',
      disabled:  _bool(r.disabled),
      expired:   _bool(r.expired),
      lastLogin: r['last-logged-in'] || '',
      inactivityTimeout: r['inactivity-timeout'] || '',
      inactivityPolicy:  r['inactivity-policy'] || '',
      protected: isMe(r.name),
    });
  }
  users.sort((a, b) => a.name.localeCompare(b.name));

  const groups = [];
  for (const r of groupRows || []) {
    if (!r || !r.name) continue;
    const pol = parsePolicy(r.policy);
    groups.push({
      id:      r['.id'] || '',
      name:    String(r.name),
      granted: pol.granted,
      denied:  pol.denied,
      skin:    r.skin || '',
      comment: r.comment || '',
      // The group the connecting account belongs to is protected too: dropping
      // `api` or `read` from it disconnects MikroDash just as surely as
      // deleting the account.
      protected: isMyG(r.name),
      members: users.filter(u => _key(u.group) === _key(r.name)).length,
    });
  }
  groups.sort((a, b) => a.name.localeCompare(b.name));

  const sessions = [];
  for (const r of activeRows || []) {
    if (!r || !r.name) continue;
    sessions.push({
      id:      r['.id'] || '',
      name:    String(r.name),
      address: r.address || '',
      via:     r.via || '',
      group:   r.group || '',
      when:    r.when || '',
      radius:  _bool(r.radius),
      // MikroDash keeps several logins per router open at once — the dashboard
      // session, and one each for alerts and the routers overview. All of them
      // are ours, and ending one buys nothing: it would simply reconnect.
      protected: isMe(r.name),
    });
  }
  sessions.sort((a, b) => (b.when || '').localeCompare(a.when || ''));

  const s = settingsRow || {};
  return {
    users, groups, sessions, self,
    passwordPolicy: {
      minLength:     Number(s['minimum-password-length'] || 0) || 0,
      minCategories: Number(s['minimum-categories'] || 0) || 0,
    },
    policies: POLICIES,
  };
}

class RosUsersCollector {
  constructor({ ros, io, state, pollMs, usernames }) {
    this.ros    = ros;
    this.io     = io;
    this.state  = state;
    this.pollMs = clampPoll(pollMs, 30000, 300000, 5000);
    this._lbl   = ros.routerLabel ? `[${ros.routerLabel}][rosusers]` : '[rosusers]';

    // The live login, plus whatever routers.json holds — see resolveSelf().
    this._usernames = (usernames && usernames.length)
      ? usernames.slice()
      : [(ros.cfg && ros.cfg.username) || ''];

    this._poll     = createPollLoop(() => this._tick(), () => this.pollMs);
    this._settings = null;
    this._ticks    = 0;
    this._lastFp   = '';
    // undefined = unprobed, false = this router has no such menu, stop asking.
    this._userAvailable     = undefined;
    this._groupAvailable    = undefined;
    this._activeAvailable   = undefined;
    this._settingsAvailable = undefined;
    // Distinguishes "read succeeded, nothing there" from "the router refused".
    // The page shows two different banners, because the fixes differ.
    this._denied = false;
    this.lastPayload = null;
  }

  async _read(cmd, flag) {
    if (this[flag] === false) return [];
    try {
      const args = cmd[1] ? [cmd[1]] : [];
      const rows = await this.ros.write(cmd[0], args);
      this[flag] = true;
      return (rows || []).filter(r => r && Object.keys(r).length);
    } catch (e) {
      const msg = String((e && e.message) || e).toLowerCase();
      if (msg.includes('no such') || msg.includes('unknown command')) this[flag] = false;
      // The documented monitoring group denies `policy`, and RouterOS gates
      // /user behind it — so a refusal here is the common case, not an edge
      // one. Latch it rather than asking every tick forever.
      else if (msg.includes('not enough permission') || msg.includes('permission denied') ||
               msg.includes('no permissions')) { this[flag] = false; this._denied = true; }
      else this.state.lastRosusersErr = e && e.message ? e.message : String(e);
      return [];
    }
  }

  /** Re-read now, after an action, so the page shows what the router did. */
  async refreshNow() {
    this._ticks = 0;
    if (this.ros.connected) await this._tick();
  }

  async _tick() {
    if (!this.ros.connected) return;

    if (this._ticks % CONFIG_EVERY === 0) {
      const rows = await this._read(SETTINGS_CMD, '_settingsAvailable');
      this._settings = rows[0] || null;
    }
    this._ticks++;

    const [userRows, groupRows, activeRows] = await Promise.all([
      this._read(USER_CMD,   '_userAvailable'),
      this._read(GROUP_CMD,  '_groupAvailable'),
      this._read(ACTIVE_CMD, '_activeAvailable'),
    ]);

    const built = buildUsersView(userRows, groupRows, activeRows, this._settings,
                                 this._usernames);
    const payload = {
      ts: Date.now(), pollMs: this.pollMs,
      ...built,
      // False when the API user cannot read /user at all, so the page can say
      // that rather than showing an empty list as if there were no users.
      available: this._userAvailable !== false,
      denied:    this._denied,
    };
    this.lastPayload = payload;
    this.state.lastRosusersTs = payload.ts;

    const fp = JSON.stringify({
      u: built.users.map(u => [u.name, u.group, u.disabled, u.address, u.comment, u.lastLogin]),
      g: built.groups.map(g => [g.name, g.granted.join('|'), g.members]),
      s: built.sessions.map(x => [x.name, x.address, x.via, x.when]),
    });
    if (fp === this._lastFp) return;
    this._lastFp = fp;
    this.io.to('page-rosusers').emit('rosusers:update', payload);
  }

  async start() {
    if (this.ros.connected) await this._tick();
    this._poll.start();
    this.ros.on('close', () => this._poll.stop());
    this.ros.on('connected', async () => {
      this._poll.stop();
      this._lastFp = '';
      this._ticks  = 0;
      this._denied = false;
      this._userAvailable = this._groupAvailable = undefined;
      this._activeAvailable = this._settingsAvailable = undefined;
      await this._tick();
      this._poll.start();
    });
  }

  suspend() { this._poll.stop(); }
  resume()  { if (this.ros.connected) this._poll.start(); }

  stop() { this._poll.stop(); this._lastFp = ''; }
}

RosUsersCollector.buildUsersView = buildUsersView;
RosUsersCollector.parsePolicy    = parsePolicy;
RosUsersCollector.buildPolicy    = buildPolicy;
RosUsersCollector.POLICIES       = POLICIES;
module.exports = RosUsersCollector;
