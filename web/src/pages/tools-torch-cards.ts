// The Torch page's five cards: Rx, Tx, Top protocol, Flows, Top talker.
//
// ── WHY THESE FIVE ──────────────────────────────────────────────────────────
//
// The table below answers "which conversations", one row each, busiest first.
// What it cannot answer at a glance is how much the interface is carrying in
// total, what that traffic mostly IS, and how many conversations there are -
// because the table shows at most TorchMaxFlows of them and a busy link has
// hundreds.
//
// So every card here is an aggregate over EVERY flow, including the quieter
// ones cut from the table. On a busy interface the Rx and Tx cards will
// therefore not match adding the visible rows up, and that is the point: they
// describe the link, not the page. The Flows card says so in its own footnote
// by naming how many are on screen.
//
// ── THE CHOOSING HAPPENS IN GO ──────────────────────────────────────────────
//
// Top protocol and Top talker are folded in internal/diag/torch.go, like the
// sniffer's, so this module and any later reader of the payload cannot disagree
// about which protocol was busiest. Nothing here picks a winner; it formats one.
//
// ── TOP TALKER IS HONEST ABOUT BEING DULL SOMETIMES ─────────────────────────
//
// Torch's rx and tx are relative to the interface, so an address is counted at
// either end of a flow. On a LAN or bridge that makes this the most useful card
// here - it names the host using the link. On a WAN or any point-to-point
// interface the router itself is one end of every flow and always wins, and the
// card then says something true and worthless. Measured on a CHR's ether1,
// where every flow ran to 10.0.2.15. It is drawn anyway rather than suppressed:
// hiding it would need this module to decide which address is "the router",
// which it cannot know.

import { el, fmtBps, protoPill } from '../dom';
import type { TorchResult } from '../gen/payloads';

function setText(id: string, text: string): void {
  const node = el(id);
  if (node) node.textContent = text;
}

// An address card is ellipsised by .tool-card-text, and an IPv6 address is
// exactly the case where the hidden half is the half that identifies the host.
// The full value goes in the title so hovering still answers the question.
function setAddr(id: string, text: string): void {
  const node = el(id);
  if (!node) return;
  node.textContent = text || '-';
  if (typeof node.setAttribute === 'function') node.setAttribute('title', text || '');
}

// The one card whose value is markup rather than text: the protocol pill, which
// `protoPill` builds and escapes. The talker address is text from the router and
// is set as text.
function setPill(id: string, html: string): void {
  const node = el(id);
  if (node) node.innerHTML = html;
}

const CARD_TEXT = ['torchRxVal', 'torchTxVal', 'torchFlowsVal', 'torchTalkerVal'];
const CARD_FOOT = ['torchRxFoot', 'torchTxFoot', 'torchProtoFoot', 'torchFlowsFoot', 'torchTalkerFoot'];

/** Draws the cards for a run; null clears them for the next one. */
export function renderTorchCards(r: TorchResult | null): void {
  if (!r) {
    for (const id of CARD_TEXT) setText(id, '-');
    setPill('torchProtoVal', '-');
    for (const id of CARD_FOOT) setText(id, '');
    return;
  }
  setText('torchRxVal', fmtBps(r.totalRxBps));
  setText('torchTxVal', fmtBps(r.totalTxBps));
  // WHAT THE RATE IS AN AVERAGE OF, which differs between the two kinds of run:
  // a bounded one averages over the whole watch, a continuous one over its last
  // few seconds. A bare rate with no window is two different numbers wearing one
  // label.
  const over = r.continuous ? 'last ' + r.reports + ' s' : 'over ' + r.seconds + ' s';
  setText('torchRxFoot', over);
  setText('torchTxFoot', over);

  setPill('torchProtoVal', r.topProtocol ? protoPill(r.topProtocol) : '-');
  setText('torchProtoFoot', r.topProtocol ? r.topProtocolShare + '% of the rate' : '');

  // FLOWS COUNTS EVERY CONVERSATION, the cut ones included, because that is the
  // number that says whether this is one download or a thousand sessions. The
  // count pill beside the page title counts the rows on screen, which is a
  // different question and is labelled as one.
  const total = r.flows.length + r.omitted;
  setText('torchFlowsVal', total.toLocaleString());
  setText('torchFlowsFoot', r.omitted ? r.flows.length + ' shown' : '');

  setAddr('torchTalkerVal', r.topTalker || '-');
  setText('torchTalkerFoot', r.topTalker ? fmtBps(r.topTalkerBps) + ' both ways' : '');
}
