package reportpdf

import (
	"io"
	"strconv"

	"github.com/go-pdf/fpdf"
)

// helveticaAscender is Helvetica's AFM Ascender, 718/1000 em, and it is what
// reconciles the two libraries' idea of where a string goes.
//
// pdfkit's `text(s, x, y)` treats y as the TOP of the line box and puts the
// baseline at `y + ascender/1000 * fontSize`; fpdf's `Text(x, y, s)` puts the
// baseline at y. Passing pdfkit's y straight through would lift every string on
// the page by most of its own height.
//
// DERIVED, not looked up: pdfkit drawing "AVTo" at y=100, size 11, on A4 emits
//
//	BT 1 0 0 1 40 733.992 Tm /F1 11 Tf [...] TJ ET
//
// and 841.89 - 733.992 - 100 = 7.898, which over a size of 11 is 0.718 exactly.
// Helvetica-Bold shares the value, which is why one constant covers both faces.
const helveticaAscender = 0.718

// fpdfCanvas draws the report for real.
//
// The impedance between the two APIs is all in one place, and it is three
// things: pdfkit builds a path and then consumes it (`.rect(...).fill(c)`) where
// fpdf's Rect takes a style string, so a path is BUFFERED here until Fill,
// Stroke or Clip says what to do with it; pdfkit positions text by the top of
// the line box where fpdf uses the baseline; and pdfkit aligns inside a width
// box itself, where fpdf would want a cell.
type fpdfCanvas struct {
	p *fpdf.Fpdf

	// The path most recently built and not yet consumed. Only one shape can be
	// pending, which is all `_render` ever builds before consuming it.
	pending   *pendingPath
	line      []point // an open MoveTo/LineTo polyline
	clipDepth int
	fontName  string
	fontSize  float64
	fillHex   string

	// Where the previous Text ended, for the one `continued` call in the document.
	contX, contY float64
}

type point struct{ x, y float64 }

type pendingPath struct {
	kind          string // "rect" or "roundedRect"
	x, y, w, h, r float64
}

// NewFPDFCanvas returns a Canvas that renders an A4 page in points, the size and
// unit `src/reports/pdf.js` opens its document with.
func NewFPDFCanvas() (Canvas, *fpdf.Fpdf) {
	// EXPLICIT SIZE, not the "A4" preset. fpdf's A4 is 841.886pt tall and
	// pdfkit's is 841.89 -- a four-thousandth of a point, which sounds ignorable
	// until you remember that a PDF's y axis runs from the BOTTOM, so every
	// string on every page is displaced by the difference. It showed up as a
	// uniform 0.004pt y error against pdfkit's own text matrices.
	p := fpdf.NewCustom(&fpdf.InitType{
		OrientationStr: "P",
		UnitStr:        "pt",
		Size:           fpdf.SizeType{Wd: pageW, Ht: pageH},
	})
	p.SetMargins(L, 0, R)
	p.SetAutoPageBreak(false, 0) // _render paginates itself, by measuring y
	p.AddPage()
	return &fpdfCanvas{p: p, fontName: "Helvetica", fontSize: 12}, p
}

// Output writes the finished document.
func Output(p *fpdf.Fpdf, w io.Writer) error { return p.Output(w) }

func (c *fpdfCanvas) Font(name string) {
	c.fontName = name
	c.applyFont()
}

func (c *fpdfCanvas) FontSize(size float64) {
	c.fontSize = size
	c.applyFont()
}

func (c *fpdfCanvas) applyFont() {
	style := ""
	if c.fontName == "Helvetica-Bold" {
		style = "B"
	}
	c.p.SetFont("Helvetica", style, c.fontSize)
}

// FillColor is pdfkit's current fill, which is what TEXT is painted with. A path
// fill takes its colour from the Fill call itself.
func (c *fpdfCanvas) FillColor(hex string) {
	c.fillHex = hex
	r, g, b := hexRGB(hex)
	c.p.SetTextColor(r, g, b)
}

func (c *fpdfCanvas) StrokeColor(hex string) {
	r, g, b := hexRGB(hex)
	c.p.SetDrawColor(r, g, b)
}

func (c *fpdfCanvas) LineWidth(w float64) { c.p.SetLineWidth(w) }

func (c *fpdfCanvas) Rect(x, y, w, h float64) {
	c.pending = &pendingPath{kind: "rect", x: x, y: y, w: w, h: h}
}

func (c *fpdfCanvas) RoundedRect(x, y, w, h, r float64) {
	c.pending = &pendingPath{kind: "roundedRect", x: x, y: y, w: w, h: h, r: r}
}

func (c *fpdfCanvas) MoveTo(x, y float64) { c.line = []point{{x, y}} }

