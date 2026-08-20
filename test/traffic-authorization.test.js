'use strict';
/**
 * Who the traffic collector delivers to, and when it stops.
 *
 * Traffic is the one collector that does NOT deliver through a room. Both of
 * its payloads go straight to a specific socket:
 *
 *     socket.emit('traffic:history', …)   on traffic:select
 *     socket.emit('traffic:update', …)    once a second, per subscriber
 *
 * That matters because the revocation sweep in index.js stops data by making a
 * socket leave its rooms — its comment says outright that "room membership is
 * the actual data boundary". For every other collector that is true. For this
 * one it is not, so revocation has to unbind the subscription as well.
 */
const test = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const path = require('path');

const TrafficCollector = require('../src/collectors/traffic');

/** A socket that records what it was sent and remembers its own listeners. */
function fakeSocket(id) {
  const listeners = new Map();
  return {
    id,
    routerId: 'r1',
    rooms: new Set(['router-r1']),
    sent: [],
    emit(event, payload) { this.sent.push({ event, payload }); },
    on(event, fn) {
      if (!listeners.has(event)) listeners.set(event, []);
      listeners.get(event).push(fn);
    },
    off(event, fn) {
      const list = listeners.get(event) || [];
      const at = list.indexOf(fn);
      if (at !== -1) list.splice(at, 1);
    },
    fire(event, payload) { for (const fn of (listeners.get(event) || []).slice()) fn(payload); },
    listenerCount(event) { return (listeners.get(event) || []).length; },
  };
}

function build() {
  const collector = new TrafficCollector({
    // Same fake shape the existing traffic tests use: the constructor wires
    // a reconnect listener, so `on` has to exist.
    ros: { connected: false, on() {}, stream: () => null, routerLabel: 'test' },
    // _processPacket idle-gates on engine.clientsCount, as every collector does.
    io: { engine: { clientsCount: 1 }, emit() {}, to() { return { emit() {} }; } },
    defaultIf: 'ether1',
    historyMinutes: 1,
    pollMs: 1000,
    state: {},
  });
  // The whitelist normally arrives from fetchInterfaces(); without it every
  // selection is refused and none of this would be reachable.
  collector.setAvailableInterfaces(['ether1', 'ether2']);
  return collector;
}

test('a subscriber receives traffic for the interface it selected', () => {
  const c = build();
  const s = fakeSocket('sock-1');
  c.bindSocket(s);
  s.fire('traffic:select', { ifName: 'ether2' });

  assert.ok(s.sent.some(m => m.event === 'traffic:history'),
    'selecting an interface replays its history');

  s.sent.length = 0;
  c._processPacket('ether2', { 'rx-bits-per-second': '1000', 'tx-bits-per-second': '2000' });
  assert.equal(s.sent.filter(m => m.event === 'traffic:update').length, 1);
});

test('samples go only to the sockets watching that interface', () => {
  const c = build();
  const a = fakeSocket('sock-a');
  const b = fakeSocket('sock-b');
  c.bindSocket(a);
  c.bindSocket(b);
  a.fire('traffic:select', { ifName: 'ether1' });
  b.fire('traffic:select', { ifName: 'ether2' });
  a.sent.length = 0; b.sent.length = 0;

  c._processPacket('ether2', { 'rx-bits-per-second': '1', 'tx-bits-per-second': '2' });
  assert.equal(a.sent.filter(m => m.event === 'traffic:update').length, 0);
  assert.equal(b.sent.filter(m => m.event === 'traffic:update').length, 1);
});

test('unbinding stops delivery and detaches the listener', () => {
  const c = build();
  const s = fakeSocket('sock-1');
  c.bindSocket(s);
  s.fire('traffic:select', { ifName: 'ether2' });
  assert.equal(s.listenerCount('traffic:select'), 1);

  c.unbindSocket(s);
  assert.equal(s.listenerCount('traffic:select'), 0,
    'a detached socket must not be able to re-subscribe');

  s.sent.length = 0;
  c._processPacket('ether2', { 'rx-bits-per-second': '1', 'tx-bits-per-second': '2' });
  assert.equal(s.sent.length, 0, 'no samples after unbind');
});

test('leaving every room does NOT stop traffic — only unbinding does', () => {
  // This is the whole point. The revocation sweep drops the socket from its
  // rooms and clears routerId; if that were sufficient, the assertions below
  // would be the other way round.
  const c = build();
  const s = fakeSocket('sock-1');
  c.bindSocket(s);
  s.fire('traffic:select', { ifName: 'ether2' });

  // Exactly what _startSessionSweep does on revocation, minus the unbind.
  s.rooms.clear();
  s.routerId = '';

  s.sent.length = 0;
  c._processPacket('ether2', { 'rx-bits-per-second': '1', 'tx-bits-per-second': '2' });
  assert.equal(s.sent.filter(m => m.event === 'traffic:update').length, 1,
    'traffic bypasses rooms, so room membership cannot be the boundary');

  // …and unbinding is what actually stops it.
  c.unbindSocket(s);
  s.sent.length = 0;
  c._processPacket('ether2', { 'rx-bits-per-second': '1', 'tx-bits-per-second': '2' });
  assert.equal(s.sent.length, 0);
});

test('revocation unbinds the traffic subscription', () => {
  // A source scan, because the sweep lives in index.js among the socket wiring
  // and there is no seam to call it through. It is the fix for the leak the
  // test above describes: without it a socket whose router access was revoked
  // keeps receiving a sample every second until it happens to disconnect.
  const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');
  const start = src.indexOf('function _startSessionSweep');
  assert.ok(start > 0, '_startSessionSweep not found');
  const sweep = src.slice(start, src.indexOf('}, 60_000);', start));

  assert.ok(/unbindSocket\(socket\)/.test(sweep),
    'the revocation sweep must unbind the traffic collector, not just leave rooms');
  // And it must happen for the router being revoked, before routerId is cleared
  // — afterwards there is nothing left to look the session up by.
  const unbindAt = sweep.indexOf('unbindSocket');
  const clearAt = sweep.indexOf("socket.routerId = ''");
  assert.ok(unbindAt > 0 && clearAt > 0 && unbindAt < clearAt,
    'unbind must run while socket.routerId still names the revoked router');
});
