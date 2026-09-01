// Palette, contrast, brightness, font and font size.
//
// SHELL-LEVEL, not a Settings page concern, even though every control lives on
// the Appearance tab. Two blocks run at load on every page so the palette is
// applied before the first paint; only the third — the one that wires the
// controls — has anything to do with Settings.
//
// ── IT IS ENTIRELY PER-BROWSER ──────────────────────────────────────────────
//
// Nothing here reaches the server. There is no payload, no settings key and no
// audit row: the whole layer is `localStorage` plus attributes on
// `<html>`, and the stylesheet does the rest through custom properties. That is
// why this is a lift-and-run port rather than a collector port — there is no
// wire format to agree on, only a DOM to leave in exactly the same state.
//
// ── ABSENT IS NOT ZERO, AGAIN ───────────────────────────────────────────────
//
// At the neutral notch the layer REMOVES `--text-main` and friends instead of
// computing the base colour and setting it. Those are not equivalent: removing
// hands the question back to the stylesheet, which may answer differently per
// palette, per media query, or per rule that has not been written yet. Setting
// the value freezes today's answer into an inline style that outranks all of
// them. The original removes; so does this.
//
// The `|| APPEAR_DEFAULT` after each `parseInt` carries a quirk worth naming:
// `parseInt('0')` is 0, which is FALSY, so a stored level of 0 reads back as the
// default rather than as the bottom of the range. Since the sliders start at 1
// that state is unreachable through the UI, but a hand-edited localStorage
// entry lands there — and reproducing it costs nothing while diverging would
// make the port disagree with the live app on a value someone can actually set.

import { el } from './dom.js';
import {
  APPEAR_DEFAULT, KEYS, FONTS, FONT_SIZES,
  CONTRAST_FACTORS, TEXT_BRIGHT_FACTORS, BG_BRIGHT_FACTORS, PALETTE_COLORS,
  type RGBA,
} from './gen/appearance-tables.js';

const root = (): HTMLElement => document.documentElement;

/** Every read and write is guarded: Safari's private mode throws on both. */
function lsGet(key: string): string | null {
  try { return localStorage.getItem(key); } catch { return null; }
}
function lsSet(key: string, value: string): void {
  try { localStorage.setItem(key, value); } catch { /* nothing to do about it */ }
}

/** A level as the attribute holds it, defaulted and clamped into a factor array. */
function level(attr: string): number {
  return Number.parseInt(root().getAttribute(attr) || String(APPEAR_DEFAULT), 10) || APPEAR_DEFAULT;
}
function factor(table: number[], lvl: number): number {
  // The `!` is safe by construction, not by hope: the index is clamped into
  // [0, len-1] and the appearance table generator refuses to emit a factor array
  // shorter than the slider that indexes it.
  return table[Math.max(0, Math.min(table.length - 1, lvl - 1))]!;
}

/**
 * Scale a colour toward white (factor > 1) or toward black (factor <= 1).
 *
 * Brightening interpolates toward 255 rather than multiplying, so a channel
 * already at 255 stays there instead of overflowing, and a channel at 0 can
 * still lift — multiplying would leave black black at every setting.
 *
 * ALPHA IS CARRIED THROUGH UNTOUCHED. Text alpha is adjusted afterwards by the
 * contrast factor; background alpha is not adjusted at all.
 */
export function scaleBright(c: RGBA, f: number): RGBA {
  let r: number, g: number, b: number;
  if (f > 1) {
    const t = Math.min(1, f - 1);
    r = Math.round(c[0] + (255 - c[0]) * t);
    g = Math.round(c[1] + (255 - c[1]) * t);
    b = Math.round(c[2] + (255 - c[2]) * t);
  } else {
    r = Math.round(c[0] * f);
    g = Math.round(c[1] * f);
    b = Math.round(c[2] * f);
  }
  return [Math.min(255, r), Math.min(255, g), Math.min(255, b), c[3]];
}

function base(): typeof PALETTE_COLORS[string] {
  const palette = root().getAttribute('data-palette') || 'default';
  const scheme = root().getAttribute('data-theme') || 'dark';
  // `default:dark` is the fallback the original uses, and the generator pins
  // every swatch to an entry — so the miss path is reachable only from a
  // hand-set attribute, and still lands somewhere real.
  return PALETTE_COLORS[palette + ':' + scheme] || PALETTE_COLORS['default:dark']!;
}

/**
 * Text colour: brightness moves the channels, contrast moves the ALPHA.
 *
 * Contrast against a background is what alpha controls here — the text sits on
 * the card colour, so thinning it lowers contrast and thickening raises it.
 * `Math.min(1, …)` is what stops a high setting producing an invalid alpha.
 */
