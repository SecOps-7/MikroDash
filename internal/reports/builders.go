package reports

import (
	"math"
	"sort"
	"strconv"
)

// PDFBuild is what one report section hands the renderer.
//
// The CSV half of `src/reports/build.js` is not here: it is already ported and
// pinned by export_test.go, and the two shapes genuinely differ — the ping CSV
// emits `ts,target,rtt_ms,loss_pct` while the ping PDF emits
// `Timestamp,Target,RTT (ms),Loss (%)` over a remapped object.
type PDFBuild struct {
	Title     string
	RowCount  int
	Truncated bool
	Columns   []string
	Rows      []map[string]any
	Meta      Meta
}

// Rows arrive as maps because that is what the export path already produces and
// what the corpus records. num reports absence and JSON null the same way the
// live `!= null` test does.
func num(m map[string]any, k string) (float64, bool) {
	v, ok := m[k]
	if !ok || v == nil {
		return 0, false
	}
	f, ok := v.(float64)
	return f, ok
}

func str(m map[string]any, k string) string {
	if v, ok := m[k]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// numOrZero is JavaScript reading a possibly-null number in ARITHMETIC.
//
// `0 + null` is 0 and `null < 1` is true, so an absent value participates as
// zero rather than being skipped. That is not a nicety: the ping report's
// `losses` array is `rows.map(r => r.loss_pct)` with no filter, so a null loss
// is counted in the denominator, sums as nothing, and passes the `< 1` test —
// reading as a perfectly good sample in all three of Uptime, Avg Loss and the
// count. A port that treated it as missing would disagree on every one.
func numOrZero(m map[string]any, k string) float64 {
	f, _ := num(m, k)
	return f
}

// dash is `v !== '—' ? v + unit : '—'`, the shape every stat box uses.
func dash(v, unit string) string {
	if v == "—" {
		return "—"
	}
	return v + unit
}

// n1 is `(v) => (v == null ? '—' : v.toFixed(1))`.
func n1(v float64, ok bool) string {
	if !ok {
		return "—"
	}
	return ToFixed(v, 1)
}

func thinRows(rows []map[string]any) []map[string]any { return Thin(rows) }

// ── ping ────────────────────────────────────────────────────────────────────

func BuildPing(rows []map[string]any, routerLabel string, from, to float64, tz string) PDFBuild {
	var rtts []float64
	var lossSum float64
	good := 0
	for _, r := range rows {
		if v, ok := num(r, "rtt_ms"); ok {
			rtts = append(rtts, v)
		}
		// UNFILTERED, deliberately -- see numOrZero.
		l := numOrZero(r, "loss_pct")
		lossSum += l
		if l < 1 {
			good++
		}
	}

	avgRtt, maxRtt := "—", "—"
	if len(rtts) > 0 {
		var s float64
		for _, v := range rtts {
			s += v
		}
		avgRtt = ToFixed(s/float64(len(rtts)), 1)
		maxRtt = ToFixed(MaxOf(rtts), 1)
	}
	avgLoss, uptime := "—", "—"
	if len(rows) > 0 {
		avgLoss = ToFixed(lossSum/float64(len(rows)), 1)
		uptime = ToFixed(float64(good)/float64(len(rows))*100, 1) + "%"
	}

	columns := []string{"Timestamp", "Target", "RTT (ms)", "Loss (%)"}
	table := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		// `r.rtt_ms ?? ''` -- nullish, NOT `||`, so a genuine 0 survives.
		var rtt any = ""
		if v, ok := num(r, "rtt_ms"); ok {
			rtt = v
		}
		table = append(table, map[string]any{
			"Timestamp": TsFmt(int64(numOrZero(r, "ts")), tz),
			"Target":    str(r, "target"),
			"RTT (ms)":  rtt,
			// Passed straight through, so a null stays a null and renders ''.
			"Loss (%)": r["loss_pct"],
		})
	}
	capped, truncated := CapRows(table, columns)

	sub := thinRows(rows)
	rtt := Line{Label: "RTT ms", Color: "#38bdf8"}
	loss := Line{Label: "Loss %", Color: "#f87171"}
	for _, r := range sub {
		x := numOrZero(r, "ts")
		if v, ok := num(r, "rtt_ms"); ok {
			rtt.Pts = append(rtt.Pts, Pt{X: x, Y: v})
		}
		// A null y is kept as a POINT with y 0: `_render` compares and scales it
		// with `<` and `-`, both of which coerce null to 0, so dropping it would
		// change the series length and keeping it as 0 is exact.
		loss.Pts = append(loss.Pts, Pt{X: x, Y: numOrZero(r, "loss_pct")})
	}

	return PDFBuild{
		Title: "Ping Stability Report", RowCount: len(rows), Truncated: truncated,
		Columns: columns, Rows: capped,
		Meta: Meta{
			Router: routerLabel, From: from, To: to,
			Stats: []Stat{
				{"Uptime", uptime},
				{"Avg RTT", dash(avgRtt, " ms")},
				{"Max RTT", dash(maxRtt, " ms")},
				{"Avg Loss", dash(avgLoss, "%")},
				{"Samples", groupDigits(len(rows))},
			},
			ChartData: &Chart{YLabel: "ms / %", Lines: []Line{rtt, loss}},
		},
	}
}

