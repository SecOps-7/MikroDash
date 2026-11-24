// CAP versions (Settings -> Features): each CAP's RouterOS version against the
// manager's, and an Upgrade button on the ones behind
// (internal/server/capversions_api.go).

import type { Socket } from '../socket';
import type { Cap } from '../gen/payloads';
import { esc, el, pill } from '../dom';
import { confirmAction } from '../confirm';
import { featureOn, onFeatures } from '../caps';

const PRE_RANK: Record<string, number> = { alpha: 1, beta: 2, rc: 3 };

/** "7.16rc2 (testing)" as its numbers, a rank below a release for alpha, beta and rc, and that tag's number. */
function parseVersion(v: string): { nums: number[]; rank: number; pre: number } {
  const m = /^\s*(\d+(?:\.\d+)*)\s*(?:(alpha|beta|rc)\s*(\d+)?)?/i.exec(v);
  if (!m) return { nums: [], rank: 4, pre: 0 };
  const tag = (m[2] || '').toLowerCase();
  return { nums: m[1]!.split('.').map(Number), rank: tag ? PRE_RANK[tag]! : 4, pre: Number(m[3] || 0) };
}

/**
 * -1, 0 or 1, comparing dotted versions number by number ("7.9" < "7.24"), and
 * a pre-release below the release it leads to ("7.16rc2" < "7.16").
 */
export function compareVersions(a: string, b: string): number {
  const pa = parseVersion(a);
  const pb = parseVersion(b);
  for (let i = 0; i < Math.max(pa.nums.length, pb.nums.length); i++) {
    const d = (pa.nums[i] || 0) - (pb.nums[i] || 0);
    if (d) return d < 0 ? -1 : 1;
  }
  const d = pa.rank - pb.rank || pa.pre - pb.pre;
  return d ? (d < 0 ? -1 : 1) : 0;
}

export interface CapVersions {
  /** What follows a CAP's version in its row: nothing, or behind and Upgrade. */
  cell(c: Cap): string;
}

export function initCapVersions(socket: Socket, rerender: () => void): CapVersions {
  let routerId = '';
  let manager = '';
  const note: Record<string, string> = {};

  function load(): void {
    if (!featureOn('capVersionsEnabled') || !routerId) { manager = ''; rerender(); return; }
    fetch('/api/capsman/version?routerId=' + encodeURIComponent(routerId), { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : { version: '' }))
      .then((d) => { manager = String((d && d.version) || ''); })
      .catch(() => { manager = ''; })
      .then(rerender);
  }

  async function upgrade(identity: string, legacy: boolean): Promise<void> {
    if (!(await confirmAction({ title: 'Upgrade the CAP?',
      message: 'Upgrade this CAP from the manager? The CAP reboots and its clients drop.',
      subject: identity, confirmLabel: 'Upgrade', danger: true }))) return;
    note[identity] = 'asking…';
    rerender();
    fetch('/api/capsman/upgrade', {
      method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ routerId, identity, legacy }),
    })
      .then((r) => r.json())
      .then((d) => { note[identity] = d && d.ok ? 'upgrading' : 'refused: ' + ((d && (d.message || d.error)) || '?'); })
      .catch(() => { note[identity] = 'the request did not reach the server'; })
      .then(rerender);
  }

  // CAPTURE, because a CAP's row opens its clients on a click of its own, and
  // a listener on the row would see the button's click first in the bubble.
  el('capsmanTable')?.addEventListener('click', (ev) => {
    const b = (ev.target as HTMLElement).closest?.('[data-capupgrade]') as HTMLElement | null;
    if (!b) return;
    ev.stopPropagation();
    void upgrade(b.getAttribute('data-capupgrade') || '', b.getAttribute('data-legacy') === '1');
  }, true);

  socket.on('router:active', (d) => { if (d && d.activeId && d.activeId !== routerId) { routerId = d.activeId; load(); } });
  socket.on('router:switched', (d) => { routerId = (d && d.activeId) || ''; load(); });
  document.addEventListener('mikrodash:pagechange', (e) => { if ((e as CustomEvent).detail === 'capsman') load(); });
  onFeatures(load);

  return {
    cell(c) {
      if (!featureOn('capVersionsEnabled') || !manager || !c.version) return '';
      const n = note[c.identity];
      if (compareVersions(c.version, manager) >= 0) return n ? ' <span class="muted-note">' + esc(n) + '</span>' : '';
      return ' ' + pill('behind ' + manager, 'warn') +
        (n ? ' <span class="muted-note">' + esc(n) + '</span>'
          : ' <button class="sbtn sbtn-outline is-sm" type="button" data-capupgrade="' + esc(c.identity) +
            '" data-legacy="' + (c.legacy ? '1' : '0') + '">Upgrade</button>');
    },
  };
}
