/**
 * RuckusCollector — polls Ruckus Unleashed for connected WiFi clients.
 *
 * Uses the Ruckus Unleashed web UI AJAX API (XML over HTTPS).
 * Auth flow based on: github.com/commscope-ruckus/RUCKUS-Unleashed
 *   1. GET /admin/login.jsp?username=...&password=...&ok=ruckus
 *   2. GET /admin/_csrfTokenVar.jsp  → extract CSRF token
 *   3. POST /admin/_cmdstat.jsp with X-CSRF-Token + text/xml body
 *
 * Completely optional — disabled when RUCKUS_HOST is not set.
 */

'use strict';
const https = require('https');

class RuckusCollector {
  constructor({ io, pollMs, state }) {
    this.io      = io;
    this.pollMs  = pollMs || 10000;
    this.state   = state;
    this.host    = process.env.RUCKUS_HOST || '';
    this.user    = process.env.RUCKUS_USER || '';
    this.pass    = process.env.RUCKUS_PASS || '';
    this.enabled = !!(this.host && this.user && this.pass);

    this.lastClients   = [];
    this.sessionCookie = null;
    this.csrfToken     = null;
    this.timer         = null;
    this._loggedFirst  = false;
  }

  // ── HTTP helper (Node built-in https) ────────────────────────────────────
  _request(options, body) {
    return new Promise((resolve, reject) => {
      const opts = {
        ...options,
        hostname: this.host,
        rejectUnauthorized: false,
      };
      const req = https.request(opts, (res) => {
        const chunks = [];
        res.on('data', (d) => chunks.push(d));
        res.on('end', () => {
          resolve({
            statusCode: res.statusCode,
            headers:    res.headers,
            body:       Buffer.concat(chunks).toString('utf8'),
          });
        });
      });
      req.setTimeout(10000, () => { req.destroy(new Error('timeout')); });
      req.on('error', reject);
      if (body) req.write(body);
      req.end();
    });
  }

  // ── Cookie helpers ──────────────────────────────────────────────────────
  _mergeCookies(setCookies) {
    if (!setCookies) return;
    const newCookies = setCookies.map(c => c.split(';')[0]);
    const existing = this.sessionCookie ? this.sessionCookie.split('; ') : [];
    const merged = new Map();
    existing.concat(newCookies).forEach(c => {
      const [k] = c.split('=');
      merged.set(k, c);
    });
    this.sessionCookie = Array.from(merged.values()).join('; ');
  }

