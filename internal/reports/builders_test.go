package reports

import (
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"
)

type builderCase struct {
	Name    string `json:"name"`
	Section string `json:"section"`
	Opts    struct {
		RouterID  string  `json:"routerId"`
		Iface     string  `json:"iface"`
		From      float64 `json:"from"`
		To        float64 `json:"to"`
		Aggregate string  `json:"aggregate"`
	} `json:"opts"`
	Data struct {
		Ping      []map[string]any `json:"ping"`
		Traffic   []map[string]any `json:"traffic"`
		Bandwidth []map[string]any `json:"bandwidth"`
		Alerts    []map[string]any `json:"alerts"`
		Conn      []ConnRow        `json:"conn"`
		Router    *struct {
			Label      string `json:"label"`
			Host       string `json:"host"`
			BwDownMbps any    `json:"bwDownMbps"`
			BwUpMbps   any    `json:"bwUpMbps"`
		} `json:"router"`
		TrafficSummary   map[string]any `json:"trafficSummary"`
		BandwidthSummary map[string]any `json:"bandwidthSummary"`
	} `json:"data"`
	Out struct {
		Title      string         `json:"title"`
		RowCount   int            `json:"rowCount"`
		Truncated  bool           `json:"truncated"`
		Columns    []string       `json:"columns"`
		FirstRow   map[string]any `json:"firstRow"`
		LastRow    map[string]any `json:"lastRow"`
		RowsLength int            `json:"rowsLength"`
		Meta       struct {
			Router   string  `json:"router"`
			From     float64 `json:"from"`
			To       float64 `json:"to"`
			Stats    []Stat  `json:"-"`
			RawStats []struct {
				Label string `json:"label"`
				Value any    `json:"value"`
			} `json:"stats"`
			ChartData *struct {
				YLabel string `json:"yLabel"`
				Lines  []struct {
					Label string           `json:"label"`
					Color string           `json:"color"`
					Pts   []map[string]any `json:"pts"`
				} `json:"lines"`
			} `json:"chartData"`
		} `json:"meta"`
	} `json:"out"`
}

func loadBuilderCases(t *testing.T) []builderCase {
	t.Helper()
	b, err := os.ReadFile("../../testdata/report-builders-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var doc struct {
		Cases []builderCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("corpus is empty -- this test would pass against nothing")
	}
	return doc.Cases
}

// fp reads an optional number out of a summary map the way the live SQL layer
// hands it over: absent and null both mean "no value", which is what makes a
// stat box read '—'.
func fp(m map[string]any, k string) *float64 {
	v, ok := m[k]
	if !ok || v == nil {
		return nil
	}
	f, ok := v.(float64)
	if !ok {
		return nil
	}
	return &f
}

func fi(m map[string]any, k string) int {
	if p := fp(m, k); p != nil {
		return int(*p)
	}
	return 0
}

