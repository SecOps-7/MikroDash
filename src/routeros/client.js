/**
 * MikroDash RouterOS client — node-routeros wrapper v0.3.3
 *
 * node-routeros stream() accepts a flattened words array.  This wrapper also
 * supports the write()-style three-argument form used by collectors:
 *   stream(command, paramsArray, callback)
 *
 * node-routeros write() signature:
 *   conn.write(cmd, paramsArray)        ← cmd string + optional array of '=k=v' strings
 */

const { RouterOSAPI } = require('node-routeros');
const EventEmitter = require('events');
const log = require('../util/logger');

class ROS extends EventEmitter {
  constructor(cfg) {
    super();
    // ~11 collectors × 2 events each = 22 listeners minimum
    this.setMaxListeners(30);
    this.cfg = cfg;
    this.conn = null;
    this.connected = false;
    this.backoffMs = 2000;
    this.maxBackoffMs = 30000;
    this._stopping = false;
    this._wakeResolve = null;
    this._sleepTimer = null;
    // One rejection signal per CONNECTION GENERATION, shared by every in-flight
    // write on that connection. Not a listener per write: setMaxListeners is 30
    // and ~22 are already taken by collectors, so a burst of concurrent writes
    // would trip MaxListenersExceededWarning. See _armCloseSignal.
    this._closeSignal = null;
    // Default sleep is interruptible: stop() can call _wakeResolve() to wake immediately.
    // Tests override this._sleep to control timing without real delays.
    this._sleep = (ms) => new Promise(resolve => {
      this._wakeResolve = resolve;
      this._sleepTimer = setTimeout(resolve, ms);
    }).finally(() => {
      this._wakeResolve = null;
      this._sleepTimer = null;
    });
  }

  // Router label — used only to prefix log lines (collectors build `_lbl` from it).
  // It comes from an admin-typed label or, while the label is still the default,
  // from the device's own board-name — so a hostile or compromised router can
  // influence it. Sanitise once here instead of at the 85+ logging call sites:
  // control characters would let a label forge whole log lines, and `%` would
  // become a format specifier if a log line ever placed the label in format-string
  // position. See AI_CONTEXT.md → "Static analysis (CodeQL)".
  set routerLabel(v) {
    this._routerLabel = String(v == null ? '' : v).replace(/[\x00-\x1f\x7f]/g, '').replace(/%/g, '');
  }

  get routerLabel() { return this._routerLabel; }

  _buildConn() {
    // Pass this.cfg.tls directly — it may be false, true, or an options object
    // such as { rejectUnauthorized: false } built by buildSession()/test endpoint.
    // node-routeros Connector passes it straight to tls.connect(), so an object
    // is required to override rejectUnauthorized.  A boolean true is converted
    // by node-routeros to {} which leaves rejectUnauthorized at its default (true).
    const opts = {
      host:     this.cfg.host,
      user:     this.cfg.username,
      password: this.cfg.password,
      port:     this.cfg.port    || 8729,
      tls:      this.cfg.tls     || false,
      timeout:  this.cfg.timeout || 15,
      // node-routeros closes a connection idle for `timeout` seconds, and
      // without keepalive nothing prevents that. A session with no collectors
      // (the status-only alert session built for a router with alerts disabled)
      // sends nothing at all, so it was closed at 15 s and reconnected after the
      // 2 s backoff, forever: a login every ~17 s against every non-active
      // router, each one a fresh TLS handshake. keepalive writes a '#' no-op
      // every timeout/2 (7.5 s), comfortably inside the window. Connections that
      // carry collectors are never idle, so it costs them nothing. (#107)
      keepalive: true,
    };
    if (this.cfg.debug) opts.debug = true;
    return new RouterOSAPI(opts);
  }

  /**
   * Ask this connection — and only this one — for bytes rather than text.
   *
   * The receiver decodes every API word as UTF-8, which is right for every
   * collector and wrong for `/file/read`: that returns raw file bytes, and a
   * UTF-8 decode replaces each invalid one with U+FFFD. It fails silently,
   * because one replacement character per bad byte leaves the reassembled
   * length matching the file size exactly. Verified against a live AX3: a
   * known blob read back with a different sha256 and 177 of its 256 distinct
   * byte values surviving.
   *
   * With `rawBytes`, Patch 3 in patch-routeros.js decodes as latin1 instead —
   * one code unit per byte, so `Buffer.from(str, 'latin1')` recovers the file
   * exactly. Only the backup transport sets it, on its own short-lived
   * connection, so no collector sees a different string than it does today.
   *
   * The receiver is rebuilt on every reconnect, which is why this runs inside
   * connectLoop rather than once at construction.
   */
  _applyRawBytes() {
    if (!this.cfg.rawBytes) return;
    const receiver = this.conn && this.conn.connector && this.conn.connector.receiver;
    if (!receiver) {
      throw new Error('rawBytes requested but the connection exposes no receiver');
    }
    // The PATCH is what reads the flag, and patch-routeros.js only warns when a
    // library update moves its target — so an unpatched receiver would accept
    // `rawBytes = true`, ignore it, and hand back a file that is the right
    // length and the wrong bytes. Refuse instead: a backup that cannot be
    // restored is worse than one that was never taken.
    if (!/rawBytes/.test(String(receiver.processRawData))) {
      throw new Error('rawBytes requested but Receiver.js is unpatched — ' +
                      'see Patch 3 in patch-routeros.js');
    }
    receiver.rawBytes = true;
  }

