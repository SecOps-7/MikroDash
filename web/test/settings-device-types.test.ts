/**
 * Settings > Devices lists routers and SwOS switches in ONE table (#142).
 *
 * The operator's placement: no separate switch table. A Type column says which
 * kind each row is; columns that only mean something for a router (the API
 * connection's TLS) show a dash on a switch row; a switch has no move arrows,
 * because the order is the router selector's and a switch is not in it.
 *
 * The header in page-settings.html and ROUTER_TABLE_COLUMNS must agree, or the
 * empty state's colspan and every row's cells drift apart from the header.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-settings-device-types.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'settings-routers.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
const FORM = path.join(ROOT, 'web', 'dist', '_compare', 'port-switch-form.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'switch-form.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + FORM, '--log-level=warning'],
  { stdio: 'inherit' });

global.document = { getElementById: () => null, querySelector: () => null, querySelectorAll: () => [], addEventListener: () => {} };
global.window = { addEventListener: () => {} };

const page = require(OUT);
const form = require(FORM);

let failed = 0;
function check(what, fn) {
  try { fn(); say('  ok   ' + what); }
  catch (e) { failed++; say('  FAIL ' + what + '\n       ' + e.message); }
}

const routers = [{ id: 'r1', label: 'Edge', host: '198.51.100.1', tls: true, model: 'RB5009', osVersion: '7.24' }];
const switches = [{ id: 's1', kind: 'swos', label: 'Core switch', host: '198.51.100.20', model: 'CSS610-8P-2S+', osVersion: '2.21' }];

function rows(html) {
  return html.split('<tr').slice(1);
}
function cells(row) {
  return row.split('<td').slice(1);
}

say('settings: routers and SwOS switches share one devices table');

check('both kinds are listed, routers first, each with its Type', () => {
  const html = page.renderRouterTable(routers, 'r1', { r1: true }, {}, 'r1', switches);
  const r = rows(html);
  assert.equal(r.length, 2, 'expected one router row and one switch row');
  assert.ok(r[0].includes('Edge') && r[0].includes('>RouterOS<'), 'the router row does not say RouterOS');
  assert.ok(r[1].includes('Core switch') && r[1].includes('>SwOS<'), 'the switch row does not say SwOS');
});

check('every row has as many cells as the header has columns', () => {
  const html = page.renderRouterTable(routers, 'r1', {}, {}, '', switches);
  const markup = fs.readFileSync(path.join(ROOT, 'web', 'src', 'ui', 'page-settings.html'), 'utf8');
  const head = markup.slice(markup.indexOf('id="rtrTable"'));
  const ths = (head.slice(0, head.indexOf('</tr>')).match(/<th[\s>]/g) || []).length;
  assert.equal(ths, page.ROUTER_TABLE_COLUMNS, 'page-settings.html has ' + ths + ' header cells');
  for (const row of rows(html)) {
    assert.equal(cells(row).length, page.ROUTER_TABLE_COLUMNS, 'a row has ' + cells(row).length + ' cells');
  }
});

check('the router-only Connection cell is a dash on a switch row', () => {
  const html = page.renderRouterTable(routers, 'r1', {}, {}, '', switches);
  const sw = rows(html)[1];
  assert.ok(!sw.includes('TLS') && !sw.includes('Unencrypted'), 'a switch row shows a TLS state');
  const conn = cells(sw)[9];
  assert.ok(conn.includes('>-<'), 'the Connection cell is not a dash: ' + conn);
});

check('a switch row has no move arrows and is never Active or Primary', () => {
  const html = page.renderRouterTable([], 's1', {}, {}, 's1', switches);
  assert.ok(!html.includes('data-rtr-action="move"'), 'a switch can be moved in the selector order');
  assert.ok(!html.includes('>Active<') && !html.includes('>Primary<'), 'a switch is marked active or primary');
  assert.ok(html.includes('data-rtr-action="edit"') && html.includes('data-rtr-action="delete"'),
    'a switch row has no Edit or Delete');
});

check('the delete prompt names a switch as a switch', () => {
  assert.ok(page.deleteSwitchPrompt('Core switch').startsWith('Delete switch "Core switch"'));
});

say('switch form: what a save sends');

check('a new switch is sent with its kind, an edit without it', () => {
  const v = { label: ' Core ', host: '198.51.100.20 ', scheme: 'http', port: '', username: '', password: 'p',
    alertsEnabled: true, reportingEnabled: false };
  const add = form.switchBody(v, true);
  assert.equal(add.kind, 'swos');
  assert.equal(add.label, 'Core');
  assert.equal(add.username, 'admin', 'a blank username is admin');
  assert.ok(!('port' in add), 'a blank port is sent instead of taking the scheme default');
  const edit = form.switchBody({ ...v, password: '' }, false);
  assert.ok(!('kind' in edit), 'an edit carries a kind');
  assert.ok(!('password' in edit), 'a blank password on an edit would clear the stored one');
});

check('the firmware family is named for a person', () => {
  assert.equal(form.familyLabel('lite'), 'SwOS Lite');
  assert.equal(form.familyLabel('swos'), 'SwOS');
});

if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
say('\nall passed');
