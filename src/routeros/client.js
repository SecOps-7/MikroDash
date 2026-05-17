/**
 * MikroDash RouterOS client — node-routeros wrapper v0.3.3
 *
 * node-routeros stream() signature:
 *   conn.stream(wordsArray, callback)   ← two args only, no params array
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
    };
    if (this.cfg.debug) opts.debug = true;
    return new RouterOSAPI(opts);
  }

  _emitConnectionError(err) {
    this.emit('connectionError', err);
    // Only forward to 'error' if someone is explicitly listening —
    // emitting 'error' with no listeners would crash the process.
    if (this.listenerCount('error') > 0) this.emit('error', err);
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
          this.emit('close');
        });

        await this.conn.connect();
        this.connected = true;
        this.backoffMs = 2000;
        // Success is logged by wireRosEvents connected handler
        this.emit('connected');

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
  stream(words, callback) {
    if (!this.conn || !this.connected) throw new Error('Not connected');
    if (!Array.isArray(words)) words = [words];
    return this.conn.stream(words, callback);
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
