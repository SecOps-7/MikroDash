'use strict';
/**
 * Replay a captured router into a real collector.
 *
 * The other half of tools/capture-fixtures.js. The capture records what a
 * collector asked a live router and what came back; this feeds those recorded
 * answers to the same collector with no router present, so the transform can be
 * asserted against hardware behaviour rather than against invented rows.
 *
 * WHY THIS IS THE POINT OF THE PORT. A Go implementation has to reproduce these
 * payloads from these same inputs. That is the contract, and it is worth more
 * than any prose description of RouterOS's habits — a fixture cannot be
 * misremembered, and it fails loudly when a behaviour is not reproduced.
 *
 * Matching is by command AND parameters, because several collectors read the
 * same menu with different proplists and the answers differ accordingly. An
 * unmatched command returns [] rather than throwing: a collector legitimately
 * probes menus a given router does not have, and the capture records that
 * absence by simply not containing them.
 */

const fs   = require('node:fs');
const path = require('node:path');

// Resolved against the CWD, not against this file: require() resolves a relative
// path from the requiring module's directory, so a bare `../MikroDash` looks for
// nodecheck/MikroDash and every collector fails to load.
// Longer than the 300ms debounce in interfaceStatus._scheduleMetaCommit, so a
// commit lands between one delivered tick and the next.
const SECTION_GAP_MS = 320;

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', '..', 'MikroDash'));
const FIXTURES = path.join(__dirname, '..', '..', 'testdata', 'fixtures');

/** Every captured fixture, as { router, collector, file, data }. */
function list() {
  if (!fs.existsSync(FIXTURES)) return [];
  const out = [];
  for (const router of fs.readdirSync(FIXTURES)) {
    const dir = path.join(FIXTURES, router);
    if (!fs.statSync(dir).isDirectory()) continue;
    for (const f of fs.readdirSync(dir)) {
      if (!f.endsWith('.json') || f.endsWith('.expected.json')) continue;
      out.push({
        router, collector: f.replace(/\.json$/, ''),
        file: path.join(dir, f),
        data: JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8')),
      });
    }
  }
  return out;
}

/** A ROS that answers only from the capture. */
function replayRos(fixture) {
  const key = (cmd, params) => cmd + ' ' + JSON.stringify(params || []);
  const byKey = new Map();
  const byCmd = new Map();
  // The EXCHANGE, not its rows: an exchange can also record a refusal, and the
  // difference between "no rows" and "refused" is one collectors branch on.
  for (const ex of fixture.exchanges || []) {
    byKey.set(key(ex.cmd, ex.params), ex);
    if (!byCmd.has(ex.cmd)) byCmd.set(ex.cmd, ex);
  }

  // Streams, recorded by tools/capture-fixtures.js. Same matching rule as reads:
  // exact command AND parameters first, then the same menu with any parameters.
  const streamByKey = new Map();
  const streamByCmd = new Map();
  for (const st of fixture.streams || []) {
    streamByKey.set(key(st.cmd, st.params), st.rows);
    if (!streamByCmd.has(st.cmd)) streamByCmd.set(st.cmd, st.rows);
  }

  const asked = [];
  const streamed = [];
  // Every in-flight stream delivery, so run() can wait for them before it reads
  // a payload. Without this the settle fires mid-delivery and the payload is
  // built from a partial view.
  const deliveries = [];
  return {
    connected: true,
    cfg: { username: 'replay' },
    routerLabel: (fixture.router && fixture.router.model) || 'replay',
    asked,
    streamed,
    async write(cmd, params) {
      asked.push({ cmd, params: params || [] });
      // Same menu, different proplist: better to answer with what was captured
      // than to pretend the menu is absent, which would exercise a fallback the
      // collector should not be taking.
      const ex = byKey.get(key(cmd, params)) || byCmd.get(cmd);
      if (!ex) return [];
      // Refusals are replayed as refusals. The capture recorded that this router
      // rejected this command; answering `[]` instead would tell the collector
      // the menu exists and is empty, which is the one thing it must not
      // conclude — see the note in tools/capture-fixtures.js.
      if (ex.error) throw new Error(ex.error);
      return ex.rows;
    },

    /**
     * Replay a recorded stream.
     *
     * Normalised exactly as ROS.stream and the recorder do, because all three
     * have to agree on the key — that rule is why tools/collector-snapshot.js
     * exists, and it was learned by six collectors capturing cleanly and then
     * replaying into nothing.
     *
     * Rows are delivered on the NEXT TICK, never synchronously: a collector
     * calls stream() and only then registers its `data` handler, so delivering
     * inside the call would fire into a handler that does not exist yet and the
     * fixture would look empty for reasons that have nothing to do with it.
     */
    stream(words, paramsOrCallback, callback) {
      const wordsArr = Array.isArray(words) ? [...words] : [words];
      let cb = null;
      if (Array.isArray(paramsOrCallback)) wordsArr.push(...paramsOrCallback);
      else if (typeof paramsOrCallback === 'string') wordsArr.push(paramsOrCallback);
      else if (typeof paramsOrCallback === 'function') cb = paramsOrCallback;
      if (typeof callback === 'function') cb = callback;

      const cmd = wordsArr[0];
      const params = wordsArr.slice(1);
      streamed.push({ cmd, params });
      const rows = streamByKey.get(key(cmd, params)) || streamByCmd.get(cmd) || [];

      const handlers = { data: [], error: [] };
      let stopped = false;

      // DELIVERED TICK BY TICK, NOT ALL AT ONCE.
      //
      // A RouterOS `=interval=N` print re-sends the whole table every tick and
      // marks each one with `.section`. Delivering every recorded row in a
      // single burst is not what the router does, and the difference is
      // visible: interfaceStatus accumulates rows into `_addrsNext` and only
      // swaps them on a debounced commit, so a burst produced an `ips` array
      // with the same address repeated once per recorded tick, and no
      // error/drop delta at all because a delta needs a commit BETWEEN two
      // ticks to have a baseline. The golden would then have encoded the
      // artefact, and the Go port would have been required to reproduce it.
      //
      // So each `.section` is delivered as its own tick with a gap after it,
      // longer than the 300ms debounce in `_scheduleMetaCommit`. Rows with no
      // `.section` — a /listen channel, which is event-shaped rather than
      // table-shaped — are one group and deliver at once.
      const sections = [];
      for (const row of rows) {
        const key = row && row['.section'] !== undefined ? row['.section'] : '_';
        const last = sections[sections.length - 1];
        if (last && last.key === key) last.rows.push(row);
        else sections.push({ key, rows: [row] });
      }

      deliveries.push((async () => {
        for (let i = 0; i < sections.length; i++) {
          await new Promise(r => setImmediate(r));
          if (stopped) return;
          for (const row of sections[i].rows) {
            for (const h of handlers.data) h(row);
            if (cb) cb(null, row);
          }
          if (i < sections.length - 1) await new Promise(r => setTimeout(r, SECTION_GAP_MS));
        }
      })());
      return {
        on(ev, h) { if (handlers[ev] && typeof h === 'function') handlers[ev].push(h); },
        stop() { stopped = true; },
        pause() {}, resume() {},
      };
    },

    on() {},
    stop() {},
    async waitUntilConnected() { return true; },
    connectLoop() {},
    /** Resolves once every recorded stream has finished delivering its ticks. */
    async streamsSettled() { await Promise.all(deliveries); },
  };
}