// ── traffic ─────────────────────────────────────────────────────────────────

// IfaceSummary is the part of the live `ifaceSummary` the PDF reads. The SQL
// behind it is already ported (db.TrafficSummary / db.BandwidthSummary); what is
// here is the derivation on top.
type IfaceSummary struct {
	RxAvgMbps, TxAvgMbps *float64
	RxMaxMbps, TxMaxMbps *float64
	RxP95Mbps, TxP95Mbps *float64
	// Totals are plain numbers defaulting to ZERO where the maxima are pointers,
	// matching db.BandwidthSummaryRow and its reasoning: "no bytes" is a total of
	// zero but an ABSENT maximum. The live side reads them through
	// `s.rxTotalMb || 0`, so null and 0 render identically and nothing is lost.
	RxTotalMb, TxTotalMb     float64
	RxMaxMb, TxMaxMb         *float64
	BandwidthSamples         int
	CapacityDown, CapacityUp int
}

// The percentage rule is `UtilPct` in params.go, already ported and already
// gated. NOT a second copy here: it took three attempts to get right there --
// Go's tie-to-even and an intermediate x10 rounding both produced plausible
// wrong answers -- and a duplicate would have to relearn that.
//
// What matters at THIS layer is only that it is not clamped at 100: the live
// comment is explicit that over-capacity is the signal worth seeing, and a
// corpus case reads "151% / 203%".

// jsParse is the unary `+` the live code applies to a toFixed result --
// `+r.rx_mbps.toFixed(1)` -- turning "10.2" back into a number so the PDF cell
// holds 10.2 rather than the string "10.2". The distinction is visible: a
// number renders without its trailing zero, so 12.0 prints as "12".
func jsParse(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

// jsRound is `Math.round`, which is NOT Go's math.Round.
//
// JavaScript rounds a half toward POSITIVE INFINITY; Go rounds it away from
// zero. They agree everywhere except an exact negative half, where
// `Math.round(-0.5)` is -0 and `math.Round(-0.5)` is -1. The traffic report's
// "Peak Util" box is `Math.round(pct)`, and a negative percentage means the
// counter went backwards -- rare, but the report renders it rather than hiding
// it, so the two must agree there too.
func jsRound(v float64) float64 { return math.Floor(v + 0.5) }

func BuildTraffic(rows []map[string]any, s IfaceSummary, routerLabel string, from, to float64, tz string) PDFBuild {
	avgRx, avgTx := n1p(s.RxAvgMbps), n1p(s.TxAvgMbps)
	peakRx, peakTx := n1p(s.RxMaxMbps), n1p(s.TxMaxMbps)

	columns := []string{"Timestamp", "Interface", "RX (Mbps)", "TX (Mbps)"}
	table := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		table = append(table, map[string]any{
			"Timestamp": TsFmt(int64(numOrZero(r, "ts")), tz),
			"Interface": str(r, "interface"),
			"RX (Mbps)": jsParse(ToFixed(numOrZero(r, "rx_mbps"), 1)),
			"TX (Mbps)": jsParse(ToFixed(numOrZero(r, "tx_mbps"), 1)),
		})
	}
	capped, truncated := CapRows(table, columns)

	util := "—"
	if p := UtilPct(s.RxMaxMbps, s.CapacityDown); p != nil {
		util = ToFixed(jsRound(*p), 0) + "% / " +
			ToFixed(jsRound(*UtilPct(s.TxMaxMbps, s.CapacityUp)), 0) + "%"
	}

	sub := thinRows(rows)
	rx := Line{Label: "RX Mbps", Color: "#38bdf8"}
	tx := Line{Label: "TX Mbps", Color: "#4ade80"}
	for _, r := range sub {
		x := numOrZero(r, "ts")
		rx.Pts = append(rx.Pts, Pt{X: x, Y: numOrZero(r, "rx_mbps")})
		tx.Pts = append(tx.Pts, Pt{X: x, Y: numOrZero(r, "tx_mbps")})
	}

	return PDFBuild{
		Title: "Traffic History Report", RowCount: len(rows), Truncated: truncated,
		Columns: columns, Rows: capped,
		Meta: Meta{
			Router: routerLabel, From: from, To: to,
			Stats: []Stat{
				{"Peak RX", dash(peakRx, " Mbps")},
				{"Peak TX", dash(peakTx, " Mbps")},
				{"Avg RX", dash(avgRx, " Mbps")},
				{"Avg TX", dash(avgTx, " Mbps")},
				{"95th RX", dash(n1p(s.RxP95Mbps), " Mbps")},
				{"Peak Util", util},
			},
			ChartData: &Chart{YLabel: "Mbps", Lines: []Line{rx, tx}},
		},
	}
}

