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

test('basic auth middleware rate limits repeated failures', () => {
  const middleware = createBasicAuthMiddleware({
    username: 'admin',
    password: 'secret',
    maxFailures: 2,
    blockMs: 60_000,
  });
  const req = { headers: {}, socket: { remoteAddress: '127.0.0.1' } };
  const makeRes = () => ({
    headers: {},
    setHeader(name, value) { this.headers[name] = value; },
    end(body) { this.body = body; },
  });

  const first = makeRes();
  middleware(req, first, () => assert.fail('first request should not authorize'));
  assert.equal(first.statusCode, 401);

  const second = makeRes();
  middleware(req, second, () => assert.fail('second request should not authorize'));
  assert.equal(second.statusCode, 401);

  const third = makeRes();
  middleware(req, third, () => assert.fail('third request should be rate limited'));
  assert.equal(third.statusCode, 429);
  assert.equal(third.body, 'Too many authentication attempts');
  assert.ok(third.headers['Retry-After']);
});

test('basic auth middleware evicts the oldest tracked IP when the failure map exceeds the cap', () => {
  const middleware = createBasicAuthMiddleware({
    username: 'admin',
    password: 'secret',
    maxFailures: 1,
    blockMs: 60_000,
    maxTrackedIPs: 2,
  });
  const makeReq = (ip, auth) => ({
    headers: auth ? { authorization: auth } : {},
    socket: { remoteAddress: ip },
  });
  const makeRes = () => ({
    headers: {},
    setHeader(name, value) { this.headers[name] = value; },
    end(body) { this.body = body; },
  });

  // Three IPs each fail once — with maxFailures=1 each gets blocked immediately.
  middleware(makeReq('10.0.0.1'), makeRes(), () => assert.fail('ip1 should not authorize'));
  middleware(makeReq('10.0.0.2'), makeRes(), () => assert.fail('ip2 should not authorize'));

  // Verify IP1 is actually blocked (429) before eviction happens.
  const blockedRes = makeRes();
  middleware(makeReq('10.0.0.1'), blockedRes, () => assert.fail('ip1 should be blocked'));
  assert.equal(blockedRes.statusCode, 429, 'ip1 should be blocked before eviction');

  // IP3's failure pushes the map past maxTrackedIPs=2, evicting IP1 (oldest).
  middleware(makeReq('10.0.0.3'), makeRes(), () => assert.fail('ip3 should not authorize'));

  // IP1's failure record was evicted, so it can now authenticate.
  let ip1Authorized = false;
  middleware(
    makeReq('10.0.0.1', 'Basic ' + Buffer.from('admin:secret').toString('base64')),
    makeRes(),
    () => { ip1Authorized = true; }
  );
  assert.equal(ip1Authorized, true, 'ip1 should be unblocked after eviction');

  // IP2 was NOT evicted (it is newer than IP1), so it should still be blocked.
  const ip2Res = makeRes();
  middleware(makeReq('10.0.0.2'), ip2Res, () => assert.fail('ip2 should still be blocked'));
  assert.equal(ip2Res.statusCode, 429, 'ip2 should remain blocked (not evicted)');
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
