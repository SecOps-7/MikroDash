package reports

// The shape `src/reports/build.js` hands the PDF renderer as `pdf.meta`.
//
// These live HERE, with the code that produces them, rather than in
// internal/reportpdf which draws them: reportpdf already imports this package
// for TsFmt and ToFixed, so the other direction would be a cycle. reportpdf
// aliases them, so `reportpdf.Meta` still names this type.
type Meta struct {
	Router string
	// Epoch milliseconds, and ZERO MEANS ABSENT: the live side tests them with
	// `meta.from && meta.to`, and 0 is falsy in JavaScript.
	From      float64
	To        float64
	Stats     []Stat
	ChartData *Chart
}

// Stat is one of the boxes across the top. SIX IS THE MAXIMUM the layout can
// hold -- `_render` draws them with `lineBreak: false`, so a seventh starts
// truncating values instead of wrapping, and traffic and bandwidth already use
// all six.
type Stat struct {
	Label string
	Value string
}

type Chart struct {
	YLabel string
	Lines  []Line
}

type Line struct {
	Label string
	Color string
	Pts   []Pt
}

type Pt struct {
	X float64
	Y float64
}
