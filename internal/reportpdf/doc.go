// Package reportpdf renders the report PDF that `src/reports/pdf.js` renders.
//
// The document is small and entirely absolute-positioned: a header bar, a meta
// row, a row of stat boxes, an optional line chart, and a paginated table. No
// images, no embedded fonts, no flowing text -- every `text()` call on the live
// side passes `lineBreak: false` and does its own alignment inside a width box.
//
// # The dependency
//
// `github.com/go-pdf/fpdf` (MIT, pure Go, no cgo) is the fifth dependency, added
// on 2026-08-25 with the operator's decision on record. It is the maintained
// fork of the archived `jung-kurt/gofpdf`. What earned it a place, in the terms
// CLAUDE.md sets for a dependency:
//
//   - It has every primitive `_render` needs and no more work is required to
//     reach them: filled and stroked rectangles, RoundedRect, MoveTo/LineTo
//     paths, ClipRect/ClipEnd for the chart, the standard-14 faces in regular
//     and bold, CellFormat with left/centre/right alignment inside a width, and
//     GetStringWidth for the legend advance. Each was compiled and run before
//     the library was chosen, not read off its README.
//   - Its Helvetica metrics ARE pdfkit's. Both take the Adobe standard-14 AFM
//     widths and agree exactly -- `AV` at size 11 is 14.6740 on both sides. That
//     is the property that decides whether the port can reproduce a document
//     whose every x is a function of a measured string, and it is pinned by
//     metrics_test.go against a corpus pdfkit itself generated.
//   - It needs no font files at runtime, so the static binary stays static and
//     stays small: the standard-14 metrics are compiled in.
//
// Its transitive requires (barcode, gofpdi, pdf417, x/image) are for features
// this port does not use; module graph pruning keeps them out of the build.
//
// # The one divergence, measured
//
// pdfkit KERNS and fpdf cannot. For "AVTo" pdfkit emits
//
//	[<41> 70 <5654> 120 <6f> 0] TJ
//
// and fpdf emits `(AVTo) Tj`. There is no kerning anywhere in fpdf v0.9.0 and no
// public route to the content stream, so this is an absent capability rather
// than an unmade call.
//
// The consequence was measured rather than estimated. Text runs render up to
// ~1.4pt wider unkerned; because `_render` centres rather than right-aligns, the
// worst POSITION error across every measured element of a real report is 0.65pt
// -- 0.23 mm -- on a centred title. Every right-aligned and advance-driven
// element measured exactly zero, because timestamps, "1.5k" and interface names
// contain no kern pairs at all.
//
// That number is why this port keeps fpdf instead of hand-writing a content
// stream: the alternative buys 0.23 mm for a PDF writer this project would then
// own. It is recorded here rather than left to be rediscovered, and
// metrics_test.go asserts the gap STILL EXISTS -- an fpdf that learned to kern
// fails the suite and forces this section to be deleted rather than left lying.
package reportpdf