/** The io a collector needs, recording what it emitted. */
function replayIo(emitted = []) {
  const chain = { to: () => chain, emit: (ev, data) => emitted.push({ ev, data }) };
  return {
    emitted,
    engine: { clientsCount: 1 },
    emit: (ev, data) => emitted.push({ ev, data }),
    ...IO_LISTENERS,
    to: () => chain,
    sockets: { adapter: { rooms: new Map() } },
  };
}

// Shared with the capture tool: if these two disagree about how to make a
// collector read, a fixture captures cleanly and then replays into nothing.
const { MODULE_OF, IO_LISTENERS, snapshot, begin, settle } = require('../../tools/collector-snapshot');

/**
 * Run one collector against one fixture and return its payload.
 *
 * streamMode:false, matching how the capture was taken — the poll path reads the
 * same menus and completes in a tick.
 */
async function run(entry) {
  const file = MODULE_OF[entry.collector] || entry.collector;
  const Collector = require(path.join(LIVE, 'src', 'collectors', file + '.js'));
  const ros = replayRos(entry.data);
  const emitted = [];
  const io = replayIo(emitted);
  const state = {};
  // defaultIf and target are the values index.js falls back to when a router
  // record names none. They need not equal the ones the capture used: both the
  // read and the stream matcher above fall back to matching by MENU when the
  // parameters differ, which is deliberate — answering with what was captured
  // beats pretending the menu is absent. What matters is that neither is
  // `undefined`, because a collector asked to stream `=interface=undefined` is
  // not exercising anything.
  //
  // `rid` IS NOT OPTIONAL EITHER, and its absence was silent in a way the other
  // two are not. Two collectors stamp it onto their payload — interfaceStatus
  // and topology — and they DEFAULT DIFFERENTLY: `rid || ''` in one, a bare
  // assignment in the other. So a replay that passed none produced a golden
  // pinning `routerId: ''` for interfaceStatus (a value no deployment emits)
  // and NO KEY AT ALL for topology, because JSON.stringify drops undefined.
  //
  // The second of those is why the port shipped a topology payload with no
  // `routerId` for the life of this project: the differential gate had nothing
  // to compare, so it compared nothing and passed. Found on 2026-08-28 by
  // `tools/live-socket-diff.js`, which watches what the two servers actually
  // send rather than what a fixture replays.
  //
  // The value is arbitrary but must be non-empty and RECOGNISABLE, so a golden
  // reader can see it came from here rather than from a capture.
  const c = new Collector({ ros, io, state, pollMs: 30000, streamMode: false,
                            defaultIf: 'ether1', target: '1.1.1.1',
                            rid: 'r-fixture' });
  let engaged = await snapshot(c);

  // The same start()-and-resume fallback the capture applies, on the same
  // condition, for the same reason — collector-snapshot.js holds the rule. If
  // these two disagree about how to drive a collector, a fixture captures
  // cleanly and replays into nothing, which is exactly how the six stream
  // collectors looked for two iterations.
  if (!engaged || (ros.asked.length === 0 && ros.streamed.length === 0)) {
    const started = await begin(c, () => ros.asked.length > 0 || ros.streamed.length > 0);
    // And ask AGAIN, as the capture does. Starting is not always reading:
    // connections' start() in poll mode only clears `_suspended` and schedules
    // a timer thirty seconds out, so the read still has to be driven — it just
    // could not have been driven before start() cleared the flag its first line
    // checks.
    if (started) engaged = (await snapshot(c)) || started;
  }
  // Let every recorded stream finish delivering its ticks before anything reads
  // a payload — a settle that fired mid-delivery would build from a partial view.
  await ros.streamsSettled();
  // Then the shared settle: take any reading the streams cannot supply, and
  // build. Harmless for the poll-driven collectors, which have neither a poll
  // nor an emit method and so pay nothing.
  await settle(c);
  try { c.stop(); } catch (_) { /* not every collector needs stopping */ }
  return { payload: c.lastPayload, emitted, asked: ros.asked, streamed: ros.streamed, state };
}

module.exports = { list, run, replayRos, replayIo, FIXTURES };
