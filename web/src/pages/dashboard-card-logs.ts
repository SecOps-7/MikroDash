// The Dashboard's Logs card (dc-card-logs): the last fifty lines, tailed.
//
// ── ARRAY.ISARRAY FIRST, AND THAT ORDER IS THE WHOLE POINT ──────────────────
//
// `data.entries || data` looks like it accepts both shapes and cannot: on a bare
// array `data.entries` is `Array.prototype.entries`, a truthy FUNCTION, so the
// first operand always wins, the isArray guard then fails, and the handler
// returns having rendered nothing.
//
// It matters because the two emit sites disagree — a bare array on connect and
// `{ entries }` on card focus — so the connect replay was silently dropped and
// the card stayed empty until a focus arrived.
//
// This port reproduced the defect deliberately and pinned it as ToDo #17. It was
// fixed upstream the same day, the pinned case turned red, and this is the port
// following. The Logs PAGE always had the correct idiom, twenty lines away.
//
// ── THE TOPIC CLASS IS FIRST-MATCH-WINS ─────────────────────────────────────
//
// A line topicked `dhcp,wireless` is DHCP, not both and not the later one. The
// order of the tests is the priority, so it is written as a chain rather than a
// table — a table would suggest the order does not matter.
//
// ── AND A LINE WITH NO MESSAGE IS DROPPED ───────────────────────────────────
//
// `logs:new` requires a truthy `message`. An empty one would render a blank row
// that scrolls the real lines out of view, fifty at a time.

import { el } from '../dom';
import { dcEsc } from './dashboard-cards-util';

export interface LogEntry {
  time?: string;
  topics?: string;
  message?: string;
  severity?: string;
}

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

export function onLogsHistory(data: { entries?: LogEntry[] } | LogEntry[] | undefined): void {
  const entries = Array.isArray(data)
    ? data
    : (data && (data as { entries?: LogEntry[] }).entries ? (data as { entries?: LogEntry[] }).entries! : []);
  if (!Array.isArray(entries)) return;
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