func (c *fpdfCanvas) LineTo(x, y float64) { c.line = append(c.line, point{x, y}) }

func (c *fpdfCanvas) Fill(hex string) {
	r, g, b := hexRGB(hex)
	c.p.SetFillColor(r, g, b)
	c.drawPending("F")
}

// Stroke consumes whichever path is open. `_render` strokes a buffered rect, a
// rounded rect, or a polyline built with MoveTo/LineTo, and after the zebra
// row's `.fill(...).stroke()` it strokes nothing at all -- pdfkit has already
// consumed that path, so this is a no-op there rather than a second shape.
func (c *fpdfCanvas) Stroke() {
	if len(c.line) > 1 {
		c.p.MoveTo(c.line[0].x, c.line[0].y)
		for _, pt := range c.line[1:] {
			c.p.LineTo(pt.x, pt.y)
		}
		c.p.DrawPath("D")
		c.line = nil
		return
	}
	c.line = nil
	c.drawPending("D")
}

func (c *fpdfCanvas) drawPending(style string) {
	if c.pending == nil {
		return
	}
	p := c.pending
	c.pending = nil
	switch p.kind {
	case "rect":
		c.p.Rect(p.x, p.y, p.w, p.h, style)
	case "roundedRect":
		c.p.RoundedRect(p.x, p.y, p.w, p.h, p.r, "1234", style)
	}
}

// Clip turns the pending rect into a clipping region. `_render` only ever clips
// a rectangle, around the chart lines.
func (c *fpdfCanvas) Clip() {
	if c.pending == nil {
		return
	}
	p := c.pending
	c.pending = nil
	c.p.ClipRect(p.x, p.y, p.w, p.h, false)
	c.clipDepth++
}

// Save/Restore bracket the clip. pdfkit's are a full graphics-state stack, but
// the only state `_render` relies on surviving a restore is the clip region, so
// closing it is the whole job -- and closing one that was never opened would
// unbalance fpdf's own stack.
func (c *fpdfCanvas) Save() {}

func (c *fpdfCanvas) Restore() {
	if c.clipDepth > 0 {
		c.p.ClipEnd()
		c.clipDepth--
	}
}

func (c *fpdfCanvas) Text(s string, x, y float64, o TextOpts) {
	c.drawText(s, x, y, o)
}

// TextContinued is the second half of the logo, and the only place pdfkit's
// `continued` is used. It resumes exactly where the previous string ended, which
// here means advancing by that string's width.
func (c *fpdfCanvas) TextContinued(s string, o TextOpts) {
	c.drawText(s, c.contX, c.contY, o)
}

func (c *fpdfCanvas) drawText(s string, x, y float64, o TextOpts) {
	enc := EncodeText(s)
	// pdfkit writes NO text matrix for an empty string, and `_render` draws one
	// for every null cell -- `row[col] != null ? String(row[col]) : ''`. Emitting
	// an empty BT/Td/Tj instead would put a mark in the file for every missing
	// value, which is a difference in the bytes for no difference on the page.
	if enc == "" {
		return
	}
	w := c.p.GetStringWidth(enc)
	tx := x
	if o.HasWidth {
		switch o.Align {
		case "center":
			tx = x + (o.Width-w)/2
		case "right":
			tx = x + (o.Width - w)
		}
	}
	c.p.Text(tx, y+helveticaAscender*c.fontSize, enc)
	c.contX, c.contY = tx+w, y
}

func (c *fpdfCanvas) WidthOfString(s string) float64 {
	return c.p.GetStringWidth(EncodeText(s))
}

// A4 as pdfkit defines it: `SIZES.A4 = [595.28, 841.89]`, those exact decimals.
const (
	pageW = 595.28
	pageH = 841.89
)

func (c *fpdfCanvas) PageWidth() float64  { return pageW }
func (c *fpdfCanvas) PageHeight() float64 { return pageH }

func (c *fpdfCanvas) AddPage() {
	// A clip left open across a page break would swallow the next page. _render
	// never does that, but an unbalanced state here is silent and total.
	for c.clipDepth > 0 {
		c.p.ClipEnd()
		c.clipDepth--
	}
	c.p.AddPage()
	c.applyFont()
}

func (c *fpdfCanvas) End() {}

// hexRGB parses "#rrggbb". Every colour in `_render` is a literal of that shape,
// so an unparseable one is a transcription bug rather than input, and black is
// the least surprising thing to draw while the test says so.
func hexRGB(hex string) (int, int, int) {
	if len(hex) != 7 || hex[0] != '#' {
		return 0, 0, 0
	}
	v, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return 0, 0, 0
	}
	return int(v>>16) & 0xFF, int(v>>8) & 0xFF, int(v) & 0xFF
}
