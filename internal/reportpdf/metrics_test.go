package reportpdf

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/go-pdf/fpdf"
)

// The corpus pdfkit generated. `Width` is what pdfkit measures, kerning and all;
// `Unkerned` is the sum of its own per-character widths.
type metricCase struct {
	Font     string  `json:"font"`
	Size     float64 `json:"size"`
	S        string  `json:"s"`
	Width    float64 `json:"width"`
	Unkerned float64 `json:"unkerned"`
}

func loadMetrics(t *testing.T) []metricCase {
	t.Helper()
	b, err := os.ReadFile("../../testdata/pdf-metrics-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var doc struct {
		Cases []metricCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("corpus is empty -- this test would pass against nothing")
	}
	return doc.Cases
}

// measurer hands back an fpdf configured the way the report configures it, plus
// a width function. A4 in points, because that is the unit `_render` works in.
func measurer(t *testing.T) func(font string, size float64, s string) float64 {
	t.Helper()
	p := fpdf.New("P", "pt", "A4", "")
	p.AddPage()
	// EVERY string handed to fpdf goes through this first.
	//
	// fpdf walks a string BYTE by byte against a cp1252 width table, so a Go
	// string -- which is UTF-8 -- makes "Café" five characters wide and draws
	// mojibake. The translator is fpdf's documented answer and the port must use
	// it on every draw and every measurement, not just the ones with an obvious
	// accent: a router identity, an interface comment or a DHCP hostname can
	// carry one, and nothing upstream constrains them to ASCII.
	//
	// This gate found that by measuring rather than by reading the docs -- the
	// non-ASCII rows of the corpus failed by up to 23pt before it was applied.
	// EncodeText, not fpdf's own UnicodeTranslatorFromDescriptor: the translator
	// SUBSTITUTES for a rune cp1252 cannot hold, and pdfkit DROPS it (zero width,
	// no glyph). See EncodeText's comment -- that difference is what the last
	// failing corpus row, the date range's `→`, was measuring.
	return func(font string, size float64, s string) float64 {
		style := ""
		if font == "Helvetica-Bold" {
			style = "B"
		}
		p.SetFont("Helvetica", style, size)
		w := p.GetStringWidth(EncodeText(s))
		if err := p.Error(); err != nil {
			t.Fatalf("fpdf error measuring %q: %v", s, err)
		}
		return w
	}
}

// TestTheWidthTableIsPdfkits is the assertion the dependency was chosen on.
//
// Every x in the report is a function of a measured string -- a centred title, a
// right-aligned date range, five centred axis ticks, a legend walked along by
// widthOfString. If the two width tables differed, no amount of care in the
// renderer would put a glyph in the same place.
//
// It compares against Unkerned, not Width: fpdf does not kern, so the sum of
// per-character widths is the like-for-like quantity. TestTheKerningGapIsStillReal
// below owns the difference.
func TestTheWidthTableIsPdfkits(t *testing.T) {
	width := measurer(t)
	// Both sides do the same arithmetic in float64 -- sum of integer glyph units,
	// divided by 1000, times the size -- so the tolerance is for the last bit of
	// the mantissa and nothing else. It is NOT a fudge factor for a real
	// disagreement: a single wrong glyph width is 0.001 em, which at the report's
	// smallest size (7pt) is 7e-6 and fails this comfortably.
	const eps = 1e-9
	var worst float64
	var worstCase metricCase
	for _, c := range loadMetrics(t) {
		got := width(c.Font, c.Size, c.S)
		if d := math.Abs(got - c.Unkerned); d > worst {
			worst, worstCase = d, c
		}
		if math.Abs(got-c.Unkerned) > eps {
			t.Errorf("%s %gpt %q: fpdf %.6f, pdfkit(unkerned) %.6f", c.Font, c.Size, c.S, got, c.Unkerned)
		}
	}
	t.Logf("largest disagreement %.3g pt on %q at %s %gpt", worst, worstCase.S, worstCase.Font, worstCase.Size)
}

// TestTheKerningGapIsStillReal holds the KNOWN divergence open.
//
// This project's rule is that a gap is documented, never hidden, and that the
// note asserts the gap STILL EXISTS so closing it fails the suite. Applied to a
// dependency: if fpdf ever learns to kern, or pdfkit stops, this fails and
// doc.go's "the one divergence" section has to be rewritten rather than left
// describing a world that moved on.
func TestTheKerningGapIsStillReal(t *testing.T) {
	width := measurer(t)
	cases := loadMetrics(t)

	var kerned, agreed int
	var worstWidth float64
	var worstCase metricCase
	for _, c := range cases {
		if math.Abs(c.Width-c.Unkerned) > 1e-9 {
			kerned++
			if d := c.Unkerned - c.Width; d > worstWidth {
				worstWidth, worstCase = d, c
			}
		} else {
			agreed++
		}
	}
	if kerned == 0 {
		t.Fatal("no corpus string is kerned any more -- delete the divergence section in doc.go")
	}
	if agreed == 0 {
		t.Fatal("every corpus string is kerned -- nothing here shows the tables agree absent a pair")
	}

	// And fpdf must still be the unkerned side. If it started kerning, the
	// width-table test above would go red without saying why; this says why.
	if got := width(worstCase.Font, worstCase.Size, worstCase.S); math.Abs(got-worstCase.Width) < 1e-9 {
		t.Fatalf("fpdf now agrees with pdfkit's KERNED width for %q -- it has learned to kern, "+
			"and doc.go's divergence section is out of date", worstCase.S)
	}

	t.Logf("%d of %d corpus strings kerned; worst width gap %.3f pt on %q at %s %gpt",
		kerned, len(cases), worstWidth, worstCase.S, worstCase.Font, worstCase.Size)
}
