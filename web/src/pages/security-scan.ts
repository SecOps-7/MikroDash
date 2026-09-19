// The Security Scan page (2026-09-19): the router's security gaps on one page,
// from internal/secscan's catalogue, with a 0 to 100 score in the top left.
//
// ── ASKED FOR, NEVER POLLED ─────────────────────────────────────────────────
//
// Opening the page asks the server for this router's last report
// (`secscan:get`). With none, or one older than FRESH_MS, it asks for a scan
// (`secscan:run`); Rescan asks for one whenever. The server reads the menus
// one at a time and sends progress, then the report; nothing runs while
// nobody is looking. A report is drawn only for the router it is about: a
// frame from the router just left is dropped.
//
// ── EVERYTHING DRAWN FROM THE REPORT ────────────────────────────────────────
//
// The cards are pure functions of the report (security-scan-cards.ts); this
// file places them, runs the score's count-up and the ring's sweep, and owns
// the tabs, the severity filter and the table's sort.

import { el, renderSortHeader, sortRows, type SortState } from '../dom';
import type { Socket } from '../socket';
import type { Report } from '../gen/payloads';
import { scoreGrade, tween } from './tools-ping-cards';
import {
  scoreLabel, categoryRow, topFindings, surfaceCard, firewallCard, accountsCard, updatesCard,
  coverageCard, findingRow, passedRow, sortValue, ago, RING_C,
} from './security-scan-cards';

/** A report older than this is rescanned when the page opens. */
const FRESH_MS = 30 * 60 * 1000;

const REFUSED: Record<string, string> = {
  denied: 'You may not scan this router.',
  unavailable: 'No router is connected.',
  busy: 'A scan of this router is already running; its result will appear here.',
};

