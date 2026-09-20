package wgconfig

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ── THE SAMPLE IS A REAL ROUTER'S OUTPUT, ENCODING A FAKE CONFIG ───────────
//
// `testdata/router-qr.txt` is the `qr` value of
// `/interface/wireguard/peers/show-client-config` on a lab CHR (7.24.4). Its
// SHAPE is what this package has to survive, and nothing but a real router
// produces that.
//
// A QR ENCODES ITS PAYLOAD, so capturing one of a real peer would commit that
// peer's private key to a public repository — the exact thing
// `tools/capture-fixtures.js` aborts on, in a form its key-name rules cannot
// see, because the secret is inside a picture. So the peer's private key was set
// to a synthetic value (43 'A's, which RouterOS clamps to `…AEA=`) and its
// addresses to TEST-NET-2 before the capture. There is nothing real in it.
func sample(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "router-qr.txt"))
	if err != nil {
		t.Fatalf("the captured sample is missing; this package's only external "+
			"reference is gone: %v", err)
	}
	return string(b)
}

// A router's symbol parses, and reads as the QR it is.
func TestARoutersQRParses(t *testing.T) {
	m, err := ParseQR(sample(t))
	if err != nil {
		t.Fatalf("a real router's QR did not parse: %v", err)
	}
	// 61 modules, version 11 — the CROPPED symbol. The router's grid is 65
	// across because it draws two light modules of quiet zone on each side, and
	// counting those is the off-by-one `crop` exists for.
	if m.Size != 61 || m.Version() != 11 {
		t.Errorf("parsed a %d-module symbol (version %d); the captured one is 61 (version 11)",
			m.Size, m.Version())
	}

	// ── THE FINDER PATTERN IS THE PROOF THE READING IS RIGHT WAY UP ────────
	//
	// `#` is LIGHT and a space is DARK, which is the opposite of what the eye
	// expects, and getting it backwards produces a symbol that still LOOKS like
	// a QR and scans as nothing. Every QR carries a 7x7 finder in three
	// corners: a dark ring, a light ring inside it, and a dark 3x3 centre. Read
	// inverted, all three tests below fail — which is why this is asserted
	// rather than the module count alone.
	for _, c := range []struct {
		name     string
		row, col int
	}{
		{"top left", 0, 0},
		{"top right", 0, m.Size - 7},
		{"bottom left", m.Size - 7, 0},
	} {
		for i := 0; i < 7; i++ {
			if !m.Dark(c.row, c.col+i) || !m.Dark(c.row+6, c.col+i) {
				t.Errorf("the %s finder's outer ring is not dark at offset %d — the light and "+
					"dark readings are swapped", c.name, i)
				break
			}
		}
		if m.Dark(c.row+1, c.col+1) {
			t.Errorf("the %s finder's inner ring is dark; it must be light", c.name)
		}
		if !m.Dark(c.row+3, c.col+3) {
			t.Errorf("the %s finder's centre is light; it must be dark", c.name)
		}
	}
}

// A SYMBOL IS NOT ALL ONE COLOUR. A parse that returned every module dark, or
// every module light, would satisfy a size check and render a solid square.
func TestTheParsedSymbolHasBothKindsOfModule(t *testing.T) {
	m, err := ParseQR(sample(t))
	if err != nil {
		t.Fatal(err)
	}
	dark := 0
	for row := 0; row < m.Size; row++ {
		for col := 0; col < m.Size; col++ {
			if m.Dark(row, col) {
				dark++
			}
		}
	}
	total := m.Size * m.Size
	// A QR's mask selection drives the dark share towards half; anything
	// outside a fifth to four fifths is not a symbol.
	if dark*5 < total || dark*5 > total*4 {
		t.Errorf("%d of %d modules are dark; that is not a QR symbol", dark, total)
	}
}

// grid builds what a router emits: a symbol of `size` modules inside a light
// quiet zone, each module repeated `width` characters across. For the cases a
// single captured sample cannot cover — other versions, other cell widths, and
// input that is not a symbol at all.
//
// `dark` is called in SYMBOL coordinates, so a caller's pattern is unaffected by
// the border. A checkerboard leaves the symbol's own edge mixed, which is what
// stops `crop` eating into it.
const testQuiet = 2

func grid(size, width int, dark func(row, col int) bool) string {
	side := size + testQuiet*2
	var b strings.Builder
	for row := 0; row < side; row++ {
		for col := 0; col < side; col++ {
			ch := "#" // light
			r, c := row-testQuiet, col-testQuiet
			if r >= 0 && c >= 0 && r < size && c < size && dark != nil && dark(r, c) {
				ch = " " // dark
			}
			b.WriteString(strings.Repeat(ch, width))
		}
	}
	return b.String()
}

