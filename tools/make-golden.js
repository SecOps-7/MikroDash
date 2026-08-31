'use strict';
/**
 * Golden payloads — what the Node collectors produce from the captured fixtures.
 *
 * The fixture corpus records what a router SAID. This records what the current
 * implementation MADE of it, which is the other half of the contract: the Go
 * port is not asked to be plausible, it is asked to be identical.
 *
 * Generated rather than written, and regenerated rather than edited. `--check`
 * fails when a golden is stale, so a change to a Node collector cannot silently
 * drift away from the Go implementation that was diffed against it — the same
 * gate tools/api-surface.js applies to the RouterOS command surface.
 *
 * WALL-CLOCK FIELDS ARE ZEROED. `ts` is taken at emit; `deltaWindowMs` is the
 * measured gap between two metadata commits. Neither can come from a fixture,
 * and leaving either in makes a golden differ from its own regeneration —
 * `deltaWindowMs` was observed flipping between 320 and 321 on consecutive runs,
 * which would have turned `--check` into an intermittent failure rather than a
 * gate. Zeroing them says out loud that they are excluded from the contract, and
 * the Go side asserts the same.
 *
 *   node tools/make-golden.js            write testdata/golden/
 *   node tools/make-golden.js --check    exit 1 if anything is stale
 */

const fs   = require('node:fs');
const path = require('node:path');

const replay = require('../nodecheck/helpers/fixture-replay');

const GOLDEN = path.join(__dirname, '..', 'testdata', 'golden');

/**
 * Collectors whose payload arrives as a NAMED EMIT rather than on lastPayload.
 *
 * `logs` has no snapshot method by design: /log/listen pushes an entry at a
 * time, and the collector forwards each one and keeps a ring buffer. But the
 * INITIAL load is an ordinary read of /log/print, and what it makes of those
 * rows is emitted whole as `logs:history` — a payload in every sense, just not
 * on the method this generator looks at first.
 *
 * It was listed as "no snapshot payload" for four iterations because of where
 * it emits, not because there was nothing to pin. Everything the Go port has to
 * reproduce for this collector — the last-500 slice, the severity
 * classification, the dropped rows with no message — is in that array.
 */
const EMIT_GOLDEN = { logs: 'logs:history' };

/**
 * Zero the wall-clock field, everywhere it appears.
 *
 * Recursive on purpose: `ts` is top-level on most payloads but nested inside
 * rows on a few (netwatch hosts carry their own), and a golden that pinned a
 * nested one would be rewritten by every run.
 */
// `firstSeen`/`lastSeen` join `ts` and `deltaWindowMs` here because topology
// stamps them from Date.now() as it reads, so two replays of ONE fixture differ
// and the golden would be stale the moment it was written. Same class of value,
// same treatment. THIS SET IS DECLARED IN THREE PLACES — here,
// nodecheck/fixture-differential.test.js and internal/collect/fixture_test.go —
// and they must agree, or a gate compares a zeroed field against a live one.
const WALL_CLOCK = new Set(['ts', 'deltaWindowMs', 'firstSeen', 'lastSeen']);

function normalise(v) {
  if (Array.isArray(v)) return v.map(normalise);
  if (v && typeof v === 'object') {
    const out = {};
    for (const k of Object.keys(v))
      out[k] = (WALL_CLOCK.has(k) && typeof v[k] === 'number') ? 0 : normalise(v[k]);
    return out;
  }
  return v;
}

async function main() {
  const check = process.argv.includes('--check');
  const fixtures = replay.list();
  if (!fixtures.length) {
    console.error('no fixtures under testdata/fixtures — nothing to generate from');
    process.exit(1);
  }

  const written = [], noPayload = [], failed = [], stale = [];

  for (const entry of fixtures) {
    const label = entry.router + '/' + entry.collector;
    let payload, emitted = [];
    try {
      ({ payload, emitted } = await replay.run(entry));
    } catch (e) {
      failed.push(label + ': ' + ((e && e.message) || e));
      continue;
    }
    // A collector that emits its payload rather than parking it — see
    // EMIT_GOLDEN. The LAST emit of the named event wins, matching what a
    // browser attaching at that moment would have been sent.
    if (!payload && EMIT_GOLDEN[entry.collector]) {
      const want = EMIT_GOLDEN[entry.collector];
      for (const e of emitted) if (e.ev === want) payload = e.data;
    }
    // Not a failure: a collector that fills a cache on the read and emits from
    // a stream callback or a heartbeat has no snapshot payload to pin, and the
    // differential suite says so rather than asserting the wrong thing about a
    // correct collector.
    //
    // CORRECTED 2026-08-24. This named "arp, netwatch, system, topology and
    // dhcpNetworks". Only ONE of those five is still in that state: the run
    // reports `arp, conns, traffic`, and netwatch, system, topology and
    // dhcpNetworks all have goldens on disk. The stale list was read as
    // authoritative one tick earlier and produced a wrong claim in the port record
    // ("system has no golden") that had to be retracted — so the names are gone
    // and the RUN is the authority. It prints the current set every time.
    if (!payload) { noPayload.push(label); continue; }

    const dir  = path.join(GOLDEN, entry.router);
    const file = path.join(dir, entry.collector + '.json');
    const body = JSON.stringify(normalise(payload), null, 2) + '\n';

    if (check) {
      const cur = fs.existsSync(file) ? fs.readFileSync(file, 'utf8') : null;
      if (cur !== body) stale.push(label);
      continue;
    }
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(file, body);
    written.push(label);
  }

  if (failed.length) {
    console.error('FAILED to replay:\n  ' + failed.join('\n  '));
    process.exit(1);
  }
  if (check) {
    if (stale.length) {
      console.error('stale goldens (' + stale.length + '):\n  ' + stale.join('\n  ') +
                    '\nrun: node tools/make-golden.js');
      process.exit(1);
    }
    console.log('goldens up to date (' + (fixtures.length - noPayload.length) + ')');
    return;
  }
  console.log('wrote ' + written.length + ' goldens to testdata/golden');
  if (noPayload.length)
    console.log('no snapshot payload (emits elsewhere): ' + noPayload.join(', '));
}

main().catch((e) => { console.error(e); process.exit(1); });