  // ── Core login to a specific host ────────────────────────────────────────
  async _loginToHost() {
    this.sessionCookie = null;
    this.csrfToken = null;

    // Step 1: Login — Ruckus Unleashed uses GET with query params
    const loginPath = '/admin/login.jsp?' + [
      'username=' + encodeURIComponent(this.user),
      'action=login.jsp',
      'password=' + encodeURIComponent(this.pass),
      'ok=ruckus',
    ].join('&');

    const res = await this._request({ method: 'GET', path: loginPath, headers: {} });
    this._mergeCookies(res.headers['set-cookie']);

    // Step 2: Fetch CSRF token from dedicated endpoint
    const csrfRes = await this._request({
      method: 'GET',
      path: '/admin/_csrfTokenVar.jsp',
      headers: this.sessionCookie ? { 'Cookie': this.sessionCookie } : {},
    });
    this._mergeCookies(csrfRes.headers['set-cookie']);

    // Response is: <script>var defined_csrf_token = "TOKEN_HERE";</script>
    // Postman: split("=").pop().substring(2, 12) — token is 10 chars
    if (csrfRes.body) {
      // Best approach: extract the quoted string directly
      const m = csrfRes.body.match(/["']([a-zA-Z0-9]{8,16})["']/);
      if (m) {
        this.csrfToken = m[1];
      } else {
        // Fallback: Postman approach
        const parts = csrfRes.body.split('=');
        if (parts.length > 1) {
          const last = parts[parts.length - 1];
          // substring(2, 12) skips the leading quote + space
          this.csrfToken = last.substring(2, 12).replace(/[^a-zA-Z0-9]/g, '');
        }
      }
    }

    return { loginStatus: res.statusCode, loginLocation: res.headers.location };
  }

  // ── Authentication (with master discovery) ─────────────────────────────
  async login() {
    const { loginStatus, loginLocation } = await this._loginToHost();

    // If redirected to a different host, that's the master — re-login there
    if (loginStatus === 302 && loginLocation) {
      try {
        const url = new URL(loginLocation, 'https://' + this.host);
        if (url.hostname && url.hostname !== this.host) {
          console.log('[ruckus] master AP detected at %s (redirected from %s)', url.hostname, this.host);
          this.host = url.hostname;
          await this._loginToHost();
        }
      } catch (_) {}
    }

    if (!this.sessionCookie) {
      throw new Error('no session cookie received');
    }
    console.log('[ruckus] authenticated to', this.host);
  }

  // ── Fetch connected clients ──────────────────────────────────────────────
  async fetchClients(retry) {
    const xmlBody = '<ajax-request action="getstat" comp="stamgr" enable-gzip="0" caller="SCI">\n <client/>\n</ajax-request>';
    const headers = {
      'Content-Type':  'text/xml',
      'Content-Length': Buffer.byteLength(xmlBody),
    };
    if (this.sessionCookie) headers['Cookie'] = this.sessionCookie;
    if (this.csrfToken)     headers['X-CSRF-Token'] = this.csrfToken;

    const res = await this._request({ method: 'POST', path: '/admin/_cmdstat.jsp', headers }, xmlBody);

    // Re-auth on 302/401/403 or login redirect
    const needsAuth = res.statusCode === 302 || res.statusCode === 401 || res.statusCode === 403
      || res.body.includes('login.jsp');
    if (needsAuth && !retry) {
      await this.login();
      return this.fetchClients(true);
    }

    return res.body;
  }

  // ── Parse XML response ───────────────────────────────────────────────────
  parseClientsXml(xml) {
    const clients = [];
    const clientRe = /<client\s+([^>]+)\/?>/gi;
    let match;
    while ((match = clientRe.exec(xml)) !== null) {
      const attrStr = match[1];
      const attrs = {};
      const attrRe = /([\w-]+)="([^"]*)"/g;
      let am;
      while ((am = attrRe.exec(attrStr)) !== null) {
        attrs[am[1]] = am[2];
      }

      const mac = (attrs.mac || '').toUpperCase();
      if (!mac) continue;

      // Signal: Ruckus reports RSSI as positive (0-100); convert to dBm
      let signal = parseInt(attrs.rssi || attrs.signal || '0', 10);
      if (signal > 0) signal = signal - 95;

      // Band: from radio-band attribute (e.g. "5g", "2.4g")
      let band = '';
      const radioBand = (attrs['radio-band'] || attrs['radio-type'] || '').toLowerCase();
      if (/6g/.test(radioBand))        band = '6GHz';
      else if (/5g/.test(radioBand))   band = '5GHz';
      else if (/2\.?4g/.test(radioBand)) band = '2.4GHz';

      // Uptime from first-assoc (epoch seconds)
      let uptime = '';
      if (attrs['first-assoc']) {
        const assocEpoch = parseInt(attrs['first-assoc'], 10);
        if (assocEpoch > 0) {
          const nowEpoch = Math.floor(Date.now() / 1000);
          const secs = Math.max(0, nowEpoch - assocEpoch);
          uptime = this._secsToUptime(secs);
        }
      }

      // Device name: prefer hostname, fall back to model/dvctype
      let name = attrs.hostname || '';
      // Ruckus uses MAC as hostname when no real name is known
      if (name === mac || name === mac.toLowerCase()) name = '';
      if (!name) name = attrs.model || attrs.dvctype || '';

      clients.push({
        mac,
        signal,
        iface:  'Ruckus',
        txRate: attrs['total-tx-bytes'] ? this._formatBytes(parseInt(attrs['total-tx-bytes'], 10)) : '',
        band,
        ip:     attrs.ip || '',
        rxRate: attrs['total-rx-bytes'] ? this._formatBytes(parseInt(attrs['total-rx-bytes'], 10)) : '',
        uptime,
        ssid:   attrs.ssid || attrs.wlan || '',
        name,
        source: 'ruckus',
      });
    }
    return clients;
  }

  _formatBytes(bytes) {
    if (bytes >= 1073741824) return (bytes / 1073741824).toFixed(1) + ' GB';
    if (bytes >= 1048576) return (bytes / 1048576).toFixed(1) + ' MB';
    if (bytes >= 1024) return (bytes / 1024).toFixed(0) + ' KB';
    return bytes + ' B';
  }

  _secsToUptime(s) {
    const d = Math.floor(s / 86400); s %= 86400;
    const h = Math.floor(s / 3600);  s %= 3600;
    const m = Math.floor(s / 60);    s %= 60;
    let out = '';
    if (d) out += d + 'd';
    if (h) out += h + 'h';
    if (m) out += m + 'm';
    out += s + 's';
    return out;
  }

  // ── Poll tick ────────────────────────────────────────────────────────────
  async tick() {
    const xml = await this.fetchClients();

    this.lastClients = this.parseClientsXml(xml);

    if (!this._loggedFirst) {
      console.log('[ruckus] fetched', this.lastClients.length, 'clients');
      this._loggedFirst = true;
    }

    this.state.lastRuckusTs = Date.now();
    delete this.state.lastRuckusErr;
  }

  // ── Start / stop ────────────────────────────────────────────────────────
  async start() {
    if (!this.enabled) {
      console.log('[ruckus] disabled — no RUCKUS_HOST configured');
      return;
    }

    try {
      await this.login();
    } catch (e) {
      console.error('[ruckus] initial login failed:', e && e.message ? e.message : e);
      this.state.lastRuckusErr = String(e && e.message ? e.message : e);
    }

    const run = async () => {
      try { await this.tick(); } catch (e) {
        this.state.lastRuckusErr = String(e && e.message ? e.message : e);
        console.error('[ruckus]', this.state.lastRuckusErr);
      }
    };
    run();
    this.timer = setInterval(run, this.pollMs);
  }
}

module.exports = RuckusCollector;
