package reportpdf

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func brandPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func header(t *testing.T, brand Brand) []Op {
	t.Helper()
	tc := newTraceCanvas()
	Render(tc, "Traffic", []string{"a"}, []map[string]any{{"a": "1"}}, nil, "UTC", brand)
	return tc.ops
}

func textsOf(ops []Op) []string {
	var out []string
	for _, o := range ops {
		if o.Op == "text" && len(o.Args) > 0 {
			if s, ok := o.Args[0].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// THE DEFAULT HEADER IS THE WORDMARK, AND NOTHING ELSE CHANGED.
func TestADefaultBrandDrawsTheMikroDashWordmark(t *testing.T) {
	ops := header(t, Brand{})
	texts := textsOf(ops)
	if len(texts) < 2 || texts[0] != "Mikro" || texts[1] != "Dash" {
		t.Errorf("the default header draws %v, want the Mikro/Dash wordmark first", texts)
	}
	for _, o := range ops {
		if o.Op == "image" {
			t.Error("the default header draws an image; only an install's own icon may")
		}
	}
}

// A NAME REPLACES THE WORDMARK, IN ONE COLOUR.
func TestACustomNameReplacesTheWordmark(t *testing.T) {
	texts := textsOf(header(t, Brand{Name: "Acme Networks"}))
	if len(texts) == 0 || texts[0] != "Acme Networks" {
		t.Fatalf("the header draws %v, want the custom name first", texts)
	}
	for _, s := range texts {
		if s == "Mikro" || s == "Dash" {
			t.Errorf("the MikroDash wordmark is still drawn beside a custom name: %v", texts)
		}
	}
}

// THE ICON SITS AT THE LEFT MARGIN, AND THE NAME MOVES OVER FOR IT.
func TestACustomIconIsDrawnBesideTheName(t *testing.T) {
	icon := brandPNG(t)
	ops := header(t, Brand{Name: "Acme", Icon: icon})
	img, name := -1, -1
	for i, o := range ops {
		if o.Op == "image" && img < 0 {
			img = i
		}
		if o.Op == "text" && len(o.Args) > 1 && o.Args[0] == "Acme" && name < 0 {
			name = i
		}
	}
	if img < 0 || name < 0 || img > name {
		t.Fatalf("image at call %d, name at call %d: the icon must be drawn, before the name", img, name)
	}
	if got := ops[img].Args; got[0] != len(icon) || got[1] != r6(L) {
		t.Errorf("the icon is drawn as %v, want %d bytes at x=%v", got, len(icon), L)
	}
	if x := ops[name].Args[1]; x != r6(L+brandIconSize+brandIconGap) {
		t.Errorf("the name is drawn at x=%v, want %v so it clears the icon", x, r6(L+brandIconSize+brandIconGap))
	}
}

// A LONG NAME IS FITTED BEFORE THE CENTRED TITLE, NOT DRAWN ACROSS IT.
func TestALongNameFitsBeforeTheTitle(t *testing.T) {
	title := "Traffic"
	pw := newTraceCanvas().PageWidth()
	titleStart := L + (pw-L-R-measureBold(title, brandTitleSize))/2
	nameX := L + brandIconSize + brandIconGap

	if size, text := nameFit("Acme", title, nameX, pw); size != brandNameMaxSize || text != "Acme" {
		t.Errorf("a short name was fitted to %v %q; it should keep the full size and text", size, text)
	}

	long := "Acme Regional Network Operations Centre!"
	if len([]rune(long)) != 40 {
		t.Fatalf("the long name is %d characters, want the 40 the branding allows", len([]rune(long)))
	}
	ops := header(t, Brand{Name: long, Icon: brandPNG(t)})
	size, drawn := brandNameMaxSize, ""
	for _, o := range ops {
		if o.Op == "fontSize" && drawn == "" && len(o.Args) == 1 {
			if v, ok := o.Args[0].(float64); ok {
				size = v
			}
		}
		if o.Op == "text" && len(o.Args) > 2 && o.Args[1] == r6(nameX) {
			drawn = o.Args[0].(string)
			if y := o.Args[2]; y != r6(30+(brandNameMaxSize-size)/2) {
				t.Errorf("the fitted name is drawn at y=%v; at %vpt it should sit at %v, centred on the icon",
					y, size, r6(30+(brandNameMaxSize-size)/2))
			}
			break
		}
	}
	if drawn == "" {
		t.Fatal("the long name was not drawn at the name position")
	}
	if size < brandNameMinSize || size > brandNameMaxSize {
		t.Errorf("the long name is drawn at %vpt, outside %v to %v", size, brandNameMinSize, brandNameMaxSize)
	}
	if end := nameX + measureBold(drawn, size); end > titleStart-brandTitleGap+0.001 {
		t.Errorf("the name %q at %vpt ends at %.1fpt; the title starts at %.1fpt", drawn, size, end, titleStart)
	}
}

// AND THE ICON REACHES A REAL PDF.
//
// The trace records that an image was asked for; this is what proves fpdf can
// draw the PNG branding.Icon produces, rather than failing the whole report.
func TestABrandedHeaderRendersToARealPDF(t *testing.T) {
	cv, doc := NewFPDFCanvas()
	Render(cv, "Traffic", []string{"a"}, []map[string]any{{"a": "1"}}, nil, "UTC",
		Brand{Name: "Acme", Icon: brandPNG(t)})
	var out bytes.Buffer
	if err := Output(doc, &out); err != nil {
		t.Fatalf("a report with a brand icon failed to render: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("/Subtype /Image")) {
		t.Error("the rendered PDF holds no image object: the icon was not drawn")
	}
}
