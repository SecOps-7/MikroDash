package reportpdf

import (
	"fmt"
	"math"
	"math/big"
	"time"

	_ "time/tzdata" // the display timezone is operator-set; alpine carries no zoneinfo

	"mikrodash/internal/reports"
)

// Margins. `const L = 40, R = 40` in pdf.js.
const (
	L = 40.0
	R = 40.0
)

// The meta shape lives in internal/reports, with the code that BUILDS it --
// this package draws it, and already imports that one for TsFmt and ToFixed, so
// owning the types here would be a cycle. Aliased rather than wrapped, so
// `reportpdf.Meta` still names exactly one type and no conversion exists to get
// out of step.
type (
	Meta  = reports.Meta
	Stat  = reports.Stat
	Chart = reports.Chart
	Line  = reports.Line
	Pt    = reports.Pt
)

// Render draws the whole report and ends the document.
//
// A transcription of `_render` in `src/reports/pdf.js`, kept in the same order
// and the same shape so the two can be read side by side. `tz` is what the live
// side fetches from `Settings.load().displayTimezone` -- passed in here because
// this package has no business loading settings, and because the gate needs to
// drive both timezone paths without touching a file.
func Render(c Canvas, title string, columns []string, rows []map[string]any, meta *Meta, tz string) {
	pw := c.PageWidth()
	inner := pw - L - R

	// ── Header bar ────────────────────────────────────────────────────────
	const hTop = 30.0
	c.Rect(0, 0, pw, 52)
	c.Fill("#0f172a")
	// Logo text
	c.Font("Helvetica-Bold")
	c.FontSize(17)
	c.FillColor("#38bdf8")
	c.Text("Mikro", L, hTop, optContinued())
	c.FillColor("#f8fafc")
	c.TextContinued("Dash", optNoBreak())
	// Report title centred
	c.Font("Helvetica-Bold")
	c.FontSize(13)
	c.FillColor("#f8fafc")
	c.Text(title, L, hTop+1, optBox(inner, "center"))
	c.FillColor("#000000") // reset

	y := 66.0

	// ── Meta info row ─────────────────────────────────────────────────────
	fmtTs := func(ts float64) string {
		if ts == 0 {
			return "—"
		}
		if s := reports.TsFmt(int64(ts), tz); s != "" {
			return s
		}
		return "—"
	}
	routerLabel := ""
	if meta != nil {
		routerLabel = meta.Router
	}
	dateRange := ""
	if meta != nil && meta.From != 0 && meta.To != 0 {
		// // AN EN DASH, not the rightwards arrow this used to carry. U+2192 is not in
		// the WinAnsi charset of the standard-14 Helvetica, and pdfkit does not
		// substitute: it emits the raw code point and advances by ZERO, so the
		// separator was invisible in every report that left the default font alone.
		// This port reproduced that faithfully — `EncodeText` DROPS a rune cp1252
		// cannot hold, for exactly that reason — and filed it as ../MikroDash/ToDo.md
		// §2. Fixed upstream in e0dcafb; followed here.
		dateRange = fmtTs(meta.From) + "  \u2013  " + fmtTs(meta.To)
	}
	if routerLabel != "" || dateRange != "" {
		c.Font("Helvetica")
		c.FontSize(8)
		c.FillColor("#64748b")
		if routerLabel != "" {
			c.Text("Router: "+routerLabel, L, y, optNoBreak())
		}
		if dateRange != "" {
			c.Text(dateRange, L, y, optBox(inner, "right"))
		}
		c.FillColor("#000000")
		y += 16
		c.MoveTo(L, y)
		c.LineTo(pw-R, y)
		c.LineWidth(0.5)
		c.StrokeColor("#e2e8f0")
		c.Stroke()
		c.LineWidth(1)
		c.StrokeColor("#000000")
		y += 10
	}

	// ── Stat boxes ────────────────────────────────────────────────────────
	if meta != nil && len(meta.Stats) > 0 {
		n := float64(len(meta.Stats))
		boxW := math.Min(110, math.Floor((inner-(n-1)*8)/n))
		const boxH = 36.0
		totalW := n*boxW + (n-1)*8
		startX := L + math.Floor((inner-totalW)/2)
		for i, s := range meta.Stats {
			bx := startX + float64(i)*(boxW+8)
			c.RoundedRect(bx, y, boxW, boxH, 4)
			c.LineWidth(0.75)
			c.StrokeColor("#cbd5e1")
			c.Stroke()
			c.Font("Helvetica-Bold")
			c.FontSize(11)
			c.FillColor("#0f172a")
			c.Text(s.Value, bx+4, y+5, optBox(boxW-8, "center"))
			c.Font("Helvetica")
			c.FontSize(7)
			c.FillColor("#64748b")
			c.Text(s.Label, bx+4, y+20, optBox(boxW-8, "center"))
		}
		c.FillColor("#000000")
		y += boxH + 14
	}

	// ── Chart ─────────────────────────────────────────────────────────────
	if meta != nil && meta.ChartData != nil && len(meta.ChartData.Lines) > 0 {
		cd := meta.ChartData
		lines := make([]Line, 0, len(cd.Lines))
		for _, l := range cd.Lines {
			if len(l.Pts) > 1 {
				lines = append(lines, l)
			}
		}
		if len(lines) > 0 {
			const ch, yAxisW, xAxisH = 110.0, 38.0, 16.0
			cLeft, cRight := L+yAxisW, pw-R
			cW := cRight - cLeft
			cTop, cBot := y, y+ch

			// Compute y-range across all lines
			yMin, yMax := math.Inf(1), math.Inf(-1)
			for _, l := range lines {
				for _, p := range l.Pts {
					if p.Y < yMin {
						yMin = p.Y
					}
					if p.Y > yMax {
						yMax = p.Y
					}
				}
			}
			if yMin == yMax {
				yMin = 0
				// `yMax = yMax || 1` -- a flat line AT ZERO gets a range of 1.
				if yMax == 0 {
					yMax = 1
				}
			}
			if yMin > 0 {
				yMin = 0
			}
			yRange := yMax - yMin
			xMin := lines[0].Pts[0].X
			xMax := lines[0].Pts[len(lines[0].Pts)-1].X
			xRange := xMax - xMin
			if xRange == 0 {
				xRange = 1
			}

			toX := func(xv float64) float64 { return cLeft + ((xv-xMin)/xRange)*cW }
			toY := func(yv float64) float64 { return cBot - ((yv-yMin)/yRange)*ch }

			// Grid lines + Y labels (5 steps)
			c.Font("Helvetica")
			c.FontSize(7)
			c.FillColor("#94a3b8")
			for step := 0; step <= 4; step++ {
				yv := yMin + (yRange/4)*float64(step)
				gy := toY(yv)
				c.MoveTo(cLeft, gy)
				c.LineTo(cRight, gy)
				c.LineWidth(0.3)
				c.StrokeColor("#e2e8f0")
				c.Stroke()
				lbl := toFixed1(yv)
				if yv >= 1000 {
					lbl = toFixed1(yv/1000) + "k"
				}
				c.Text(lbl, L, gy-4, optBox(yAxisW-4, "right"))
			}
			if cd.YLabel != "" {
				c.Text(cd.YLabel, L, y+ch/2-4, optBox(yAxisW-4, "right"))
			}

			// X axis time labels (5 ticks) — format adapts to span; respects displayTimezone
			const hour, day = 3600000.0, 86400000.0
			spanMs := xRange
			labelW := 28.0
			if spanMs > 12*hour && spanMs <= 3*day {
				labelW = 54
			}
			for ti := 0; ti <= 4; ti++ {
				ts := xMin + (xRange/4)*float64(ti)
				tx := toX(ts)
				lbl := pdfTick(ts, spanMs, tz)
				c.Text(lbl, tx-labelW/2, cBot+3, optBox(labelW, "center"))
			}
			c.FillColor("#000000")

			// Border
			c.Rect(cLeft, cTop, cW, ch)
			c.LineWidth(0.5)
			c.StrokeColor("#cbd5e1")
			c.Stroke()
			c.LineWidth(1)

			// Lines
			for _, line := range lines {
				pts := line.Pts
				c.Save()
				c.Rect(cLeft, cTop, cW, ch)
				c.Clip()
				c.MoveTo(toX(pts[0].X), toY(pts[0].Y))
				for i := 1; i < len(pts); i++ {
					c.LineTo(toX(pts[i].X), toY(pts[i].Y))
				}
				c.LineWidth(1.2)
				c.StrokeColor(orDefault(line.Color))
				c.Stroke()
				c.Restore()
			}

			// Legend
			legX := cLeft
			for _, line := range lines {
				c.Rect(legX, cBot+xAxisH+2, 10, 6)
				c.Fill(orDefault(line.Color))
				c.Font("Helvetica")
				c.FontSize(7)
				c.FillColor("#334155")
				c.Text(line.Label, legX+13, cBot+xAxisH+1, optNoBreak())
				legX += 13 + c.WidthOfString(line.Label) + 16
			}
			c.FillColor("#000000")

			y = cBot + xAxisH + 18
		}
	}

	// ── Table ─────────────────────────────────────────────────────────────
	colW := math.Floor(inner / float64(len(columns)))
	drawTableHeader := func(yh float64) {
		c.Rect(L, yh, inner, 14)
		c.Fill("#f1f5f9")
		c.Font("Helvetica-Bold")
		c.FontSize(8)
		c.FillColor("#0f172a")
		for i, col := range columns {
			c.Text(col, L+float64(i)*colW+3, yh+3, optBox(colW-4, ""))
		}
		c.FillColor("#000000")
	}
	drawTableHeader(y)
	y += 14

	c.Font("Helvetica")
	c.FontSize(7.5)
	rowIdx := 0
	for _, row := range rows {
		if y > c.PageHeight()-50 {
			c.AddPage()
			y = 40
			drawTableHeader(y)
			c.Font("Helvetica")
			c.FontSize(7.5)
			y += 14
			rowIdx = 0
		}
		if rowIdx%2 == 1 {
			c.Rect(L, y, inner, 12)
			c.Fill("#f8fafc")
			c.Stroke()
		}
		c.FillColor("#334155")
		for i, col := range columns {
			c.Text(jsString(row[col]), L+float64(i)*colW+3, y+2, optBox(colW-4, ""))
		}
		c.FillColor("#000000")
		y += 12
		rowIdx++
	}

	c.End()
}

