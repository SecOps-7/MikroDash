'use strict';
/**
 * Sortable table headers.
 *
 * Nine tables clicked their way out of being sorted at all. `_renderSortHeader`
 * updates `sortState` itself and then calls back with no arguments; those nine
 * passed `function (key) { ... }` and recomputed the state from a `key` that was
 * always `undefined`, so the first click set `col` to `undefined`, every row
 * then compared equal, and no later click could recover it. The same mismatch
 * kept `dir` numeric, so the header class came out `sort-1` — which no
 * stylesheet defines, which is why a table that had silently stopped sorting
 * still looked like one that had never been sorted.
 *
 * The helper is browser code with no module boundary, so it is lifted out of
 * app.js by name and driven against a DOM stub small enough to read.
 */

const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const path = require('path');
const vm = require('node:vm');

const SRC = fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');

/** Lift one top-level `function name(...) { ... }` out of app.js by brace matching. */
function lift(name) {
  const start = SRC.indexOf('function ' + name + '(');
  assert.notEqual(start, -1, name + ' is gone from app.js');
  let depth = 0;
  for (let i = SRC.indexOf('{', start); i < SRC.length; i++) {
    if (SRC[i] === '{') depth++;
    else if (SRC[i] === '}' && --depth === 0) return SRC.slice(start, i + 1);
  }
  throw new Error('unbalanced braces reading ' + name);
}

/**
 * Just enough DOM. `innerHTML` is parsed back into th stubs because the helper
 * writes the row as a string and then wires handlers onto the elements it made
 * — the round trip through markup is exactly where the class name matters.
 */
function fakeThead() {
  const tr = {
    _ths: [],
    _html: '',
    set innerHTML(html) {
      tr._html = html;
      tr._ths = (html.match(/<th[^>]*>/g) || []).map((tag) => {
        const cls = /class="([^"]*)"/.exec(tag);
        return { className: cls ? cls[1] : '', _clicks: [],
                 addEventListener(_e, fn) { this._clicks.push(fn); },
                 click() { this._clicks.forEach(f => f()); } };
      });
    },
    get innerHTML() { return tr._html; },
    querySelectorAll() { return tr._ths; },
  };
  return tr;
}

function harness() {
  const tr = fakeThead();
  // The two functions are evaluated in a context holding nothing but the `$`
  // they close over, so a helper that started reaching for some other global
  // fails here rather than passing on a stray from this file's scope.
  const sandbox = { $: () => tr };
  vm.runInNewContext(lift('_sortMul') + '\n' + lift('_renderSortHeader'), sandbox);
  return { tr, _sortMul: sandbox._sortMul, _renderSortHeader: sandbox._renderSortHeader };
}

const COLS = [{ key: 'name', label: 'Name' }, { key: 'ts', label: 'When' }];

test("the direction a comparator multiplies by follows the helper's convention", () => {
  const { _sortMul } = harness();
  assert.equal(_sortMul({ col: 'name', dir: 'asc' }), 1);
  assert.equal(_sortMul({ col: 'name', dir: 'desc' }), -1);
  // An unset direction must not produce NaN and silently flatten a table.
  assert.equal(_sortMul({ col: 'name' }), 1, 'no direction sorts ascending, not nowhere');
});

test('one click sorts by the clicked column', () => {
  const { tr, _renderSortHeader } = harness();
  const sortState = { col: 'name', dir: 'asc' };
  let renders = 0;
  _renderSortHeader('thead', COLS, sortState, () => { renders++; });

  tr.querySelectorAll()[1].click();                      // click "When"
  assert.equal(sortState.col, 'ts', 'the clicked column is the sort column');
  assert.equal(sortState.dir, 'asc', 'a new column starts ascending');
  assert.equal(renders, 1, 'the table is asked to re-render');
});

test('clicking the same column again reverses it, and only then', () => {
  const { tr, _renderSortHeader } = harness();
  const sortState = { col: 'name', dir: 'asc' };
  _renderSortHeader('thead', COLS, sortState, () => {});

  tr.querySelectorAll()[0].click();
  assert.equal(sortState.dir, 'desc', 'the active column toggles');
  tr.querySelectorAll()[0].click();
  assert.equal(sortState.dir, 'asc', 'and toggles back');
  assert.equal(sortState.col, 'name', 'without ever losing the column');
});

test('the active column carries a class the stylesheet actually defines', () => {
  // `sort-1` and `sort--1` were emitted for nine tables and styled by nothing,
  // so the sort arrow never appeared.
  const { tr, _renderSortHeader } = harness();
  const sortState = { col: 'ts', dir: 'desc' };
  _renderSortHeader('thead', COLS, sortState, () => {});

  const css = fs.readFileSync(path.join(__dirname, '..', 'public', 'index.html'), 'utf8');
  const marked = tr.querySelectorAll().filter(th => /\bsort-/.test(th.className));
  assert.equal(marked.length, 1, 'exactly one column is marked as sorted');
  const cls = /\bsort-[\w-]+/.exec(marked[0].className)[0];
  assert.equal(cls, 'sort-desc');
  assert.ok(css.includes('th.' + cls), cls + ' is emitted but no rule defines it');
});

test('no sort callback takes a key argument', () => {
  // The helper has already updated sortState by the time it calls back, so a
  // callback that recomputes the state from a parameter gets `undefined` and
  // destroys it. This is the shape that broke nine tables.
  const offenders = [];
  const re = /_renderSortHeader\([^;]*?function\s*\(([^)]*)\)/g;
  let m;
  while ((m = re.exec(SRC)) !== null) {
    if (m[1].trim()) offenders.push(m[1].trim());
  }
  assert.deepEqual(offenders, [],
    'these _renderSortHeader callbacks declare a parameter the helper never passes: '
    + offenders.join(', '));
});

test('no comparator multiplies by a raw sort direction', () => {
  // `sort.dir * x` is only correct while dir is numeric, which is the convention
  // that conflicts with the helper. Comparators go through _sortMul instead.
  const raw = SRC.split('\n')
    .map((line, i) => ({ line, n: i + 1 }))
    .filter(({ line }) => /\b\w*[Ss]ort\w*\.dir\s*\*[^=]/.test(line));
  assert.deepEqual(raw.map(r => r.n), [],
    'these lines multiply by a raw .dir instead of _sortMul():\n'
    + raw.map(r => '  ' + r.n + ': ' + r.line.trim()).join('\n'));
});