func TestBuildersMatchTheLiveReportBuilders(t *testing.T) {
	for _, c := range loadBuilderCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			// A null router is the live `Routers.getById` finding nothing: the
			// label falls back to the id, and the capacities to their defaults.
			label := c.Opts.RouterID
			capDown, capUp := capOf(nil), capOf(nil)
			if c.Data.Router != nil {
				label = RouterLabel(c.Data.Router.Label, c.Data.Router.Host, c.Opts.RouterID)
				capDown = capOf(c.Data.Router.BwDownMbps)
				capUp = capOf(c.Data.Router.BwUpMbps)
			}

			s := IfaceSummary{
				RxAvgMbps:        fp(c.Data.TrafficSummary, "rxAvgMbps"),
				TxAvgMbps:        fp(c.Data.TrafficSummary, "txAvgMbps"),
				RxMaxMbps:        fp(c.Data.TrafficSummary, "rxMaxMbps"),
				TxMaxMbps:        fp(c.Data.TrafficSummary, "txMaxMbps"),
				RxP95Mbps:        fp(c.Data.TrafficSummary, "rxP95Mbps"),
				TxP95Mbps:        fp(c.Data.TrafficSummary, "txP95Mbps"),
				RxTotalMb:        f0(c.Data.BandwidthSummary, "rxTotalMb"),
				TxTotalMb:        f0(c.Data.BandwidthSummary, "txTotalMb"),
				RxMaxMb:          fp(c.Data.BandwidthSummary, "rxMaxMb"),
				TxMaxMb:          fp(c.Data.BandwidthSummary, "txMaxMb"),
				BandwidthSamples: fi(c.Data.BandwidthSummary, "samples"),
				CapacityDown:     capDown, CapacityUp: capUp,
			}

			var got PDFBuild
			switch c.Section {
			case "ping":
				got = BuildPing(c.Data.Ping, label, c.Opts.From, c.Opts.To, "")
			case "traffic":
				got = BuildTraffic(c.Data.Traffic, s, label, c.Opts.From, c.Opts.To, "")
			case "bandwidth":
				got = BuildBandwidth(c.Data.Bandwidth, s, c.Opts.Aggregate, label, c.Opts.From, c.Opts.To, "")
			case "alerts":
				got = BuildAlerts(c.Data.Alerts, label, c.Opts.From, c.Opts.To, "")
			case "connectivity":
				got = BuildConnectivity(c.Data.Conn, label, c.Opts.From, c.Opts.To, "")
			default:
				t.Fatalf("unknown section %q", c.Section)
			}

			if got.Title != c.Out.Title {
				t.Errorf("title %q, live %q", got.Title, c.Out.Title)
			}
			if got.RowCount != c.Out.RowCount {
				t.Errorf("rowCount %d, live %d", got.RowCount, c.Out.RowCount)
			}
			if got.Truncated != c.Out.Truncated {
				t.Errorf("truncated %v, live %v", got.Truncated, c.Out.Truncated)
			}
			if !reflect.DeepEqual(got.Columns, c.Out.Columns) {
				t.Errorf("columns %v, live %v", got.Columns, c.Out.Columns)
			}
			if len(got.Rows) != c.Out.RowsLength {
				t.Errorf("built %d table rows, live built %d", len(got.Rows), c.Out.RowsLength)
			}
			if len(got.Rows) > 0 {
				if !reflect.DeepEqual(got.Rows[0], c.Out.FirstRow) {
					t.Errorf("first row\n  got  %v\n  live %v", got.Rows[0], c.Out.FirstRow)
				}
				if !reflect.DeepEqual(got.Rows[len(got.Rows)-1], c.Out.LastRow) {
					t.Errorf("last row\n  got  %v\n  live %v", got.Rows[len(got.Rows)-1], c.Out.LastRow)
				}
			}

			if got.Meta.Router != c.Out.Meta.Router {
				t.Errorf("meta.router %q, live %q", got.Meta.Router, c.Out.Meta.Router)
			}

			// The stat boxes, in order and with their exact labels -- the labels are
			// composed for bandwidth ("Busiest Day ↓") and switch on aggregation.
			if len(got.Meta.Stats) != len(c.Out.Meta.RawStats) {
				t.Fatalf("%d stat boxes, live has %d", len(got.Meta.Stats), len(c.Out.Meta.RawStats))
			}
			for i, want := range c.Out.Meta.RawStats {
				g := got.Meta.Stats[i]
				if g.Label != want.Label {
					t.Errorf("stat %d label %q, live %q", i, g.Label, want.Label)
				}
				if g.Value != jsValue(want.Value) {
					t.Errorf("stat %q value %q, live %q", want.Label, g.Value, jsValue(want.Value))
				}
			}

			if (got.Meta.ChartData == nil) != (c.Out.Meta.ChartData == nil) {
				t.Fatalf("chartData present=%v, live present=%v",
					got.Meta.ChartData != nil, c.Out.Meta.ChartData != nil)
			}
			if got.Meta.ChartData == nil {
				return
			}
			if got.Meta.ChartData.YLabel != c.Out.Meta.ChartData.YLabel {
				t.Errorf("yLabel %q, live %q", got.Meta.ChartData.YLabel, c.Out.Meta.ChartData.YLabel)
			}
			if len(got.Meta.ChartData.Lines) != len(c.Out.Meta.ChartData.Lines) {
				t.Fatalf("%d chart lines, live has %d",
					len(got.Meta.ChartData.Lines), len(c.Out.Meta.ChartData.Lines))
			}
			for i, wl := range c.Out.Meta.ChartData.Lines {
				gl := got.Meta.ChartData.Lines[i]
				if gl.Label != wl.Label || gl.Color != wl.Color {
					t.Errorf("line %d is %q/%q, live %q/%q", i, gl.Label, gl.Color, wl.Label, wl.Color)
				}
				if len(gl.Pts) != len(wl.Pts) {
					t.Fatalf("line %q has %d points, live has %d", wl.Label, len(gl.Pts), len(wl.Pts))
				}
				for j, wp := range wl.Pts {
					// A null y is kept as zero. `_render` compares and scales it with
					// `<` and `-`, both of which coerce null to 0, so the two draw the
					// same point -- and dropping it would change the series length.
					wx, _ := wp["x"].(float64)
					wy, _ := wp["y"].(float64)
					if gl.Pts[j].X != wx || gl.Pts[j].Y != wy {
						t.Errorf("line %q point %d is (%v,%v), live (%v,%v)",
							wl.Label, j, gl.Pts[j].X, gl.Pts[j].Y, wx, wy)
					}
				}
			}
		})
	}
}

// jsValue renders a stat value the way JSON gave it back: the live builders
// produce strings everywhere, and a number appearing here would be a change.
func jsValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return JSNumber(t)
	case nil:
		return ""
	}
	return ""
}

// capOf routes the corpus's JSON number through CapacityOr, the ONE gated
// implementation of `Math.max(1, parseInt(...) || 1000)`. Writing the rule out
// again here would mean this test agreed with a second copy rather than with the
// code that ships.
func capOf(v any) int {
	f, ok := v.(float64)
	if !ok {
		return CapacityOr("")
	}
	return CapacityOr(strconv.Itoa(int(f)))
}

// f0 reads a summary number that DEFAULTS TO ZERO rather than being absent --
// the totals, where "no bytes" is a total of nothing and not a missing value.
func f0(m map[string]any, k string) float64 {
	if p := fp(m, k); p != nil {
		return *p
	}
	return 0
}