// orDefault is `line.color || '#38bdf8'`, which the live side writes TWICE --
// once for the stroke and once for the legend swatch.
func orDefault(hex string) string {
	if hex == "" {
		return "#38bdf8"
	}
	return hex
}

// jsString is `row[col] != null ? String(row[col]) : ”`.
//
// The `!= null` is deliberate on the live side and reproduced here: it lets 0
// and "" through, where a truthiness test would render an empty cell for a
// genuine zero. A JSON-decoded row gives numbers as float64, and a missing key
// gives nil -- which is what `undefined` becomes, and which `!= null` also
// rejects.
func jsString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return jsNumberString(t)
	case int:
		return fmt.Sprintf("%d", t)
	case int64:
		return fmt.Sprintf("%d", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// jsNumberString is JavaScript's `String(n)` for the values a report cell holds.
//
// Go's %v prints 1e+06 where JS prints 1000000, and prints 5 for 5.0 only by
// accident of formatting. strconv's 'g' with -1 precision is the shortest
// round-tripping form, which is what JS specifies, but it still uses exponent
// notation at a different threshold -- so the integral case is handled first,
// which is the only one a report cell realistically holds.
func jsNumberString(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return new(big.Float).SetFloat64(f).Text('f', -1)
	}
	return new(big.Float).SetFloat64(f).Text('g', -1)
}

