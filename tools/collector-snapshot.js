'use strict';
/**
 * How to make a MikroDash collector take exactly one reading.
 *
 * ONE DEFINITION, SHARED BY THE CAPTURE AND THE REPLAY. They must agree: the
 * capture drives a collector against a live router and the replay drives the
 * same collector against the recording, so if one knows about `_loadInitial`
 * and the other does not, six collectors capture cleanly and then "ask the
 * router nothing" on the way back. That is exactly what happened, which is why
 * this file exists rather than a second copy of the list.
 *
 * There is no single convention in the collectors — they grew their own names
 * as they were written. The order is a preference, not an alphabet: `_tick`
 * drives a full emit where `_loadInitial` only fills a cache, so the earlier
 * entry produces the more useful reading when a collector has both.
 */

const SNAPSHOT_METHODS = ['_tick', 'refreshNow', '_pollOnce', '_pollAllOnce',
                          '_pollResourceOnce', 'tick', '_loadInitial', '_fetchOnce'];

/** collector key -> module file, where the two differ. */
const MODULE_OF = { conns: 'connections', ifStatus: 'interfaceStatus' };

/**
 * Take one reading. Returns the method used, or null if the collector has none —
 * a pure-streaming collector such as traffic genuinely has nothing to snapshot,
 * which is a shape rather than a failure.
 */
async function snapshot(collector) {
  const name = SNAPSHOT_METHODS.find(m => typeof collector[m] === 'function');
  if (!name) return null;
  await collector[name]();
  return name;
}

/**
 * Put the collector into the state the RUNNING APP puts it in.
 *
 * A snapshot method is not always self-sufficient. Five of the six collectors
 * that had no fixture read nothing when driven directly, each for its own
 * reason, and all five reasons reduce to the same one: `start()` is what
 * establishes the state the read path checks for.
 *
 *   connections  `_pollOnce` returns at `if (!this._started)`.
 *   wireless     `_pollOnce` returns at `if (!this._pollTypes.size)`; the set is
 *                filled by `_startDelivery()`, which only start() calls.
 *   traffic      has no snapshot method at all — start() opening the
 *                /interface/monitor-traffic stream is the entire read path.
 *   ping         start() -> `_startPing()` -> `_pollPingOnce()`.
 *   talkers      start() -> `_startTalkers()` -> `_pollTalkersOnce()`.
 *
 * WHY THIS IS A FALLBACK RATHER THAN THE DEFAULT. Driving every collector
 * through start() would be more faithful still, and it is tempting. It would
 * also re-drive all twenty-one collectors that already capture cleanly — start()
 * can only make a collector do MORE, so every one of those fixtures could gain
 * rows, every golden would move, and the Go differential gate would have to be
 * re-verified for the collectors already ported against them. That is a corpus-
 * wide change to solve a six-collector problem.
 *
 * So it runs only when a reading produced NOTHING — no read, no stream row.
 * That condition is observable rather than declared, which is the same reason
 * this tool wraps the transport instead of listing commands per collector: a
 * hard-coded list of "collectors that need starting" would be wrong the first
 * time a collector changed shape, and wrong silently.
 *
 * The caller decides WHEN to fall back, because the capture and the replay
 * measure "produced nothing" differently — one watches a live transport, the
 * other counts what the fixture was asked for. They must agree on the RULE, and
 * that rule lives here.
 */
const START_METHODS = ['start'];

/**
 * START AND THEN RESUME, because started is not the same as running.
 *
 * connections sets `this._suspended = true` in its CONSTRUCTOR and `start()`
 * does not clear it — only `resume()` does, called from the page-gating path
 * when a viewer attaches. So a merely-started ConnectionsCollector still
 * returns at `if (!this._started || this._suspended)` and reads nothing, which
 * is precisely how it looked: started, willing, and silent.
 *
 * Resuming is the honest state to capture in. The fake io reports
 * `clientsCount: 1` — the capture is claiming a viewer is watching, and every
 * collector's gating believes it. Claiming that and then leaving the collector
 * suspended is the contradiction, not the fix.
 *
 * Collectors that resume themselves (wireless calls `this.resume()` at the end
 * of its own start()) are unharmed: resume is idempotent in all of them.
 */
const RESUME_METHODS = ['resume'];

async function begin(collector, engaged = () => false) {
  const name = START_METHODS.find(m => typeof collector[m] === 'function');
  if (!name) return null;
  await collector[name]();

  // RESUME ONLY IF START() DID NOT ALREADY READ.
  //
  // Both halves of this were learned the hard way, in opposite directions.
  // Resuming unconditionally double-reads: ping's start() calls
  // `_pollPingOnce()` and resume() calls `_startPing()` again, so the fixture
  // recorded /tool/ping twice. Not resuming at all leaves connections silent:
  // it sets `this._suspended = true` in its CONSTRUCTOR, start() does not clear
  // it, and `_pollOnce` returns at `if (!this._started || this._suspended)`.
  //
  // Gating on `collector._suspended` looks like the precise question and is not
  // — it describes the collector's opinion of itself, and connections' start()
  // leaves that flag in a state that varies with how the ros connection event
  // landed. The question that actually matters is whether the ROUTER was
  // engaged, and only the caller can see that: the capture watches a live
  // transport, the replay counts what the fixture was asked for. So the caller
  // supplies the measurement and the RULE stays here, which is the same split
  // the fallback condition itself uses.
  if (engaged()) return name;
  const res = RESUME_METHODS.find(m => typeof collector[m] === 'function');
  if (res) await collector[res]();
  return name;
}

