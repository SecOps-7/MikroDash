// THE THREE PRESETS - Home, Standard, Advanced - ONCE.
//
// Settings → Visible Pages applies and detects them, and a role's page matrix
// applies them.
//
// ── WHAT EACH ONE IS FOR (the operator, 2026-11-23) ─────────────────────────
//
//   Home       a home router, for somebody who is not a network engineer: their
//              Wi-Fi, who is connected, how much is used and by what, whether the
//              internet is up, and updates.
//   Standard   the middle: what a technical user looks at, with the diagnostic
//              tools, but not the pages that only matter to a network engineer
//              or that run anything typed (Terminal, the AI Agent).
//   Advanced   every page.
//
// ── WHERE A PAGE'S TIER COMES FROM ───────────────────────────────────────────
//
// - a hand-built page: PAGE_TIERS below, one table for all of them. The two
//   frozen lists read out of the Node app (VIEW_PRESETS) were replaced by it;
// - a generated page: its `tier`, declared beside it in internal/areas.
//
// A page in neither is Advanced, so a page added to the nav joins Advanced by
// existing. A tier includes the tiers below it: Standard is Home and more.

import { PAGE_NAV_MAP } from './gen/view-presets';
import { AREAS } from './gen/areas';
import { ALL_NAV_PAGES } from './gen/page-keys';

export type Tier = 'home' | 'standard' | 'advanced';

const RANK: Record<Tier, number> = { home: 0, standard: 1, advanced: 2 };

/** Every hand-built page below Advanced. Dashboard and Settings are always shown. */
export const PAGE_TIERS: Readonly<Record<string, Tier>> = {
  'wifi-networks': 'home',
  'wifi-clients': 'home',
  'wifi-map': 'home',
  dhcp: 'home',
  bandwidth: 'home',
  apps: 'home',
  wan: 'home',
  packages: 'home',

  interfaces: 'standard',
  connections: 'standard',
  'network-topology': 'standard',
  devices: 'standard',
  dns: 'standard',
  vlans: 'standard',
  vpn: 'standard',
  firewall: 'standard',
  logs: 'standard',
  netwatch: 'standard',
  'security-scan': 'standard',
  queues: 'standard',
  backups: 'standard',
  reports: 'standard',
  // The four diagnostics probe: they send packets or watch a counter. The
  // Packet Sniffer captures the traffic itself, so it is Advanced, with the
  // Terminal, which runs whatever is typed, and the AI Agent.
  'tools-ping': 'standard',
  'tools-traceroute': 'standard',
  'tools-torch': 'standard',
  'tools-btest': 'standard',
};

/** The pages a preset never turns off, whatever the tier. */
const ALWAYS = new Set(['dashboard', 'settings']);

function tierOf(page: string): Tier {
  const area = AREAS.find((a) => a.key === page);
  if (area) return area.tier;
  return PAGE_TIERS[page] || 'advanced';
}

/** Every page a preset decides: the hand-built nav pages and the generated ones. */
function allPages(): string[] {
  const set = new Set<string>([...ALL_NAV_PAGES, ...Object.values(PAGE_NAV_MAP), ...AREAS.map((a) => a.key)]);
  return [...set].filter((k) => !ALWAYS.has(k));
}

/** The page keys each preset turns on. */
export function presetTiers(): Record<Tier, string[]> {
  const pages = allPages();
  const upTo = (t: Tier): string[] => pages.filter((k) => RANK[tierOf(k)] <= RANK[t]);
  return { home: upTo('home'), standard: upTo('standard'), advanced: upTo('advanced') };
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
