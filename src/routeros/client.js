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

        this.conn.on('error', (err) => {
          // Suppress — wireRosEvents connectionError handler logs the classified reason
          this.connected = false;
          this._emitConnectionError(err);
        });

        this.conn.on('close', () => {
          this.connected = false;
          this._safeEmit('close');
        });

        await this.conn.connect();
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
  async write(cmd, params, timeoutMs = this.cfg.writeTimeoutMs || 30000) {
    if (!this.conn || !this.connected) throw new Error('Not connected');
    const activeConn = this.conn;
    let timer = null;

    try {
      const result = await Promise.race([
        activeConn.write(cmd, params || []),
        new Promise((_, reject) => {
          timer = setTimeout(() => reject(new Error(`RouterOS write timeout (${timeoutMs}ms): ${cmd}`)), timeoutMs);
        }),
      ]);
      // Normalise null/undefined (e.g. from !empty responses before patch applies)
      return Array.isArray(result) ? result : (result == null ? [] : result);
    } catch (err) {
      const msg = String(err && err.message ? err.message : err);
      if (msg.includes('write timeout') && this.conn === activeConn) {
        this.connected = false;
        try { activeConn.close(); } catch (_) {}
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
    this._stopping = true;
    if (this._sleepTimer) { clearTimeout(this._sleepTimer); this._sleepTimer = null; }
    if (this._wakeResolve) { this._wakeResolve(); this._wakeResolve = null; }
    if (this.conn) {
      try { this.conn.close(); } catch (_) {}
    }
  }
}

module.exports = ROS;