/**
 * How long to let a started collector work before closing the log.
 *
 * A collector that needed starting does its reading on its own schedule — that
 * is why the snapshot method could not do it. ping is the binding case: start()
 * fires `_pollPingOnce()` without awaiting it, and that issues
 * `/tool/ping =count=3 =interval=1`, so the answer is three seconds away. The
 * 400ms that suffices for a fire-and-forget follow-up would record the
 * subscription and none of the result.
 */
const BEGIN_WINDOW_MS = 6000;

/**
 * Methods that build a payload from whatever the collector currently holds.
 *
 * A stream-driven collector does not emit from its snapshot method: ifStatus's
 * `refreshNow()` only restarts its metadata streams, and the payload is built
 * later on an emit timer. Replaying such a collector and reading `lastPayload`
 * straight afterwards therefore finds null — not because the fixture is wrong,
 * but because nothing has asked it to build yet.
 *
 * Listed here, beside SNAPSHOT_METHODS, for the same reason that list exists:
 * the capture and the replay must agree about how to drive a collector, and the
 * one time they disagreed six collectors captured cleanly and replayed into
 * nothing.
 */
const EMIT_METHODS = ['_buildAndEmit'];

/**
 * Methods that take one reading of something the STREAMS cannot supply.
 *
 * interfaceStatus is the case: its rates come from `/interface/monitor-traffic`,
 * which it can only ask about once the metadata streams have told it which
 * interfaces exist. So the order is not negotiable — streams deliver, the meta
 * commit lands, and only then is there a list of names to poll rates for.
 *
 * Without this the ifStatus fixture captured cleanly and carried `rxMbps: 0` for
 * every interface: true to what the collector had, and useless for testing the
 * rate path. Read-only, like everything else the capture drives.
 */
const POLL_METHODS = ['_pollRatesOnce'];

/**
 * Handles for a build the collector has SCHEDULED but not yet run.
 *
 * dhcpNetworks ends `_pollAllOnce()` with `_scheduleRebuild()`, a 10ms timer, so
 * its snapshot method returns before the payload exists. settle() used to take
 * its fast path here — no `_buildAndEmit`, no `_pollRatesOnce`, nothing to wait
 * for — and run() called `stop()` while the timer was still pending. The payload
 * came back null, and both the replay suite and make-golden.js reported the
 * collector as one that "fills a cache and emits elsewhere".
 *
 * IT DOES NOT. It builds a perfectly good payload ten milliseconds later, and
 * the harness was describing a gap of its own making. That is worth naming
 * rather than fixing quietly: this project has now found three guards that could
 * not fail and one that measured itself, and the lesson each time is the same —
 * a gate reporting an absence is making a claim, and the claim needs checking.
 */
const DEBOUNCE_FIELDS = ['_rebuildDebounce'];

/** Is a scheduled build still pending? */
function buildPending(collector) {
  return DEBOUNCE_FIELDS.some(f => collector[f] != null);
}

/**
 * Let asynchronous deliveries land, then force a payload if the collector has
 * a method for it.
 *
 * The wait is what makes a recorded stream usable: the replay hands its rows
 * over on the next tick, because a collector registers its `data` handler after
 * stream() returns, so anything delivered synchronously would arrive before
 * anyone was listening.
 */
async function settle(collector, ms = 400) {
  const emit = EMIT_METHODS.find(m => typeof collector[m] === 'function');
  const poll = POLL_METHODS.find(m => typeof collector[m] === 'function');
  // Poll-driven collectors emit from their snapshot method and have nothing to
  // wait for. Checking first rather than sleeping unconditionally keeps the
  // whole replay suite fast — most of the fixtures pay nothing.
  if (!emit && !poll && !buildPending(collector)) return null;

  // A scheduled build is an asynchronous delivery like any other, and it is
  // short: wait for the timer to fire rather than for the full `ms`, so the one
  // collector that needs this pays ten milliseconds and not four hundred.
  if (!emit && !poll) {
    for (let i = 0; i < 100 && buildPending(collector); i++) {
      await new Promise(r => setTimeout(r, 5));
    }
    return 'scheduled-build';
  }
  // Longer than the 300ms debounce in interfaceStatus._scheduleMetaCommit. A
  // shorter wait leaves `_ifaces` empty, the rates poll finds no names to ask
  // about, `_buildAndEmit` returns at its first line, and the payload comes back
  // null for a fixture that is perfectly good.
  await new Promise(r => setTimeout(r, ms));
  // Poll BEFORE emitting: the rates have to be in hand for the payload to carry
  // them, and the poll is what the streams cannot do for themselves.
  if (poll) await collector[poll]();
  if (emit) await collector[emit]();
  return emit || poll;
}

/**
 * The io surface every collector expects to exist.
 *
 * ping and talkers both call `io.on('connection', ...)` from their CONSTRUCTOR,
 * so an io without a listener surface does not fail at read time with a useful
 * message — it throws before the collector exists at all. The capture's fake io
 * had these methods; the replay's did not, and three fixtures captured cleanly
 * and then reported "the collector neither read from nor subscribed to the
 * router", which is true and says nothing about why.
 *
 * Here rather than in either caller for the same reason SNAPSHOT_METHODS is
 * here: the capture and the replay must agree about the environment they put a
 * collector in, and the two times they did not, the symptom appeared a long way
 * from the cause.
 */
const IO_LISTENERS = { on() {}, off() {}, once() {}, removeListener() {} };

module.exports = { SNAPSHOT_METHODS, EMIT_METHODS, MODULE_OF, BEGIN_WINDOW_MS,
                   IO_LISTENERS, snapshot, begin, settle };
