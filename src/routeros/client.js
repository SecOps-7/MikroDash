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
const util = require('util');
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

function formatError(err) {
  if (!err) return 'Unknown error';
  const parts = [];
  const name = err.name || (err.constructor && err.constructor.name) || 'Error';
  const message = err.message || String(err);
  parts.push(`${name}: ${message}`);

  for (const key of ['code', 'errno', 'syscall', 'address', 'port']) {
    if (err[key] !== undefined) parts.push(`${key}=${err[key]}`);
  }
  if (err.cause) {
    const cause = err.cause;
    parts.push(`cause=${cause.name || 'Error'}:${cause.message || String(cause)}`);
    for (const key of ['code', 'errno', 'syscall', 'address', 'port']) {
      if (cause[key] !== undefined) parts.push(`cause.${key}=${cause[key]}`);
    }
  }

  if (parts.length === 1) {
    const inspected = util.inspect(err, { depth: 2, breakLength: Infinity });
    if (inspected && inspected !== '[object Object]') parts.push(inspected);
  }
  return parts.join(' ');
}

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
    this._sleep = sleep;
  }

  _buildConn() {
    const opts = {
      host:     this.cfg.host,
      user:     this.cfg.username,
      password: this.cfg.password,
      port:     this.cfg.port    || 8729,
      tls:      this.cfg.tls     !== false,
      timeout:  this.cfg.timeout || 15,
    };
    if (this.cfg.tls && this.cfg.tlsInsecure) {
      opts.tlsOptions = { rejectUnauthorized: false };
    }
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
        console.log(`[ROS] connecting to ${host}:${port} as "${user}" (${tls ? 'TLS' : 'plain'})…`);
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
      console.log(`[ROS] reconnecting to ${host}:${port} in ${this.backoffMs}ms…`);
      await this._sleep(this.backoffMs);
      this.backoffMs = Math.min(this.backoffMs * 2, this.maxBackoffMs);
    }
  }

  async waitUntilConnected(timeoutMs = 60000) {
    if (this.connected) return;
    const deadline = Date.now() + timeoutMs;
    while (!this.connected) {
      if (Date.now() > deadline) throw new Error('Timed out waiting for RouterOS connection');
      await sleep(200);
    }
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
    if (this.conn) {
      try { this.conn.close(); } catch (_) {}
    }
  }
}

module.exports = ROS;
