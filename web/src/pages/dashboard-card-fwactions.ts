// The Dashboard's FW Actions card (dc-card-fwaction): which actions the firewall
// is actually taking, as a bar per action.
//
// ── ENABLED RULES ONLY ──────────────────────────────────────────────────────
//
// A disabled rule is skipped. The card is about what the firewall IS doing, not
// what it could do if switched on — so a ruleset with forty disabled drop rules
// shows the accepts, which is the honest picture of live traffic handling.
//
// ── SEVEN, AND THE BAR IS RELATIVE TO THE BIGGEST ───────────────────────────
//
// Top seven by count, and each bar is a percentage OF THE LARGEST rather than of
// the total. So the top action is always a full bar and the rest are read
// against it — a shape comparison, not a share-of-total one.
//
// ── AN UNNAMED ACTION IS `?` ────────────────────────────────────────────────
//
// `r.action || '?'` — a rule whose action the router did not report is counted
// under a literal question mark rather than dropped, so the totals still add up.

import { el } from '../dom';
import { dcEsc } from './dashboard-cards-util';

export interface FwRule {
  action?: string;
  disabled?: boolean;
}
export interface FirewallPayload {
  filter?: FwRule[];
  nat?: FwRule[];
  mangle?: FwRule[];
  raw?: FwRule[];
}

const ACTION_COLOUR: Record<string, string> = {
  accept: 'rgba(52,211,153,.8)', drop: 'rgba(248,113,113,.8)',
  reject: 'rgba(251,113,133,.8)', masquerade: 'rgba(56,189,248,.8)',
  'dst-nat': 'rgba(251,191,36,.8)', 'src-nat': 'rgba(251,191,36,.8)',
  log: 'rgba(167,139,250,.8)', passthrough: 'rgba(52,211,153,.6)',
};

export function renderFwActionsCard(data: FirewallPayload): void {
  const filter = data.filter || [], nat = data.nat || [];
  const mangle = data.mangle || [], raw = data.raw || [];
  const all = filter.concat(nat, mangle, raw);

  const counts: Record<string, number> = {};
  for (const r of all) {
    if (r.disabled) continue;
    const a = r.action || '?';
    counts[a] = (counts[a] || 0) + 1;
  }
  // `Object.entries` order is insertion order for string keys, and the sort is
  // NOT stable-broken by anything else — two actions with the same count keep
  // the order the rules were read in. That is the live behaviour and it is why
  // the sort compares counts only.
  const entries = Object.entries(counts).sort((a, b) => b[1] - a[1]).slice(0, 7);
  const maxA = entries.length ? entries[0]![1] : 1;

  const listEl = el('dc-fwActionList');
  if (!listEl) return;
  // `|| '<div…No rules…>'` on the JOINED STRING, not on the array: an empty
  // list joins to '', which is falsy, so the fallback row appears. Written the
  // same way rather than as a length test, because that is where the original
  // puts the decision.
  listEl.innerHTML = entries.map((e) => {
    const col = ACTION_COLOUR[e[0]] || 'rgba(99,130,190,.7)';
    return '<div class="fw-action-row">' +
      '<span class="fw-action-name" style="color:' + col + '">' + dcEsc(e[0]) + '</span>' +
      '<div class="fw-action-bar-wrap"><div class="fw-action-bar" style="width:' +
        Math.round((e[1] / maxA) * 100) + '%;background:' + col + '"></div></div>' +
      '<span class="fw-action-count">' + e[1] + '</span>' +
    '</div>';
  }).join('') ||
    '<div class="fw-action-row"><span class="fw-action-name" style="color:var(--text-muted)">No rules</span></div>';
}
