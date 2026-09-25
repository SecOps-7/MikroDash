/**
 * Settings -> About.
 *
 * ── ONE FETCH, RENDERED FOUR WAYS ──────────────────────────────────────────
 *
 * `/api/about` answers with the build, the runtime, the dependencies and the
 * release notes in one reply, because they are one screen. This module draws
 * each part and owns the two tabs underneath.
 *
 * ── EVERYTHING IS OPTIONAL, AND THAT IS THE POINT ──────────────────────────
 *
 * A local build has no commit. An install with no outbound network has no
 * update check. An image without the changelog has no release notes. Each of
 * those renders as ABSENT rather than as an empty row, because a label with
 * nothing after it reads as a value that failed to load.
 */

import { el, esc } from '../dom';

interface AboutNote { kind: string; text: string }
interface AboutRelease { version: string; title?: string; date?: string; entries: AboutNote[] }
interface AboutDep { name: string; version: string; licence: string; url?: string; kind: string }
interface AboutPayload {
  ok?: boolean;
  version?: string; commit?: string; branch?: string; built?: string;
  runtime?: { go: string; platform: string; memoryMb: number; container?: string; uptimeSec: number };
  database?: { engine: string; schema: number };
  update?: { latest?: string; current?: boolean; checkedAt?: number };
  releases?: AboutRelease[];
  deps?: AboutDep[];
}

let loaded = false;
let deps: AboutDep[] = [];

/** How long the process has been up, in the largest two units that fit. */
function uptime(sec: number): string {
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return d + 'd ' + h + 'h';
  if (h > 0) return h + 'h ' + m + 'm';
  return m + 'm';
}

/** A fact with an optional pill, skipped entirely when it has no value. */
function fact(value: string | undefined, pill = false): string {
  if (!value) return '';
  return pill
    ? '<span class="about-pill">' + esc(value) + '</span>'
    : '<span class="about-fact">' + esc(value) + '</span>';
}

function renderHead(d: AboutPayload): void {
  const v = el('aboutVersion');
  if (v) v.textContent = 'v' + (d.version || '-');

  // ── THE BADGE APPEARS ONLY WHEN THE CHECK ANSWERED ────────────────────
  //
  // `latest` is empty when GitHub could not be reached, was rate limited, or
  // has never been asked. Showing nothing is the honest rendering; a green
  // tick meaning "the request failed" is the one state worse than silence.
  const up = el('aboutUpdate');
  if (up) {
    const latest = d.update?.latest;
    if (!latest) {
      up.hidden = true;
    } else {
      up.hidden = false;
      up.className = 'about-update ' + (d.update?.current ? 'is-current' : 'is-behind');
      up.textContent = d.update?.current ? 'Up to date' : 'v' + latest + ' available';
    }
  }

  // Branch, commit and build date are stamped at image build time and absent
  // from a local build. Each is dropped rather than shown empty.
  const bits: string[] = [];
  if (d.branch) bits.push(esc(d.branch));
  if (d.commit) bits.push(esc(d.commit.slice(0, 7)));
  if (d.built) bits.push(esc(d.built));
  if (d.runtime) bits.push('up ' + esc(uptime(d.runtime.uptimeSec)));
  const b = el('aboutBuild');
  if (b) b.innerHTML = bits.join('<span class="about-sep">/</span>');
}

function renderSystem(d: AboutPayload): void {
  const rt = el('aboutRuntime');
  if (rt && d.runtime) {
    rt.innerHTML = [
      fact(d.runtime.go, true),
      fact(d.runtime.platform),
      fact(d.runtime.memoryMb ? d.runtime.memoryMb + ' MB' : ''),
      fact(d.runtime.container),
    ].filter(Boolean).join('');
  }
  const db = el('aboutDatabase');
  if (db && d.database) {
    db.innerHTML = [
      fact(d.database.engine, true),
      fact(d.database.schema ? 'Schema v' + d.database.schema : ''),
    ].filter(Boolean).join('');
  }
}

/**
 * One release, collapsed.
 *
 * COLLAPSED EXCEPT THE FIRST. Fifty releases expanded is a page nobody reads;
 * the newest is the one somebody came for, so it opens and the rest wait.
 */
