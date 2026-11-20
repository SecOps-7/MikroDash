/**
 * EVERY COPY GOES THROUGH copyText (web/src/dom.ts).
 *
 * `navigator.clipboard` does not exist on an insecure origin, and MikroDash is
 * usually reached over plain http on a LAN address. A direct
 * `navigator.clipboard.writeText(...)` (with or without `?.`) then copies
 * nothing and says nothing: the button looks dead. It has shipped that way
 * four times: three call sites fixed when copyText was written, and the Copy
 * button of Settings > Shared links (2026-11-20). copyText falls back to a
 * textarea and execCommand('copy'), which works over http.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const SRC = path.join(ROOT, 'web', 'src');

const files: string[] = [];
const walk = (dir: string): void => {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p);
    else if (p.endsWith('.ts')) files.push(p);
  }
};
walk(SRC);
assert.ok(files.length > 100, 'the source walk found only ' + files.length + ' files');

// A write through the clipboard API, ignoring comments.
const WRITE = /navigator\s*(as\s+[^)]*\))?\s*\.clipboard\s*\??\.\s*write(Text)?\s*\(/;
const offenders: string[] = [];
for (const f of files) {
  if (f.endsWith(path.join('src', 'dom.ts'))) continue;
  const code = fs.readFileSync(f, 'utf8').split('\n').filter((l) => !/^\s*(\/\/|\*)/.test(l)).join('\n');
  if (WRITE.test(code)) offenders.push(path.relative(ROOT, f));
}
assert.deepStrictEqual(offenders, [], 'these write the clipboard directly; use copyText from dom.ts: ' + offenders.join(', '));

// The check can fail: the shape that shipped is caught.
assert.ok(WRITE.test("void navigator.clipboard?.writeText(text).catch(() => {});"), 'the check does not catch the shape that shipped');
assert.ok(!WRITE.test("copyText(text, done);"), 'the check catches the helper itself');

console.log('clipboard: every copy goes through copyText (' + files.length + ' files read)');
