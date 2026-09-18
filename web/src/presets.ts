// THE THREE PRESETS — Home, Standard, Advanced — ONCE.
//
// Settings → Visible Pages applies and detects them, and a role's page matrix
// applies them. They were built in three places from the frozen VIEW_PRESETS and
// PAGE_NAV_MAP, which name only the hand-built pages that have a Visible Pages
// toggle. So every generated page, Tools and the AI Agent were in no preset:
// Advanced left them untouched on Visible Pages, and a role given Advanced got
// no access to any of them (found 2026-09-18).
//
// ── WHERE A PAGE'S TIER COMES FROM ───────────────────────────────────────────
//
// - a hand-built page with a toggle: the frozen VIEW_PRESETS, and Advanced for
//   the rest of PAGE_NAV_MAP, as before;
// - a generated page: its `tier`, declared beside it in internal/areas;
// - a hand-built page with NO toggle: HAND_TIERS below. They matter to roles,
//   which grant every page, and not to Visible Pages, which has no box for them.
//
// A tier includes the tiers below it: Standard is Home and more.

import { PAGE_NAV_MAP, VIEW_PRESETS } from './gen/view-presets';
import { AREAS } from './gen/areas';

export type Tier = 'home' | 'standard' | 'advanced';

const RANK: Record<Tier, number> = { home: 0, standard: 1, advanced: 2 };

/** Hand-built pages with no Visible Pages toggle, placed by the operator. */
const HAND_TIERS: Record<string, Tier> = {
  tools: 'standard',
  'ai-agent': 'advanced',
};

function upTo(tier: Tier): string[] {
  return [
    ...AREAS.filter((a) => RANK[a.tier] <= RANK[tier]).map((a) => a.key),
    ...Object.keys(HAND_TIERS).filter((k) => RANK[HAND_TIERS[k]!] <= RANK[tier]),
  ];
}

/** The page keys each preset turns on. */
export function presetTiers(): Record<Tier, string[]> {
  return {
    home: [...(VIEW_PRESETS.home as string[]), ...upTo('home')],
    standard: [...(VIEW_PRESETS.standard as string[]), ...upTo('standard')],
    advanced: [...Object.keys(PAGE_NAV_MAP).map((k) => PAGE_NAV_MAP[k] as string), ...upTo('advanced')],
  };
}

/**
 * Every toggle on the Visible Pages card a preset sets, as page key and box:
 * the hand-built pages' `s_<settingsKey>` and the generated pages'
 * `data-area-toggle`. A toggle not rendered is left out, and a preset neither
 * sets it nor counts it, which is how the card has always treated a missing one.
 */
export function presetToggles(): { page: string; box: HTMLInputElement }[] {
  const out: { page: string; box: HTMLInputElement }[] = [];
  for (const sKey of Object.keys(PAGE_NAV_MAP)) {
    const box = document.getElementById('s_' + sKey) as HTMLInputElement | null;
    if (box) out.push({ page: PAGE_NAV_MAP[sKey] as string, box });
  }
  document.querySelectorAll<HTMLInputElement>('input[data-area-toggle]').forEach((box) => {
    out.push({ page: box.getAttribute('data-area-toggle') || '', box });
  });
  return out;
}
