'use strict';
/**
 * NO COMMITTED FILE CARRIES A REAL CREDENTIAL.
 *
 * ── WHY THIS EXISTS, AND WHAT IT IS NOT ─────────────────────────────────────
 *
 * `assertClean()` in tools/capture-fixtures.js already enforces this for CAPTURES
 * from a live router, and it is a positive structural check: every value under an
 * identifying key must be a token the tool minted. That is the stronger design
 * and nothing here replaces it.
 *
 * It has one blind spot, and on 2026-08-31 that blind spot cost a live secret.
 * A HAND-WRITTEN test case in tools/sanitize-cases.js -- a case proving that
 * sanitizeErr redacts a bot token -- used the operator's real token as the sample
 * string instead of inventing one. No capture was involved, so no capture-time
 * check ran. GitHub secret scanning found it in a public repository.
 *
 * So this scans what capture-fixtures cannot see: everything hand-written or
 * generated that ends up committed.
 *
 * ── THE SHAPES, AND WHY SO FEW ──────────────────────────────────────────────
 *
 * Only shapes with a low false-positive rate are worth checking. An audit that
 * cries wolf trains the habit of ignoring it, which is worse than no audit --
 * the same reasoning that got the live-repo hook disabled.
 *
 * A placeholder is recognised structurally rather than by an allowlist: a real
 * secret has entropy, and a value whose secret half is one repeated character,
 * or spells a stand-in word, is nobody's credential.
 */

const fs = require('fs');
const path = require('path');
const { execFileSync } = require('child_process');

const ROOT = path.join(__dirname, '..');

// Tracked files only. An untracked scratch file is not the exposure vector --
// file CONTENT reaching a public repository is, and only tracked files do that.
const files = execFileSync('git', ['-C', ROOT, 'ls-files', '-z'], { maxBuffer: 64 << 20 })
  .toString('utf8').split('\0').filter(Boolean);

// Text only, and not the places a match is meaningless.
const SKIP = /^(web\/public\/vendor\/|docs\/port-history\/|CHANGELOG\.md$)/;
const TEXT = /\.(js|cjs|mjs|ts|go|json|md|ya?ml|sh|py|html|css|txt)$/;

const RULES = [
  {
    id: 'telegram-bot-token',
    // digits, a colon, then the secret half.
    re: /\b(\d{6,12}):([A-Za-z0-9_-]{30,45})\b/g,
    secret: (m) => m[2],
  },
  {
    id: 'private-key-block',
    re: /-----BEGIN (?:RSA |EC |OPENSSH |PGP )?PRIVATE KEY-----/g,
    secret: () => null, // no entropy test; the header alone is the finding
  },
];

/**
 * Is the secret half obviously not a secret?
 *
 * Structural, not a list. A value made of one repeated character, or of a short
 * alphabet, or spelling a stand-in word, is a placeholder. Anything else is
 * treated as real, which is the safe direction: a false alarm costs a minute and
 * a miss costs a rotation.
 */
function isPlaceholder(v) {
  if (!v) return false;
  if (new Set(v).size <= 2) return true;                 // AAAA..., ababab...
  if (/^(?:0123456789|abcdef|x+|X+)/.test(v)) return true;
  if (/example|placeholder|redacted|dummy|sample|fake|test|xxxx/i.test(v)) return true;
  return false;
}

// Comments are stripped before scanning. This file explains the shapes it looks
// for, and a checker that fails on its own explanation teaches the next reader to
// weaken the pattern rather than fix the code. That has happened three times in
// this repo already.
function stripComments(s, file) {
  if (/\.(js|cjs|mjs|ts|go|css)$/.test(file)) {
    return s.replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/(^|[^:])\/\/[^\n]*/g, '$1');
  }
  if (/\.(sh|py|ya?ml)$/.test(file)) return s.replace(/(^|\n)\s*#[^\n]*/g, '$1');
  return s;
}

const findings = [];
let scanned = 0;

for (const rel of files) {
  if (SKIP.test(rel) || !TEXT.test(rel)) continue;
  const abs = path.join(ROOT, rel);
  let raw;
  try {
    if (fs.statSync(abs).size > 8 << 20) continue;
    raw = fs.readFileSync(abs, 'utf8');
  } catch { continue; }
  scanned++;

  const body = stripComments(raw, rel);
  for (const rule of RULES) {
    rule.re.lastIndex = 0;
    let m;
    while ((m = rule.re.exec(body)) !== null) {
      if (isPlaceholder(rule.secret(m))) continue;
      const line = body.slice(0, m.index).split('\n').length;
      findings.push({ rel, line, id: rule.id });
    }
  }
}

if (findings.length) {
  console.error(`credential-audit: ${findings.length} possible credential(s) in committed files\n`);
  for (const f of findings) console.error(`  ${f.rel}:${f.line}  ${f.id}`);
  console.error(`
Values are not printed. If one of these is real:
  1. ROTATE IT FIRST. Removing the file does not remove it from history, and
     this repository is public.
  2. Replace it with a structurally obvious placeholder -- a repeated character
     for the secret half keeps the shape a test needs without being a secret.
If it is already a placeholder this audit did not recognise, widen
isPlaceholder() rather than narrowing the rule.`);
  process.exit(1);
}

console.log(`credential-audit: ${scanned} committed files scanned, no credential shapes found`);
