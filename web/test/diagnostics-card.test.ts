/**
 * THE API DIAGNOSTICS CARD.
 *
 * ── WHY THIS ONE NEEDS A GATE OF ITS OWN ────────────────────────────────────
 *
 * The card rendered NOTHING for the entire life of this port. Nothing emitted
 * `diagnostics:update`, so the renderer never ran, and an empty card is
 * indistinguishable from a card with nothing to report —
 * `internal/verify/event_test.go` recorded the silence as deliberate and it sat
 * there unchallenged. `dashboard-wiring.test.ts` proves the handler is SUBSCRIBED
 * and writes *an* element. That is exactly the check the old card would have
 * passed the day before it broke, because the failure was upstream of it.
 *
 * So this drives the renderer directly and reads what it produced. The subject is
 * an INSTRUMENT: its failure mode is a plausible wrong number, not a blank card,
 * and a plausible wrong number is invisible to a wiring check.
 *
 * ── THE THREE LAYERS ARE THE CONTRACT ───────────────────────────────────────
 *
 * `Collector-Architecture.md` names acquisition, derivation and views. The card
 * exists to make those three legible on a running install, so a section going
 * missing is a real regression even though every individual number is still
 * right.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.diag-entry.ts');
fs.writeFileSync(ENTRY,
  "export { renderDiagnosticsCard } from '../web/src/pages/dashboard-card-diagnostics.js';\n");
const OUT = path.join(ROOT, 'testdata', '.diag.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

function full(over: any = {}) {
  return Object.assign({
    routerId: 'r1',
    label: 'lab',
    acquisition: {
      commandsPerMin: 118, inFlight: 2, cap: 4, channels: 3,
      menus: 11, streamed: 3, polled: 8,
      reads: [
        { menu: '/interface/wifi/registration-table/print', streamed: true },
        { menu: '/ip/arp/print', streamed: false },
        { menu: '/ip/dns/static/print', streamed: false },
      ],
      more: 0,
      sources: [
        { source: 'Collectors', perMin: 80 },
        { source: 'Security Scan', perMin: 37 },
        { source: 'Other', perMin: 1 },
      ],
      busiest: [
        { menu: '/ip/arp/print', source: 'Collectors', perMin: 30 },
        { menu: '/ip/service/print', source: 'Security Scan', perMin: 1 },
        { menu: '/app/print', source: 'Apps', perMin: 5 },
      ],
      busiestMore: 0,
    },
    derivation: { payloadsPerMin: 240 },
    views: { running: 7, gated: 22, dormant: 2, rooms: 5, holds: ['alerts', 'history'] },
  }, over);
}

function render(payload: any) {
  const doc = makeDoc(['dc-diagTotal', 'dc-diagList']);
  const prev = global.document;
  global.document = doc;
  try {
    const { renderDiagnosticsCard } = require(OUT);
    renderDiagnosticsCard(payload);
  } finally {
    global.document = prev;
  }
  return {
    total: doc.getElementById('dc-diagTotal').textContent,
    html: doc.getElementById('dc-diagList').innerHTML,
  };
}

let checks = 0;
function ok(cond: unknown, msg: string) { assert.ok(cond, msg); checks++; }

// ── 1. all three layers are named ───────────────────────────────────────────
//
// The section headings are the card's entire reason to exist in this shape. A
// renderer that dropped one would still show every number and would still pass a
// wiring check.
{
  const { html } = render(full());
  for (const layer of ['Acquisition', 'Derivation', 'Views']) {
    ok(html.includes('>' + layer + '<'),
      `the ${layer} heading is missing — the card no longer shows a layer of the architecture`);
  }
  ok((html.match(/class="diag-layer"/g) || []).length >= 3,
    'fewer than three layer headings rendered');
}

// ── 2. the headline is the command rate ─────────────────────────────────────
//
// It used to be "active streams", which is a LEVEL and not a load: a router
// being polled hard reads zero. The one number the card owes an operator is what
// this app costs the device.
{
  const { total } = render(full());
  ok(total === '118', `headline is ${JSON.stringify(total)}, want the command rate 118`);
}

// ── 3. the streamed/polled split renders, and in flight carries its ceiling ──
{
  const { html } = render(full());
  ok(html.includes('pushed by router'), 'the streamed count is not labelled');
  ok(html.includes('>2 / 4<'),
    'in flight does not render as "2 / 4"; a slot count without its cap says nothing');
}

// ── 4. EVERY menu is named, with how it is delivered ────────────────────────
//
// This section was first written as "shared reads" — the menus more than one
// collector wants. Measured on the live fleet across seven pages on 2026-09-10:
// no menu ever has more than one subscriber, because this app coalesces a level
// up (one collector owns a menu, others read its derived index). The section
// would never have rendered. Listing the menus is the true version of it, and it
// is the only thing on the card that says WHAT is being asked rather than how
// much.
{
  const { html } = render(full());
  for (const m of ['/interface/wifi/registration-table/print', '/ip/arp/print', '/ip/dns/static/print']) {
    ok(html.includes(m), `${m} is missing — a read an operator cannot account for`);
  }
  // ANCHORED TO THE MENU. A bare `includes('pushed')` is satisfied by the
  // "· pushed by router" COUNTS row above, so it passes against a renderer that
  // labels every menu the same — measured: that mutation survived the loose
  // version of this check.
  ok(html.includes('>/interface/wifi/registration-table/print</span><span class="vpn-hs-badge hs-ok">pushed<'),
    'the streamed menu is not labelled as pushed on its own row');
  ok(html.includes('>/ip/arp/print</span><span class="vpn-hs-badge hs-info">polled<'),
    'the polled menu is not labelled as polled on its own row');
}

// ── 5. no menus, no section. An empty heading over nothing reads as a card
//      that failed to load.
{
  const p = full();
  p.acquisition.reads = [];
  ok(!render(p).html.includes('Menus subscribed'),
    'the Menus subscribed heading rendered with nothing under it');
}

// ── 5b. a truncated list SAYS SO ────────────────────────────────────────────
//
// Silent truncation on an instrument is the same failure as a wrong number: the
// card looks complete and is not.
{
  const p = full();
  p.acquisition.more = 4;
  ok(render(p).html.includes('+ 4 more'),
    'the list was capped and the card does not admit it');
  ok(!render(full()).html.includes('more'),
    'a complete list claims it was truncated');
}

// ── 6. zero is styled as a resting value, not as a live one ─────────────────
//
// Nothing running is a SUCCESS on this card — it means demand correctly stopped
// asking the router for things nobody is looking at. Painting it like a live
// figure would teach an operator to read a working idle install as a fault.
{
  const quiet = full({
    acquisition: {
      commandsPerMin: 0, inFlight: 0, cap: 4, channels: 0,
      menus: 0, streamed: 0, polled: 0, reads: [], more: 0,
      sources: [], busiest: [], busiestMore: 0,
    },
    derivation: { payloadsPerMin: 0 },
    views: { running: 0, gated: 22, dormant: 0, rooms: 0, holds: [] },
  });
  const { total, html } = render(quiet);
  ok(total === '0', `an idle router shows ${JSON.stringify(total)}, want "0"`);
  ok(!html.includes('diag-count-active'),
    'a figure is painted as live on a router where nothing is running');
  ok(html.includes('diag-count-zero'), 'nothing is painted as a resting value either');
  // Dormant is omitted at zero rather than shown as "0": a line saying no
  // collector is asleep is noise on a card that is already dense.
  ok(!html.includes('dormant'), 'the dormant line renders when nothing is dormant');
}

// ── 7. the holds line explains a router nobody is watching ──────────────────
//
// Without it, an operator looking at an unwatched router sees collectors running
// and no reason, which is the most confusing thing this card can show.
{
  const { html } = render(full());
  ok(html.includes('kept alive by'), 'the holds line is missing');
  ok(html.includes('alerts, history'), 'the holds are not named');

  const none = full();
  none.views.holds = [];
  ok(!render(none).html.includes('kept alive by'),
    'the holds line renders with no holds, claiming a reason that does not exist');
}

// ── 8. a missing section is an em dash, never a zero ────────────────────────
//
// The server sending nothing and the router doing nothing are DIFFERENT, and a
// card that renders both as "0" is lying about one of them. This is the payload
// shape a stale client or a half-built session produces.
{
  const { total, html } = render({});
  ok(total === '—', `an empty payload shows ${JSON.stringify(total)}, want an em dash`);
  ok(html.includes('&mdash;'), 'an absent figure rendered as something other than an em dash');
  ok(!html.includes('diag-count-active'), 'an absent figure is painted as live');
}

// ── 9. the row label is escaped ─────────────────────────────────────────────
//
// Menu paths are our own constants today, so this is not a live injection route
// — it is the guard that keeps it from becoming one. `wireless`'s interface-name
// injection (0.7.35) started the same way: a value nobody thought was attacker
// controlled reached innerHTML unescaped.
{
  const p = full();
  p.acquisition.reads = [{ menu: '<img src=x onerror=1>', streamed: false }];
  const { html } = render(p);
  ok(!html.includes('<img'), 'a menu name reached innerHTML as markup');
  ok(html.includes('&lt;img'), 'the menu name was dropped rather than escaped');
}

// ── 10. WHO ASKED: every source is named, as a pill of its kind ─────────────
//
// The headline counts every command, the on-demand features' included. Without
// this section a Security Scan's reads moved the number with nothing on the card
// to account for them. The kind decides the colour, so a feature added later is
// amber without this file learning its name.
{
  const { html } = render(full());
  ok(html.includes('Who asked'), 'the per-source section is missing');
  ok(html.includes('<span class="vpn-hs-badge hs-info">Collectors</span>'),
    'the collectors are not a blue pill');
  ok(html.includes('<span class="vpn-hs-badge hs-warn">Security Scan</span>'),
    'an on-demand feature is not an amber pill');
  ok(html.includes('<span class="vpn-hs-badge hs-never">Other</span>'),
    'an unnamed source is not a grey pill');
  // The bar is the source's share of the HEADLINE: 37 of 118 is 31%.
  ok(html.includes('style="width:31%"'), 'the Security Scan bar is not its share of the command rate');
  const p = full();
  p.acquisition.sources = [];
  ok(!render(p).html.includes('Who asked'), 'the per-source heading rendered with nothing under it');
}

// ── 11. BUSIEST MENUS: a sortable table, busiest first, re-sortable ─────────
{
  const doc = makeDoc(['dc-diagTotal', 'dc-diagList']);
  const prev = global.document;
  global.document = doc;
  try {
    const { renderDiagnosticsCard } = require(OUT);
    renderDiagnosticsCard(full());
    // The shim realises an id written inside innerHTML as an EMPTY node, so the
    // first paint is read from the list and the re-sort from the body it rewrites.
    const list = doc.getElementById('dc-diagList').innerHTML as string;
    const body = () => doc.getElementById('dc-diagBusyBody').innerHTML as string;
    const order = (html: string) => ['/ip/arp/print', '/app/print', '/ip/service/print']
      .map((m) => html.indexOf('>' + m + '<'));
    const [arp, app, svc] = order(list.slice(list.indexOf('dc-diagBusyBody')));
    ok(arp >= 0 && app > arp && svc > app, 'the busiest menus are not ordered by rate, busiest first');
    ok(list.includes('<td><span class="vpn-hs-badge hs-warn">Apps</span></td>'), 'a menu row does not name its source');
    const head = doc.getElementById('dc-diagBusyHead');
    ok(head.innerHTML.includes('/ min'), 'the busiest table has no header');
    // Clicking the rate header flips it to ascending.
    const ths = head.querySelectorAll('th');
    ok(ths.length === 3, `the header has ${ths.length} sortable cells, want 3`);
    ths[2]._clicks.forEach((fn: () => void) => fn());
    const [arp2, app2, svc2] = order(body());
    ok(svc2 >= 0 && app2 > svc2 && arp2 > app2, 'clicking the rate header did not reverse the order');
  } finally {
    global.document = prev;
  }
  const p = full();
  p.acquisition.busiestMore = 6;
  ok(render(p).html.includes('+ 6 more'), 'the busiest list was capped and the card does not admit it');
  // A menu the AI agent's raw command names is typed by a person: escaped.
  p.acquisition.busiest = [{ menu: '<img src=x onerror=1>', source: 'AI agent', perMin: 1 }];
  const { html } = render(p);
  ok(!html.includes('<img'), 'a busiest menu reached innerHTML as markup');
  ok(html.includes('&lt;img'), 'the busiest menu was dropped rather than escaped');
}

fs.rmSync(OUT, { force: true });
say(`diagnostics-card: all checks passed (${checks})`);
