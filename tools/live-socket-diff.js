'use strict';
/**
 * The SOCKET payloads, live against live — what none of the audits can see.
 *
 * ---- WHAT IS ALREADY COVERED, AND WHAT IS NOT -----------------------------
 *
 *   event-audit.js    the port's emits against the port's subscriptions
 *   emit-audit.js     the LIVE server's emits against the port's
 *   payload-keys.js   the CONTROL payloads (res:ok, *:caps, *:ok), and its own
 *                     header warns it unions keys across emit sites, so a key
 *                     missing from ONE site is invisible
 *
 * All three are static, read out of source. None has ever seen a payload a real
 * router produced. This connects to BOTH servers, watches the same router, and
 * compares the event names and payload SHAPES that actually arrive.
 *
 * ---- SHAPES, NOT VALUES, AND THE REASON IS NOT LAZINESS -------------------
 *
 * These are LIVE SAMPLES. Two sessions polling one router a second apart get
 * different byte counts, different uptimes, different client lists —
 * legitimately. Byte-equality is right for an HTTP read of a database (see
 * `tools/live-diff.sh`, which uses it) and wrong here: it would report every
 * event as differing and bury the differences that matter.
 *
 * What matters is the VOCABULARY (an event one side never sends) and the KEY SET
 * (a field one side never fills). Both are how a page renders blank.
 *
 * ---- TWO PROTOCOLS -------------------------------------------------------
 *
 * The live app runs socket.io; the port runs a raw WebSocket carrying
 * `{"event":…,"data":…}`. No one client talks to both, so this speaks each
 * directly — socket.io's framing is four cases for a listener: `0` open, `40`
 * connect, `2` ping (answer `3`), `42["name",payload]` event.
 *
 * ---- IT OPENS A SECOND SESSION ON A LIVE ROUTER --------------------------
 *
 * Connecting to the Go server starts its collectors against a router Node is
 * already polling. The operator authorised exactly that for verification —
 * transient and page-driven, as against the background pool, which does NOT run
 * during coexistence. It is read-only, and it disconnects when the window
 * closes.
 *
 *   MDU=<user> MDP=<password> node tools/live-socket-diff.js [seconds]
 */

// `ws` comes from the app container's node_modules, because this repo has no
// node dependencies of its own and is not going to grow one for a hand-run tool.
// WS_PATH lets the caller point at a copy: the script must run on the HOST,
// where both servers are reachable — inside the app container `127.0.0.1:3097`
// is that container's own loopback, which is where the first run failed.
const WebSocket = require(process.env.WS_PATH || '/app/node_modules/ws');
const http = require('node:http');

const NODE_HOST = process.env.NODE_HOST || '127.0.0.1:3081';
const GO_HOST = process.env.GO_HOST || '127.0.0.1:3097';
const SECONDS = Number(process.argv[2] || 25);
const MDU = process.env.MDU;
const MDP = process.env.MDP;
if (!MDU || !MDP) {
  console.error('set MDU and MDP to a dashboard login');
  process.exit(1);
}

/** Log in through the GO server, which proxies to Node, so one cookie fits both. */
function login() {
  return new Promise((resolve, reject) => {
    const body = JSON.stringify({ username: MDU, password: MDP });
    const [host, port] = GO_HOST.split(':');
    const req = http.request({
      host, port, path: '/api/auth/login', method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body) },
    }, (res) => {
      const set = res.headers['set-cookie'] || [];
      const jar = set.map((c) => c.split(';')[0]).join('; ');
      res.resume();
      res.on('end', () => (jar ? resolve(jar) : reject(new Error('no cookie; login failed'))));
    });
    req.on('error', reject);
    req.end(body);
  });
}

function get(hostPort, path, cookie) {
  return new Promise((resolve, reject) => {
    const [host, port] = hostPort.split(':');
    http.get({ host, port, path, headers: { Cookie: cookie } }, (res) => {
      let b = '';
      res.on('data', (d) => { b += d; });
      res.on('end', () => {
        try {
          resolve(JSON.parse(b));
        } catch (e) {
          reject(e);
        }
      });
    }).on('error', reject);
  });
}

