/**
 * BULK DEVICE IMPORT (#150): reading the CSV, and drawing what the server says.
 *
 *   - RFC 4180: quoted cells keep their separators, line breaks and doubled
 *     quotes; a byte-order mark is dropped; CRLF and LF both end a row.
 *   - Excel's semicolon files read the same as comma files.
 *   - Headers match ignoring case, spaces and dashes, with aliases; unknown
 *     columns are reported, not silently dropped; blank rows are skipped and
 *     `line` is the row's place in the file.
 *   - The template reads back to its own two rows.
 *   - Everything drawn is escaped.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const OUT = path.join(ROOT, 'testdata', '.dimp.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'device-import.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
const D = require(OUT);

{
  const t = D.parseCsv('﻿a,b,c\r\n"x, y","say ""hi""","two\nlines"\n1,,3');
  assert.deepStrictEqual(t, [['a', 'b', 'c'], ['x, y', 'say "hi"', 'two\nlines'], ['1', '', '3']]);
  say('ok  quotes, doubled quotes, embedded line breaks, BOM, CRLF and LF');
}

{
  assert.strictEqual(D.detectDelimiter('host;name;port\n1,2;3'), ';');
  assert.strictEqual(D.detectDelimiter('host,name;x,port'), ',');
  assert.strictEqual(D.detectDelimiter('"a;b;c",d\n'), ',', 'separators inside quotes do not count');
  const semi = D.readRows('Host;Name;Credential Profile\n192.0.2.1;"Edge; north";MikroDash login\n');
  assert.strictEqual(semi.error, '');
  assert.strictEqual(semi.rows[0].name, 'Edge; north');
  assert.strictEqual(semi.rows[0].credentialProfile, 'MikroDash login');
  say('ok  a semicolon file (Excel in comma-decimal locales) reads the same');
}

{
  const r = D.readRows('HOST,Label,user,Uplink-Port,Notes,\n192.0.2.1,Edge,admin,ether2,ignore me,\n,,,,,\n\n192.0.2.2,,,,,\n');
  assert.strictEqual(r.error, '');
  assert.deepStrictEqual(r.ignored, ['Notes'], 'an unknown column is reported, an empty header is not');
  assert.strictEqual(r.rows.length, 2, 'blank rows are skipped');
  assert.deepStrictEqual([r.rows[0].line, r.rows[1].line], [2, 5], 'line is the place in the file');
  assert.strictEqual(r.rows[0].host, '192.0.2.1');
  assert.strictEqual(r.rows[0].name, 'Edge');
  assert.strictEqual(r.rows[0].username, 'admin');
  assert.strictEqual(r.rows[0].uplinkInterface, 'ether2');
  assert.match(D.readRows('name,port\nx,1\n').error, /"host" column/);
  assert.match(D.readRows('host\n\n').error, /no device rows/);
  assert.match(D.readRows('').error, /empty/);
  say('ok  headers by alias and case, unknown columns reported, blank rows skipped, line numbers');
}

{
  const r = D.readRows(D.TEMPLATE);
  assert.strictEqual(r.error, '');
  assert.deepStrictEqual(r.ignored, [], 'every template column is one the reader knows');
  assert.strictEqual(r.rows.length, 2);
  assert.strictEqual(r.rows[0].sites, 'Branch|North');
  assert.strictEqual(r.rows[1].credentialProfile, 'MikroDash login');
  assert.strictEqual(r.rows[1].password, '', 'the profile example signs in with the profile');
  say('ok  the template reads back to its own rows');
}

{
  const evil = '<img src=x onerror=1>';
  const plan = { rows: [{ line: 2, label: evil, host: evil, port: 8729, status: 'error', reason: evil, mode: 'plain',
    profile: '', sites: [evil], newSites: [evil] }], newSites: [evil], ready: 0, duplicates: 0, errors: 1, accounts: 0 };
  const html = D.previewHtml(plan);
  assert.ok(!html.includes('<img'), 'the preview escapes');
  assert.match(html, /hs-stale/, 'an error row is a red pill');
  assert.match(html, /dimp-new/, 'a site the import creates is marked new');
  const job = { running: false, started: 0, summary: '', rows: [{ line: 2, label: evil, state: 'warning', message: evil }] };
  assert.ok(!D.progressHtml(job).includes('<img'), 'the progress table escapes');
  assert.strictEqual(D.planSummary({ rows: [], newSites: ['A', 'B'], ready: 3, duplicates: 1, errors: 2, accounts: 1 }),
    '3 ready, 1 duplicate skipped, 2 with errors. Creates 2 sites: A, B. Creates the mikrodash account on 1 router.');
  say('ok  preview and progress are escaped, and the summary says what will be written');
}

fs.rmSync(OUT, { force: true });
say('device-import: all checks passed');
