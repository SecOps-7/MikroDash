// The RJ-45 port drawing, shared by the Interfaces page's Ports panel and the
// Dashboard's Physical Ports card.
//
// In the live app this is ONE file-scope function called from both places
// (`public/app.js:1695`), and the dashboard card's comment says so explicitly:
// "portSvg is a file-scope function — safe to call directly". Duplicating it
// here would let the two drawings drift apart, which is the same species of
// mistake the live repo had just finished fixing in the card next to it — the
// card was a COPY of the Interfaces markup and the copy is what drifted.
//
// The two callers are NOT identical, and the differences are deliberate rather
// than drift: the card admits sfp and sfp-sfpplus where the panel takes ether
// only, and the card's LABEL uses dcEsc where the panel uses esc. Both escapers
// are correct in text position; they differ on quotes, so the two produce
// different innerHTML for the same interface. The port reproduces each side as
// it stands rather than unifying them.

export function portSvg(sz: number): string {
  // Ethernet port — RJ-45 front view. Outer housing, inner socket recess, 8
  // contact pins across the bottom of the socket, one LED dot top-right.
  const w = sz, h = Math.round(sz * 1.1);
  const rx = Math.max(2, Math.round(sz * 0.09));
  const sox = Math.round(w * 0.15);
  const sow = w - sox * 2;
  const soy = Math.round(h * 0.22);
  const soh = Math.round(h * 0.58);
  const pinW = Math.max(1, Math.round(sow / 10));
  const pinH = Math.max(3, Math.round(h * 0.16));
  const pinY = soy + soh - pinH;
  const pinGap = (sow - 8 * pinW) / 9;
  const ledR = Math.max(2, Math.round(sz * 0.07));
  const ledX = w - Math.round(sz * 0.14);
  const ledY = Math.round(sz * 0.11);
  let pins = '';
  for (let p = 0; p < 8; p++) {
    const px = sox + pinGap + p * (pinW + pinGap);
    pins += '<rect x="' + px.toFixed(1) + '" y="' + pinY + '" width="' + pinW +
            '" height="' + pinH + '" rx="0.5" fill="rgba(200,215,240,.35)"/>';
  }
  return '<svg class="if-port-svg" width="' + w + '" height="' + h + '" viewBox="0 0 ' + w + ' ' + h +
    '" xmlns="http://www.w3.org/2000/svg">' +
    '<rect class="port-body" x="0.5" y="0.5" width="' + (w - 1) + '" height="' + (h - 1) + '" rx="' + rx +
      '" stroke-width="1.5" fill-opacity="1"/>' +
    '<rect x="' + sox + '" y="' + soy + '" width="' + sow + '" height="' + soh +
      '" rx="2" fill="rgba(5,8,16,.5)" stroke="rgba(99,130,190,.2)" stroke-width="0.8"/>' +
    pins +
    '<circle class="port-led" cx="' + ledX + '" cy="' + ledY + '" r="' + ledR + '"/>' +
  '</svg>';
}