function releaseRow(r: AboutRelease, first: boolean): string {
  const n = r.entries.length;
  const badge = first ? '<span class="about-latest">Latest</span>' : '';
  const when = r.date || r.title || '';
  const body = r.entries.map((e) =>
    '<li class="about-note-row">'
    + '<span class="about-kind about-kind-' + esc((e.kind || 'other').toLowerCase()) + '">'
    + esc(e.kind || 'Change') + '</span>'
    + '<span class="about-note-text">' + esc(e.text) + '</span></li>').join('');
  return '<details class="about-release"' + (first ? ' open' : '') + '>'
    + '<summary><span class="about-rel-v">v' + esc(r.version) + '</span>' + badge
    + '<span class="about-count">' + n + (n === 1 ? ' change' : ' changes') + '</span>'
    + '<span class="about-rel-when">' + esc(when) + '</span></summary>'
    + '<ul class="about-notes">' + body + '</ul></details>';
}

function renderReleases(list: AboutRelease[]): void {
  const wrap = el('aboutReleases');
  const count = el('aboutNotesCount');
  if (count) count.textContent = String(list.length);
  if (!wrap) return;
  wrap.innerHTML = list.length
    ? list.map((r, i) => releaseRow(r, i === 0)).join('')
    // NOT AN EMPTY BOX. The changelog ships in the image; its absence is a
    // packaging fault, and saying so beats a blank panel that reads as loading.
    : '<p class="about-empty">No release notes are packaged with this build.</p>';
}

function depRow(d: AboutDep): string {
  const link = d.url
    ? '<a href="' + esc(d.url) + '" target="_blank" rel="noopener" class="about-dep-link"'
      + ' aria-label="Open ' + esc(d.name) + '">&#8599;</a>'
    : '';
  return '<div class="about-dep">'
    + '<span class="about-dep-name">' + esc(d.name) + '</span>'
    + '<span class="about-dep-ver">' + esc(d.version) + '</span>'
    + '<span class="about-dep-lic">' + esc(d.licence) + '</span>'
    + link + '</div>';
}

function renderDeps(filter = ''): void {
  const wrap = el('aboutDeps');
  const count = el('aboutDepsCount');
  if (count) count.textContent = String(deps.length);
  if (!wrap) return;
  const q = filter.trim().toLowerCase();
  const rows = q
    ? deps.filter((d) => d.name.toLowerCase().includes(q) || d.licence.toLowerCase().includes(q))
    : deps;
  wrap.innerHTML = rows.length
    ? rows.map(depRow).join('')
    : '<p class="about-empty">Nothing matches that.</p>';
}

function selectTab(which: string): void {
  document.querySelectorAll('[data-abouttab]').forEach((b) => {
    b.classList.toggle('active', (b as HTMLElement).getAttribute('data-abouttab') === which);
  });
  document.querySelectorAll('[data-aboutpanel]').forEach((p) => {
    p.classList.toggle('active', (p as HTMLElement).getAttribute('data-aboutpanel') === which);
  });
}

/**
 * Load once per page lifetime.
 *
 * The reply is a constant for the life of the process apart from the uptime, so
 * re-fetching on every visit to the tab would spend a request to move one
 * number nobody is watching.
 */
export function loadAbout(): void {
  if (loaded) return;
  loaded = true;
  void fetch('/api/about', { credentials: 'same-origin' })
    .then((r) => r.json())
    .then((d: AboutPayload) => {
      if (!d || d.ok === false) return;
      renderHead(d);
      renderSystem(d);
      renderReleases(d.releases || []);
      deps = d.deps || [];
      renderDeps();
    })
    .catch(() => {
      // ALLOWED TO FAIL SILENTLY, and only here: every value on this page is
      // informational, so a failed load leaves the markup's own placeholders
      // rather than an error the operator can do nothing about. The latch is
      // released so the next visit tries again.
      loaded = false;
    });
}

export function initAbout(): void {
  document.querySelectorAll('[data-abouttab]').forEach((b) => {
    b.addEventListener('click', () =>
      selectTab((b as HTMLElement).getAttribute('data-abouttab') || 'notes'));
  });
  // DELEGATED on the document, because the search box lives inside a panel that
  // is in the markup from the start but only rendered into after the fetch.
  document.addEventListener('input', (ev) => {
    const t = ev.target as HTMLInputElement | null;
    if (t?.id === 'aboutDepSearch') renderDeps(t.value);
  });
}
