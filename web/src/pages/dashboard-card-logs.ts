// The Dashboard's Logs card (dc-card-logs): the last fifty lines, tailed.
//
// ── `logs:history` IS AN ARRAY, FROM EVERY SENDER ───────────────────────────
//
// Both senders - the collector, and the replay on a card's focus - go through
// `EvLogsHistory`, declared `[]LogEntry`. The two once disagreed (a bare array
// and `{ entries }`), and a `data.entries || data` guard dropped the bare array
// because `Array.prototype.entries` is truthy. One declared type is what closes
// that for good, so the handler takes the array and nothing else.
//
// ── THE TOPIC CLASS IS FIRST-MATCH-WINS ─────────────────────────────────────
//
// A line topicked `dhcp,wireless` is DHCP, not both and not the later one. The
// order of the tests is the priority, so it is written as a chain rather than a
// table - a table would suggest the order does not matter.
//
// ── AND A LINE WITH NO MESSAGE IS DROPPED ───────────────────────────────────
//
// `logs:new` requires a truthy `message`. An empty one would render a blank row
// that scrolls the real lines out of view, fifty at a time.

import { el } from '../dom';
import { dcEsc } from './dashboard-cards-util';
import type { LogEntry } from '../gen/payloads';

const DC_LOG_MAX = 50;
let lines: LogEntry[] = [];

export function renderLogsCard(): void {
  const node = el('dc-logs');
  if (!node) return;
  if (!lines.length) { node.innerHTML = ''; return; }
  node.innerHTML = lines.map((e) => {
    const sev = e.severity || 'info';
    let cls = 'log-line log-' + sev;
    if (e.topics) {
      const t = e.topics.toLowerCase();
      if (t.indexOf('dhcp') >= 0) cls += ' log-dhcp';
      else if (t.indexOf('wireless') >= 0) cls += ' log-wireless';
      else if (t.indexOf('firewall') >= 0) cls += ' log-firewall';
      else if (t.indexOf('system') >= 0) cls += ' log-system';
    }
    return '<span class="' + cls + '">' +
      '<span class="log-time">' + dcEsc(e.time || '') + '</span> ' +
      (e.topics ? '<span class="log-topic">[' + dcEsc(e.topics) + ']</span> ' : '') +
      dcEsc(e.message) +
    '</span>';
  }).join('');
  // Pinned to the bottom on every render: this is a tail, and a tail that has to
  // be scrolled is not one.
  node.scrollTop = node.scrollHeight;
}

export function onLogsHistory(entries: LogEntry[]): void {
  lines = entries.slice(-DC_LOG_MAX);
  renderLogsCard();
}

export function onLogsNew(entry: LogEntry | undefined): void {
  if (!entry || !entry.message) return;
  lines.push(entry);
  if (lines.length > DC_LOG_MAX) lines.shift();
  renderLogsCard();
}

/** A switch to another router shares no log tail. */
export function resetLogsCard(): void {
  lines = [];
}
