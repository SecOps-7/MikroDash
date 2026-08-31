#!/usr/bin/env node
'use strict';
/**
 * Pin the backup config-diff against the LIVE `src/backups/diff.js`.
 *
 * THREE THINGS ARE BEING PINNED AND THEY FAIL DIFFERENTLY.
 *
 *   normalize   Miss the volatile header and EVERY backup reports as drifted,
 *               which is the usual reason a config-drift tool ends up ignored.
 *               Strip too much and a real change stops being one.
 *   fingerprint The hash decides whether a pair is written at all. A port whose
 *               hash differs from Node's is not interoperable with the archive
 *               already on disk — every existing backup would read as drift on
 *               the first run after cutover.
 *   diff        Myers' algorithm plus unified-hunk grouping. The grouping is the
 *               part with room to disagree: whether a run of unchanged lines
 *               splits one hunk into two depends on a `> CONTEXT * 2` test, and
 *               an off-by-one there produces a diff that still LOOKS right.
 *
 * The gap cases below therefore step 5, 6, 7 and 8 unchanged lines between two
 * changes, which straddles the 2×CONTEXT=6 boundary in both directions. A corpus
 * that only ever used a large gap would agree with any threshold at all.
 *
 *   node tools/backup-diff-cases.js            write
 *   node tools/backup-diff-cases.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = process.env.BACKUP_DIFF_OUT ||
  path.join(__dirname, '..', 'testdata', 'backup-diff-cases.json');

const D = require(path.join(LIVE, 'src', 'backups', 'diff.js'));
for (const fn of ['normalize', 'normalizeLines', 'fingerprint', 'diff']) {
  if (typeof D[fn] !== 'function') {
    console.error('src/backups/diff.js no longer exports ' + fn);
    process.exit(1);
  }
}
if (D.CONTEXT !== 3 || D.MAX_EDITS !== 4000) {
  console.error('CONTEXT/MAX_EDITS changed; the port hard-codes 3 and 4000.');
  process.exit(1);
}

const HEADER = '# 2026-08-19 20:35:21 by RouterOS 7.24';
const HEADER2 = '# 2026-08-20 21:00:00 by RouterOS 7.25';
const STABLE = ['# software id = HR2S-3YN6', '#', '# model = C53UiG+5HPaxD2HPaxD'];

const lines = (n, tag) => Array.from({ length: n }, (_, i) => '/ip address set ' + tag + i);

// ── normalize / fingerprint ─────────────────────────────────────────────────
const texts = {
  'empty': '',
  'null-ish': null,
  'header only': HEADER,
  'header + body': [HEADER, ...STABLE, '/ip dns set servers=1.1.1.1'].join('\n'),
  'different header, same body': [HEADER2, ...STABLE, '/ip dns set servers=1.1.1.1'].join('\n'),
  'no header at all': [...STABLE, '/ip dns set servers=1.1.1.1'].join('\n'),
  // A first line that is a comment but NOT the volatile one must survive.
  'other leading comment': ['# just a note', '/ip dns set servers=1.1.1.1'].join('\n'),
  // A volatile-looking line NOT first is not stripped.
  'volatile header second': ['/ip dns set servers=1.1.1.1', HEADER].join('\n'),
  'CRLF endings': [HEADER, 'a', 'b'].join('\r\n'),
  'lone CR endings': [HEADER, 'a', 'b'].join('\r'),
  'trailing newline': [HEADER, 'a', 'b', ''].join('\n'),
  'two trailing newlines': [HEADER, 'a', 'b', '', ''].join('\n'),
  'blank line inside': [HEADER, 'a', '', 'b'].join('\n'),
  'header with no body': [HEADER, ''].join('\n'),
};
const normalizeCases = Object.entries(texts).map(([name, text]) => ({
  name, text,
  lines: D.normalizeLines(text),
  normalized: D.normalize(text),
  fingerprint: D.fingerprint(text),
}));

// ── diff ────────────────────────────────────────────────────────────────────
const diffCases = [];
const addDiff = (name, oldText, newText) => {
  diffCases.push({ name, oldText, newText, want: D.diff(oldText, newText) });
};

addDiff('identical', 'a\nb\nc', 'a\nb\nc');
addDiff('identical but for the volatile header',
        [HEADER, 'a', 'b'].join('\n'), [HEADER2, 'a', 'b'].join('\n'));
addDiff('both empty', '', '');
addDiff('empty to content', '', 'a\nb');
addDiff('content to empty', 'a\nb', '');
addDiff('one line changed', 'a\nb\nc', 'a\nB\nc');
addDiff('one line added', 'a\nc', 'a\nb\nc');
addDiff('one line removed', 'a\nb\nc', 'a\nc');
addDiff('added at the very start', 'a\nb', 'z\na\nb');
addDiff('added at the very end', 'a\nb', 'a\nb\nz');
addDiff('every line different', 'a\nb\nc', 'x\ny\nz');
addDiff('CRLF vs LF only', 'a\r\nb\r\nc', 'a\nb\nc');

// ── hunk grouping: gaps straddling 2*CONTEXT ────────────────────────────────
// Two changes separated by N unchanged lines. N > 6 must split into two hunks;
// N <= 6 must stay as one. A corpus using only a large gap agrees with any test.
for (const gap of [0, 1, 5, 6, 7, 8, 20]) {
  const a = ['CHANGE-A', ...lines(gap, 'mid'), 'CHANGE-B'];
  const b = ['change-a', ...lines(gap, 'mid'), 'change-b'];
  addDiff('two changes with a gap of ' + gap, a.join('\n'), b.join('\n'));
}

// Context at the edges: a change on the first and last line, with enough
// surrounding lines that the hunk has to clamp rather than run off.
addDiff('change on the first line', ['a', ...lines(10, 'x')].join('\n'),
                                     ['A', ...lines(10, 'x')].join('\n'));
addDiff('change on the last line', [...lines(10, 'x'), 'a'].join('\n'),
                                    [...lines(10, 'x'), 'A'].join('\n'));
addDiff('changes at both ends', ['a', ...lines(10, 'x'), 'b'].join('\n'),
                                 ['A', ...lines(10, 'x'), 'B'].join('\n'));

// A realistic small edit in a large file — the case Myers is fast for.
const big = lines(500, 'r');
const bigEdited = big.slice();
bigEdited[250] = '/ip address set r250 comment="edited"';
addDiff('one line in five hundred', big.join('\n'), bigEdited.join('\n'));

// Past MAX_EDITS: `truncated`, with added/removed null rather than a partial
// diff that looks complete.
addDiff('wholesale rewrite exceeds MAX_EDITS',
        lines(2500, 'old').join('\n'), lines(2500, 'new').join('\n'));

const out = JSON.stringify({
  context: D.CONTEXT, maxEdits: D.MAX_EDITS,
  volatileHeader: String(D.VOLATILE_HEADER),
  normalizeCases, diffCases,
}, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== out) { console.error('backup-diff-cases.json is stale — run: node tools/backup-diff-cases.js'); process.exit(1); }
  console.log('backup-diff-cases.json is up to date');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, out);
  const hunks = diffCases.reduce((n, c) => n + c.want.hunks.length, 0);
  console.log('wrote ' + OUT + ' — ' + normalizeCases.length + ' normalize, ' +
              diffCases.length + ' diff, ' + hunks + ' hunks, ' +
              diffCases.filter(c => c.want.truncated).length + ' truncated');
}