/** A shape: sorted top-level keys, or the keys of an array's first row. */
function shapeOf(v) {
  if (Array.isArray(v)) {
    return v.length && v[0] && typeof v[0] === 'object'
      ? '[' + Object.keys(v[0]).sort().join(',') + ']'
      : '[]';
  }
  if (v && typeof v === 'object') return '{' + Object.keys(v).sort().join(',') + '}';
  return typeof v;
}

// Focus each page in turn, spreading them across the window so every one gets a
// poll or two. Both sides are driven by the same schedule, so neither is asked
// for a page the other was not.
function cyclePages(send) {
  const each = Math.max(400, Math.floor((SECONDS * 1000) / PAGES.length));
  PAGES.forEach((p, i) => setTimeout(() => send(p), i * each));
}

function record(seen, name, payload) {
  if (!seen.has(name)) seen.set(name, new Set());
  seen.get(name).add(shapeOf(payload));
}

// THE PAGES TO SWEEP, in turn.
//
// Both servers start a page's collectors on FOCUS, so a run that focuses one
// page compares one page's events and says nothing about the rest — the first
// run of this script focused `dashboard` only, and reported 27 events the live
// app sent and the port did not, almost all of which were simply other pages'.
//
// Focusing each in turn, on both sides, is what makes "only the live app sent
// this" mean something.
const PAGES = (process.env.PAGES || 'dashboard,interfaces,wan,vlans,bridges,wifi,wireless,'
  + 'capsman,dhcp,dns,routing,ppp,vpn,bandwidth,queues,connections,firewall,rosusers,logs,'
  + 'packages,topology').split(',');

// ── EVENTS THE LIVE APP SENDS AND THIS PORT DELIBERATELY DOES NOT ──────────
//
// Listed rather than skipped, exactly as `tools/live-diff.sh`'s
// `expected_differ()` does it: the run still shows the event was seen and a
// reader can see WHY, because a silent omission is how `/api/cities` went
// unverified for weeks.
//
// THE LEDGER FAILS IN BOTH DIRECTIONS. An event in here that the port STARTS
// sending is reported too, so the entry has to be deleted rather than left
// lying — the same rule `nodecheck`'s KNOWN_INCOMPLETE list follows.
//
// Every entry here was traced to source on 2026-08-28, after the first sweep
// reported eight discrepancies and four of them turned out to be real defects.
const EXPECTED_LIVE_ONLY = {
  'ros:status':
    'MERGED. This port emits ONE room-scoped `router:status` doing the work the live '
    + 'app splits between `ros:status` (session state, carries `reason`) and a global '
    + '`router:status` (list reachability). Decided during the port. It is '
    + 'why `router:status` also shows a payload-shape difference below.',
  'interfaces:list':
    'REPLACED by `ifstatus:names`, which BOTH servers send — so it is in the common '
    + 'set, not missing. The live app emits both; this port emits only the one.',
  'ping:history':
    'AN ARTEFACT OF THIS PROBE, not of the port. The port sends it on dashboard focus '
    + 'and ONLY when the history is non-empty (an empty one would clear a chart the '
    + 'viewer is already watching). `dashboard` is the first page this sweep focuses, '
    + 'and the Go server under test was started seconds earlier with an empty ping '
    + 'history, while the live app has been up for days. The same class as '
    + '`/api/localcc` in live-diff.sh: the fixture, not the port.',
  'routers:update':
    'A MECHANISM DIFFERENCE. The live app sends the router list over the socket in '
    + 'sendInitialState (index.js:4218); this port loads it over HTTP in `loadRouters()` '
    + 'and uses the socket only for CHANGES (`broadcastRouterList`). Nothing '
    + 'user-visible differs. Invisible to emit-audit, which asks only whether the event '
    + 'is emitted somewhere, not on which paths.',
};

