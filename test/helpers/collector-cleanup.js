'use strict';
/**
 * Stop every collector a test file constructs, once that file is done.
 *
 * Collectors schedule timers that are meant to keep a SERVER alive. A test that
 * constructs one and never stops it therefore keeps the TEST process alive too,
 * and four suites did exactly that — 109 unstopped instances in one file alone.
 *
 * That is why the documented command used to carry `--test-force-exit`, and why the
 * reported test count was unstable: the flag kills the process the moment the
 * runner thinks it is finished, which intermittently truncated the tail of the
 * largest file. Fewer tests reported, still green — the most misleading result
 * a suite can give.
 *
 * Wrapping the constructor rather than editing every call site keeps this to one
 * line per require, and means a test added later is covered without anyone
 * remembering to clean up.
 *
 *   const SystemCollector = track(require('../src/collectors/system'));
 */
const { after } = require('node:test');

const tracked = [];

function track(Ctor) {
  return new Proxy(Ctor, {
    construct(target, args) {
      const inst = Reflect.construct(target, args);
      tracked.push(inst);
      return inst;
    },
  });
}

// Suspend as well as stop: a few collectors park their streams in suspend() and
// only release the rest in stop(), and calling both is harmless on all of them.
after(() => {
  for (const c of tracked) {
    for (const m of ['stop', 'suspend']) {
      try { if (typeof c[m] === 'function') c[m](); } catch (_) { /* teardown must not fail a run */ }
    }
  }
  tracked.length = 0;
});

module.exports = { track, tracked };
