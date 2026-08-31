package reportpdf

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// TestEveryCorpusPayloadProducesAWellFormedPDF is the other half of the gate.
//
// render_test.go proves the port makes the right DRAWING DECISIONS -- the calls,
// their order, their arguments. It says nothing about whether those calls
// produce a file a reader can open, because its Canvas only records them. This
// drives the same 31 payloads through the fpdf Canvas and looks at the bytes.
//
// The two together are the claim: the right marks, in a real document.
func TestEveryCorpusPayloadProducesAWellFormedPDF(t *testing.T) {
	for _, c := range loadRenderCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			cv, p := NewFPDFCanvas()
			p.SetCompression(false) // so the content stream can be read below
			Render(cv, c.Title, c.Columns, c.Rows, c.Meta.toMeta(), c.TZ)

			var buf bytes.Buffer
			if err := Output(p, &buf); err != nil {
				t.Fatalf("Output: %v", err)
			}
			if err := p.Error(); err != nil {
				t.Fatalf("fpdf reported an error while drawing: %v", err)
			}

			b := buf.Bytes()
			if !bytes.HasPrefix(b, []byte("%PDF-")) {
				t.Fatalf("output does not begin with a PDF header: %q", firstBytes(b))
			}
			if !bytes.Contains(b, []byte("%%EOF")) {
				t.Fatal("output has no EOF marker -- the document was never finished")
			}
			if len(b) < 800 {
				t.Fatalf("output is %d bytes, which is too small to be this report", len(b))
			}

			// The page count must match what _render decided. It paginates by
			// measuring y, and the corpus records the addPage calls it made.
			wantPages := 1
			for _, op := range c.Ops {
				if op.Op == "addPage" {
					wantPages++
				}
			}
			if got := p.PageCount(); got != wantPages {
				t.Errorf("document has %d page(s), the recorded drawing has %d", got, wantPages)
			}

			// The clip stack must be balanced. An unclosed clip swallows
			// everything drawn after it, which is invisible in a byte count and
			// total on the page.
			body := buf.String()
			if q, n := strings.Count(body, " W n"), strings.Count(body, "\nQ"); q > n {
				t.Errorf("%d clip(s) opened but only %d graphics states closed", q, n)
			}

			// And the text must actually be in there. The title is drawn on every
			// report, and a cp1252-encodable column heading with it.
			if !containsPDFText(body, EncodeText(c.Title)) {
				t.Errorf("the report title %q does not appear in the content stream", c.Title)
			}
		})
	}
}

// TestTheContinuedLogoResumesWhereTheFirstHalfEnded pins the one place pdfkit's
// `continued` is used -- the two-colour "Mikro|Dash" wordmark.
//
// It earns its own test because it is the only call whose x is not passed in:
// getting it wrong draws "Dash" on top of "Mikro", which every other check here
// would happily pass.
func TestTheContinuedLogoResumesWhereTheFirstHalfEnded(t *testing.T) {
	cv, p := NewFPDFCanvas()
	p.SetCompression(false)
	Render(cv, "T", []string{"C"}, nil, nil, "")
	var buf bytes.Buffer
	if err := Output(p, &buf); err != nil {
		t.Fatalf("Output: %v", err)
	}

	xs := textXPositions(buf.String(), []string{"Mikro", "Dash"})
	if len(xs) != 2 {
		t.Fatalf("expected both halves of the wordmark in the stream, found %d", len(xs))
	}
	// "Mikro" at Helvetica-Bold 17 is about 44pt wide; "Dash" must start after it
	// and not far after, or the two are not one word.
	gap := xs[1] - xs[0]
	if gap < 30 || gap > 60 {
		t.Errorf("Dash starts %.2fpt after Mikro; the halves are not adjacent", gap)
	}
}

var textOpRe = regexp.MustCompile(`BT\s+([0-9.]+)\s+([0-9.]+)\s+Td\s+\((?:[^)\\]|\\.)*\)\s*Tj`)

// textXPositions finds the x of each `Td (…) Tj` whose string is one of `want`.
func textXPositions(body string, want []string) []float64 {
	var out []float64
	for _, m := range textOpRe.FindAllStringSubmatch(body, -1) {
		for _, w := range want {
			if strings.Contains(m[0], "("+w+")") {
				var f float64
				_, _ = fmt.Sscanf(m[1], "%g", &f)
				out = append(out, f)
			}
		}
	}
	return out
}