export function reapplyTextVars(): void {
  const contrastLvl = level('data-contrast');
  const brightLvl = level('data-text-bright');
  const r = root();
  if (contrastLvl === APPEAR_DEFAULT && brightLvl === APPEAR_DEFAULT) {
    r.style.removeProperty('--text-main');
    r.style.removeProperty('--text-muted');
    return;
  }
  const b = base();
  const cf = factor(CONTRAST_FACTORS, contrastLvl);
  const bf = factor(TEXT_BRIGHT_FACTORS, brightLvl);
  const compute = (c: RGBA): string => {
    const bc = scaleBright(c, bf);
    const a = Math.min(1, +(bc[3] * cf).toFixed(3));
    return 'rgba(' + bc[0] + ',' + bc[1] + ',' + bc[2] + ',' + a + ')';
  };
  r.style.setProperty('--text-main', compute(b.main));
  r.style.setProperty('--text-muted', compute(b.muted));
}

/** Background: one slider, and the alpha is left exactly as the palette set it. */
export function reapplyBgVars(): void {
  const lvl = level('data-bg-bright');
  const r = root();
  if (lvl === APPEAR_DEFAULT) {
    r.style.removeProperty('--bg-deep');
    r.style.removeProperty('--bg-card');
    return;
  }
  const b = base();
  const bf = factor(BG_BRIGHT_FACTORS, lvl);
  const scale = (c: RGBA): string => {
    const bc = scaleBright(c, bf);
    return 'rgba(' + bc[0] + ',' + bc[1] + ',' + bc[2] + ',' + bc[3] + ')';
  };
  r.style.setProperty('--bg-deep', scale(b.bgDeep));
  r.style.setProperty('--bg-card', scale(b.bgCard));
}

/** The active swatch is the one matching BOTH palette and mode — the same
 *  palette in light and dark are two swatches, and only one is current. */
export function syncSwatches(): void {
  const palette = root().getAttribute('data-palette') || 'default';
  const scheme = root().getAttribute('data-theme') || 'dark';
  document.querySelectorAll<HTMLElement>('.theme-swatch').forEach((sw) => {
    sw.classList.toggle('active',
      sw.dataset.palette === palette && sw.dataset.mode === scheme);
  });
}

const MOON = 'M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z';
const SUN = 'M12 3v1m0 16v1m9-9h-1M4 12H3m15.364-6.364l-.707.707M6.343 17.657l-.707.707' +
  'M17.657 17.657l-.707-.707M6.343 6.343l-.707-.707M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z';

/**
 * The default palette REMOVES `data-palette` rather than setting it to
 * "default": the stylesheet's base rules are the default palette, and an
 * attribute selector for it would have to be written and kept in step. What is
 * STORED is still the string "default", so the two are deliberately different.
 *
 * `data-bs-theme` is set alongside `data-theme` because Bootstrap reads its own
 * attribute and knows nothing about this one.
 */
export function applyPalette(palette: string, scheme?: string): void {
  const s = scheme || root().getAttribute('data-theme') || 'dark';
  if (!palette || palette === 'default') {
    root().removeAttribute('data-palette');
  } else {
    root().setAttribute('data-palette', palette);
  }
  root().setAttribute('data-theme', s);
  root().setAttribute('data-bs-theme', s === 'light' ? 'light' : 'dark');
  lsSet(KEYS.palette, palette || 'default');
  lsSet(KEYS.theme, s);
  const p = el('themeIconPath');
  if (p) p.setAttribute('d', s === 'light' ? SUN : MOON);
  reapplyTextVars();
  reapplyBgVars();
  syncSwatches();
}

/**
 * Light or dark, independent of which palette is chosen.
 *
 * Note what it does NOT do: it never calls `syncSwatches`, though `applyPalette`
 * does and the active swatch depends on the scheme just as much as on the
 * palette. So toggling the theme leaves the Appearance tab highlighting the
 * swatch for the scheme you just left, until something else re-syncs it —
 * opening Settings does, via the pagechange handler.
 *
 * That is the live behaviour, reproduced rather than corrected. It is a visible
 * quirk on one tab; "fixing" it here would make the port disagree with the app
 * it is replacing, and the rule is that the user-visible line does not move.
 */
export function applyTheme(t: string): void {
  root().setAttribute('data-theme', t);
  root().setAttribute('data-bs-theme', t === 'light' ? 'light' : 'dark');
  const p = el('themeIconPath');
  if (p) p.setAttribute('d', t === 'light' ? SUN : MOON);
  lsSet(KEYS.theme, t);
  reapplyTextVars();
  reapplyBgVars();
}

/** An unknown id falls back to Syne — FONTS[1], by position, as the original. */
export function applyFont(fontId: string): void {
  const font = FONTS.find((f) => f.id === fontId) || FONTS[1]!;
  root().style.setProperty('--font-ui', font.family);
  lsSet(KEYS.font, font.id);
}

/** `px: null` REMOVES the property. Writing 16px instead would override a
 *  stylesheet or user setting that had picked something else. */
export function applyFontSize(sizeId: string): void {
  const size = FONT_SIZES.find((f) => f.id === sizeId) || FONT_SIZES[2]!;
  if (size.px === null) {
    root().style.removeProperty('font-size');
  } else {
    root().style.fontSize = size.px + 'px';
  }
  lsSet(KEYS.fontSize, size.id);
}