// toFixed1 is `yv.toFixed(1)`, and it lives in internal/reports because
// `fmtDataMB` needs the same rule at three different precisions.
//
// One implementation, not two: this was a private copy here until
// `report-format-cases.js` pinned the general function, and two copies of a
// rounding rule is exactly the drift that corpus exists to prevent. The
// renderer's own gate re-checks it against 4446 recorded drawing calls, so the
// fold-in is verified rather than assumed.
func toFixed1(x float64) string { return reports.ToFixed(x, 1) }

// pdfTick is `_pdfTick`: the x-axis label, whose FORMAT depends on the span and
// on whether a display timezone is set.
//
// The two paths do not agree, and that is the live behaviour rather than a
// transcription slip. `sv-SE` is presumably chosen because it renders a full
// date ISO-style, but asked for month and day alone it gives day/month with
// SLASHES -- `25/08 15:30` against the fallback's `08-25 06:00`. Recorded in
// ../MikroDash/ToDo.md §3 and reproduced here exactly, because a port that
// tidied it up would render a date the live app does not.
func pdfTick(ts, spanMs float64, tz string) string {
	const hour, day = 3600000.0, 86400000.0
	t := time.UnixMilli(int64(ts)).UTC()
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			t = t.In(loc)
		}
		// An unloadable zone falls through with UTC, which is what Intl does with
		// an invalid timeZone only in the sense that it throws -- the live side
		// would 500. Falling through renders a correct UTC label instead of
		// losing the report.
	}
	// ── ONE SET OF FORMATS FOR BOTH PATHS, WHICH IS THE WHOLE FIX ────────────
	//
	// The timezone branch used to render `02/01` where the plain branch rendered
	// `01-02` -- DAY/MONTH against MONTH-DAY. That was faithful: the live side
	// asked `sv-SE` for month and day alone and got `25/08`, because a locale
	// chosen for rendering a COMPLETE date ISO-style has its own opinion about a
	// partial one. So setting a display timezone silently REVERSED the field
	// order, and `08-09` and `09-08` became the same label depending on a setting
	// the reader of the PDF cannot see.
	//
	// Reported as ../MikroDash/ToDo.md §3 and fixed upstream in e0dcafb, which
	// assembles the label from `Intl.formatToParts` rather than letting a locale
	// lay it out. Go never needed the locale, so following the fix means simply
	// deleting the second set of formats: only the LOCATION differs now.
	switch {
	case spanMs <= 12*hour:
		return t.Format("15:04")
	case spanMs <= 3*day:
		return t.Format("01-02 15:04")
	default:
		return t.Format("01-02")
	}
}
