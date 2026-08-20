'use strict';
/**
 * The report PDF renderer, and the two sinks it can write to.
 *
 * This was `_toPdf` in index.js, where it did `doc.pipe(res)` and set its own
 * HTTP headers. That made it unusable from anywhere without an Express
 * response — including a scheduled report, which needs a Buffer to hand to
 * nodemailer.
 *
 * So the drawing is separated from where the bytes go. `_render` below is the
 * original code, moved verbatim; the only edit is `_tsFmt` -> `tsFmt`, now that
 * it comes from ./format instead of being a sibling in index.js.
 *
 * Two sinks:
 *   pipe()      streams to an Express response, exactly as before
 *   toBuffer()  resolves a Buffer, for an email attachment
 *
 * Worth knowing: the piped path commits its headers before rendering starts, so
 * a failure mid-render produces a truncated PDF rather than a 500. The buffered
 * path has no such problem, which is another reason the scheduler must use
 * toBuffer() rather than faking a response object.
 */

const PDFDocument = require('pdfkit');
const Settings = require('../settings');
const { tsFmt } = require('./format');

const L = 40, R = 40;

/**
 * Draw the whole report into `doc`. Ends the document.
 *
 * meta: { router, from, to, stats:[{label,value}],
 *         chartData:{ lines:[{label,color,pts:[{x,y}]}], yLabel } }
 */
