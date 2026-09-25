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
interface AboutDep { name: string; version: string; licence: string; url?: string; kind: string; note?: string }
interface AboutPayload {
  ok?: boolean;
  version?: string; commit?: string; branch?: string; built?: string;
  runtime?: {
    go: string; platform: string; memoryMb: number;
    kernel?: string; container?: string; image?: string; uptimeSec: number;
  };
  database?: { engine: string; schema: number };
  update?: { latest?: string; current?: boolean; checkedAt?: number };
  releases?: AboutRelease[];
  deps?: AboutDep[];
}

/**
 * The icons this page draws, as raw 24x24 stroke paths.
 *
 * INLINE, like every other icon in this app: there is no sprite and no icon
 * module, and adding one for six glyphs would be a mechanism with one caller.
 * They are stroke-only so `currentColor` carries the theme, which is why none
 * of them sets a fill.
 */
const ICONS: Record<string, string> = {
  branch: '<line x1="6" y1="3" x2="6" y2="15"/><circle cx="18" cy="6" r="3"/>'
    + '<circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/>',
  commit: '<polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/>',
  date: '<rect x="3" y="4" width="18" height="18" rx="2"/><line x1="16" y1="2" x2="16" y2="6"/>'
    + '<line x1="8" y1="2" x2="8" y2="6"/><line x1="3" y1="10" x2="21" y2="10"/>',
  clock: '<circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/>',
  server: '<rect x="2" y="2" width="20" height="8" rx="2"/><rect x="2" y="14" width="20" height="8" rx="2"/>'
    + '<line x1="6" y1="6" x2="6.01" y2="6"/><line x1="6" y1="18" x2="6.01" y2="18"/>',
  memory: '<rect x="4" y="4" width="16" height="16" rx="2"/><rect x="9" y="9" width="6" height="6"/>'
    + '<line x1="9" y1="1" x2="9" y2="4"/><line x1="15" y1="1" x2="15" y2="4"/>'
    + '<line x1="9" y1="20" x2="9" y2="23"/><line x1="15" y1="20" x2="15" y2="23"/>'
    + '<line x1="20" y1="9" x2="23" y2="9"/><line x1="20" y1="14" x2="23" y2="14"/>'
    + '<line x1="1" y1="9" x2="4" y2="9"/><line x1="1" y1="14" x2="4" y2="14"/>',
  kernel: '<path d="M12 2 2 7l10 5 10-5-10-5z"/><polyline points="2 17 12 22 22 17"/>'
    + '<polyline points="2 12 12 17 22 12"/>',
};

/** One icon, sized by CSS. Returns nothing for a name that is not in the set. */
function ico(name: string, cls = 'about-ico'): string {
  const d = ICONS[name];
  return d ? '<svg viewBox="0 0 24 24" class="' + cls + '">' + d + '</svg>' : '';
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

/**
 * A fact, with an optional leading icon, SKIPPED ENTIRELY when it has no value.
 *
 * Skipping rather than rendering an empty row is the rule for this whole page:
 * a label with nothing after it reads as a value that failed to load, and half
 * of what is shown here is genuinely absent on some installs.
 */
function fact(value: string | undefined, icon = '', pill = false): string {
  if (!value) return '';
  if (pill) return '<span class="about-pill">' + esc(value) + '</span>';
  return '<span class="about-fact">' + ico(icon) + esc(value) + '</span>';
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
  // from a local build. Each is dropped rather than shown empty, and each
  // carries its own icon so the line reads without separators doing the work.
  const bits: string[] = [];
  if (d.branch) bits.push(fact(d.branch, 'branch'));
  if (d.commit) bits.push(fact(d.commit.slice(0, 7), 'commit'));
  if (d.built) bits.push(fact(d.built, 'date'));
  if (d.runtime) bits.push(fact('up ' + uptime(d.runtime.uptimeSec), 'clock'));
  const b = el('aboutBuild');
  if (b) b.innerHTML = bits.join('');
}

function renderSystem(d: AboutPayload): void {
  const rt = el('aboutRuntime');
  if (rt && d.runtime) {
    const r = d.runtime;
    rt.innerHTML = [
      fact(r.go, '', true),
      fact(r.platform, 'server'),
      fact(r.memoryMb ? r.memoryMb + ' MB' : '', 'memory'),
      fact(r.kernel, 'kernel'),
      // THE CONTAINER LINE. The badge is detected from inside the sandbox and
      // the name beside it is declared by the operator, so the badge can
      // appear alone: that is a container nobody named, not a missing value.
      r.container
        ? '<span class="about-line">'
          + '<span class="about-pill about-pill-docker">' + esc(r.container) + '</span>'
          + (r.image ? '<span class="about-fact about-dim">' + esc(r.image) + '</span>' : '')
          + '</span>'
        : '',
    ].filter(Boolean).join('');
  }
  const db = el('aboutDatabase');
  if (db && d.database) {
    db.innerHTML = [
      fact(d.database.engine, '', true),
      fact(d.database.schema ? 'Schema v' + d.database.schema : '', ''),
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
    + '<span class="about-dep-ver">' + esc(d.version || '-')
      // `patched` for a module go.mod replaces with a local copy: the version
      // is the upstream release this is a patch OF, and without the note the
      // row claims to ship stock v3.0.1, which it does not.
      + (d.note ? '<span class="about-dep-note">' + esc(d.note) + '</span>' : '')
      + '</span>'
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