// ── SAME EVENT, SHAPE DIFFERS, AND THE REASON IS KNOWN ─────────────────────
const EXPECTED_SHAPE = {
  'router:status':
    'The merge above: this port\'s single event carries `reason`, which the live '
    + 'global `router:status` does not — the live app puts it on `ros:status` instead. '
    + 'Both of this port\'s listeners read the union.',
  'logs:history':
    'The live app emits a BARE ARRAY on connect and `{entries}` on card focus, and '
    + '`public/app.js:9261` carries a comment about the bug that caused. This port '
    + 'emits the array on both paths and both its consumers guard with `Array.isArray` '
    + 'first, so they accept either. One shape where the live app has two.',
};

/** Watch the live app's socket.io. */
function watchNode(cookie, routerId, seen) {
  return new Promise((resolve) => {
    const ws = new WebSocket(`ws://${NODE_HOST}/socket.io/?EIO=4&transport=websocket`,
      { headers: { Cookie: cookie } });
    ws.on('message', (raw) => {
      const s = raw.toString();
      if (s.startsWith('0')) { ws.send('40'); return; }   // open -> connect
      if (s === '2') { ws.send('3'); return; }            // ping -> pong
      if (s.startsWith('40')) {                            // connected
        // The live client's own vocabulary, which differs from the port's:
        // `router:switch` with an object, and `page:focus` with a string.
        ws.send('42' + JSON.stringify(['router:switch', { routerId }]));
        cyclePages((p) => ws.send('42' + JSON.stringify(['page:focus', p])));
        return;
      }
      if (!s.startsWith('42')) return;
      let arr;
      try {
        arr = JSON.parse(s.slice(2));
      } catch {
        return;
      }
      record(seen, arr[0], arr[1]);
    });
    ws.on('error', (e) => { console.error('  node socket: ' + e.message); resolve(); });
    setTimeout(() => { try { ws.close(); } catch { /* closing */ } resolve(); }, SECONDS * 1000);
  });
}

/** Watch the port's raw WebSocket. */
function watchGo(cookie, routerId, seen) {
  return new Promise((resolve) => {
    const ws = new WebSocket(`ws://${GO_HOST}/ws`, { headers: { Cookie: cookie } });
    // THE HANDSHAKE THE PORT'S OWN CLIENT USES, and it is not the live one.
    //
    // `main.ts` sends `router:select` with a BARE STRING and then `page:focus`.
    // The first run of this script sent `router:switch` with an object — the
    // live app's event — and `ws.go`'s dispatch has no case for it, so the
    // connection sat open and silent while Node sent 39 events. Zero on one side
    // is not a finding, it is a broken probe, and the guard at the bottom said so
    // rather than reporting a catastrophic difference.
    //
    // `page:focus` matters as much: the port starts a page's collectors on focus,
    // so a socket that selects a router and focuses nothing receives almost
    // nothing.
    ws.on('open', () => {
      ws.send(JSON.stringify({ event: 'router:select', data: routerId }));
      cyclePages((p) => ws.send(JSON.stringify({ event: 'page:focus', data: p })));
    });
    ws.on('message', (raw) => {
      let msg;
      try {
        msg = JSON.parse(raw.toString());
      } catch {
        return;
      }
      if (msg.event) record(seen, msg.event, msg.data);
    });
    ws.on('error', (e) => { console.error('  go socket: ' + e.message); resolve(); });
    setTimeout(() => { try { ws.close(); } catch { /* closing */ } resolve(); }, SECONDS * 1000);
  });
}

