// The Packet Sniffer's four cards: Packets, Captured, Top protocol, Top talker.
//
// ── WHY THESE FOUR ──────────────────────────────────────────────────────────
//
// A capture's table answers "what went past"; the question it cannot answer at a
// glance is "how much, and who". The first two come from the router's protocol
// table, which totals the whole capture rather than the rows still in its memory
// buffer - so they keep counting after the oldest packets have been dropped, and
// they are the numbers the pcap export will contain. The second two are the
// router's own protocol and host tables, which is the same material Winbox draws
// on its Protocols and Hosts tabs, reduced to the one row of each that matters.
//
// TOP PROTOCOL IS AN IP PROTOCOL where the capture has any. "ip, 98% of bytes"
// is true of almost every capture; "tcp, 96.8%" says something. The fold does
// the choosing (internal/diag/sniffer.go) so the page and a future reader of the
// payload cannot disagree about it.

import { el, fmtBytes, protoPill } from '../dom';
import type { SnifferResult } from '../gen/payloads';

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
// `protoPill` builds and escapes. Nothing else here goes near innerHTML - the
// address on the Top talker card is text from the router and is set as text.
function setPill(id: string, html: string): void {
  const node = el(id);
  if (node) node.innerHTML = html;
}

/** Draws the cards for a capture so far; null clears them for the next one. */
export function renderSnifferCards(r: SnifferResult | null): void {
  if (!r) {
    for (const id of ['snifferPacketsVal', 'snifferBytesVal', 'snifferTalkerVal']) setText(id, '-');
    setPill('snifferProtoVal', '-');
    setText('snifferProtoFoot', '');
    setText('snifferTalkerFoot', '');
    return;
  }
  setText('snifferPacketsVal', r.totalPackets.toLocaleString());
  setText('snifferBytesVal', fmtBytes(r.totalBytes));
  // THE PROTOCOL PILL, not a second colour scheme: protoPill is what every
  // other protocol column in the app uses.
  setPill('snifferProtoVal', r.topProtocol ? protoPill(r.topProtocol) : '-');
  setText('snifferProtoFoot', r.topProtocol ? r.topProtocolShare + '% of bytes' : '');
  setAddr('snifferTalkerVal', r.topTalker || '-');
  setText('snifferTalkerFoot', r.topTalker ? fmtBytes(r.topTalkerBytes) + ' both ways' : '');
}