  // emit() runs listeners synchronously — one throwing listener would otherwise
  // escape connectLoop's catch and permanently end the reconnect loop (or, from
  // a conn callback, crash the process). Contain it here.
  _safeEmit(event, arg) {
    try { this.emit(event, arg); }
    catch (e) { console.error('%s', `[ROS] "${event}" listener threw:`, e && e.message ? e.message : e); }
  }

  _emitConnectionError(err) {
    this._safeEmit('connectionError', err);
    // Only forward to 'error' if someone is explicitly listening —
    // emitting 'error' with no listeners would crash the process.
    if (this.listenerCount('error') > 0) this._safeEmit('error', err);
  }

  async connectLoop() {
    while (!this._stopping) {
      const host = this.cfg.host;
      const port = this.cfg.port || 8729;
      const user = this.cfg.username;
      const tls  = this.cfg.tls !== false;
      try {
        log.debug(`[ROS] connecting to ${host}:${port} as "${user}" (${tls ? 'TLS' : 'plain'})…`);
        this.conn = this._buildConn();
        this._armCloseSignal();

        this.conn.on('error', (err) => {
          // Suppress — wireRosEvents connectionError handler logs the classified reason
          this.connected = false;
          this._fireCloseSignal('error');
          this._emitConnectionError(err);
        });

        this.conn.on('close', () => {
          this.connected = false;
          this._fireCloseSignal('close');
          this._safeEmit('close');
        });

        await this.conn.connect();

        // stop() may have run while we were connecting or logging in. That is
        // not harmless: RouterOSAPI.close() returns early while `connected` is
        // false, so stop()'s close did NOTHING and this is now a live,
        // authenticated login that nobody owns. Emitting 'connected' here would
        // start all collectors on a session the pool has already dropped, which
        // then holds that login open and fires alerts through a stale closure.
        // A bare `break` is not enough — it would leave the same zombie socket.
        if (this._stopping) {
          try {
            const p = this.conn.close();
            if (p && typeof p.catch === 'function') p.catch(() => {});
          } catch (_) {}
          break;
        }

        this._applyRawBytes();
        this.connected = true;
        this.backoffMs = 2000;
        // Success is logged by wireRosEvents connected handler
        this._safeEmit('connected');

        await new Promise((resolve) => {
          this.conn.once('close', resolve);
          this.conn.once('error', resolve);
        });

      } catch (e) {
        this.connected = false;
        // Don't log here — wireRosEvents connectionError handler logs the classified reason
        this._emitConnectionError(e);
      }

      if (this._stopping) break;
      log.debug(`[ROS] reconnecting to ${host}:${port} in ${this.backoffMs}ms…`);
      await this._sleep(this.backoffMs);
      this.backoffMs = Math.min(this.backoffMs * 2, this.maxBackoffMs);
    }
  }

  async waitUntilConnected(timeoutMs = 60000) {
    if (this.connected) return;
    return new Promise((resolve, reject) => {
      const t = setTimeout(() => {
        this.off('connected', onConn);
        reject(new Error('Timed out waiting for RouterOS connection'));
      }, timeoutMs);
      const onConn = () => {
        clearTimeout(t);
        resolve();
      };
      this.once('connected', onConn);
    });
  }

  /**
   * One-shot command. Returns Promise<Array<object>>.
   * params is an optional array of '=key=value' strings.
   * timeoutMs caps how long we wait for a reply (default 30 s).
   */
  /**
   * Arm a fresh close signal for the connection just built.
   *
   * Exists because a one-shot write can otherwise ONLY end via its 30 s timer.
   * The library settles a channel promise on `done` or `trap` and on nothing
   * else: Channel.close() emits and removes listeners without rejecting, and
   * write channels are not registered, so stopAllStreams() never reaches them.
   * A write in flight when the socket dies therefore hangs for the full
   * timeout, which is exactly what #118 reported.
   */
  _armCloseSignal() {
    const sig = { fired: false };
    sig.promise = new Promise((_, reject) => { sig.fail = reject; });
    // Nobody may be awaiting this when the socket dies. A bare rejected promise
    // would reach process.on('unhandledRejection') once per reconnect; this
    // marks it handled and does not affect racers, which attach their own.
    sig.promise.catch(() => {});
    this._closeSignal = sig;
  }

  _fireCloseSignal(why) {
    const sig = this._closeSignal;
    if (!sig || sig.fired) return;
    sig.fired = true;
    sig.fail(new Error(`RouterOS connection closed before reply (${why})`));
  }

