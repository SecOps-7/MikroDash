// The Dashboard's WAN Flow card (dc-card-wanflow): the WAN page's live Sankey,
// scaled to the card.
//
// The engine is `wan-flow.ts`'s, the same one the WAN page draws with, in its
// "box" fit: laid out at this card's aspect ratio and scaled into it, so the
// diagram grows and shrinks with the card's edges and a resize never crops it.
// The data is `wan:update`, which reaches this socket through the card's room
// (`dash-card-wan`) while the card is on the Dashboard.

import { el } from '../dom';
import { t } from '../i18n';
import type { Socket } from '../socket';
import { createWanFlow } from './wan-flow';

/** On screen: the Dashboard is open and this card is placed on it. A card
 *  waiting in the Add panel is not displayed, so it has no layout box. */
function onScreen(): boolean {
  return !!el('page-dashboard')?.classList.contains('active') && !!el('dc-card-wanflow')?.offsetParent;
}

export function initWanFlowCard(socket: Socket): void {
  const card = createWanFlow(socket, {
    id: 'dcWanFlow', fit: 'box', visible: onScreen,
    cardId: 'dcWanFlowCard', wrapId: 'dcWanFlowWrap', svgId: 'dcWanFlowSvg', emptyId: 'dcWanFlowEmpty',
    noUplinks: t('No uplinks to draw. The WAN page says why.'),
  });
  socket.on('wan:update', (d) => { if (d) card.note(d); });
  socket.on('router:switched', () => card.clear());
  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'dashboard') card.redraw();
  });
}