function _render(doc, title, columns, rows, meta) {
  const PW = doc.page.width;
  const inner = PW - L - R;

  // ── Header bar ────────────────────────────────────────────────────────
  const hTop = 30;
  doc.rect(0, 0, PW, 52).fill('#0f172a');
  // Logo text
  doc.font('Helvetica-Bold').fontSize(17).fillColor('#38bdf8')
     .text('Mikro', L, hTop, { continued: true })
     .fillColor('#f8fafc')
     .text('Dash', { lineBreak: false });
  // Report title centred
  doc.font('Helvetica-Bold').fontSize(13).fillColor('#f8fafc')
     .text(title, L, hTop + 1, { width: inner, align: 'center', lineBreak: false });
  doc.fillColor('#000000'); // reset

  let y = 66;

  // ── Meta info row ─────────────────────────────────────────────────────
  const fmtTs = ts => ts ? tsFmt(ts) || '—' : '—';
  const routerLabel = (meta && meta.router) ? meta.router : '';
  const dateRange   = (meta && meta.from && meta.to)
    ? `${fmtTs(meta.from)}  →  ${fmtTs(meta.to)}`
    : '';
  if (routerLabel || dateRange) {
    doc.font('Helvetica').fontSize(8).fillColor('#64748b');
    if (routerLabel) doc.text(`Router: ${routerLabel}`, L, y, { lineBreak: false });
    if (dateRange)   doc.text(dateRange, L, y, { width: inner, align: 'right', lineBreak: false });
    doc.fillColor('#000000');
    y += 16;
    doc.moveTo(L, y).lineTo(PW - R, y).lineWidth(0.5).strokeColor('#e2e8f0').stroke();
    doc.lineWidth(1).strokeColor('#000000');
    y += 10;
  }

  // ── Stat boxes ────────────────────────────────────────────────────────
  if (meta && meta.stats && meta.stats.length) {
    const n     = meta.stats.length;
    const boxW  = Math.min(110, Math.floor((inner - (n - 1) * 8) / n));
    const boxH  = 36;
    const totalW = n * boxW + (n - 1) * 8;
    const startX = L + Math.floor((inner - totalW) / 2);
    meta.stats.forEach((s, i) => {
      const bx = startX + i * (boxW + 8);
      doc.roundedRect(bx, y, boxW, boxH, 4).lineWidth(0.75).strokeColor('#cbd5e1').stroke();
      doc.font('Helvetica-Bold').fontSize(11).fillColor('#0f172a')
         .text(String(s.value), bx + 4, y + 5, { width: boxW - 8, align: 'center', lineBreak: false });
      doc.font('Helvetica').fontSize(7).fillColor('#64748b')
         .text(s.label, bx + 4, y + 20, { width: boxW - 8, align: 'center', lineBreak: false });
    });
    doc.fillColor('#000000');
    y += boxH + 14;
  }

  // ── Chart ─────────────────────────────────────────────────────────────
  if (meta && meta.chartData && meta.chartData.lines && meta.chartData.lines.length) {
    const cd      = meta.chartData;
    const lines   = cd.lines.filter(l => l.pts && l.pts.length > 1);
    if (lines.length) {
      const CH = 110, yAxisW = 38, xAxisH = 16;
      const cLeft = L + yAxisW, cRight = PW - R;
      const cW    = cRight - cLeft;
      const cTop  = y, cBot = y + CH;

      // Compute y-range across all lines
      let yMin = Infinity, yMax = -Infinity;
      lines.forEach(l => l.pts.forEach(p => { if (p.y < yMin) yMin = p.y; if (p.y > yMax) yMax = p.y; }));
      if (yMin === yMax) { yMin = 0; yMax = yMax || 1; }
      if (yMin > 0) yMin = 0;
      const yRange = yMax - yMin;
      const xMin = lines[0].pts[0].x;
      const xMax = lines[0].pts[lines[0].pts.length - 1].x;
      const xRange = xMax - xMin || 1;

      const toX = xv => cLeft + ((xv - xMin) / xRange) * cW;
      const toY = yv => cBot  - ((yv - yMin) / yRange) * CH;

      // Grid lines + Y labels (5 steps)
      doc.font('Helvetica').fontSize(7).fillColor('#94a3b8');
      for (let step = 0; step <= 4; step++) {
        const yv  = yMin + (yRange / 4) * step;
        const gy  = toY(yv);
        doc.moveTo(cLeft, gy).lineTo(cRight, gy).lineWidth(0.3).strokeColor('#e2e8f0').stroke();
        const lbl = yv >= 1000 ? (yv / 1000).toFixed(1) + 'k' : yv.toFixed(1);
        doc.text(lbl, L, gy - 4, { width: yAxisW - 4, align: 'right', lineBreak: false });
      }
      if (cd.yLabel) {
        doc.text(cd.yLabel, L, y + CH / 2 - 4, { width: yAxisW - 4, align: 'right', lineBreak: false });
      }

      // X axis time labels (5 ticks) — format adapts to span; respects displayTimezone
      const _tz      = Settings.load().displayTimezone || '';
      const HOUR     = 3600000, DAY = 86400000;
      const spanMs   = xRange;
      const labelW   = spanMs <= 12 * HOUR ? 28 : spanMs <= 3 * DAY ? 54 : 28;
      const _pdfTick = ts => {
        if (_tz) {
          let opts;
          if (spanMs <= 12 * HOUR) opts = { timeZone:_tz, hour:'2-digit', minute:'2-digit', hour12:false };
          else if (spanMs <= 3 * DAY) opts = { timeZone:_tz, month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit', hour12:false };
          else opts = { timeZone:_tz, month:'2-digit', day:'2-digit' };
          return new Intl.DateTimeFormat('sv-SE', opts).format(new Date(ts));
        }
        const d = new Date(ts), p = n => String(n).padStart(2, '0');
        if (spanMs <= 12 * HOUR)  return `${p(d.getHours())}:${p(d.getMinutes())}`;
        if (spanMs <= 3  * DAY)   return `${p(d.getMonth()+1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
        return `${p(d.getMonth()+1)}-${p(d.getDate())}`;
      };
      for (let ti = 0; ti <= 4; ti++) {
        const ts  = xMin + (xRange / 4) * ti;
        const tx  = toX(ts);
        const lbl = _pdfTick(ts);
        doc.text(lbl, tx - labelW / 2, cBot + 3, { width: labelW, align: 'center', lineBreak: false });
      }
      doc.fillColor('#000000');

      // Border
      doc.rect(cLeft, cTop, cW, CH).lineWidth(0.5).strokeColor('#cbd5e1').stroke();
      doc.lineWidth(1);

      // Lines
      lines.forEach(line => {
        const pts = line.pts;
        doc.save();
        doc.rect(cLeft, cTop, cW, CH).clip();
        doc.moveTo(toX(pts[0].x), toY(pts[0].y));
        for (let i = 1; i < pts.length; i++) doc.lineTo(toX(pts[i].x), toY(pts[i].y));
        doc.lineWidth(1.2).strokeColor(line.color || '#38bdf8').stroke();
        doc.restore();
      });

      // Legend
      let legX = cLeft;
      lines.forEach(line => {
        doc.rect(legX, cBot + xAxisH + 2, 10, 6).fill(line.color || '#38bdf8');
        doc.font('Helvetica').fontSize(7).fillColor('#334155')
           .text(line.label, legX + 13, cBot + xAxisH + 1, { lineBreak: false });
        legX += 13 + doc.widthOfString(line.label) + 16;
      });
      doc.fillColor('#000000');

      y = cBot + xAxisH + 18;
    }
  }

  // ── Table ─────────────────────────────────────────────────────────────
  const colW = Math.floor(inner / columns.length);
  const _drawTableHeader = yh => {
    doc.rect(L, yh, inner, 14).fill('#f1f5f9');
    doc.font('Helvetica-Bold').fontSize(8).fillColor('#0f172a');
    columns.forEach((col, i) => doc.text(col, L + i * colW + 3, yh + 3, { width: colW - 4, lineBreak: false }));
    doc.fillColor('#000000');
  };
  _drawTableHeader(y);
  y += 14;

  doc.font('Helvetica').fontSize(7.5);
  let rowIdx = 0;
  for (const row of rows) {
    if (y > doc.page.height - 50) {
      doc.addPage();
      y = 40;
      _drawTableHeader(y);
      doc.font('Helvetica').fontSize(7.5);
      y += 14; rowIdx = 0;
    }
    if (rowIdx % 2 === 1) doc.rect(L, y, inner, 12).fill('#f8fafc').stroke();
    doc.fillColor('#334155');
    columns.forEach((col, i) => {
      const v = row[col] != null ? String(row[col]) : '';
      doc.text(v, L + i * colW + 3, y + 2, { width: colW - 4, lineBreak: false });
    });
    doc.fillColor('#000000');
    y += 12;
    rowIdx++;
  }

  doc.end();
}

/** Stream to an Express response. The original behaviour, unchanged. */
function pipe(title, columns, rows, res, meta) {
  const doc = new PDFDocument({ margin: L, size: 'A4' });
  res.setHeader('Content-Type', 'application/pdf');
  res.setHeader('Content-Disposition', `attachment; filename="${title}.pdf"`);
  doc.pipe(res);
  _render(doc, title, columns, rows, meta);
}

/**
 * Resolve the finished PDF as a Buffer.
 *
 * `maxBytes` guards a report whose row count would otherwise produce a
 * hundred-megabyte attachment no mail server will accept: the document is
 * destroyed as soon as the running total is exceeded, rather than after it has
 * all been built.
 *
 * The 'error' listener is not optional. Without it a throw inside _render
 * leaves this promise pending forever, and the scheduler that awaited it never
 * runs again.
 */
function toBuffer(title, columns, rows, meta, maxBytes) {
  return new Promise((resolve, reject) => {
    const doc = new PDFDocument({ margin: L, size: 'A4' });
    const chunks = [];
    let total = 0;
    let stopped = false;

    doc.on('data', (c) => {
      if (stopped) return;
      total += c.length;
      if (maxBytes && total > maxBytes) {
        stopped = true;
        doc.destroy();
        reject(new Error('report PDF exceeded ' + maxBytes + ' bytes'));
        return;
      }
      chunks.push(c);
    });
    doc.on('error', (e) => { if (!stopped) { stopped = true; reject(e); } });
    doc.on('end', () => { if (!stopped) resolve(Buffer.concat(chunks)); });

    try {
      _render(doc, title, columns, rows, meta);
    } catch (e) {
      if (!stopped) { stopped = true; reject(e); }
    }
  });
}

module.exports = { pipe, toBuffer, _render };
