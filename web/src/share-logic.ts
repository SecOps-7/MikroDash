// The share shell's decisions, kept pure so web/test can drive them without a
// DOM: which slide comes next, how long a slide shows, which theme applies,
// and what the transition costs. web/src/entry/share.ts does the drawing.

import { fmtDate } from './timefmt';

/** One slide as the server hands it to the shell (shareSlideView in Go). */
export interface ShareSlide {
  page: string;
  router: string;
  secs: number;
  shareable: boolean;
  layout?: unknown;
}

/** The link's options as the server hands them over (sharelink.Options). */
export interface ShareOptions {
  theme?: string;
  view?: string;
  defaultSecs?: number;
  transition?: string;
  speed?: string;
  atEnd?: string;
  pauseOnHover?: boolean;
  titleBar?: boolean;
  progress?: boolean;
  controls?: boolean;
  offline?: string;
  warmUpSecs?: number;
  card?: string;
}

/** The whole inlined config (shareConfig in Go). */
export interface ShareConfig {
  ticket: string;
  ticketExp: number;
  kind: 'page' | 'slideshow' | 'card' | 'feed';
  label: string;
  page: string;
  router: string;
  layout?: unknown;
  options: ShareOptions;
  slides: ShareSlide[];
  expiresAt: number | null;
  full: boolean;
  reads?: string[];
  cards?: string[];
}

/**
 * A shared Dashboard's layout: the link's own (or the default), with every
 * card the server will not feed hidden, so no empty card is drawn.
 */
export function sharedDashboardLayout<C extends { id: string; visible: boolean }>(
  defaults: readonly C[], saved: readonly C[] | null, allowed: readonly string[],
): C[] {
  const ok = new Set(allowed);
  return defaults.map((d) => {
    const s = saved?.find((x) => x.id === d.id);
    const c = s ? { ...d, ...s } : { ...d };
    return { ...c, visible: c.visible && ok.has(c.id) };
  });
}

export const DEFAULT_SLIDE_SECS = 30;

/** How long slide i shows: its own time, else the link's default, else 30s. */
export function slideSecs(s: ShareSlide, o: ShareOptions): number {
  return s.secs > 0 ? s.secs : (o.defaultSecs && o.defaultSecs > 0 ? o.defaultSecs : DEFAULT_SLIDE_SECS);
}

/**
 * The slide after `from`, skipping any in `skip` (unshareable, or offline
 * under Skip). -1 when there is none: every slide is skipped, or the link
 * stops at its end (`atEnd` "stop") and `from` is the last one to show.
 */
export function nextSlide(from: number, slides: readonly ShareSlide[], skip: ReadonlySet<number>, atEnd?: string): number {
  const n = slides.length;
  for (let step = 1; step <= n; step++) {
    const i = from + step;
    if (i >= n && atEnd === 'stop') return -1;
    const j = ((i % n) + n) % n;
    if (!skip.has(j)) return j;
  }
  return -1;
}

/** The slide before `from`, for the viewer's Previous control. */
export function prevSlide(from: number, slides: readonly ShareSlide[], skip: ReadonlySet<number>): number {
  const n = slides.length;
  for (let step = 1; step <= n; step++) {
    const j = (((from - step) % n) + n) % n;
    if (!skip.has(j)) return j;
  }
  return -1;
}

/** The theme to apply: the link's choice, or the browser's for "follow". */
export function themeFor(option: string | undefined, prefersDark: boolean): 'light' | 'dark' {
  if (option === 'light' || option === 'dark') return option;
  return prefersDark ? 'dark' : 'light';
}

/** Milliseconds a transition takes; 0 when none, or when motion is reduced. */
export function transitionMs(o: ShareOptions, reducedMotion: boolean): number {
  if (reducedMotion || !o.transition || o.transition === 'none') return 0;
  return o.speed === 'fast' ? 300 : o.speed === 'slow' ? 1000 : 600;
}

/** Whether an option that defaults ON is on. */
export function onByDefault(v: boolean | undefined): boolean {
  return v !== false;
}

/** ", until <date>", or "" for a link that never expires. */
export function expiryNote(expiresAt: number | null): string {
  if (!expiresAt) return '';
  return ', until ' + fmtDate(expiresAt);
}

/** When to renew a ticket: a minute before it lapses, and never sooner than 5s. */
export function renewInMs(ticketExp: number, now: number): number {
  return Math.max(5000, ticketExp - now - 60_000);
}
