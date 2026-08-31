package reportpdf

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
)

// The corpus recorded from the live `_render`. See tools/pdf-render-cases.js.
type renderCase struct {
	Name      string           `json:"name"`
	TZ        string           `json:"tz"`
	Title     string           `json:"title"`
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
	Meta      *rawMeta         `json:"meta"`
	Ops       []Op             `json:"ops"`
	Positions []struct {
		S        string  `json:"s"`
		X        float64 `json:"x"`
		Y        float64 `json:"y"`
		Size     float64 `json:"size"`
		Font     string  `json:"font"`
		Kerned   float64 `json:"kerned"`
		Unkerned float64 `json:"unkerned"`
	} `json:"positions"`
	Reads []struct {
		S        string  `json:"s"`
		Width    float64 `json:"width"`
		Unkerned float64 `json:"unkerned"`
	} `json:"reads"`
}

// rawMeta decodes the JavaScript object as written, so a key the live side omits
// stays omitted rather than becoming a zero the port might read differently.
type rawMeta struct {
	Router    string  `json:"router"`
	From      float64 `json:"from"`
	To        float64 `json:"to"`
	Stats     []Stat  `json:"-"`
	ChartData *Chart  `json:"-"`
	RawStats  []struct {
		Label string `json:"label"`
		Value any    `json:"value"`
	} `json:"stats"`
	RawChart *struct {
		YLabel string `json:"yLabel"`
		Lines  []struct {
			Label string  `json:"label"`
			Color string  `json:"color"`
			Pts   []Pt    `json:"pts"`
			_     float64 `json:"-"`
		} `json:"lines"`
	} `json:"chartData"`
}

func (m *rawMeta) toMeta() *Meta {
	if m == nil {
		return nil
	}
	out := &Meta{Router: m.Router, From: m.From, To: m.To}
	for _, s := range m.RawStats {
		out.Stats = append(out.Stats, Stat{Label: s.Label, Value: jsString(s.Value)})
	}
	if m.RawChart != nil {
		ch := &Chart{YLabel: m.RawChart.YLabel}
		for _, l := range m.RawChart.Lines {
			ch.Lines = append(ch.Lines, Line{Label: l.Label, Color: l.Color, Pts: l.Pts})
		}
		out.ChartData = ch
	}
	return out
}

func loadRenderCases(t *testing.T) []renderCase {
	t.Helper()
	b, err := os.ReadFile("../../testdata/pdf-render-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var doc struct {
		TZ    string       `json:"tz"`
		Cases []renderCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if doc.TZ != "UTC" {
		t.Fatalf("corpus was recorded in %q, not UTC -- its clock-dependent labels are unportable", doc.TZ)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("corpus is empty -- this test would pass against nothing")
	}
	return doc.Cases
}

// TestRenderDrawsWhatTheLiveRendererDraws is item 6's gate.
//
// It compares the DRAWING, call for call, not the PDF: pdfkit and fpdf lay out a
// file differently, so two byte-identical pages give two different files. Every
// op must match in name, arity and argument.
//
// The one licensed difference is the legend's x advance. `_render` walks it with
// `doc.widthOfString(label)`, which is KERNED on the live side and cannot be on
// this one. Rather than tolerate a fuzzy x, the test carries the difference as a
// running DEBT computed from the corpus's own recorded reads, and requires the
// residual to be exactly zero. A label with no kern pair contributes nothing, so
// most cases are compared with no correction at all -- and the correction can
// only ever move a legend coordinate, never a chart line or a table cell.
func TestRenderDrawsWhatTheLiveRendererDraws(t *testing.T) {
	for _, c := range loadRenderCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			tc := newTraceCanvas()
			Render(tc, c.Title, c.Columns, c.Rows, c.Meta.toMeta(), c.TZ)

			if len(tc.ops) != len(c.Ops) {
				t.Fatalf("drew %d calls, live drew %d\n%s", len(tc.ops), len(c.Ops), firstDiff(tc.ops, c.Ops))
			}

			// The kern debt, accumulated as legend labels are measured. Index i of
			// tc.reads pairs with index i of c.Reads: the same labels, in order.
			debt, read := 0.0, 0
			for i, want := range c.Ops {
				got := tc.ops[i]
				if got.Op != want.Op {
					t.Fatalf("call %d: drew %s, live drew %s\n%s", i, got.Op, want.Op, firstDiff(tc.ops, c.Ops))
				}
				// The allowance applies to ARG 0 ONLY -- the walked x -- and only on
				// the two ops the legend advance moves. A legend swatch is
				// `rect(legX, ..., 10, 6)`: its y, width and height are constants and
				// must still match exactly, which an allowance spread across every
				// numeric argument would have quietly stopped checking.
				allow, allowIdx := 0.0, -1
				if idx, ok := legendXArg(want); ok && debt != 0 {
					allow, allowIdx = debt, idx
				}
				if msg := argsDiffer(got.Args, want.Args, allow, allowIdx); msg != "" {
					t.Fatalf("call %d (%s): %s\n  drew %v\n  live %v", i, got.Op, msg, got.Args, want.Args)
				}
				// A measurement happens AFTER the legend text it follows, so the debt
				// grows only once the label has been drawn -- exactly as in _render.
				if got.Op == "text" && read < len(tc.reads) && read < len(c.Reads) &&
					len(got.Args) == 4 && got.Args[0] == c.Reads[read].S {
					debt += tc.reads[read] - c.Reads[read].Width
					read++
				}
			}

			if read != len(c.Reads) {
				t.Errorf("accounted for %d legend measurements, live made %d", read, len(c.Reads))
			}

			// THE MEASUREMENT ITSELF, not just its effect.
			//
			// Without this the gate is unfalsifiable here: the kern debt above is
			// derived from the port's OWN measurement, so a port that measured
			// badly produces a correction that cancels its own error exactly. That
			// is not a hypothetical -- dropping EncodeText from WidthOfString
			// survived mutation until this check existed.
			//
			// The live side records both numbers, and fpdf's answer must be the
			// UNKERNED one exactly, for the same reason metrics_test.go compares
			// against that column.
			for i, want := range c.Reads {
				if i >= len(tc.reads) {
					break
				}
				if tc.reads[i] != want.Unkerned {
					t.Errorf("measured %q as %.6f, live's own sum-of-parts is %.6f",
						want.S, tc.reads[i], want.Unkerned)
				}
			}
			if debt != 0 {
				t.Logf("kerning debt carried: %.6f pt over %d legend labels", debt, read)
			}
		})
	}
}