  async write(cmd, params, timeoutMs = this.cfg.writeTimeoutMs || 30000) {
    if (!this.conn || !this.connected) throw new Error('Not connected');
    const activeConn = this.conn;
    let timer = null;

    const closeSig = this._closeSignal;   // may be null in tests that set .conn by hand

    try {
      const pending = activeConn.write(cmd, params || []);
      // A trap arriving AFTER the timeout or the close signal has already won
      // the race would otherwise be an unhandled rejection on a promise nobody
      // is holding any more.
      if (pending && typeof pending.catch === 'function') pending.catch(() => {});

      const racers = [
        pending,
        new Promise((_, reject) => {
          timer = setTimeout(() => reject(new Error(`RouterOS write timeout (${timeoutMs}ms): ${cmd}`)), timeoutMs);
        }),
      ];
      // Fail fast when the connection goes away instead of waiting out the full
      // 30 s. Without this a teardown mid-write left ~26 write-capable
      // collectors each stalled for the timeout (#118).
      if (closeSig) racers.push(closeSig.promise);

      const result = await Promise.race(racers);
      // Normalise null/undefined (e.g. from !empty responses before patch applies)
      return Array.isArray(result) ? result : (result == null ? [] : result);
    } catch (err) {
      const msg = String(err && err.message ? err.message : err);
      // Only a genuine TIMEOUT tears the connection down. A 'closed before
      // reply' rejection means the connection is already gone, so closing again
      // would be redundant — and would be the second close on a socket that is
      // mid-teardown.
      if (msg.includes('write timeout') && !msg.includes('closed before reply') && this.conn === activeConn) {
        this.connected = false;
        // close() returns a REJECTED promise when already closing, which a
        // try/catch cannot catch — it is not a throw. Without this .catch it
        // reaches process.on('unhandledRejection').
        try {
          const p = activeConn.close();
          if (p && typeof p.catch === 'function') p.catch(() => {});
        } catch (_) {}
      }
      throw err;
    } finally {
      if (timer) clearTimeout(timer);
    }
  }

  /**
   * Persistent push stream.
   * CORRECT signature: conn.stream(wordsArray, callback)
   *   wordsArray — ['/cmd', '=param=value', ...]
   *   callback   — function(err, data) called on every !re sentence
   * Returns a Stream object with .stop(), .pause(), .resume() methods.
   */
  stream(words, paramsOrCallback, callback) {
    if (!this.conn || !this.connected) throw new Error('Not connected');
    const wordsArr = Array.isArray(words) ? [...words] : [words];
    let cb = null;

    if (Array.isArray(paramsOrCallback)) wordsArr.push(...paramsOrCallback);
    else if (typeof paramsOrCallback === 'string') wordsArr.push(paramsOrCallback);
    else if (typeof paramsOrCallback === 'function') cb = paramsOrCallback;

    if (typeof callback === 'function') cb = callback;
    return this.conn.stream(wordsArr, cb);
  }

  stop() {
    // Idempotent, and that is load-bearing rather than tidiness.
    //
    // stop() closes the connection; closing emits 'close'; the 'close' handler
    // calls _safeEmit('close'), which runs every listener the CALLER attached.
    // So a caller who stops the client when it closes — the obvious thing to
    // write, and what test/collector-lifecycle.test.js does — re-enters here and
    // recurses synchronously until the stack gives out. It does not throw
    // anywhere useful: the process simply stops making progress, which is how
    // this presented for months as a test that "timed out after 1000ms" and read
    // as flakiness under load.
    //
    // Nothing in this repo stops a ROS client from its own close handler today,
    // so this was not reachable in production. It is guarded because the shape
    // is one a caller is entitled to write, not because someone did.
    if (this._stopping) return;
    this._stopping = true;
    // Lower `connected` HERE, not when the socket's close event eventually
    // lands. teardownSession calls stop() and then yields only 150 ms; any
    // collector tick inside that window used to see connected === true, pass
    // its own guard and pass write()'s gate, and hand a command to a dying
    // socket — which the library silently QUEUES rather than refusing
    // (Transmitter pools when !socket.writable). Nothing then settles it. (#118)
    this.connected = false;
    if (this._sleepTimer) { clearTimeout(this._sleepTimer); this._sleepTimer = null; }
    if (this._wakeResolve) { this._wakeResolve(); this._wakeResolve = null; }
    // Fire the signal from here too, not only from the 'close' handler. When
    // stop() lands on a connection that is still connecting, RouterOSAPI.close()
    // returns early without emitting 'close' at all, so a close-event-only
    // signal would leave every in-flight write hanging for the full timeout.
    this._fireCloseSignal('stopped');
    if (this.conn) {
      try {
        const p = this.conn.close();
        if (p && typeof p.catch === 'function') p.catch(() => {});
      } catch (_) {}
    }
  }
}

module.exports = ROS;
