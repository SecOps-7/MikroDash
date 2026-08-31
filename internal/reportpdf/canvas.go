package reportpdf

// Canvas is the drawing surface Render draws on -- exactly the calls
// `src/reports/pdf.js` makes on a pdfkit document, and no others.
//
// It exists so the port can be COMPARED rather than merely run. `traceCanvas`
// records the calls for the gate; the fpdf implementation puts ink on a page.
// Both are driven by the same Render, so a gate that passes is a statement about
// the code that ships, not about a parallel implementation written to satisfy it.
//
// The methods are the pdfkit names on purpose. This is a transcription, and a
// reader with `pdf.js` open in the other window should be able to follow it line
// for line; renaming Fill to FillPath would buy nothing and cost that.
type Canvas interface {
	// Graphics state.
	Font(name string)
	FontSize(size float64)
	FillColor(hex string)
	StrokeColor(hex string)
	LineWidth(w float64)

	// Paths. Fill and Stroke consume the path most recently built, which is how
	// pdfkit's chained `.rect(...).fill(c)` behaves.
	Rect(x, y, w, h float64)
	RoundedRect(x, y, w, h, r float64)
	MoveTo(x, y float64)
	LineTo(x, y float64)
	Fill(hex string)
	Stroke()
	Clip()

	Save()
	Restore()

	// Text. Two forms, because pdfkit has two: the positioned call, and the
	// follow-up that continues from where the last one left off. The logo is the
	// only place the second is used and it is why FillColor can change mid-line.
	Text(s string, x, y float64, opts TextOpts)
	TextContinued(s string, opts TextOpts)

	// Reads. Render's arithmetic depends on all three.
	WidthOfString(s string) float64
	PageWidth() float64
	PageHeight() float64

	AddPage()
	End()
}

// TextOpts mirrors the pdfkit options object, including WHICH KEYS ARE PRESENT.
//
// That matters more than it looks. `{ continued: true }` and
// `{ width: w, align: 'center', lineBreak: false }` are different objects to
// pdfkit, and a Canvas that flattened them into one struct with zero values
// would let the gate pass while the port asked for something the live side never
// asked for. Each field is a pointer or a bool paired with a Has flag so the
// trace can emit the same key set.
type TextOpts struct {
	Continued    bool
	HasContinued bool

	Width    float64
	HasWidth bool

	Align string // "" when the key is absent

	LineBreak    bool
	HasLineBreak bool
}

// The two option shapes `_render` actually uses, named so the call sites read
// like the JavaScript they come from.
func optContinued() TextOpts { return TextOpts{Continued: true, HasContinued: true} }
func optNoBreak() TextOpts   { return TextOpts{HasLineBreak: true} }
func optBox(w float64, align string) TextOpts {
	return TextOpts{Width: w, HasWidth: true, Align: align, HasLineBreak: true}
}
