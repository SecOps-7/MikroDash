// Package wgconfig turns a WireGuard peer's client configuration into something
// a phone can scan.
//
// ── THE ROUTER ALREADY DRAWS THE QR, AND THAT DECIDED THIS PACKAGE ─────────
//
// `/interface/wireguard/peers/show-client-config` (RouterOS 7.12.2+) returns the
// finished client `.conf` in a `conf` value, and — with `show-sensitive=yes` —
// a rendered QR SYMBOL in a `qr` value. It is the same command the console and
// Winbox use for their own QR display.
//
// The plan for this feature was a hand-written QR encoder here: Reed-Solomon
// over GF(256), block interleaving, mask scoring, the lot. About seven hundred
// lines whose correctness could only be argued against a specification, since a
// wrong QR renders perfectly and scans to garbage. Measuring the router first
// made all of it unnecessary. What is left is geometry: reshape the router's
// grid and draw it.
//
// That is strictly better than encoding it here. There is no algorithm to own,
// no eighth dependency, and the symbol is produced by the same code MikroTik
// ships to its own clients — correct by construction rather than by this app
// agreeing with a spec.
//
// ── THE FORMAT, MEASURED RATHER THAN DOCUMENTED ────────────────────────────
//
// Nothing in MikroTik's documentation describes the `qr` value's shape, so it
// was read off a lab CHR (7.24.4):
//
//   - one flat string, NO line breaks;
//   - two characters only. `#` is LIGHT and a space is DARK, which is the
//     opposite of what the eye expects. The proof is in the value itself: the
//     border is entirely `#`, and a QR's quiet zone is light by definition. The
//     finder patterns read correctly only under that reading.
//   - each module is repeated horizontally. Two characters wide on every sample
//     taken, which is what a terminal needs to look square — but DERIVED here
//     rather than assumed, because it is undocumented and free to change.
//   - the symbol's size follows the payload. Versions 11, 12 and 13 were all
//     produced by one peer as its endpoint and DNS lines grew.
package wgconfig

import (
	"fmt"
	"strings"
)

// Matrix is a QR symbol: a square grid of modules, dark or light.
type Matrix struct {
	// Size is the module count on a side: 21 for version 1, 177 for version 40.
	Size int
	dark []bool
}

// Dark reports whether one module is dark. Out of range is light, so a caller
// drawing a margin does not have to bounds-check every cell.
func (m Matrix) Dark(row, col int) bool {
	if row < 0 || col < 0 || row >= m.Size || col >= m.Size {
		return false
	}
	return m.dark[row*m.Size+col]
}

// Version is the QR version this symbol is, 1 to 40.
func (m Matrix) Version() int { return (m.Size - 17) / 4 }

// validSize reports whether n is a QR symbol's module count: 21, 25 … 177.
func validSize(n int) bool { return n >= 21 && n <= 177 && (n-17)%4 == 0 }

// ParseQR reshapes RouterOS's `qr` value into a matrix.
//
// ── THE GEOMETRY IS DERIVED, AND THE ALTERNATIVE IS WHY ────────────────────
//
// A constant 130-character row would have worked on every sample taken and
// would break silently the day a router emits a different symbol — not with an
// error, but with a scrambled grid that still renders as a plausible QR. So the
// cell width is SOLVED for instead: the length must factor as modules² × width,
// the module count must be one a QR can actually have, and the border must be
// the light quiet zone every symbol carries. A grid that fails any of those is
// refused rather than drawn.
func ParseQR(s string) (Matrix, error) {
	if s == "" {
		return Matrix{}, fmt.Errorf("the router returned no QR code")
	}
	if i := strings.IndexFunc(s, func(r rune) bool { return r != '#' && r != ' ' }); i >= 0 {
		return Matrix{}, fmt.Errorf("the QR code holds %q at offset %d; only '#' and ' ' were expected",
			s[i:i+1], i)
	}
	for width := 1; width <= 8; width++ {
		if len(s)%width != 0 {
			continue
		}
		cells := len(s) / width
		side := isqrt(cells)
		if side*side != cells {
			continue
		}
		full := Matrix{Size: side, dark: make([]bool, cells)}
		for row := 0; row < side; row++ {
			for col := 0; col < side; col++ {
				// A space is DARK — see the package header.
				full.dark[row*side+col] = s[(row*side+col)*width] == ' '
			}
		}
		if m, ok := crop(full); ok {
			return m, nil
		}
	}
	return Matrix{}, fmt.Errorf("the QR code is %d characters, which is no square QR symbol at any "+
		"cell width; the router's format has changed", len(s))
}