func n1p(v *float64) string {
	if v == nil {
		return "—"
	}
	return ToFixed(*v, 1)
}

// ── bandwidth ───────────────────────────────────────────────────────────────

func BuildBandwidth(rows []map[string]any, s IfaceSummary, aggregate, routerLabel string, from, to float64, tz string) PDFBuild {
	columns := []string{"Timestamp", "Interface", "Download (MB)", "Upload (MB)"}
	table := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		table = append(table, map[string]any{
			"Timestamp":     TsFmt(int64(numOrZero(r, "ts")), tz),
			"Interface":     str(r, "interface"),
			"Download (MB)": jsParse(ToFixed(numOrZero(r, "rx_mb"), 1)),
			"Upload (MB)":   jsParse(ToFixed(numOrZero(r, "tx_mb"), 1)),
		})
	}
	capped, truncated := CapRows(table, columns)

	noun := BucketNoun(aggregate)
	countLabel := "Samples"
	if aggregate != "" {
		countLabel = "Buckets"
	}

	sub := thinRows(rows)
	down := Line{Label: "Download MB", Color: "#38bdf8"}
	up := Line{Label: "Upload MB", Color: "#4ade80"}
	for _, r := range sub {
		x := numOrZero(r, "ts")
		down.Pts = append(down.Pts, Pt{X: x, Y: numOrZero(r, "rx_mb")})
		up.Pts = append(up.Pts, Pt{X: x, Y: numOrZero(r, "tx_mb")})
	}

	return PDFBuild{
		Title: "Bandwidth Usage Report", RowCount: len(rows), Truncated: truncated,
		Columns: columns, Rows: capped,
		Meta: Meta{
			Router: routerLabel, From: from, To: to,
			Stats: []Stat{
				{"Total Download", FmtDataMB(s.RxTotalMb)},
				{"Total Upload", FmtDataMB(s.TxTotalMb)},
				// `(s.rxTotalMb || 0) + (s.txTotalMb || 0)` -- both nulls become 0.
				{"Total", FmtDataMB(s.RxTotalMb + s.TxTotalMb)},
				{"Busiest " + noun + " ↓", orDash(s.RxMaxMb)},
				{"Busiest " + noun + " ↑", orDash(s.TxMaxMb)},
				{countLabel, groupDigits(s.BandwidthSamples)},
			},
			ChartData: &Chart{YLabel: "MB/min", Lines: []Line{down, up}},
		},
	}
}

func orDash(v *float64) string {
	if v == nil {
		return "—"
	}
	return FmtDataMB(*v)
}

// ── alerts ──────────────────────────────────────────────────────────────────