// EVERY CELL WIDTH AND A SPREAD OF VERSIONS. The router emits two characters per
// module today; that is undocumented and free to change, which is why the width
// is solved for rather than assumed.
func TestTheGeometryIsDerivedNotAssumed(t *testing.T) {
	checker := func(row, col int) bool { return (row+col)%2 == 0 }
	for _, size := range []int{21, 45, 65, 69, 177} {
		for width := 1; width <= 4; width++ {
			m, err := ParseQR(grid(size, width, checker))
			if err != nil {
				t.Errorf("a %d-module symbol at %d characters per module did not parse: %v",
					size, width, err)
				continue
			}
			if m.Size != size {
				t.Errorf("parsed %d modules from a %d-module symbol at width %d",
					m.Size, size, width)
				continue
			}
			// The pattern survived the reshape: a width solved wrongly would
			// still produce a square of the right size from some inputs.
			if !m.Dark(2, 2) || m.Dark(2, 3) {
				t.Errorf("the modules are scrambled at size %d width %d", size, width)
			}
		}
	}
}

// A GRID THAT IS NOT A SYMBOL IS REFUSED, NOT DRAWN. Every case here would
// otherwise render as a plausible QR that scans to nothing.
func TestAMalformedQRIsRefused(t *testing.T) {
	checker := func(row, col int) bool { return (row+col)%2 == 0 }
	good := grid(21, 2, checker)
	for _, c := range []struct{ name, in string }{
		{"empty", ""},
		{"a character that is not a module", good[:100] + "X" + good[101:]},
		{"a length that is no square at any width", strings.Repeat("#", 8451)},
		{"a square that is not a QR size", grid(64, 1, checker)},
		// A grid of one colour survives every arithmetic check at its full
		// size — there is no quiet zone to strip — and would render as a solid
		// box that scans to nothing.
		{"all dark", strings.Repeat(" ", 65*65*2)},
		{"all light", strings.Repeat("#", 65*65*2)},
	} {
		if _, err := ParseQR(c.in); err == nil {
			t.Errorf("%s parsed; a symbol that is wrong must be refused rather than "+
				"rendered as one that scans to nothing", c.name)
		}
	}
}

// ── THE OUTPUT ALPHABET IS CLOSED ──────────────────────────────────────────
//
// The SVG reaches the browser's innerHTML beside router-derived data, so what
// makes that defensible is that it CANNOT carry anything but shapes and
// integers. Built entirely from the matrix, never from the payload.
func TestTheSVGIsOnlyShapesAndIntegers(t *testing.T) {
	m, err := ParseQR(sample(t))
	if err != nil {
		t.Fatal(err)
	}
	svg := m.SVG()
	ok := regexp.MustCompile(`^<svg [^<>]*>(<rect[^<>]*/>)+</svg>$`)
	if !ok.MatchString(svg) {
		t.Errorf("the SVG is not only <svg> and <rect>: %.200s", svg)
	}
	if strings.Contains(svg, "script") || strings.Contains(svg, "&") {
		t.Errorf("the SVG carries something that is not a shape: %.200s", svg)
	}
	// A WHITE FIELD, and this is not decoration: the app has a dark theme, and
	// dark modules on a transparent background are unscannable. No other test
	// here could see that — they would all find the modules present.
	if !strings.Contains(svg, `fill="#ffffff"`) {
		t.Error("the SVG has no light background; on a dark page it is unscannable")
	}
	// The quiet zone is in the viewBox, not merely hoped for.
	if !strings.Contains(svg, `viewBox="0 0 69 69"`) {
		t.Errorf("a 61-module symbol with a 4-module quiet zone should be 69 across: %.80s", svg)
	}
}

// A ROUND TRIP THROUGH THE SVG, so "the modules are drawn where the matrix says"
// is checked rather than assumed. Reads the rects back out and compares every
// module against the matrix.
func TestTheSVGDrawsExactlyTheDarkModules(t *testing.T) {
	m, err := ParseQR(sample(t))
	if err != nil {
		t.Fatal(err)
	}
	drawn := map[[2]int]bool{}
	re := regexp.MustCompile(`<rect x="(\d+)" y="(\d+)" width="(\d+)" height="1"`)
	for _, g := range re.FindAllStringSubmatch(m.SVG(), -1) {
		x, y, w := atoi(g[1]), atoi(g[2]), atoi(g[3])
		for i := 0; i < w; i++ {
			drawn[[2]int{y - quiet, x + i - quiet}] = true
		}
	}
	if len(drawn) == 0 {
		t.Fatal("no modules were drawn at all")
	}
	for row := 0; row < m.Size; row++ {
		for col := 0; col < m.Size; col++ {
			if m.Dark(row, col) != drawn[[2]int{row, col}] {
				t.Fatalf("module (%d,%d) is dark=%v in the matrix and drawn=%v in the SVG",
					row, col, m.Dark(row, col), drawn[[2]int{row, col}])
			}
		}
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
