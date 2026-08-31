package reportpdf

import "strings"

// cp1252High is the part of Windows-1252 that is NOT Latin-1: the 0x80-0x9F
// block, which ISO-8859-1 leaves as control codes and cp1252 fills with
// punctuation. Everything else is identity -- 0x00-0x7F is ASCII and 0xA0-0xFF
// is Latin-1 -- so only these 27 need a table.
//
// The em dash matters most: `_render` has no em dash of its own, but a router
// comment, a DHCP hostname or an interface description can carry one, and
// dropping it would silently shorten a table cell.
var cp1252High = map[rune]byte{
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84,
	'…': 0x85, '†': 0x86, '‡': 0x87, 'ˆ': 0x88,
	'‰': 0x89, 'Š': 0x8A, '‹': 0x8B, 'Œ': 0x8C,
	'Ž': 0x8E, '‘': 0x91, '’': 0x92, '“': 0x93,
	'”': 0x94, '•': 0x95, '–': 0x96, '—': 0x97,
	'˜': 0x98, '™': 0x99, 'š': 0x9A, '›': 0x9B,
	'œ': 0x9C, 'ž': 0x9E, 'Ÿ': 0x9F,
}

// EncodeText turns a Go (UTF-8) string into the cp1252 bytes fpdf measures and
// draws, and DROPS every rune cp1252 cannot hold.
//
// # Why dropping is the faithful choice, not the lazy one
//
// pdfkit gives a rune outside the standard-14 charset a width of ZERO and emits
// it as a raw two-byte glyph id the font has no glyph for. Measured, not
// assumed: `→` is 0.0000pt at every size, and "a→b" comes out as
//
//	[<61219262> 0] TJ
//
// So on the live side such a character occupies no horizontal space and puts no
// mark on the page. Dropping it here produces exactly that: the same advance,
// and the same nothing rendered. Substituting a `?` -- which is what a naive
// encoder does, and what fpdf's own translator does -- would be the divergence,
// because it would both draw a glyph the live app does not draw and push
// everything after it along by half an em.
//
// # The live defect this exposes
//
// `src/reports/pdf.js` builds its date range as `${from}  →  ${to}`, and that
// arrow is invisible and zero-width in every report PDF the live app has ever
// produced. Recorded in ../MikroDash/ToDo.md. The port reproduces the behaviour
// rather than quietly fixing it, per the porting rule; fixing it upstream is the
// live repo's call and would change what this gate compares against.
func EncodeText(s string) string {
	// Fast path: the overwhelming majority of report strings are ASCII.
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r < 0x80, r >= 0xA0 && r <= 0xFF:
			b.WriteByte(byte(r))
		default:
			if c, ok := cp1252High[r]; ok {
				b.WriteByte(c)
			}
			// else: dropped, matching pdfkit's zero-width notdef.
		}
	}
	return b.String()
}
