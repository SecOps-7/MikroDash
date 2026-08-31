// Dashboard card 3 — PHYSICAL PORTS (`ifstatus:update`).
//
// A row of RJ-45 drawings, one per physical port, coloured by state. It is a
// near-copy of the Interfaces page's Ports panel and the live repo has just
// finished repairing the ways that copy had drifted, so the differences that
// REMAIN are the ones to reproduce exactly:
//
//   - The FILTER is wider here: ether, sfp and sfp-sfpplus, where the panel
//     takes ether alone. A router whose uplink is an SFP cage shows it on the
//     dashboard and not on the Interfaces panel.
//   - The LABEL uses dcEsc; the panel uses esc. Both are correct in text
//     position and they differ on quotes, so an interface named qt"test renders
//     as different innerHTML in the two places while displaying the same text.
//     Not a defect, and not unified — a port reproduces what is there.
//
// THE TITLE USES esc, AND THAT ONE WAS A DEFECT until 2026-08-24 (ToDo #16).
// It lands in an ATTRIBUTE, where dcEsc is wrong: dcEsc round-trips through a
// text node, which is the browser's own text escaper, so it leaves " and '
// alone. The live repo settled the reachability question on hardware that this
// port could not reach — `/interface/bridge/add name="qt\"test"` is accepted on
// a hAP ac2 running RouterOS 7.24 and the API returns the raw quote — so
// `ether1" onmouseover="x` closes this attribute and opens another. Whoever can
// name an interface can inject one. This card was held back until that fix
// landed rather than ported with the quirk reproduced.

import { esc } from '../dom';
import { dcEsc } from './dashboard-cards-util';
import { portSvg } from './port-svg';

export interface PhysIface {
  name: string;
  type: string;
  running: boolean;
  disabled: boolean;
  ips?: string[];
}

export interface IfStatusPayload {
  interfaces?: PhysIface[];
}

const PHYSICAL = ['ether', 'sfp', 'sfp-sfpplus'];

export function renderPhysPortsCard(data: IfStatusPayload): void {
  const panel = document.getElementById('dc-ifPortsPanel');
  if (!panel) return;

  // `data.interfaces || []`, NOT `data && data.interfaces` — the live handler
  // dereferences `data` unguarded and throws if it is ever undefined. Guarding
  // it here would make the port survive a payload the live card dies on, which
  // is a divergence rather than a hardening: the two would then disagree about
  // whether a broken emit is visible.
  const ifaces = (data.interfaces || []).filter((i) => PHYSICAL.indexOf(i.type) !== -1);
  if (!ifaces.length) {
    panel.innerHTML = '<div style="font-size:.72rem;color:var(--text-muted)">No ethernet ports</div>';
    return;
  }

  // The drawing shrinks as ports multiply so the row still fits the card.
  const n = ifaces.length;
  const sz = n <= 8 ? 44 : n <= 16 ? 36 : n <= 24 ? 30 : 26;

  panel.innerHTML = ifaces.map((i) => {
    const state = i.disabled ? 'dis' : i.running ? 'up' : 'down';
    return '<div class="if-port-item" data-state="' + state + '" title="' +
      esc(i.name) + (i.ips && i.ips.length ? ' — ' + esc(i.ips[0]!) : '') +
      (i.running ? ' (up)' : i.disabled ? ' (disabled)' : ' (down)') + '">' +
      portSvg(sz) +
      '<span class="if-port-label">' + dcEsc(i.name) + '</span>' +
    '</div>';
  }).join('');
}