/**
 * The boot half, run on every page before the first paint.
 *
 * Note what it does NOT do: it never calls `applyPalette`, and never sets
 * `data-theme`. The theme attribute is preflight's, written before this script
 * parses; going through `applyPalette` here would rewrite localStorage from
 * values just read out of it and fire the icon and swatch updates against a
 * document that has neither yet.
 */
export function initAppearance(): void {
  // THE THEME FIRST, because that is the order the live file runs them in and
  // the order is load-bearing: `applyTheme` recomputes the text and background
  // variables, and at this point the contrast and brightness attributes have not
  // been written yet — so it computes them at the defaults, which means removing
  // them. The palette block below then sets the attributes and recomputes for
  // real. Running the two the other way round would leave the first computation
  // standing and the sliders ignored until something else repainted.
  applyTheme(lsGet(KEYS.theme) || 'dark');

  applyFont(lsGet(KEYS.font) || 'syne');
  applyFontSize(lsGet(KEYS.fontSize) || 'normal');

  const num = (key: string): number =>
    Number.parseInt(lsGet(key) || String(APPEAR_DEFAULT), 10) || APPEAR_DEFAULT;
  const palette = lsGet(KEYS.palette) || 'default';
  if (palette && palette !== 'default') root().setAttribute('data-palette', palette);
  root().setAttribute('data-contrast', String(num(KEYS.contrast)));
  root().setAttribute('data-text-bright', String(num(KEYS.textBright)));
  root().setAttribute('data-bg-bright', String(num(KEYS.bgBright)));
  reapplyTextVars();
  reapplyBgVars();
}

/**
 * The controls, wired once.
 *
 * Sliders listen for `input`, not `change`: the point is that the page repaints
 * while the handle is moving, so what you see is what that notch does. `change`
 * would fire once on release and turn a continuous adjustment into guesswork.
 *
 * The pagechange handler pushes the DOM's state back INTO the controls. The
 * attributes are the source of truth — they were set from localStorage at boot,
 * possibly in another tab — so a form that kept its own copy would show a stale
 * position the first time Settings is opened.
 */
export function wireAppearance(): void {
  document.querySelectorAll<HTMLElement>('.theme-swatch').forEach((sw) => {
    sw.addEventListener('click', () => {
      applyPalette(sw.dataset.palette || 'default', sw.dataset.mode || 'dark');
    });
  });

  // Attribute, storage key and which half to recompute, carried together. The
  // contrast and text-brightness sliders share `reapplyTextVars` because they
  // are two inputs to one colour; the background slider recomputes only its own.
  const sliders: [string, string, string, () => void][] = [
    ['appearanceContrast', 'data-contrast', KEYS.contrast, reapplyTextVars],
    ['appearanceTextBright', 'data-text-bright', KEYS.textBright, reapplyTextVars],
    ['appearanceBgBright', 'data-bg-bright', KEYS.bgBright, reapplyBgVars],
  ];
  for (const [id, attr, storeKey, reapply] of sliders) {
    const input = el<HTMLInputElement>(id);
    if (!input) continue;
    input.addEventListener('input', () => {
      root().setAttribute(attr, input.value);
      lsSet(storeKey, input.value);
      reapply();
    });
  }

  // The header's sun/moon button. It reads the CURRENT attribute rather than a
  // remembered value, so a theme changed from anywhere else — the swatches, or
  // another tab writing localStorage — still toggles from what is on screen.
  el('themeToggle')?.addEventListener('click', () => {
    const cur = root().getAttribute('data-theme') || 'dark';
    applyTheme(cur === 'light' ? 'dark' : 'light');
  });

  const fontSel = el<HTMLSelectElement>('appearanceFont');
  const sizeSel = el<HTMLSelectElement>('appearanceFontSize');
  if (fontSel) fontSel.addEventListener('change', () => applyFont(fontSel.value));
  if (sizeSel) sizeSel.addEventListener('change', () => applyFontSize(sizeSel.value));

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'settings') return;
    syncAppearanceControls();
  });
}

/** Split out of the pagechange handler so the gate can drive it directly. */
export function syncAppearanceControls(): void {
  syncSwatches();
  const put = (id: string, attr: string): void => {
    const input = el<HTMLInputElement>(id);
    if (input) input.value = root().getAttribute(attr) || String(APPEAR_DEFAULT);
  };
  put('appearanceContrast', 'data-contrast');
  put('appearanceTextBright', 'data-text-bright');
  put('appearanceBgBright', 'data-bg-bright');
  const fontSel = el<HTMLSelectElement>('appearanceFont');
  if (fontSel) fontSel.value = lsGet(KEYS.font) || 'syne';
  const sizeSel = el<HTMLSelectElement>('appearanceFontSize');
  if (sizeSel) sizeSel.value = lsGet(KEYS.fontSize) || 'normal';
}
