'use strict';
/**
 * Static DNS record types.
 *
 * The resource offered six of the nine types RouterOS defines. MX, NS and SRV
 * were missing, and the two halves of that compounded into data loss:
 *
 *   - `fieldHtml()` emits no blank option for a *required* select and marks
 *     `selected` only where the value matches an option. With the router's
 *     value absent, nothing was selected, the browser fell back to
 *     `selectedIndex = 0` — "A" — and Save sent `=type=A`. The MX preference,
 *     the SRV target and port, the NS delegation were gone, with nothing on
 *     screen suggesting the form was showing something other than the record.
 *   - Even a correct value could not have been saved: the select check
 *     validates against `options`, so `type=MX` was refused. Uneditable and
 *     silently rewritable at once.
 *
 * Both halves are pinned here, plus the collector side: the Address column is
 * one column for all nine types, so it has to be fed the value half of each.
 */

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const vm = require('node:vm');

const R = require('../src/routeros/resources');
const DnsCollector = require('../src/collectors/dns');
const { parseStaticEntries } = DnsCollector;

const APP = fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');
const DNS_SRC = fs.readFileSync(
  path.join(__dirname, '..', 'src', 'collectors', 'dns.js'), 'utf8');

/** Every type RouterOS documents for /ip/dns/static, in the manual's order. */
const ROUTEROS_TYPES = ['A', 'AAAA', 'CNAME', 'FWD', 'MX', 'NS', 'NXDOMAIN', 'SRV', 'TXT'];

const dnsStatic = () => R.byKey('dnsStatic');
const fieldOf = (name) => dnsStatic().fields.find(f => f.name === name);
// What the browser is actually handed: describe() is where a declared `type`
// becomes the `input` fieldHtml() switches on, so a render test that skipped it
// would exercise a shape the form never sees.
const describedField = (name) => R.describe(dnsStatic()).fields.find(f => f.name === name);

// ── What the form offers ─────────────────────────────────────────────────────

test('every record type RouterOS defines is offered', () => {
  // A type the router can hold but the form cannot name is not a gap, it is a
  // record that gets rewritten as something else the moment anyone opens it.
  assert.deepEqual(fieldOf('type').options, ROUTEROS_TYPES);
});

test('MX, NS and SRV validate rather than being refused', () => {
  const cases = [
    { type: 'MX',  extra: { mxExchange: 'mx1.lan', mxPreference: '10' } },
    { type: 'NS',  extra: { ns: 'ns1.lan' } },
    // No trailing dot: the router refuses that, so a fixture is not the place
    // to show it, even though our own validator does not police it.
    { type: 'SRV', extra: { srvTarget: 'host.lan', srvPort: '5060',
                            srvPriority: '0', srvWeight: '0' } },
  ];
  for (const c of cases) {
    const v = R.validate(dnsStatic(), Object.assign({ name: 'x.lan', type: c.type }, c.extra));
    assert.deepEqual(v.errors, [], c.type + ' should validate');
    assert.equal(v.values.type, c.type);
  }
});

test('a type the router does not have is still refused', () => {
  // Widening the list must not have turned the check off.
  const v = R.validate(dnsStatic(), { name: 'x.lan', type: 'MXX' });
  assert.ok(v.errors.length, 'MXX is not a DNS record type');
});

test("each type's own fields apply to it and to nothing else", () => {
  const applies = (field, type) => R.fieldApplies(fieldOf(field), { type });
  for (const [field, type] of [['mxExchange', 'MX'], ['mxPreference', 'MX'],
                               ['ns', 'NS'], ['srvTarget', 'SRV'],
                               ['srvPort', 'SRV'], ['srvPriority', 'SRV'],
                               ['srvWeight', 'SRV']]) {
    assert.ok(applies(field, type), field + ' must show for ' + type);
    assert.ok(!applies(field, 'A'), field + ' must not show for an A record');
  }
  // And the original four still behave.
  assert.ok(R.fieldApplies(fieldOf('address'), { type: 'A' }));
  assert.ok(!R.fieldApplies(fieldOf('address'), { type: 'MX' }));
});

test('a placeholder never shows a value the router would refuse', () => {
  // The MikroTik manual says srv-target "ends in a dot", so the first cut of
  // this field suggested `host.lan.`. A live hAP ac2 refuses that with
  // `bad SRV data` and accepts the same name without the dot. A placeholder is
  // a suggestion, and suggesting the one form that fails is worse than none:
  // the error names neither the field nor the dot.
  const srv = fieldOf('srvTarget');
  assert.ok(!/\.$/.test(srv.placeholder || ''),
    'srv-target must not be suggested with a trailing dot');
  // `bad SRV data` covers the record's name shape too, and nothing else says so.
  assert.match(srv.help || '', /_service\._proto/,
    'the SRV naming rule has to be on screen; the router error does not explain it');
});

test('only the name half of each type is required', () => {
  // RouterOS defaults the numeric properties to 0. Demanding a preference
  // before a comment could be saved would be the form inventing a rule.
  assert.ok(fieldOf('mxExchange').required);
  assert.ok(fieldOf('ns').required);
  assert.ok(fieldOf('srvTarget').required);
  for (const n of ['mxPreference', 'srvPort', 'srvPriority', 'srvWeight']) {
    assert.ok(!fieldOf(n).required, n + ' must not be required');
  }
});

