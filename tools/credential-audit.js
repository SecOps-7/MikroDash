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
//
// ENUMERATED, AND EACH ONE IS COUNTED IN THE OUTPUT. A widened skip is the one
// way this audit can go quiet while still reporting "full coverage" -- both
// `scanned` and `eligible` fall together, so the ratio cannot see it. Printing
// what each prefix cost makes the widening visible in the sweep log and in the
// diff, which is where a deliberate one belongs.
const SKIP_RULES = [
  ['web/public/vendor/', 'third-party, self-hosted to avoid a CDN'],
  ['CHANGELOG.md', 'release prose quoting fixes, not a place credentials live'],
];
const SKIP = new RegExp(
  '^(' + SKIP_RULES.map(([p]) => p.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + (p.endsWith('/') ? '' : '$')).join('|') + ')'
);
const skipCounts = new Map(SKIP_RULES.map(([p]) => [p, 0]));
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
const unreadable = [];
const oversize = [];
let scanned = 0;
let eligible = 0;

for (const rel of files) {
  if (SKIP.test(rel)) {
    for (const [pre] of SKIP_RULES) {
      if (rel === pre || rel.startsWith(pre)) { skipCounts.set(pre, skipCounts.get(pre) + 1); break; }
    }
    continue;
  }
  if (!TEXT.test(rel)) continue;
  eligible++;
  const abs = path.join(ROOT, rel);
  let raw;
  try {
    // OVERSIZE IS REPORTED, NOT SWALLOWED. Nothing tracked is near 8 MB today
    // (the largest is a 5.4 MB recording), so this is a guard against a future
    // file, and a guard nobody hears about is not one.
    if (fs.statSync(abs).size > 8 << 20) { oversize.push(rel); continue; }
    raw = fs.readFileSync(abs, 'utf8');
  } catch (e) {
    // A FILE THAT CANNOT BE READ IS A FAILURE, not a skip. This was `catch
    // { continue }`, which is exactly the shape that hides the thing this audit
    // exists to find: a file present in the index, unreadable here, and
    // therefore never checked for a credential -- silently.
    unreadable.push(`${rel}: ${e.code || e.message}`);
    continue;
  }
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

// ── COVERAGE IS THE INVARIANT, AND IT IS ASSERTED HERE ──────────────────────
//
// This audit used to be in the gate census (`tools/verify.sh`), which fails when
// a gate's number drops. That was the wrong guard: the number is how many files
// the REPOSITORY has, so deleting `CUTOVER.md` on 2026-08-31 "shrank" it from
// 1268 to 1267 and turned a green tree red.
//
// The property actually worth holding does not move when a file is deleted:
// every eligible file was opened and scanned. If that stops being true -- a
// widened SKIP, an unreadable path, a file grown past the size cap -- it is
// reported HERE, where the reason is known, instead of as a number that dropped.
if (unreadable.length || oversize.length) {
  console.error(`credential-audit: ${unreadable.length + oversize.length} eligible file(s) were NOT scanned\n`);
  for (const u of unreadable) console.error(`  UNREADABLE  ${u}`);
  for (const o of oversize) console.error(`  OVER 8 MB   ${o}`);
  console.error(`
An eligible file that goes unscanned is a file no credential check ever saw.
Fix the cause; do not widen SKIP to make this quiet.`);
  process.exit(1);
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

// The file count is printed LAST and deliberately reads as context rather than a
// score: it is the size of the repository, not of this check. `verify.sh` names
// this gate in CENSUS_NOT_CORPUS for that reason.
console.log(
  `credential-audit: ${RULES.length} rules, full coverage (${scanned}/${eligible} eligible files), ` +
  'no credential shapes found');
console.log(
  '  excluded: ' +
  SKIP_RULES.map(([p]) => `${p} (${skipCounts.get(p)})`).join(', '));