(async () => {
  const cookie = await login();
  const routers = await get(GO_HOST, '/api/routers', cookie);
  const routerId = routers.routers[0].id;
  console.log(`watching ${routers.routers[0].label} for ${SECONDS}s on both servers\n`);

  const nodeSeen = new Map();
  const goSeen = new Map();
  await Promise.all([watchNode(cookie, routerId, nodeSeen), watchGo(cookie, routerId, goSeen)]);

  const all = [...new Set([...nodeSeen.keys(), ...goSeen.keys()])].sort();
  const onlyNode = [];
  const onlyGo = [];
  const shapeDiff = [];
  for (const name of all) {
    const n = nodeSeen.get(name);
    const g = goSeen.get(name);
    if (n && !g) { onlyNode.push(name); continue; }
    if (g && !n) { onlyGo.push(name); continue; }
    const ns = [...n].sort().join(' | ');
    const gs = [...g].sort().join(' | ');
    if (ns !== gs) shapeDiff.push({ name, node: ns, go: gs });
  }

  console.log(`events seen: ${nodeSeen.size} on node, ${goSeen.size} on go, `
    + `${all.length - onlyNode.length - onlyGo.length} in common\n`);

  const unexplained = onlyNode.filter((n) => !EXPECTED_LIVE_ONLY[n]);
  const explained = onlyNode.filter((n) => EXPECTED_LIVE_ONLY[n]);
  if (unexplained.length) {
    console.log('ONLY THE LIVE APP SENT THESE — a page listening for one renders blank:');
    for (const n of unexplained) console.log('  ' + n + '   ' + [...nodeSeen.get(n)].join(' | '));
    console.log('');
  }
  if (explained.length) {
    console.log('live-only AND EXPECTED (see EXPECTED_LIVE_ONLY):');
    for (const n of explained) console.log('  ' + n);
    console.log('');
  }
  // THE OTHER DIRECTION: an entry describing something that is no longer true.
  const staleLedger = Object.keys(EXPECTED_LIVE_ONLY).filter((n) => !onlyNode.includes(n));
  if (staleLedger.length) {
    console.log('EXPECTED_LIVE_ONLY names events that were NOT live-only this run. Either the');
    console.log('port started sending them — delete the entry — or the run never triggered them:');
    for (const n of staleLedger) {
      console.log('  ' + n + (goSeen.has(n) ? '   (the port SENT it — the entry is stale)'
        : '   (neither side sent it; the sweep may not have reached its path)'));
    }
    console.log('');
  }
  if (onlyGo.length) {
    console.log('only the port sent these (not necessarily wrong — it serves pages Node does not,');
    console.log('and the two disagree about which events a page-focus triggers):');
    for (const n of onlyGo) console.log('  ' + n);
    console.log('');
  }
  const badShape = shapeDiff.filter((d) => !EXPECTED_SHAPE[d.name]);
  if (badShape.length) {
    console.log('SAME EVENT, DIFFERENT PAYLOAD SHAPE:');
    for (const d of badShape) {
      console.log('  ' + d.name);
      console.log('    node: ' + d.node);
      console.log('    go:   ' + d.go);
    }
    console.log('');
  }
  const okShape = shapeDiff.filter((d) => EXPECTED_SHAPE[d.name]);
  if (okShape.length) {
    console.log('shape differs AND IS EXPECTED (see EXPECTED_SHAPE):');
    for (const d of okShape) console.log('  ' + d.name);
    console.log('');
  }
  if (!unexplained.length && !badShape.length) {
    console.log('every unexplained difference is gone: the port sent what the live app sent, '
      + 'with the same payload shape, except the entries both ledgers above account for');
  }
  // A run that saw nothing proves nothing. Say so rather than reporting success.
  if (nodeSeen.size === 0 || goSeen.size === 0) {
    console.error('\nONE SIDE SENT NOTHING AT ALL — the comparison above is meaningless. '
      + 'Check the login, the router id, and that both servers are up.');
    process.exit(1);
  }
  // Explained differences do not fail the run; a stale ledger entry does, because
  // a note that is no longer true is worse than no note.
  process.exit(unexplained.length || badShape.length || staleLedger.length ? 1 : 0);
})().catch((e) => {
  console.error(e.message);
  process.exit(1);
});
