// Interface translation in the browser (#94). The markup is translated when
// the frontend is built (`internal/i18n`, `cmd/webbuild`); this is the half for
// text the code builds at run time.
//
// ── ONLY WHAT PASSES THROUGH t() IS TRANSLATED ──────────────────────────────
//
// Router data (names, comments, hostnames, SSIDs) is never passed to t(), so it
// is never translated: the boundary holds by construction rather than by a
// pattern hoping not to match. A t() whose first argument is not a literal
// string fails the build's checks, because that is the one way router data
// could reach a catalog.
//
// ── THE CATALOG IS IN THE PAGE ──────────────────────────────────────────────
//
// A translated page carries its catalog as <script type="application/json"
// id="i18n-catalog">, written at build time, so t() needs no request and cannot
// race the first render. An English page carries only the language list (when
// there is more than one language), and t() returns its input. A string the
// catalog lacks stays English, so a catalog that falls behind degrades rather
// than breaks.

interface Embedded {
  lang: string;
  strings: Record<string, string>;
  languages: { code: string; name: string }[];
}

let loaded: Embedded | null = null;

function data(): Embedded {
  if (loaded) return loaded;
  loaded = { lang: 'en', strings: {}, languages: [] };
  try {
    const e = typeof document !== 'undefined' ? document.getElementById('i18n-catalog') : null;
    if (e && e.textContent) loaded = { ...loaded, ...(JSON.parse(e.textContent) as Partial<Embedded>) };
  } catch { /* an unreadable catalog is English */ }
  return loaded;
}

/**
 * The text to show for `src`: its translation, or `src` itself. `{name}` in
 * the result is replaced by `vars.name`, AFTER translation, so a translator
 * can move a value to wherever the language puts it:
 *
 *   t('{n} devices are waiting', { n: 3 })
 */
export function t(src: string, vars?: Record<string, string | number>): string {
  const s = data().strings[src] || src;
  if (!vars) return s;
  return s.replace(/\{([a-zA-Z][a-zA-Z0-9]*)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m));
}

let regions: Intl.DisplayNames | null | undefined;

/**
 * A country's name for a two-letter code: the English table's entry on an
 * English page, the browser's own name for it in any other language (the
 * `Intl` region names every browser ships), so two hundred country names need
 * no catalog entries. An unknown code falls back to the English entry, then to
 * the code itself.
 */
export function countryName(cc: string, english: string | undefined): string {
  if (data().lang !== 'en') {
    if (regions === undefined) {
      try { regions = new Intl.DisplayNames([data().lang], { type: 'region' }); } catch { regions = null; }
    }
    try {
      const n = regions && /^[A-Z]{2}$/.test(cc) ? regions.of(cc) : undefined;
      if (n && n !== cc) return n;
    } catch { /* an unknown code is not an error */ }
  }
  return english || cc;
}

/** The language being shown: 'en' unless a translated page is loaded. */
export function currentLang(): string {
  return data().lang;
}

/** The languages this build offers, English first, for the selectors. Empty
 *  when there is no translation at all, and the selectors then stay hidden. */
export function languages(): { code: string; name: string }[] {
  return data().languages;
}

/** Choose a language: remembered in a cookie the server reads to pick the
 *  page, for a year, then the page reloads in it. 'en' is a choice too, and
 *  beats the browser's own preference. */
export function setLang(code: string): void {
  if (!/^[a-z]{2,3}(-[A-Z][A-Za-z]{1,3})?$/.test(code)) return;
  document.cookie = 'md_lang=' + code + '; path=/; max-age=31536000; SameSite=Lax';
  location.reload();
}

/**
 * Wire a language selector: fill it from the languages this build has, show
 * its wrapper only when there is a choice to make, and switch on change. The
 * sign-in page and Settings, Appearance both use it.
 */
export function bindLanguageSelect(select: HTMLSelectElement | null, wrap: HTMLElement | null): void {
  const langs = languages();
  if (!select || !wrap || langs.length < 2) return;
  select.innerHTML = '';
  for (const l of langs) {
    const o = document.createElement('option');
    o.value = l.code;
    o.textContent = l.name;
    select.appendChild(o);
  }
  select.value = currentLang();
  wrap.hidden = false;
  select.addEventListener('change', () => setLang(select.value));
}

/** For tests: forget the loaded catalog so the next t() reads the page again. */
export function resetI18n(): void {
  loaded = null;
  regions = undefined;
}
