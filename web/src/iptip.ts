// The IP tooltip: what a `.has-ip-tip` element shows on hover.
//
// Document-level and shared, not owned by any one card — the Dashboard's
// Connections card and the Connections page both render elements that carry it.
// Ported when the Dashboard card landed, because `attr-audit.js` refuses to let
// a `data-ip` be rendered by something and read by nothing: a control with no
// listener is the defect shape this port has already hit four times.
//
// ── THE ELEMENT IS BUILT, NOT EXTRACTED ─────────────────────────────────────
//
// There is no `.ip-tip` in `index.html` to lift: the live app creates the div
// and appends it to <body> at load. Reproduced, including the class name, so the
// existing stylesheet rule applies unchanged.
//
// ── THE 'NONE' READ-BACK IS THE MOVE HANDLER'S ONLY GUARD ───────────────────
//
// `mousemove` re-positions the tooltip only when it is displayed, and it decides
// that by READING BACK `style.display`. That is not how this port would write it
// from scratch — a boolean would be cheaper — but the read is what makes the
// mouseleave handler's write authoritative, and reproducing it keeps a single
// source of truth rather than adding a second one that can disagree.

import { esc } from './dom';

export function initIpTip(): void {
  const tip = document.createElement('div');
  tip.className = 'ip-tip';
  document.body.appendChild(tip);

  function showTip(target: HTMLElement, e: MouseEvent): void {
    const ip = target.dataset.ip || '', org = target.dataset.org || '', cat = target.dataset.cat || '';
    if (!ip) { tip.style.display = 'none'; return; }
    tip.innerHTML = esc(ip) + (org ? '<span class="ip-tip-org">' + esc(org) + '</span>' +
      '<span class="ip-tip-cat svc-badge svc-' + (cat || 'other') + '">' + esc(cat) + '</span>' : '');
    tip.style.transform = 'translate(' + (e.clientX + 14) + 'px,' + (e.clientY - 32) + 'px)';
    tip.style.display = 'block';
  }

  document.addEventListener('mouseover', (e) => {
    const t = e.target as HTMLElement | null;
    const target = t && t.closest ? t.closest<HTMLElement>('.has-ip-tip') : null;
    if (target) showTip(target, e as MouseEvent); else tip.style.display = 'none';
  });
  document.addEventListener('mousemove', (e) => {
    if (tip.style.display === 'none') return;
    tip.style.transform = 'translate(' + ((e as MouseEvent).clientX + 14) + 'px,' +
      ((e as MouseEvent).clientY - 32) + 'px)';
  });
  // CAPTURING, as the original is: mouseleave does not bubble, so a listener on
  // `document` never fires in the bubble phase and the tooltip would stay up
  // after the pointer left the window.
  document.addEventListener('mouseleave', () => { tip.style.display = 'none'; }, true);
}