// isLegendAdvanced names the only two ops whose x is walked by the legend's
// `legX += 13 + widthOfString(label) + 16`, and so the only two that may inherit
// the kern debt: the colour swatch and the label beside it.
//
// Recognised by SHAPE against the LIVE op, deliberately: the swatch is the only
// rect in the document that is 10 by 6, and the legend label is the only
// positioned text drawn with `{ lineBreak: false }` and nothing else. A table
// cell carries a width and an align; a chart tick carries all three. So a
// mis-transcribed table or chart coordinate cannot reach this allowance.
// It returns WHICH argument is that x, because the two ops disagree: a swatch is
// `rect(legX, ...)` and its x is arg 0, while a label is `text(s, legX+13, ...)`
// and its x is arg 1. Allowing a fixed index would have licensed drift in the
// swatch's y and left the label's x compared exactly -- passing for the wrong
// reason on one and failing for the right reason on the other.
func legendXArg(want Op) (int, bool) {
	switch want.Op {
	case "rect":
		// The only 10x6 rect in the document.
		if len(want.Args) == 4 && want.Args[2] == 10.0 && want.Args[3] == 6.0 {
			return 0, true
		}
	case "text":
		// The only positioned text drawn with `{ lineBreak: false }` and nothing
		// else -- a table cell carries a width and an align, a chart tick all three.
		if len(want.Args) == 4 && len(optsOf(want.Args[3])) == 1 && hasKey(optsOf(want.Args[3]), "lineBreak") {
			return 1, true
		}
	}
	return -1, false
}

func optsOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func hasKey(m map[string]any, k string) bool { _, ok := m[k]; return ok }

func argsDiffer(got, want []any, allow float64, allowIdx int) string {
	if len(got) != len(want) {
		return fmt.Sprintf("arity %d, live %d", len(got), len(want))
	}
	for i := range got {
		a := 0.0
		if i == allowIdx {
			a = allow // the walked x, and nothing else in the call
		}
		if msg := valueDiffers(got[i], want[i], a); msg != "" {
			return fmt.Sprintf("arg %d: %s", i, msg)
		}
	}
	return ""
}

func valueDiffers(got, want any, allow float64) string {
	switch w := want.(type) {
	case float64:
		g, ok := got.(float64)
		if !ok {
			return fmt.Sprintf("got %T %v, live number %v", got, got, w)
		}
		if g == w || (allow != 0 && math.Abs(g-(w+allow)) < 1e-6) {
			return ""
		}
		return fmt.Sprintf("%v, live %v", g, w)
	case string:
		if g, ok := got.(string); ok && g == w {
			return ""
		}
		return fmt.Sprintf("%q, live %q", got, w)
	case bool:
		if g, ok := got.(bool); ok && g == w {
			return ""
		}
		return fmt.Sprintf("%v, live %v", got, w)
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Sprintf("got %T, live options object", got)
		}
		if len(g) != len(w) {
			return fmt.Sprintf("options %v, live %v", g, w)
		}
		for k, wv := range w {
			gv, present := g[k]
			if !present {
				return fmt.Sprintf("options missing %q (live %v)", k, w)
			}
			// The x-position allowance never applies inside an options object: a
			// `width` is a layout constant, not a walked coordinate.
			if msg := valueDiffers(gv, wv, 0); msg != "" {
				return fmt.Sprintf("option %q: %s", k, msg)
			}
		}
		return ""
	case nil:
		if got == nil {
			return ""
		}
		return fmt.Sprintf("got %v, live null", got)
	}
	return fmt.Sprintf("unhandled live arg %T %v", want, want)
}

// firstDiff points at the first call that differs, which is far more use than a
// dump of three thousand.
func firstDiff(got, want []Op) string {
	n := len(got)
	if len(want) < n {
		n = len(want)
	}
	for i := 0; i < n; i++ {
		if got[i].Op != want[i].Op || argsDiffer(got[i].Args, want[i].Args, 0, -1) != "" {
			return fmt.Sprintf("  first difference at call %d:\n    drew %s %v\n    live %s %v",
				i, got[i].Op, got[i].Args, want[i].Op, want[i].Args)
		}
	}
	if len(got) > len(want) {
		return fmt.Sprintf("  drew %d extra call(s), first: %s %v", len(got)-len(want), got[n].Op, got[n].Args)
	}
	if len(want) > len(got) {
		return fmt.Sprintf("  missed %d call(s), first: %s %v", len(want)-len(got), want[n].Op, want[n].Args)
	}
	return "  (no difference found)"
}