func BuildAlerts(rows []map[string]any, routerLabel string, from, to float64, tz string) PDFBuild {
	open, resolved := 0, 0
	counts := map[string]int{}
	var order []string // insertion order, which is what decides a tie -- see below
	for _, r := range rows {
		if _, ok := num(r, "resolved_at"); ok {
			resolved++
		} else {
			open++
		}
		t := str(r, "alert_type")
		if _, seen := counts[t]; !seen {
			order = append(order, t)
		}
		counts[t]++
	}

	// `Object.entries(counts).sort((a,b) => b[1]-a[1])[0]`.
	//
	// V8's sort is STABLE and an object's string keys iterate in insertion order,
	// so a tie resolves to whichever type was seen FIRST. Go's map iteration is
	// randomised, so reproducing this needs the insertion order kept explicitly --
	// without it the corpus's tie case would pass or fail at random.
	top := "—"
	if len(order) > 0 {
		idx := append([]string(nil), order...)
		sort.SliceStable(idx, func(i, j int) bool { return counts[idx[i]] > counts[idx[j]] })
		top = idx[0]
	}

	columns := []string{"Fired At", "Type", "Subject", "Detail", "Resolved At", "Down Time"}
	table := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		firedAt := numOrZero(r, "fired_at")
		resolvedAt, hasResolved := num(r, "resolved_at")
		downTime := "—"
		if hasResolved {
			downTime = FmtDuration(int64(resolvedAt - firedAt))
		}
		table = append(table, map[string]any{
			"Fired At":    TsFmt(int64(firedAt), tz),
			"Type":        str(r, "alert_type"),
			"Subject":     str(r, "subject"),
			"Detail":      str(r, "detail"),
			"Resolved At": TsFmt(int64(resolvedAt), tz),
			"Down Time":   downTime,
		})
	}
	capped, truncated := CapRows(table, columns)

	return PDFBuild{
		Title: "Alert Events Report", RowCount: len(rows), Truncated: truncated,
		Columns: columns, Rows: capped,
		Meta: Meta{
			Router: routerLabel, From: from, To: to,
			Stats: []Stat{
				{"Total", groupDigits(len(rows))},
				{"Open", groupDigits(open)},
				{"Resolved", groupDigits(resolved)},
				{"Top Type", top},
			},
			// No ChartData: alert events are discrete, not a series.
		},
	}
}

// ── connectivity ────────────────────────────────────────────────────────────

func BuildConnectivity(rows []ConnRow, routerLabel string, from, to float64, tz string) PDFBuild {
	rows = AnnotateDowntime(rows)

	var resolvedMs []float64
	offline := 0
	var totalDown float64
	for _, r := range rows {
		if r.Connected != 0 {
			continue
		}
		offline++
		if r.DowntimeMs != nil {
			resolvedMs = append(resolvedMs, float64(*r.DowntimeMs))
			totalDown += float64(*r.DowntimeMs)
		}
	}

	columns := []string{"Timestamp", "Status", "Down Duration"}
	table := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		status := "Offline"
		if r.Connected != 0 {
			status = "Online"
		}
		dur := "—"
		switch {
		case r.Connected == 0 && r.DowntimeMs != nil:
			dur = FmtDuration(*r.DowntimeMs)
		case r.Connected == 0:
			// An outage with no resolution. Neither a duration nor a blank.
			dur = "Ongoing"
		}
		table = append(table, map[string]any{
			"Timestamp": TsFmt(r.TS, tz), "Status": status, "Down Duration": dur,
		})
	}
	capped, truncated := CapRows(table, columns)

	total, longest := "—", "—"
	if totalDown != 0 { // `totalDownMs ? … : '—'` -- zero is falsy, so 0ms reads as no downtime
		total = FmtDuration(int64(totalDown))
	}
	if len(resolvedMs) > 0 {
		longest = FmtDuration(int64(MaxOf(resolvedMs)))
	}

	return PDFBuild{
		Title: "Connectivity Report", RowCount: len(rows), Truncated: truncated,
		Columns: columns, Rows: capped,
		Meta: Meta{
			Router: routerLabel, From: from, To: to,
			Stats: []Stat{
				{"Total Events", groupDigits(len(rows))},
				{"Offline Events", groupDigits(offline)},
				{"Total Downtime", total},
				{"Longest Outage", longest},
			},
		},
	}
}

// RouterLabel is `_routerLabel`: the label, else the host, else the id.
func RouterLabel(label, host, id string) string {
	if label != "" {
		return label
	}
	if host != "" {
		return host
	}
	return id
}
