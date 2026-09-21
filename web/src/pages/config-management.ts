// The Config Management page: its tabs, and the panels each one owns.
//
// Everything on this page is read on demand over REST (/api/config/…); there
// is no collector behind it, so nothing is fetched while it is not shown.

import { el } from '../dom';

const TABS = ['library', 'editor', 'deploy', 'history', 'drift'] as const;
type Tab = (typeof TABS)[number];

export function initConfigManagementPage(isVisible: (page: string) => boolean): void {
  let tab: Tab = 'library';

  function show(next: Tab): void {
    tab = next;
    document.querySelectorAll<HTMLElement>('#cfgTabs [data-cfgtab]').forEach((b) => {
      const on = b.getAttribute('data-cfgtab') === next;
      b.classList.toggle('active', on);
      b.setAttribute('aria-selected', String(on));
    });
    for (const t of TABS) {
      const p = el('cfgPanel-' + t);
      if (p) p.hidden = t !== next;
    }
  }

  el('cfgTabs')?.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement).closest('[data-cfgtab]');
    const t = b?.getAttribute('data-cfgtab') as Tab | null;
    if (t && TABS.includes(t)) show(t);
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'config-management') return;
    if (isVisible('config-management')) show(tab);
  });
}
