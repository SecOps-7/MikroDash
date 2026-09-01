package reportpdf

import (
	"github.com/go-pdf/fpdf"
)

// Op is one recorded drawing call: the pdfkit method name and its arguments, in
// the shape the pdf-render corpus records them.
type Op struct {
	Op   string `json:"op"`
	Args []any  `json:"args"`
}

// traceCanvas records what Render draws instead of drawing it.
//
// Its WidthOfString is REAL -- it measures through fpdf, with EncodeText applied
// -- because the legend advance is arithmetic and a stub would make the gate
// compare the port against a number the port does not use. That is also the one
// place fpdf's missing kerning reaches a coordinate, which render_test.go
// accounts for exactly rather than tolerating.
type traceCanvas struct {
	ops   []Op
	reads []float64
	m     *fpdf.Fpdf
}

func newTraceCanvas() *traceCanvas {
	p := fpdf.New("P", "pt", "A4", "")
	p.AddPage()
	return &traceCanvas{m: p}
}

func (t *traceCanvas) push(op string, args ...any) { t.ops = append(t.ops, Op{Op: op, Args: args}) }

func (t *traceCanvas) Font(name string)      { t.push("font", name) }
func (t *traceCanvas) FontSize(size float64) { t.push("fontSize", r6(size)) }
func (t *traceCanvas) FillColor(hex string)  { t.push("fillColor", hex) }
func (t *traceCanvas) StrokeColor(h string)  { t.push("strokeColor", h) }
func (t *traceCanvas) LineWidth(w float64)   { t.push("lineWidth", r6(w)) }

func (t *traceCanvas) Rect(x, y, w, h float64) { t.push("rect", r6(x), r6(y), r6(w), r6(h)) }
func (t *traceCanvas) RoundedRect(x, y, w, h, r float64) {
	t.push("roundedRect", r6(x), r6(y), r6(w), r6(h), r6(r))
}
func (t *traceCanvas) MoveTo(x, y float64) { t.push("moveTo", r6(x), r6(y)) }
func (t *traceCanvas) LineTo(x, y float64) { t.push("lineTo", r6(x), r6(y)) }
func (t *traceCanvas) Fill(hex string)     { t.push("fill", hex) }
func (t *traceCanvas) Stroke()             { t.push("stroke") }
func (t *traceCanvas) Clip()               { t.push("clip") }
func (t *traceCanvas) Save()               { t.push("save") }
func (t *traceCanvas) Restore()            { t.push("restore") }
func (t *traceCanvas) AddPage()            { t.push("addPage") }
func (t *traceCanvas) End()                { t.push("end") }

func (t *traceCanvas) Text(s string, x, y float64, o TextOpts) {
	t.push("text", s, r6(x), r6(y), o.toMap())
}
func (t *traceCanvas) TextContinued(s string, o TextOpts) { t.push("text", s, o.toMap()) }

// toMap renders the options with EXACTLY the keys pdfkit was given, so a port
// asking for something the live side never asked for shows up as a difference
// rather than as a zero value nobody compares.
func (o TextOpts) toMap() map[string]any {
	m := map[string]any{}
	if o.HasContinued {
		m["continued"] = o.Continued
	}
	if o.HasWidth {
		m["width"] = r6(o.Width)
	}
	if o.Align != "" {
		m["align"] = o.Align
	}
	if o.HasLineBreak {
		m["lineBreak"] = o.LineBreak
	}
	return m
}

func (t *traceCanvas) WidthOfString(s string) float64 {
	t.m.SetFont("Helvetica", "", 7) // the legend is the only caller, at Helvetica 7
	w := r6(t.m.GetStringWidth(EncodeText(s)))
	t.reads = append(t.reads, w)
	return w
}

// A4 in points, the size pdf.js opens its document at.
func (t *traceCanvas) PageWidth() float64  { return 595.28 }
func (t *traceCanvas) PageHeight() float64 { return 841.89 }

// r6 matches the corpus generator's rounding. Both sides do the same float64
// arithmetic from the same inputs, so this trims the last bits of the mantissa
// and nothing else; a real disagreement is orders of magnitude larger.
func r6(v float64) float64 {
	return float64(int64(v*1e6+sign(v)*0.5)) / 1e6
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}
