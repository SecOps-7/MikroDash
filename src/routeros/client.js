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
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

class ROS extends EventEmitter {
  constructor(cfg) {
    super();
    // ~11 collectors × 2 events each = 22 listeners minimum
    this.setMaxListeners(30);
    this.on('error', () => {});
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

  async connectLoop() {
    while (!this._stopping) {
      try {
        this.conn = this._buildConn();

        this.conn.on('error', (err) => {
          console.error('[ROS] error:', err && err.message ? err.message : err);
          this.connected = false;
          this.emit('error', err);
        });

        this.conn.on('close', () => {
          if (this.connected) console.log('[ROS] connection closed');
          this.connected = false;
          this.emit('close');
        });

        await this.conn.connect();
        this.connected = true;
        this.backoffMs = 2000;
        console.log('[ROS] connected to', this.cfg.host);
        this.emit('connected');

        await new Promise((resolve) => {
          this.conn.once('close', resolve);
          this.conn.once('error', resolve);
        });

      } catch (e) {
        this.connected = false;
        console.error('[ROS] connect failed:', e && e.message ? e.message : e);
        this.emit('error', e);
      }

      if (this._stopping) break;
      console.log(`[ROS] reconnecting in ${this.backoffMs}ms…`);
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
