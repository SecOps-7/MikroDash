/**
 * The install's own name and icon (issue #131).
 *
 * An operator running several MikroDash instances in several tabs could not
 * tell them apart, so an administrator can rename the app and give it its own
 * icon (Settings, Appearance, Branding). This applies that wherever the browser
 * shows the app's name: the top-left wordmark and sidebar icon, the tab title
 * and icon, and the login page. PDF reports and report emails take it on the
 * server.
 *
 * ── THE NAME IS TEXT, NEVER MARKUP ──────────────────────────────────────────
 *
 * It is set as a text node. The default wordmark's accented "Dash" is a span
 * this code creates, not a string it parses, so a name such as
 * `<img src=x onerror=…>` shows as those characters.
 *
 * ── AND THE ICON IS ONE OF TWO URLS ─────────────────────────────────────────
 *
 * The server answers either the default `/logo.png` or the stored icon with its
 * version. Anything else in the response is ignored rather than put in an `src`.
 */

import { FONTS } from './gen/appearance-tables.js';

export interface Branding {
  /** The stored name; empty means the default. */
  name: string;
  /** What to show: the name, or MikroDash. */
  displayName: string;
  /** An appearance font id; empty means the wordmark's own font. */
  font: string;
  /** The icon URL to load. */
  icon: string;
  customIcon: boolean;
}

export const DEFAULT_NAME = 'MikroDash';
const DEFAULT_ICON = '/logo.png';

export const DEFAULT_BRANDING: Branding = {
  name: '', displayName: DEFAULT_NAME, font: '', icon: DEFAULT_ICON, customIcon: false,
};

const ICON_URL = /^\/(?:logo\.png|brand\/icon\.png\?v=\d+)$/;

/** A server answer as a Branding, with anything unexpected replaced by the default. */
export function normaliseBranding(raw: unknown): Branding {
  if (!raw || typeof raw !== 'object') return { ...DEFAULT_BRANDING };
  const r = raw as Record<string, unknown>;
  const name = typeof r.name === 'string' ? r.name : '';
  const font = typeof r.font === 'string' ? r.font : '';
  const icon = typeof r.icon === 'string' && ICON_URL.test(r.icon) ? r.icon : DEFAULT_ICON;
  return { name, displayName: name || DEFAULT_NAME, font, icon, customIcon: icon !== DEFAULT_ICON };
}

/** The CSS font family for a font id, or '' for an empty or unknown id. */
export function fontFamily(id: string): string {
  if (!id) return '';
  const f = FONTS.find((x) => x.id === id);
  return f ? f.family : '';
}

/** Draw the wordmark into an element: the name as text, or Mikro + accented Dash. */
export function renderWordmark(target: HTMLElement, name: string, font: string): void {
  target.textContent = '';
  if (name) {
    target.appendChild(document.createTextNode(name));
  } else {
    target.appendChild(document.createTextNode('Mikro'));
    const accent = document.createElement('span');
    accent.textContent = 'Dash';
    target.appendChild(accent);
  }
  const family = fontFamily(font);
  if (family) target.style.setProperty('font-family', family);
  else target.style.removeProperty('font-family');
}

/**
 * Apply branding to whatever of the app or the login page is on screen.
 * `titleSuffix` is appended to the tab title, e.g. " - Sign In".
 */
export function applyBranding(b: Branding, titleSuffix = ''): void {
  const top = document.getElementById('topbarLogo');
  if (top) renderWordmark(top, b.name, b.font);

  const loginName = document.querySelector<HTMLElement>('.login-brand-name');
  if (loginName) {
    loginName.textContent = b.displayName;
    const family = fontFamily(b.font);
    if (family) loginName.style.setProperty('font-family', family);
    else loginName.style.removeProperty('font-family');
  }

  document.querySelectorAll<HTMLImageElement>('.nav-logo img, .login-brand img').forEach((img) => {
    img.src = b.icon;
    img.alt = b.displayName;
  });

  const favicon = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
  if (favicon) favicon.href = b.icon;

  document.title = b.displayName + titleSuffix;
}

/**
 * Fetch the branding and apply it. A failed fetch leaves the page as its markup
 * drew it, which is the default app.
 */
export function loadBranding(titleSuffix = ''): Promise<Branding> {
  return fetch('/api/branding', { credentials: 'same-origin' })
    .then((r) => (r.ok ? r.json() : null))
    .then((j: unknown) => {
      const b = normaliseBranding(j);
      applyBranding(b, titleSuffix);
      return b;
    })
    .catch(() => ({ ...DEFAULT_BRANDING }));
}