// ── What the form renders ────────────────────────────────────────────────────

/** Lift a `function name(...) {...}` out of app.js by brace matching. */
function lift(name) {
  const start = APP.indexOf('function ' + name + '(');
  assert.notEqual(start, -1, name + ' is gone from app.js');
  let depth = 0;
  for (let i = APP.indexOf('{', start); i < APP.length; i++) {
    if (APP[i] === '{') depth++;
    else if (APP[i] === '}' && --depth === 0) return APP.slice(start, i + 1);
  }
  throw new Error('unbalanced braces reading ' + name);
}

function renderField(f, value, choices) {
  const sandbox = {};
  vm.runInNewContext(
    [lift('esc'), lift('selectHtml'), lift('fieldHtml')].join('\n'), sandbox);
  return sandbox.fieldHtml(f, value, choices);
}

/** [{value, selected}] for every <option> in a rendered select. */
function options(html) {
  return (html.match(/<option[^>]*>/g) || []).map(tag => ({
    value: (/value="([^"]*)"/.exec(tag) || [, ''])[1],
    selected: / selected/.test(tag),
  }));
}

test('a value the option list does not name is still shown, and selected', () => {
  // The data-loss half. With nothing selected the browser falls back to option
  // zero, so "exactly one option is selected and it is ours" is the whole test.
  const html = renderField({ name: 'type', label: 'Type', input: 'select',
                             required: true, options: ['A', 'AAAA', 'CNAME'] }, 'MX');
  const opts = options(html);
  const selected = opts.filter(o => o.selected);
  assert.equal(selected.length, 1, 'exactly one option must be selected');
  assert.equal(selected[0].value, 'MX', 'the record is an MX record; say so');
  assert.ok(opts.some(o => o.value === 'MX'), 'MX must be among the options');
});

test('a required select still offers no blank option', () => {
  // Preserving an unknown value must not have introduced a blank that the
  // required check would then reject.
  const html = renderField({ name: 'type', label: 'Type', input: 'select',
                             required: true, options: ['A', 'AAAA'] }, 'A');
  assert.ok(!options(html).some(o => o.value === ''), 'no blank option when required');
});

test('an optional select keeps its blank, and an unset value selects nothing', () => {
  const html = renderField({ name: 'x', label: 'X', input: 'select',
                             options: ['one', 'two'] }, '');
  const opts = options(html);
  assert.ok(opts.some(o => o.value === ''), 'an optional select can be emptied');
  assert.equal(opts.filter(o => o.selected).length, 0);
});

test('every type in the resource round-trips through the form unchanged', () => {
  // The end-to-end statement of the bug: open a record of each type, change
  // nothing, and the form still says what the router said.
  for (const t of ROUTEROS_TYPES) {
    const html = renderField(describedField('type'), t);
    const selected = options(html).filter(o => o.selected);
    assert.equal(selected.length, 1, t + ': exactly one option selected');
    assert.equal(selected[0].value, t, t + ' must round-trip');
  }
});

// ── What the table shows ─────────────────────────────────────────────────────

test('the collector fetches the value half of every type', () => {
  // The declaration is read rather than the constant, which is not exported.
  // Quotes, `+` and whitespace come out so a proplist split across source lines
  // reads the same as one written on a single line.
  const decl = DNS_SRC.slice(DNS_SRC.indexOf('const STATIC_CMD'));
  const proplist = decl.slice(0, decl.indexOf('];') + 2).replace(/['\s+]/g, '');
  for (const prop of ['address', 'cname', 'forward-to', 'text',
                      'mx-exchange', 'ns', 'srv-target']) {
    assert.ok(new RegExp('[=,]' + prop + '\\b').test(proplist),
      prop + ' is missing from the static proplist, so its column renders blank');
  }
});

test("the Address column shows each type's value rather than nothing", () => {
  const out = parseStaticEntries([
    { '.id': '*1', name: 'a.lan',    type: 'A',     address: '10.0.0.1' },
    { '.id': '*2', name: 'c.lan',    type: 'CNAME', cname: 'a.lan' },
    { '.id': '*3', name: 'f.lan',    type: 'FWD',   'forward-to': '10.0.0.53' },
    { '.id': '*4', name: 'mail.lan', type: 'MX',    'mx-exchange': 'mx1.lan' },
    { '.id': '*5', name: 'zone.lan', type: 'NS',    ns: 'ns1.lan' },
    { '.id': '*6', name: '_sip.lan', type: 'SRV',   'srv-target': 'host.lan.' },
    { '.id': '*7', name: 'txt.lan',  type: 'TXT',   text: 'v=spf1 -all' },
  ]);
  const by = Object.fromEntries(out.map(e => [e.name, e.address]));
  assert.equal(by['a.lan'], '10.0.0.1');
  assert.equal(by['c.lan'], 'a.lan');
  assert.equal(by['f.lan'], '10.0.0.53');
  assert.equal(by['mail.lan'], 'mx1.lan');
  assert.equal(by['zone.lan'], 'ns1.lan');
  assert.equal(by['_sip.lan'], 'host.lan.');
  assert.equal(by['txt.lan'], 'v=spf1 -all');
});