func containsPDFText(body, s string) bool {
	if s == "" {
		return true
	}
	// fpdf escapes (, ) and \ inside a literal string.
	r := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`)
	return strings.Contains(body, "("+r.Replace(s)+")")
}

func firstBytes(b []byte) []byte {
	if len(b) > 24 {
		return b[:24]
	}
	return b
}

// TestGlyphsLandWherePdfkitPutsThem is the check that "nothing user-visible may
// change" actually rests on.
//
// The op comparison in render_test.go proves the port ASKS for the right things.
// This proves the resulting file puts the glyphs in the same place, which is a
// different claim: between the two sits pdfkit's text engine, which turns the
// top of a line box into a baseline and resolves `{ width, align }` into an x
// using its own measurement. A port that recorded every call correctly and then
// placed text by a different rule would pass the other gate and print a
// different page.
//
// The corpus records pdfkit's own emitted text matrices. Both libraries write
// the translation in PDF user space, origin bottom-left, so the numbers are
// directly comparable.
//
// THE ONE EXPECTED DIFFERENCE IS DERIVED, NOT TOLERATED. An aligned string
// starts a function of its own width in -- half for centre, all of it for right
// -- and pdfkit measures KERNED where fpdf cannot. So the expected x is
//
//	live.X + (kerned - unkerned) * f     f = 0 left, 0.5 centre, 1 right
//
// computed per string from the widths the corpus recorded. A left-aligned string
// gets no allowance at all, and neither does any y.
func TestGlyphsLandWherePdfkitPutsThem(t *testing.T) {
	// fpdf writes coordinates with TWO DECIMALS (`Td` reads `230.83`, not
	// `230.8265`), so the emitted number can differ from the intended one by up to
	// half a hundredth of a point. That is a property of how the file is printed,
	// not of where the text was put, and it is the only reason this is not an
	// exact comparison. A real placement error -- a wrong baseline, a missed
	// alignment -- is two orders of magnitude larger, as the failures that got
	// this test written were.
	// Slightly over half a hundredth: the boundary case (a true 56.505 printed as
	// "56.50") lands exactly on 0.005 and float representation puts it a hair
	// either side.
	const eps = 0.0051

	var worst float64
	var worstWhere string
	for _, c := range loadRenderCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			cv, p := NewFPDFCanvas()
			p.SetCompression(false)
			Render(cv, c.Title, c.Columns, c.Rows, c.Meta.toMeta(), c.TZ)
			var buf bytes.Buffer
			if err := Output(p, &buf); err != nil {
				t.Fatalf("Output: %v", err)
			}

			got := allTextPositions(buf.String())
			if len(got) != len(c.Positions) {
				t.Fatalf("emitted %d positioned strings, pdfkit emitted %d", len(got), len(c.Positions))
			}

			// The legend walks its x with widthOfString, so every label after the
			// first inherits the difference between the two measurements of the
			// labels before it. Same debt as render_test.go carries over the op
			// stream, tracked here over the emitted positions.
			debt, read := 0.0, 0
			caseWorst, caseWorstS := 0.0, ""
			for i, want := range c.Positions {
				// f is how much of its own width an aligned string starts in by.
				// Recovered from the recorded op rather than assumed, so a string
				// whose alignment the port got wrong fails here too.
				// The corpus pairs a position with a text op by counting non-empty
				// strings; so does alignFactor. If the two ever count differently,
				// every subsequent string is compared against a later one's
				// position and the failures look like placement errors. Assert the
				// pairing instead of trusting it.
				if op, ok := nthTextOp(c.Ops, i); !ok || op != want.S {
					t.Fatalf("position %d is recorded as %q but the %d'th text op is %q",
						i, want.S, i, op)
				}
				f, continued := alignFactor(c.Ops, i)

				// THE WIDTH PDFKIT ALIGNED BY IS NOT widthOfString(s).
				//
				// Laying text out inside a `width` box, pdfkit splits on spaces and
				// measures the pieces, which loses a kern pair across each space.
				// "Peak TX" measures 26.145 whole and 26.845 as a sum of characters,
				// but the box it is centred in was sized 26.495 -- one of its two
				// space-adjacent kerns applied, not both.
				//
				// So the live width is RECOVERED from pdfkit's own emitted x rather
				// than recomputed. That keeps the check meaningful: the port must
				// apply the same alignment RULE, and may differ only by its own
				// (unkerned) measurement of the string.
				delta := 0.0
				if f > 0 {
					boxX, boxW, ok := textBox(c.Ops, i)
					if !ok {
						t.Fatalf("%q is aligned but its op carries no width box", want.S)
					}
					liveW := boxW - (want.X-boxX)/f
					// And that recovered width must sit in the band the two
					// measurements bracket, or something other than kerning is going
					// on and this derivation is hiding it.
					//
					// MIN AND MAX, not kerned-then-unkerned: Helvetica has POSITIVE
					// kern pairs as well as negative ones -- "ry" is +0.33pt at 11pt
					// -- so the whole-string width is not always the smaller of the
					// two. Written the narrow way first, and eight corpus titles said
					// otherwise.
					lo, hi := want.Kerned, want.Unkerned
					if lo > hi {
						lo, hi = hi, lo
					}
					if liveW < lo-eps || liveW > hi+eps {
						t.Errorf("%q: pdfkit aligned by %.4f, outside its own %.4f .. %.4f",
							want.S, liveW, lo, hi)
					}
					delta = (liveW - want.Unkerned) * f
				}
				if isLegendLabel(c.Ops, i) {
					delta += debt
				}
				if continued {
					// The wordmark's second half resumes where the first ended, so
					// it inherits the FIRST half's kern difference rather than any
					// of its own.
					//
					// AND WITH THE OPPOSITE SIGN to an aligned string, which is not
					// a detail: an aligned string that measures wider starts further
					// LEFT, because it is centred or right-set inside a fixed box; a
					// resumed string starts further RIGHT, because it begins where
					// the wider text before it finished. Getting this backwards puts
					// "Dash" 0.68pt off instead of 0.34, which is how it was caught.
					delta = c.Positions[i-1].Unkerned - c.Positions[i-1].Kerned
				}
				wantX := want.X + delta

				if d := abs(got[i].x - wantX); d > eps {
					t.Errorf("%q: x %.4f, pdfkit %.4f (expected %.4f after a %.4fpt kern correction)",
						want.S, got[i].x, want.X, wantX, delta)
				}
				if d := abs(got[i].y - want.Y); d > eps {
					t.Errorf("%q: y %.4f, pdfkit %.4f", want.S, got[i].y, want.Y)
				}
				if d := abs(got[i].x - want.X); d > worst {
					worst, worstWhere = d, want.S
				}
				if d := abs(got[i].x - want.X); d > caseWorst {
					caseWorst, caseWorstS = d, want.S
				}
				if read < len(c.Reads) && want.S == c.Reads[read].S && isLegendLabel(c.Ops, i) {
					debt += c.Reads[read].Unkerned - c.Reads[read].Width
					read++
				}
			}
			t.Logf("worst x offset from pdfkit in this report: %.4f pt on %q", caseWorst, caseWorstS)
		})
	}
	t.Logf("largest raw x difference from pdfkit anywhere in the corpus: %.4f pt on %q", worst, worstWhere)
}

// alignFactor finds the alignment the live side used for the i'th non-empty
// positioned string, and turns it into the fraction of its own width the string
// starts in by.
// It also reports whether the call was a CONTINUED one -- two arguments rather
// than four -- because such a string has no x of its own and inherits the
// previous one's instead.
func alignFactor(ops []Op, idx int) (float64, bool) {
	n := -1
	for _, op := range ops {
		if op.Op != "text" {
			continue
		}
		if str, ok := op.Args[0].(string); !ok || str == "" {
			continue
		}
		n++
		if n != idx {
			continue
		}
		if len(op.Args) == 2 {
			return 0, true
		}
		o := optsOf(op.Args[len(op.Args)-1])
		if _, hasWidth := o["width"]; !hasWidth {
			return 0, false
		}
		switch o["align"] {
		case "center":
			return 0.5, false
		case "right":
			return 1, false
		}
		return 0, false
	}
	return 0, false
}

var anyTextRe = regexp.MustCompile(`BT\s+([0-9.-]+)\s+([0-9.-]+)\s+Td`)

type xy struct{ x, y float64 }

func allTextPositions(body string) []xy {
	var out []xy
	for _, m := range anyTextRe.FindAllStringSubmatch(body, -1) {
		var a, b float64
		_, _ = fmt.Sscanf(m[1], "%g", &a)
		_, _ = fmt.Sscanf(m[2], "%g", &b)
		out = append(out, xy{a, b})
	}
	return out
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// nthTextOp returns the string of the n'th non-empty text call.
func nthTextOp(ops []Op, n int) (string, bool) {
	i := -1
	for _, op := range ops {
		if op.Op != "text" {
			continue
		}
		str, ok := op.Args[0].(string)
		if !ok || str == "" {
			continue
		}
		i++
		if i == n {
			return str, true
		}
	}
	return "", false
}

// textBox returns the x and width of the box the n'th non-empty text call was
// aligned inside.
func textBox(ops []Op, n int) (float64, float64, bool) {
	i := -1
	for _, op := range ops {
		if op.Op != "text" {
			continue
		}
		str, ok := op.Args[0].(string)
		if !ok || str == "" {
			continue
		}
		i++
		if i != n {
			continue
		}
		if len(op.Args) != 4 {
			return 0, 0, false
		}
		x, ok1 := op.Args[1].(float64)
		w, ok2 := optsOf(op.Args[3])["width"].(float64)
		return x, w, ok1 && ok2
	}
	return 0, 0, false
}

// isLegendLabel is the n'th non-empty text call being a legend entry: the only
// positioned text in the document drawn with `{ lineBreak: false }` and nothing
// else. A table cell carries a width, a chart tick a width and an align.
func isLegendLabel(ops []Op, n int) bool {
	i := -1
	for _, op := range ops {
		if op.Op != "text" {
			continue
		}
		str, ok := op.Args[0].(string)
		if !ok || str == "" {
			continue
		}
		i++
		if i != n {
			continue
		}
		o := optsOf(op.Args[len(op.Args)-1])
		return len(op.Args) == 4 && len(o) == 1 && hasKey(o, "lineBreak")
	}
	return false
}