// crop removes the quiet zone the router draws around the symbol, and is the
// check that makes the geometry solution unambiguous rather than merely
// arithmetic.
//
// ── THE BORDER IS NOT PART OF THE SYMBOL, AND COUNTING IT IS AN OFF-BY-ONE ─
//
// RouterOS surrounds its QR with two light modules. Returning the grid whole
// made `Size` four too large and `Version` one too high — a symbol reported as
// version 12 that is really version 11. NOTHING DOWNSTREAM WOULD HAVE NOTICED:
// the SVG draws whatever it is given, and a QR with a wider quiet zone scans
// perfectly well. It was caught by asserting where the FINDER PATTERN is, which
// is the only assertion that could see it.
//
// The border's width is MEASURED rather than assumed to be two, and the symbol
// inside it must be a size a QR can actually have — so a wrongly solved cell
// width, which produces a scrambled grid with a ragged border, is rejected here
// rather than drawn.
func crop(m Matrix) (Matrix, bool) {
	q := 0
	for q*2 < m.Size && lightRing(m, q) {
		q++
	}
	size := m.Size - q*2
	if !validSize(size) {
		return Matrix{}, false
	}
	// A GRID OF ONE COLOUR IS NOT A SYMBOL. An all-dark square has no quiet
	// zone to strip, so it survives every check above at its full size and
	// would render as a solid black box that scans to nothing.
	if !mixed(m) {
		return Matrix{}, false
	}
	out := Matrix{Size: size, dark: make([]bool, size*size)}
	for row := 0; row < size; row++ {
		for col := 0; col < size; col++ {
			out.dark[row*size+col] = m.Dark(row+q, col+q)
		}
	}
	return out, true
}

// mixed reports whether a grid carries both kinds of module.
func mixed(m Matrix) bool {
	first := m.Dark(0, 0)
	for i := range m.dark {
		if m.dark[i] != first {
			return true
		}
	}
	return false
}

// lightRing reports whether the ring `i` modules in from the edge is all light.
func lightRing(m Matrix, i int) bool {
	last := m.Size - 1 - i
	for k := i; k <= last; k++ {
		if m.Dark(i, k) || m.Dark(last, k) || m.Dark(k, i) || m.Dark(k, last) {
			return false
		}
	}
	return true
}

func isqrt(n int) int {
	r := 0
	for (r+1)*(r+1) <= n {
		r++
	}
	return r
}

// quiet is the margin drawn around the symbol, in modules.
//
// The specification's minimum is four. RouterOS already includes two of its
// own, so this is generous rather than exact — a wider quiet zone never harms a
// scan, and being short of one does.
const quiet = 4

// SVG draws the matrix as a standalone inline SVG.
//
// ── THE BACKGROUND RECT IS NOT DECORATION ──────────────────────────────────
//
// This app has a dark theme. A QR of dark modules on a transparent background
// renders as dark-on-dark and is unscannable, and no test in this repository
// could see that — it would assert the modules are present, and they would be.
// So the symbol carries its own white field.
//
// One `<rect>` per run of dark modules rather than a path: the output is then
// only `<svg>`, `<rect>` and integers, which is what makes it safe to put into
// `innerHTML` beside router-derived data.
func (m Matrix) SVG() string {
	side := m.Size + quiet*2
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" `+
		`shape-rendering="crispEdges" role="img" aria-label="WireGuard client configuration">`, side, side)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#ffffff"/>`, side, side)
	for row := 0; row < m.Size; row++ {
		// RUN-LENGTH ALONG EACH ROW: one rect per horizontal run of dark
		// modules rather than one per module. A version 13 symbol is 69x69,
		// which is 4761 rects at worst and roughly a tenth of that in practice.
		col := 0
		for col < m.Size {
			if !m.Dark(row, col) {
				col++
				continue
			}
			run := 1
			for col+run < m.Size && m.Dark(row, col+run) {
				run++
			}
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="1" fill="#000000"/>`,
				col+quiet, row+quiet, run)
			col += run
		}
	}
	b.WriteString(`</svg>`)
	return b.String()
}
