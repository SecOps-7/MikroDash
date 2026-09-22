/**
 * THE AI ENDPOINT'S CERTIFICATE PIN, THE BROWSER'S HALF (2026-09-22).
 *
 * The "accept a self-signed certificate" switch became a pin (code scanning
 * alert 164). Test Connection reports a refused certificate and the page offers
 * to trust it. Pinned here:
 *   - the pin is sent on every Test, EMPTY included, so a cleared field can be
 *     tested before it is saved (the old checkbox's rule);
 *   - the fingerprint is shown as colon-separated pairs, which is what fills the
 *     field and what the server normalises back;
 *   - a certificate that changed under a pin says so first, since that is the
 *     one to read twice before trusting.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.aicert-entry.ts');
fs.writeFileSync(ENTRY, "export { aiTestPayload, certSummary, formatFingerprint } from '../web/src/pages/settings-notif-test.js';\n");
const OUT = path.join(ROOT, 'testdata', '.aicert.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);

const doc = makeDoc(['s_aiBaseUrl', 's_aiModel', 's_aiApiKey', 's_aiHeaders', 's_aiTimeoutMs', 's_aiTlsPin']);
global.document = doc;

// ── THE PIN IS ALWAYS SENT, EMPTY INCLUDED ─────────────────────────────────
doc.nodes.s_aiTlsPin.value = '';
let p = mod.aiTestPayload();
assert.ok('aiTlsPin' in p && p.aiTlsPin === '', `an empty pin was not sent: ${JSON.stringify(p)}`);
doc.nodes.s_aiTlsPin.value = '  AB:CD  ';
p = mod.aiTestPayload();
assert.strictEqual(p.aiTlsPin, 'AB:CD', 'the pin was not sent trimmed');
assert.ok(!('aiTlsInsecure' in p), 'the retired switch is still sent');

// ── THE FINGERPRINT AS A PERSON COMPARES IT ───────────────────────────────
const hex = 'ab'.repeat(32);
const shown = mod.formatFingerprint(hex);
assert.strictEqual(shown, Array(32).fill('AB').join(':'), `formatted as ${shown}`);

// ── WHAT THE BOX SAYS ──────────────────────────────────────────────────────
const cert = { fingerprint: hex, subject: 'CN=llm.lan', issuer: 'CN=llm.lan', names: ['llm.lan', '192.0.2.5'],
  notAfter: '2027-01-01', selfSigned: true, mismatch: false };
const fresh = mod.certSummary(cert);
assert.match(fresh.head, /not trusted.*self-signed/, fresh.head);
const rows = Object.fromEntries(fresh.rows);
assert.strictEqual(rows['SHA-256'], shown);
assert.strictEqual(rows.Names, 'llm.lan, 192.0.2.5');
assert.strictEqual(rows.Expires, '2027-01-01');
const changed = mod.certSummary({ ...cert, mismatch: true, names: [] });
assert.match(changed.head, /DIFFERENT certificate/, 'a certificate that changed under a pin reads like a first trust');
assert.ok(!Object.fromEntries(changed.rows).Names, 'an empty name list became a row');

fs.rmSync(OUT, { force: true });
console.log('ai-cert-trust: all checks passed');
