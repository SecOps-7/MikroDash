// Two of the Dashboard's extra cards, both fed by `wireless:update`:
// Signal Health (dc-card-signal) and Band Split (dc-card-band).
//
// ── A MISSING SIGNAL READS AS EXCELLENT ─────────────────────────────────────
//
// `parseInt(c.signal, 10) || 0` turns an absent, empty or unparseable signal
// into 0 — and 0 dBm is stronger than the -55 boundary, so such a client lands
// in the EXCELLENT bucket. It is not a plausible reading (0 dBm is a watt at the
// antenna) but it is what the live card counts, and a client mid-association can
// arrive without one.
//
// Reproduced rather than corrected. A port that treated it as "unknown" would
// show a different distribution than the app it replaces, on the routers where
// it happens.
//
// ── THE BAND MATCH IS EXACT, SO A NEW BAND COUNTS NOWHERE ───────────────────
//
// `'2.4GHz'`, `'5GHz'`, `'6GHz'` and nothing else. A client whose band is spelt
// any other way is counted in no bucket at all — the three numbers simply do not
// sum to the client count. Also live behaviour, and the reason the card shows
// counts rather than percentages.
//
// ── THE 6GHz ROW HIDES ITSELF ───────────────────────────────────────────────
//
// Zero 6GHz clients hides the row entirely, so a router with no 6GHz radio shows
// a two-row card rather than a row of zeros. It reappears the moment one
// associates — the row is hidden on the COUNT, not on whether the radio exists.

import { el } from '../dom';

export interface WirelessClient {
  signal?: string | number;
  band?: string;
}
export interface WirelessPayload {
  clients?: WirelessClient[];
}

export function renderWirelessCards(data: WirelessPayload): void {
  const clients = data.clients || [];

  // ── Signal Health ─────────────────────────────────────────────────────────
  let cntE = 0, cntG = 0, cntF = 0, cntP = 0;
  for (const c of clients) {
    // `parseInt(…, 10) || 0` — see the header. Note parseInt takes the LEADING
    // number, so "-58dBm" reads as -58 rather than failing.
    const s = parseInt(String(c.signal), 10) || 0;
    if (s >= -55) cntE++;
    else if (s >= -65) cntG++;
    else if (s >= -75) cntF++;
    else cntP++;
  }
  // `|| 1` guards the division below, not the display: with no clients the bars
  // are all 0% and the card is hidden anyway.
  const total = clients.length || 1;

  const noData = el('dc-sigNoData'), health = el('dc-wlSigHealth');
  if (noData) noData.style.display = clients.length ? 'none' : '';
  if (health) health.style.display = clients.length ? '' : 'none';

  const setSig = (barId: string, cntId: string, count: number): void => {
    const b = el(barId), cn = el(cntId);
    if (b) b.style.width = Math.round((count / total) * 100) + '%';
    if (cn) cn.textContent = String(count);
  };
  setSig('dc-wlSigBarE', 'dc-wlSigCntE', cntE);
  setSig('dc-wlSigBarG', 'dc-wlSigCntG', cntG);
  setSig('dc-wlSigBarF', 'dc-wlSigCntF', cntF);
  setSig('dc-wlSigBarP', 'dc-wlSigCntP', cntP);

  // ── Band Split ────────────────────────────────────────────────────────────
  let b24 = 0, b5 = 0, b6 = 0;
  for (const c of clients) {
    if (c.band === '2.4GHz') b24++;
    else if (c.band === '5GHz') b5++;
    else if (c.band === '6GHz') b6++;
  }
  const n24 = el('dc-wlBandNum24'), n5 = el('dc-wlBandNum5');
  const n6 = el('dc-wlBandNum6'), r6 = el('dc-wlBandRow6');
  if (n24) n24.textContent = String(b24);
  if (n5) n5.textContent = String(b5);
  if (n6) n6.textContent = String(b6);
  if (r6) r6.style.display = b6 > 0 ? '' : 'none';
}
