// The Dashboard's Security Score card (dc-card-secscore): the Security Scan
// page's score card, on the Dashboard.
//
// ── NEVER EMPTY, NEVER SCANNING ON ITS OWN AFTER THAT ───────────────────────
//
// The server answers the card being shown with the router's last report, and
// scans only when there is none (the operator's call, 2026-09-19). After that
// the card shows the last result and its age; Rescan asks for a new one. Any
// scan of the router, from the page, this card or the assistant, reaches the
// card as it runs (`secscore:state` to the card's room).
//
// ── THE PAGE'S PIECES ───────────────────────────────────────────────────────
//
// The ring, the grade thresholds and words, and the age are the page's own
// (security-scan-cards.ts, tools-ping-cards.ts), so the card and the page can
// never disagree on what a score means.

import type { Socket } from '../socket';
import type { SecScorePayload } from '../gen/payloads';
import { el, esc } from '../dom';
import { scoreGrade } from './tools-ping-cards';
import { GRADE_WORD, RING_C, ago } from './security-scan-cards';

const SEVS = [['critical', 'Critical'], ['high', 'High'], ['medium', 'Medium'], ['low', 'Low']] as const;

const REFUSED: Record<string, string> = {
  denied: 'You may not scan this router.',
  unavailable: 'No router is connected.',
};

/** The router the card is showing; a frame about another is dropped. */
let routerId = '';
let last: SecScorePayload | null = null;

function setText(id: string, v: string): void {
  const e = el(id);
  if (e) e.textContent = v;
}

/** The label beside the ring: the grade and the issues, as the page words it. */
export function secScoreLabel(d: SecScorePayload): string {
  if (!d.has) return d.running ? 'Scanning…' : 'Not scanned yet';
  return GRADE_WORD[scoreGrade(d.score)] + ' · ' + (d.issues === 1 ? '1 issue' : d.issues + ' issues');
}

/** The four severity tiles; a tile with issues is lit. */
export function secScoreSevs(d: SecScorePayload): string {
  return SEVS.map(([k, label]) => {
    const n = d.has ? d[k] : 0;
    return '<div class="dc-sec-sev sev-' + k + (n ? ' is-on' : '') + '"><b>' + (d.has ? n : '–') +
      '</b><span>' + esc(label) + '</span></div>';
  }).join('');
}

/** The line under the tiles: progress, a refusal, or the checks and the age. */
export function secScoreMeta(d: SecScorePayload): string {
  if (d.running) return 'Scanning ' + d.done + ' of ' + d.total + ' menus…';
  if (d.code) return REFUSED[d.code] || (d.message ? 'The last scan failed: ' + d.message : 'The last scan failed.');
  if (!d.has) return '';
  return d.passed + ' of ' + d.checks + ' checks passed · ' + ago(d.scannedAt);
}

export function renderSecScoreCard(d: SecScorePayload): void {
  if (routerId && d.routerId && d.routerId !== routerId) return;
  // A refusal of Rescan keeps the report already on the card.
  if (d.code && d.code !== 'failed' && last?.has) d = { ...last, code: d.code, running: false };
  last = d;
  const card = el('dc-secScore');
  const g = d.has ? scoreGrade(d.score) : '';
  card?.classList.toggle('grade-good', g === 'good');
  card?.classList.toggle('grade-fair', g === 'fair');
  card?.classList.toggle('grade-poor', g === 'poor');
  el('dc-secRing')?.setAttribute('stroke-dashoffset', (RING_C * (1 - (d.has ? d.score : 0) / 100)).toFixed(1));
  setText('dc-secVal', d.has ? String(d.score) : '—');
  setText('dc-secLabel', secScoreLabel(d));
  const sevs = el('dc-secSevs');
  if (sevs) sevs.innerHTML = secScoreSevs(d);
  const meta = el('dc-secMeta');
  if (meta) {
    meta.textContent = secScoreMeta(d);
    meta.classList.toggle('is-bad', !!d.code);
  }
  el('dc-secProgress')?.classList.toggle('is-on', d.running);
  const bar = el('dc-secProgressBar');
  if (bar) bar.style.width = (d.running && d.total ? Math.round((d.done / d.total) * 100) : 0) + '%';
  const btn = el<HTMLButtonElement>('dc-secRescan');
  if (btn) {
    btn.classList.toggle('is-busy', d.running);
    btn.disabled = d.running;
  }
}

/** Back to waiting, for a router switch: the new router's state follows. */
function reset(): void {
  last = null;
  setText('dc-secVal', '—');
  setText('dc-secLabel', 'Waiting for the router…');
  setText('dc-secMeta', '');
  const sevs = el('dc-secSevs');
  if (sevs) sevs.innerHTML = '';
  el('dc-secRing')?.setAttribute('stroke-dashoffset', RING_C.toFixed(1));
  el('dc-secProgress')?.classList.remove('is-on');
  for (const c of ['grade-good', 'grade-fair', 'grade-poor']) el('dc-secScore')?.classList.remove(c);
}

export function initSecScoreCard(socket: Socket): void {
  socket.on('secscore:state', (d) => renderSecScoreCard(d));
  socket.on('router:switched', (d) => {
    routerId = d.activeId;
    reset();
  });
  el('dc-secRescan')?.addEventListener('click', () => {
    const btn = el<HTMLButtonElement>('dc-secRescan');
    if (btn) {
      btn.classList.add('is-busy');
      btn.disabled = true;
    }
    socket.emit('secscore:scan', {});
  });
  el('dc-secOpen')?.addEventListener('click', (e) => {
    e.preventDefault();
    document.querySelector<HTMLElement>('.nav-item[data-page="security-scan"]')?.click();
  });
  // THE AGE TICKS ONLY WHILE THE DASHBOARD IS SHOWN, as the page's does:
  // started on arriving, cleared on leaving, so no timer runs unseen.
  let ticker: ReturnType<typeof setInterval> | null = null;
  document.addEventListener('mikrodash:pagechange', (e) => {
    if (ticker) {
      clearInterval(ticker);
      ticker = null;
    }
    if ((e as CustomEvent).detail !== 'dashboard') return;
    ticker = setInterval(() => {
      if (last?.has && !last.running && !last.code) setText('dc-secMeta', secScoreMeta(last));
    }, 30_000);
  });
}
