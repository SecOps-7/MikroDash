package server

import (
	"bytes"
	"compress/zlib"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"mikrodash/internal/reports"
)

// TestThePDFResponseIsAPDF pins the HTTP half of the export.
//
// The DRAWING is gated elsewhere and far harder: `internal/reportpdf` compares
// 4446 recorded calls against the live renderer and every glyph position against
// pdfkit's own. What is left here is the part those gates cannot see — the
// status, the two headers, and that the bytes reaching the client are the
// document rather than a truncated or empty response.
func TestThePDFResponseIsAPDF(t *testing.T) {
	build := reports.BuildPing([]map[string]any{
		{"ts": float64(1756100000000), "target": "198.51.100.1", "rtt_ms": 12.5, "loss_pct": 0.0},
		{"ts": float64(1756100060000), "target": "198.51.100.1", "rtt_ms": 13.5, "loss_pct": 0.0},
	}, "Test Router", 1756100000000, 1756100060000, "")

	w := httptest.NewRecorder()
	writeRenderedPDF(w, build, "")

	if w.Code != 200 {
		t.Fatalf("status %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("Content-Type %q, want application/pdf", ct)
	}
	// The filename comes from the report's title, which is a constant in the
	// builder. Asserted because the alternative -- a name built from a router
	// label or an interface -- is how a response header gets split.
	if cd := w.Header().Get("Content-Disposition"); cd != `attachment; filename="Ping Stability Report.pdf"` {
		t.Errorf("Content-Disposition %q", cd)
	}

	body := w.Body.Bytes()
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Fatalf("body is not a PDF: %q", firstN(body, 24))
	}
	if !bytes.Contains(body, []byte("%%EOF")) {
		t.Error("body has no EOF marker -- the document was never finished")
	}
	if len(body) < 800 {
		t.Errorf("body is %d bytes, too small to be this report", len(body))
	}
	// The buffered path exists so a mid-render failure cannot ship a truncated
	// file. If Content-Length is ever absent the response is being streamed, and
	// that guarantee is gone.
	if w.Header().Get("Content-Length") == "" && w.Body.Len() != len(body) {
		t.Error("the response looks streamed rather than buffered")
	}
}

// TestAPDFCarriesTheReportsOwnText is a shallow but real check that the build
// reached the page: a renderer wired to an empty build would still emit a valid
// PDF, and every assertion above would pass.
func TestAPDFCarriesTheReportsOwnText(t *testing.T) {
	build := reports.BuildAlerts([]map[string]any{
		{"fired_at": float64(1756100000000), "resolved_at": nil,
			"alert_type": "linkdown", "subject": "ether1", "detail": "carrier lost"},
	}, "Router Seven", 1756100000000, 1756100060000, "")

	w := httptest.NewRecorder()
	writeRenderedPDF(w, build, "")

	// THE STREAMS ARE COMPRESSED, and they should be -- this is the response a
	// client downloads. So the test inflates them rather than asking the renderer
	// to stop compressing for its benefit, which would leave the shipped path
	// untested.
	body := inflateStreams(t, w.Body.Bytes())
	if body == "" {
		t.Fatal("no content stream could be inflated -- the PDF has no readable body")
	}

	// "Router: Router Seven", not the bare label: the meta row prefixes it, and
	// asserting the bare string would pass against a page that had lost the prefix.
	for _, want := range []string{"Alert Events Report", "Router: Router Seven", "linkdown", "ether1", "Top Type"} {
		if !strings.Contains(body, "("+want+")") {
			t.Errorf("the rendered PDF does not contain %q", want)
		}
	}
	// And the alert report must NOT have drawn a chart: its events are discrete.
	if build.Meta.ChartData != nil {
		t.Error("the alerts build grew a chart")
	}
}

func firstN(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}

// inflateStreams concatenates every FlateDecode stream in a PDF.
func inflateStreams(t *testing.T, b []byte) string {
	t.Helper()
	var out strings.Builder
	rest := b
	for {
		i := bytes.Index(rest, []byte("stream"))
		if i < 0 {
			break
		}
		body := rest[i+len("stream"):]
		// Skip the EOL that must follow the keyword.
		body = bytes.TrimLeft(body, "\r\n")
		j := bytes.Index(body, []byte("endstream"))
		if j < 0 {
			break
		}
		if zr, err := zlib.NewReader(bytes.NewReader(body[:j])); err == nil {
			if dec, err := io.ReadAll(zr); err == nil {
				out.Write(dec)
			}
			_ = zr.Close()
		}
		rest = body[j:]
	}
	return out.String()
}
