// The device modal's sections for a SwOS switch (#142).
//
// A switch's frame carries `switch` instead of the router fields: identity,
// temperature, the host-table size and every port with its rates, error
// totals, PoE output and SFP module. These two sections stand where a router
// shows its resources and its ports; the WAN chart and the Clients tab do not
// apply and are left out by the modal.
//
// Rx is var(--accent-rx) and Tx var(--accent-tx), as everywhere.

import { esc, fmtBps, pill } from '../dom';
import type { Live, SwitchPortLive } from '../gen/payloads';
import { familyLabel } from './switch-form';

/** Seconds as the card writes an uptime: the two largest units. */
export function switchUptime(sec: number | null | undefined): string {
  if (sec == null) return '-';
  const parts: string[] = [];
  const units: [number, string][] = [[604800, 'w'], [86400, 'd'], [3600, 'h'], [60, 'm'], [1, 's']];
  let rest = Math.floor(sec);
  for (const [n, u] of units) {
    const v = Math.floor(rest / n);
    rest -= v * n;
    if (v > 0 || parts.length) parts.push(v + u);
    if (parts.length === 2) break;
  }
  return parts.length ? parts.join(' ') : '0s';
}

function kv(k: string, v: string): string {
  return '<div class="dvm-kv"><dt>' + esc(k) + '</dt><dd>' + v + '</dd></div>';
}

/** The switch at a glance, where a router shows CPU and memory. */
export function switchSummaryHtml(live: Live | null): string {
  const head = '<div class="dvm-sec-head"><h3>Switch</h3>'
    + pill('Preview: monitoring only', 'warn') + '</div>';
  const sw = live?.switch;
  if (!live || !sw) return head + '<div class="dvm-skel" style="height:64px"></div>';
  if (!live.connected && !sw.ports.length) {
    return head + '<div class="dvm-none">' + esc(sw.lastError || 'Offline: no reading yet.') + '</div>';
  }
  const dash = '<span class="text-muted">-</span>';
  return head + '<dl class="dvm-details">'
    + kv('Model', sw.model ? esc(sw.model) : dash)
    + kv('Firmware', sw.version ? esc(familyLabel(sw.scheme) + ' ' + sw.version) : dash)
    + kv('Uptime', esc(switchUptime(sw.uptimeSec)))
    + kv('Temperature', sw.tempC != null ? esc(sw.tempC + ' °C') : dash)
    + kv('MAC table', sw.hosts != null ? esc(sw.hosts + ' address' + (sw.hosts === 1 ? '' : 'es')) : dash)
    + '</dl>'
    + (sw.lastError ? '<div class="dvm-none">' + esc(sw.lastError) + '</div>' : '');
}

function statusPill(p: SwitchPortLive): string {
  if (!p.enabled) return pill('Disabled', 'muted');
  if (!p.up) return pill('Down', 'err');
  return pill('Up' + (p.speed ? ' ' + p.speed : '') + (p.fullDuplex ? '' : ' half'), 'ok');
}

function poeCell(p: SwitchPortLive): string {
  if (!p.poe) return '<span class="text-muted">-</span>';
  const on = p.poe.status === 'powered on';
  return esc(p.poe.status) + (on ? ' <span class="text-muted">' + esc(p.poe.powerW.toFixed(1) + ' W') + '</span>' : '');
}

function sfpCell(p: SwitchPortLive): string {
  const m = p.sfp;
  if (!m) return '<span class="text-muted">-</span>';
  const bits = [m.part || m.vendor];
  if (m.tempC != null) bits.push(m.tempC + ' °C');
  if (m.rxDbm != null) bits.push('Rx ' + m.rxDbm.toFixed(1) + ' dBm');
  return esc(bits.filter(Boolean).join(', '));
}

/** Every port: state, rates, error totals, PoE and the SFP module. */
export function switchPortsHtml(live: Live | null): string {
  const sw = live?.switch;
  const ports = sw ? sw.ports : [];
  const up = ports.filter((p) => p.up && p.enabled).length;
  const head = '<div class="dvm-sec-head"><h3>Ports</h3>'
    + (ports.length ? pill(up + ' of ' + ports.length + ' up', 'muted') : '') + '</div>';
  if (!live || !sw) return head + '<div class="dvm-skel" style="height:64px"></div>';
  if (!ports.length) return head + '<div class="dvm-none">' + (live.connected ? 'No ports reported.' : 'Offline: no port reading.') + '</div>';
  const rate = (v: number | null, cls: string) => v == null
    ? '<span class="text-muted">-</span>'
    : '<span style="color:var(' + cls + ')">' + esc(fmtBps(v)) + '</span>';
  const rows = ports.map((p) => '<tr>'
    + '<td>' + esc(p.name) + '</td>'
    + '<td>' + statusPill(p) + '</td>'
    + '<td class="rtl-num">' + rate(p.rxBps, '--accent-rx') + '</td>'
    + '<td class="rtl-num">' + rate(p.txBps, '--accent-tx') + '</td>'
    + '<td class="rtl-num">' + (p.rxErrors + p.txErrors > 0
      ? '<span title="received ' + p.rxErrors + ', sent ' + p.txErrors + '">' + esc(String(p.rxErrors + p.txErrors)) + '</span>'
      : '<span class="text-muted">0</span>') + '</td>'
    + '<td>' + poeCell(p) + '</td>'
    + '<td>' + sfpCell(p) + '</td>'
    + '</tr>').join('');
  return head + '<div class="dvm-clients-scroll"><table class="table dvm-swports">'
    + '<thead><tr><th>Port</th><th>State</th><th class="rtl-num">Rx</th><th class="rtl-num">Tx</th>'
    + '<th class="rtl-num" title="Errors counted since the switch started">Errors</th><th>PoE</th><th>SFP</th></tr></thead>'
    + '<tbody>' + rows + '</tbody></table></div>';
}