export function initSecurityScanPage(socket: Socket, isVisible: (page: string) => boolean): void {
  let report: Report | null = null;
  let scannedAt = 0;
  let running = false;
  let routerId = '';
  let filter = 'all';
  const sort: SortState = { col: 'severity', dir: 'asc' };

  const setText = (id: string, t: string): void => {
    const n = el(id);
    if (n) n.textContent = t;
  };
  const setHTML = (id: string, h: string): void => {
    const n = el(id);
    if (n) n.innerHTML = h;
  };

  function setRunning(on: boolean, done = 0, total = 0): void {
    running = on;
    const btn = el<HTMLButtonElement>('secScanRun');
    if (btn) {
      btn.disabled = on;
      btn.textContent = on ? 'Scanning…' : 'Rescan';
    }
    el('secScanProgress')?.classList.toggle('is-on', on);
    const fill = el('secScanProgressBar');
    if (fill) fill.style.width = (on && total ? Math.round((done / total) * 100) : 0) + '%';
    if (on) setText('secScanStatus', total ? 'Scanning… ' + done + ' of ' + total + ' settings read' : 'Scanning…');
  }

  function run(): void {
    if (running) return;
    setRunning(true);
    socket.emit('secscan:run', {});
  }

  function drawFindings(): void {
    if (!report) return;
    const rows = report.findings.filter((f) => f.status === 'fail' && (filter === 'all' || f.severity === filter));
    renderSortHeader('secFindingsHead', [
      { key: 'severity', label: 'Severity' },
      { key: 'title', label: 'Finding' },
      { key: 'category', label: 'Category' },
      { label: 'Fix' },
      { label: '' },
    ], sort, drawFindings);
    const keyed = rows.map((f) => ({ f, severity: sortValue(f, 'severity'), title: sortValue(f, 'title'),
      category: sortValue(f, 'category') }));
    const sorted = sortRows(keyed, sort.col, sort.dir).map((k) => k.f);
    setHTML('secFindingsRows', sorted.length ? sorted.map(findingRow).join('')
      : '<tr><td colspan="5" class="empty-state">' + (filter === 'all' ? 'No issues found' : 'No ' + filter + ' issues') + '</td></tr>');
    document.querySelectorAll('#secFilters [data-filter]').forEach((b) =>
      b.classList.toggle('active', b.getAttribute('data-filter') === filter));
  }

  function draw(): void {
    if (!report) return;
    const r = report;
    // The score, top left: the ring sweeps and the number counts up.
    el('secScoreRing')?.setAttribute('stroke-dashoffset', (RING_C * (1 - r.score / 100)).toFixed(1));
    const card = el('secScoreCard');
    if (card) {
      const g = scoreGrade(r.score);
      card.classList.toggle('grade-good', g === 'good');
      card.classList.toggle('grade-fair', g === 'fair');
      card.classList.toggle('grade-poor', g === 'poor');
    }
    const whole = (x: number): string => String(Math.round(x));
    tween(el('secScoreVal'), r.score, whole);
    setText('secScoreLabel', scoreLabel(r));
    setText('secScoreSub', r.passed + ' of ' + r.findings.length + ' checks passed');
    tween(el('secSevCritical'), r.failed?.critical ?? 0, whole);
    tween(el('secSevHigh'), r.failed?.high ?? 0, whole);
    tween(el('secSevMedium'), r.failed?.medium ?? 0, whole);
    tween(el('secSevLow'), r.failed?.low ?? 0, whole);
    setHTML('secCats', r.categories.map(categoryRow).join(''));
    setHTML('secTop', topFindings(r));
    setHTML('secSurface', surfaceCard(r));
    setHTML('secFirewall', firewallCard(r));
    setHTML('secAccounts', accountsCard(r));
    setHTML('secUpdates', updatesCard(r));
    setHTML('secCoverage', coverageCard(r));
    // The pill counts what the score's label calls issues: info observations
    // are listed, not counted.
    const issues = r.findings.filter((f) => f.status === 'fail' && f.severity !== 'info').length;
    const badge = el('secScanBadge');
    if (badge) {
      badge.textContent = String(issues);
      badge.className = 'card-badge' + (issues ? ' active-blue' : '');
    }
    const passed = r.findings.filter((f) => f.status === 'pass');
    setHTML('secPassedRows', passed.length ? passed.map(passedRow).join('')
      : '<tr><td colspan="3" class="empty-state">No check passed</td></tr>');
    drawFindings();
    el('secPanel-overview')?.classList.add('sec-in');
    setText('secScanAge', scannedAt ? ago(scannedAt) : '');
  }

  function clear(): void {
    report = null;
    scannedAt = 0;
    setRunning(false);
    setText('secScanStatus', '');
    setText('secScanAge', '');
    const val = el('secScoreVal');
    if (val) {
      val.textContent = '—';
      delete val.dataset.v;
    }
    setText('secScoreLabel', 'Not scanned yet');
    setText('secScoreSub', '');
    el('secScoreRing')?.setAttribute('stroke-dashoffset', RING_C.toFixed(1));
    for (const id of ['secCats', 'secTop', 'secSurface', 'secFirewall', 'secAccounts', 'secUpdates', 'secCoverage']) setHTML(id, '');
    setHTML('secFindingsRows', '<tr><td colspan="5" class="empty-state">Not scanned yet</td></tr>');
    setHTML('secPassedRows', '<tr><td colspan="3" class="empty-state">Not scanned yet</td></tr>');
    el('secPanel-overview')?.classList.remove('sec-in');
  }

  socket.on('secscan:result', (d) => {
    if (routerId && d.routerId && d.routerId !== routerId) return; // about the router just left
    if (d.code === 'stopped') return;
    if (d.code) {
      setRunning(false);
      setText('secScanStatus', REFUSED[d.code] || d.message || 'The scan did not finish.');
      // Another viewer's scan of this router: its result is asked for shortly.
      if (d.code === 'busy') {
        setTimeout(() => { if (isVisible('security-scan')) socket.emit('secscan:get', {}); }, 3000);
      }
      return;
    }
    if (d.running && !d.report) {
      setRunning(true, d.done, d.total);
      return;
    }
    if (d.report) {
      report = d.report;
      scannedAt = d.scannedAt;
      setRunning(d.running);
      setText('secScanStatus', '');
      draw();
    }
    // Opened with no report, or an old one: scan now.
    if (!d.running && (!d.report || Date.now() - d.scannedAt > FRESH_MS) && isVisible('security-scan')) run();
  });

  el('secScanRun')?.addEventListener('click', run);

  // THE AGE TICKS ONLY WHILE THE PAGE IS SHOWN: started on opening it, cleared
  // on leaving, so no timer runs for a page nobody is looking at.
  let ticker: ReturnType<typeof setInterval> | null = null;
  document.addEventListener('mikrodash:pagechange', (e) => {
    if (ticker) {
      clearInterval(ticker);
      ticker = null;
    }
    if ((e as CustomEvent).detail !== 'security-scan') return;
    socket.emit('secscan:get', {});
    ticker = setInterval(() => { if (scannedAt) setText('secScanAge', ago(scannedAt)); }, 30000);
  });
  socket.on('router:switched', (d) => {
    routerId = d.activeId;
    clear();
    if (isVisible('security-scan')) socket.emit('secscan:get', {});
  });

  // The tabs: one panel at a time.
  function showTab(key: string): void {
    for (const k of ['overview', 'findings', 'passed']) {
      const p = el('secPanel-' + k);
      if (p) p.style.display = k === key ? '' : 'none';
    }
    document.querySelectorAll('#secScanTabs [data-sectab]').forEach((b) => {
      const on = b.getAttribute('data-sectab') === key;
      b.classList.toggle('active', on);
      b.setAttribute('aria-selected', on ? 'true' : 'false');
    });
  }
  el('secScanTabs')?.addEventListener('click', (e) => {
    const key = (e.target as HTMLElement | null)?.closest?.('[data-sectab]')?.getAttribute('data-sectab');
    if (key) showTab(key);
  });

  // A severity tile opens the findings at that severity; the chips filter
  // them; Open › goes to the page a finding is fixed on, as its nav item would.
  el('secScanBody')?.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    const sev = t?.closest?.('[data-secsev]')?.getAttribute('data-secsev');
    if (sev) {
      filter = sev;
      showTab('findings');
      drawFindings();
      return;
    }
    const f = t?.closest?.('[data-filter]')?.getAttribute('data-filter');
    if (f) {
      filter = f;
      drawFindings();
      return;
    }
    const go = t?.closest?.('[data-goto]')?.getAttribute('data-goto');
    if (go) {
      e.preventDefault();
      document.querySelector<HTMLElement>('.nav-item[data-page="' + go + '"]')?.click();
    }
  });

}
