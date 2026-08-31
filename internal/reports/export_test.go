package reports

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestExportFormattersMatchLive pins `format.js`'s tsFmt and fmtDuration, which
// are NOT the page's formatters of the same purpose.
//
// The cases that matter are the ones where the two families disagree: zero (""
// here, "—" there), a negative instant (before the epoch), and a sub-second
// duration that rounds to "0s" rather than to nothing.
func TestExportFormattersMatchLive(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "report-history-cases.json"))
	if err != nil {
		t.Skipf("report-history-cases.json missing — regenerate it in the app container: %v", err)
	}
	var c struct {
		ExportFmt struct {
			TsFmt []struct {
				In  int64  `json:"in"`
				Out string `json:"out"`
			} `json:"tsFmt"`
			FmtDuration []struct {
				In  int64  `json:"in"`
				Out string `json:"out"`
			} `json:"fmtDuration"`
		} `json:"exportFmt"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parsing the cases: %v", err)
	}
	if len(c.ExportFmt.TsFmt) == 0 {
		t.Fatal("no export formatter cases — regenerate tools/report-history-cases.js")
	}
	// The live side reads its zone from Settings, and this /data has none set —
	// so these cases exercise the UTC branch, which is the one a downloaded file
	// gets by default.
	for _, tc := range c.ExportFmt.TsFmt {
		if got := TsFmt(tc.In, ""); got != tc.Out {
			t.Errorf("TsFmt(%d, \"\") = %q, format.js says %q", tc.In, got, tc.Out)
		}
	}
	for _, tc := range c.ExportFmt.FmtDuration {
		if got := FmtDuration(tc.In); got != tc.Out {
			t.Errorf("FmtDuration(%d) = %q, format.js says %q", tc.In, got, tc.Out)
		}
	}
}

// TestExportFormattersDifferFromThePage pins the DIFFERENCE itself.
//
// Both pairs are easy to unify by accident — one formatter, fewer lines, and a
// downloaded file that silently changes meaning. This fails if anybody does.
func TestExportFormattersDifferFromThePage(t *testing.T) {
	if TsFmt(0, "") != "" {
		t.Error(`TsFmt(0) must be "" — the page's "—" is a string in a date column`)
	}
	if got := TsFmt(1767225600000, ""); got != "2026-01-01 00:00:00 UTC" {
		t.Errorf("TsFmt with no zone = %q, want the UTC suffix — a file is read elsewhere later", got)
	}
	if FmtDuration(0) != "" {
		t.Error(`FmtDuration(0) must be "" here, where the page gives "—"`)
	}
}
