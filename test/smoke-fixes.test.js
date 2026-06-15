const test = require('node:test');
const assert = require('node:assert/strict');

const ROS = require('../src/routeros/client');
const TrafficCollector = require('../src/collectors/traffic');
const { extractAddress } = require('../src/util/ip');
const RingBuffer = require('../src/util/ringbuffer');

test('extractAddress handles IPv4, IPv6 and destination keys', () => {
  assert.equal(extractAddress('198.51.100.10:443'), '198.51.100.10');
  assert.equal(extractAddress('[2001:db8::1]:443/tcp'), '2001:db8::1');
  assert.equal(extractAddress('2001:db8::10'), '2001:db8::10');
  assert.equal(extractAddress('203.0.113.7:51820/udp'), '203.0.113.7');
});

test('RingBuffer preserves insertion order without growing beyond capacity', () => {
  const buf = new RingBuffer(3);
  buf.push(1);
  buf.push(2);
  buf.push(3);
  buf.push(4);
  assert.deepEqual(buf.toArray(), [2, 3, 4]);
});

test('traffic collector ignores invalid interface selections', () => {
  const io = { to() { return { emit() {} }; }, emit() {}, engine: { clientsCount: 0 } };
  const ros = { connected: true, on() {}, stream() { return { on() {}, stop() {} }; } };
  const collector = new TrafficCollector({
    ros,
    io,
    defaultIf: 'wan',
    historyMinutes: 1,
    state: {},
  });
  collector.setAvailableInterfaces([{ name: 'wan' }, { name: 'lan' }]);

  const handlers = {};
  const socket = {
    id: 'socket-1',
    on(event, handler) { handlers[event] = handler; },
    emit() {},
  };

  collector.bindSocket(socket);
  handlers['traffic:select']({ ifName: 'bogus' });
  assert.equal(collector.subscriptions.get(socket.id).ifName, 'wan', 'bogus selection keeps default');

  handlers['traffic:select']({ ifName: 'lan' });
  assert.equal(collector.subscriptions.get(socket.id).ifName, 'lan', 'valid selection updates subscription');
  assert.ok(collector.hist.has('lan'), 'history buffer initialized for selected interface');
});

test('ROS emitter tolerates error events without a custom listener', () => {
  const ros = new ROS({});
  // _emitConnectionError guards against the Node.js default behaviour of
  // throwing unhandled 'error' events — it only forwards to 'error' when a
  // listener is registered. Calling it with no listener must not throw.
  assert.doesNotThrow(() => ros._emitConnectionError(new Error('boom')));
});
