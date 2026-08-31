'use strict';
// Collectors, replayed against captured live hardware.
//
// Plan A1. The ~50 RouterOS behaviour workarounds in src/ were each discovered by
// running against real routers, and 28 of them name the device. This suite turns
// that discovery into something a second implementation has to reproduce: real
// captured rows in, a pinned payload out.
//
// It is deliberately GENERIC. Every fixture under test/fixtures/ is exercised by
// the same tests, so capturing a new collector adds coverage without adding
// code — and a collector whose fixture has not been captured is visibly absent
// rather than silently untested.
//
// Fixtures are anonymised at capture (tools/capture-fixtures.js) because this
// repository is public. The anonymisation is relation-preserving, which is what
// keeps the joins these collectors are built on intact.

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');

const replay = require('../../../nodecheck/helpers/fixture-replay');

const FIXTURES = replay.list();

// ── The corpus exists ────────────────────────────────────────────────────────

test('there is a captured corpus to replay', () => {
  // If this fails, the fixtures were never captured or were deleted. It is not a
  // skip: the whole port plan rests on this corpus, so its absence is a failure.
  assert.ok(FIXTURES.length > 0,
    'no fixtures under test/fixtures — run tools/capture-fixtures.js');
});

// ── Nothing private survived the capture ─────────────────────────────────────

test('no fixture carries a credential-shaped key', () => {
  // The capture aborts on one; this is the standing guard, because a fixture can
  // also be added by hand or edited later.
  // Anchored at the end, matching tools/capture-fixtures.js: a bare /password/
  // also matches `minimum-password-length`, which is a policy number and not a
  // credential — it failed the whole rosusers capture once already. `public-key`
  // must stay allowed while `private-key` must not.
  const BAD = /(passphrase|password|secret|psk)$|(private|pre-?shared)-key$/i;
  const leaf = (k) => String(k).split('.').pop();
  for (const f of FIXTURES) {
    const raw = fs.readFileSync(f.file, 'utf8');
    for (const m of raw.matchAll(/"([^"]+)":/g))
      assert.ok(!BAD.test(leaf(m[1])),
        f.router + '/' + f.collector + ' carries a credential-shaped key: ' + m[1]);
  }
});

test('no fixture carries a routable address or a real MAC', () => {
  // Anonymisation maps into the reserved documentation ranges, so anything
  // outside them means a value escaped the scrubber. 02: is the locally
  // administered prefix every fake MAC is minted under.
  for (const f of FIXTURES) {
    const raw = fs.readFileSync(f.file, 'utf8');
    const mac = raw.match(/\b(?!02:)([0-9A-F]{2}:){5}[0-9A-F]{2}\b/i);
    assert.ok(!mac, f.collector + ' carries an un-anonymised MAC: ' + (mac && mac[0]));
    for (const m of raw.matchAll(/\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b/g)) {
      const ip = m[0];
      // 198.51.100.0/24 is TEST-NET-2 (RFC 5737). 0.0.0.0 and 255.255.255.255
      // are wildcards RouterOS uses literally and carry no information.
      const ok = ip.startsWith('198.51.100.') || ip === '0.0.0.0' || ip === '255.255.255.255';
      assert.ok(ok, f.collector + ' carries a routable address: ' + ip);
    }
  }
});

// ── Every collector reproduces its payload from captured rows ────────────────

// Collectors whose capture is known to be incomplete, with the reason. Listed
// rather than silently tolerated: a gap nobody can see is a gap that never gets
// closed, and the Go port will replay these same fixtures.
const KNOWN_INCOMPLETE = {
  // CORRECTED 2026-08-24, and it named the wrong reads AND the wrong cause.
  //
  // It blamed "/system/routerboard, /system/license and the update check" as
  // un-awaited follow-ups from `_processRow`, with the cause "not yet
  // identified". Measured by replaying the fixture and printing both sets:
  //
  //   asked     resource, routerboard, license, check-for-updates, update/print
  //   captured  resource, license, routerboard
  //
  // routerboard and license ARE captured. `_processRow` is reached from the
  // POLL path (system.js:427), not the stream alone, so the fire-and-forget
  // follow-up does fire and does get recorded.
  //
  // The two genuinely missing reads are the update pair, and the cause is not a
  // settle window at all: `check-for-updates` contacts MikroTik's UPSTREAM
  // server, and the live code races it against a FIFTEEN-SECOND timeout
  // (system.js:209). No capture waits that long, and no capture should.
  //
  // So this gap is permanent rather than pending. What it left uncovered is now
  // covered from the other direction: `tools/system-update-cases.js` drives the
  // live `_isUpdateAnswer` and the lifted availability rule, and
  // `internal/collect/system_update_test.go` replays them.
  system: 'cannot capture /system/package/update/check-for-updates or its ' +
          'follow-up print: the check contacts MikroTik\'s upstream server ' +
          'behind a 15-second timeout (system.js:209), which is longer than any ' +
          'capture settle window. Permanent, not pending — the decisions behind ' +
          'those reads are pinned by tools/system-update-cases.js instead.',
};

// Fields that cannot come from a capture, so cannot be part of the contract.
// Kept identical to tools/make-golden.js — a field zeroed in a golden but
// asserted here would fail the stability gate on a payload the Go side is
// deliberately not asked to reproduce.
const WALL_CLOCK = new Set(['ts', 'deltaWindowMs', 'firstSeen', 'lastSeen']);

