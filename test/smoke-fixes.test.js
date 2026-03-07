const test = require('node:test');
const assert = require('node:assert/strict');

const { createBasicAuthMiddleware } = require('../src/auth/basicAuth');
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

test('basic auth middleware challenges unauthorized requests and accepts valid credentials', () => {
  const middleware = createBasicAuthMiddleware({ username: 'admin', password: 'secret' });
  let ended = false;
  const res = {
    headers: {},
    setHeader(name, value) { this.headers[name] = value; },
    end(body) { ended = body; },
  };

  middleware({ headers: {} }, res, () => assert.fail('should not authorize missing credentials'));
  assert.equal(res.statusCode, 401);
  assert.match(res.headers['WWW-Authenticate'], /^Basic /);
  assert.equal(ended, 'Authentication required');

  const req = {
    headers: {
      authorization: 'Basic ' + Buffer.from('admin:secret').toString('base64'),
    },
  };
  middleware(req, { setHeader() {}, end() {} }, () => { ended = 'ok'; });
  assert.equal(ended, 'ok');
});

test('traffic collector ignores invalid interface selections and prunes unused polls', () => {
  const io = { to() { return { emit() {} }; }, emit() {} };
  const ros = { connected: true, on() {} };
  const collector = new TrafficCollector({
    ros,
    io,
    defaultIf: 'wan',
    historyMinutes: 1,
    state: {},
  });
  collector.setAvailableInterfaces([{ name: 'wan' }, { name: 'lan' }]);
  collector._startPoll = function(ifName) { this.started = (this.started || []).concat(ifName); };
  collector._pruneUnusedPolls = function() { this.pruned = true; };

  const handlers = {};
  const socket = {
    id: 'socket-1',
    on(event, handler) { handlers[event] = handler; },
    emit() {},
  };

  collector.bindSocket(socket);
  handlers['traffic:select']({ ifName: 'bogus' });
  assert.equal(collector.subscriptions.get(socket.id), 'wan');
  assert.deepEqual(collector.started || [], []);

  handlers['traffic:select']({ ifName: 'lan' });
  assert.equal(collector.subscriptions.get(socket.id), 'lan');
  assert.deepEqual(collector.started, ['lan']);
  assert.equal(collector.pruned, true);
});

test('ROS emitter tolerates error events without a custom listener', () => {
  const ros = new ROS({});
  assert.doesNotThrow(() => ros.emit('error', new Error('boom')));
});