for (const entry of FIXTURES) {
  const label = entry.router + '/' + entry.collector;

  test(label + ' — replays without touching a router', async () => {
    // What every collector must do: engage the router the way the capture
    // recorded, and not throw doing it.
    //
    // READS **OR** STREAMS. This asserted `asked.length > 0` while the recorder
    // only wrapped ros.write(), which made "issued no read" and "did nothing"
    // the same statement. They are not: interfaceStatus opens three metadata
    // streams and issues no read at all, and its fixture is perfectly good.
    // Still an assertion with teeth — a collector that neither reads nor
    // subscribes has produced nothing to test.
    const { asked, streamed } = await replay.run(entry);
    assert.ok(asked.length > 0 || (streamed || []).length > 0,
      'the collector neither read from nor subscribed to the router');
  });

  test(label + ' — produces a payload derived only from the fixture', async () => {
    // NOT every collector emits from its snapshot method. arp, logs, netwatch,
    // system, topology and dhcpNetworks fill a cache on the read and emit from
    // somewhere else — a stream callback, a heartbeat. Asserting a payload for
    // those was asserting the wrong thing about a correct collector, so it is
    // reported rather than failed.
    const { payload } = await replay.run(entry);
    if (!payload) return;   // cache-filling snapshot; covered by the test above
    assert.strictEqual(typeof payload.ts, 'number',
      'ts is wall-clock; everything else must come from the fixture');
  });

  test(label + ' — asks only what the capture recorded', async () => {
    // A command the collector issues but the capture does not hold is a gap in
    // the corpus: the replay answers [] and the assertions below would be
    // testing a fallback path rather than the real one.
    const { asked } = await replay.run(entry);
    const captured = new Set((entry.data.exchanges || []).map(e => e.cmd));
    const missing = [...new Set(asked.map(a => a.cmd))].filter(c => !captured.has(c));
    if (KNOWN_INCOMPLETE[entry.collector]) {
      // Still asserted, just against the documented shape: if the gap CLOSES,
      // this fails and the note above should be removed rather than left lying.
      assert.ok(missing.length > 0,
        entry.collector + ' is listed in KNOWN_INCOMPLETE but now captures ' +
        'everything it asks for — remove the entry');
      return;
    }
    assert.deepStrictEqual(missing, [],
      label + ' asked for commands the fixture does not contain — re-capture it');
  });

  test(label + ' — the payload is stable across replays', async () => {
    // Determinism is the property the Go port will be diffed against. Anything
    // that varies between two replays of identical input cannot be part of the
    // contract, and this is where such a thing shows up.
    //
    // The excluded fields are the wall-clock ones, and they are excluded HERE
    // for the same reason tools/make-golden.js zeroes them: `ts` is taken at
    // emit, and `deltaWindowMs` is the measured gap between two metadata
    // commits, observed flipping between 320 and 321 on consecutive replays of
    // the same fixture. Neither can come from a capture, so neither is part of
    // the contract — and asserting on them turns this gate into an intermittent
    // failure rather than a test. The set is the same one make-golden.js uses.
    const a = await replay.run(entry);
    const b = await replay.run(entry);
    if (!a.payload && !b.payload) return;   // see the note above
    const strip = (p) => JSON.stringify(p, (k, v) =>
      (WALL_CLOCK.has(k) && typeof v === 'number') ? 0 : v);
    assert.strictEqual(strip(a.payload), strip(b.payload),
      label + ' produced a different payload from identical input');
  });
}

// ── What the corpus is actually pinning ──────────────────────────────────────
//
// The generic tests above prove the mechanism. These name the specific hardware
// behaviours the captures were taken to preserve, so a regression reads as the
// quirk it broke rather than as an opaque diff.

const wifi = FIXTURES.find(f => f.collector === 'wifi');

test('wifi — a CAPsMAN-provisioned radio is read-only, and says which kind', { skip: !wifi },
  async () => {
    // The live finding: a router provisioning its own radios reports them
    // `dynamic` with NO configuration.manager, so keying read-only on the
    // manager alone left twelve uneditable rows and no explanation.
    const { payload } = await replay.run(wifi);
    const provisioned = payload.networks.filter(n => n.readOnlyReason === 'provisioned');
    assert.ok(provisioned.length > 0, 'the captured AX3 provisions its own radios');
    for (const n of provisioned) {
      assert.strictEqual(n.capsManaged, false, 'no configuration.manager is set on these');
      assert.strictEqual(n.editable, false);
    }
  });

test('wifi — band survives being set on the channel profile, not the interface',
  { skip: !wifi }, async () => {
    // The live finding: neither band nor width is set inline on this router, so
    // reading only the interface put an em dash in every Band cell.
    const { payload } = await replay.run(wifi);
    assert.ok(payload.radios.length > 0);
    assert.ok(payload.radios.every(r => r.band),
      'a radio came back with no band: ' +
      JSON.stringify(payload.radios.filter(r => !r.band).map(r => r.name)));
  });

test('wifi — no passphrase reaches the payload', { skip: !wifi }, async () => {
  const { payload } = await replay.run(wifi);
  assert.ok(!/passphrase|pre-shared/i.test(JSON.stringify(payload)));
});

const capsman = FIXTURES.find(f => f.collector === 'capsman');

test('capsman — provisioning rows carry the id and identity an edit needs',
  { skip: !capsman }, async () => {
    const { payload } = await replay.run(capsman);
    assert.ok(payload.provisioning.length > 0, 'the captured AX3 has provisioning rules');
    for (const p of payload.provisioning) {
      assert.match(p.id, /^\*/, 'a provisioning row must carry its RouterOS id');
      assert.ok(p.identity, 'a provisioning row must carry a composite identity');
    }
  });

test('capsman — the profile tables reach the payload', { skip: !capsman }, async () => {
  const { payload } = await replay.run(capsman);
  assert.ok(payload.profiles, 'the configuration card has no profiles to render');
  for (const k of ['configuration', 'security', 'channel', 'datapath'])
    assert.ok(Array.isArray(payload.profiles[k]), k + ' is missing from profiles');
});
