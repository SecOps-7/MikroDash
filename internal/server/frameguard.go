package server

// MikroDash is never shown inside another site's frame.
//
// ── WHY ─────────────────────────────────────────────────────────────────────
//
// Without a framing policy any page on the internet can load MikroDash, signed
// in or at its login page, in an invisible frame and lead a visitor into
// clicking a real button through it (clickjacking). Nothing in the app frames
// a page of its own, so every frame is refused, the app's own origin included.
//
// ── TWO HEADERS, ON EVERY RESPONSE ──────────────────────────────────────────
//
//	Content-Security-Policy: frame-ancestors 'none'   current browsers
//	X-Frame-Options: DENY                             older ones and webviews
//
// Set before the handler runs, so a response is covered whatever path it takes
// (a page, the API, a static file, a 404, a redirect, a refusal, the socket
// upgrade), and enforced again as the response is sent, so a handler that sets
// its own Content-Security-Policy gets `frame-ancestors 'none'` merged INTO
// that header rather than a second header beside it, and one that sets a
// framing policy of its own has it replaced. Only this directive is set, so no
// other CSP behaviour changes.

import (
	"bufio"
	"net"
	"net/http"
	"strings"
)

const (
	cspHeader           = "Content-Security-Policy"
	frameOptionsHeader  = "X-Frame-Options"
	frameAncestorsNone  = "frame-ancestors 'none'"
	frameOptionsDenyAll = "DENY"
)

// frameGuard wraps a handler so every response it sends refuses framing.
func frameGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		applyFrameHeaders(w.Header())
		next.ServeHTTP(&frameGuardWriter{ResponseWriter: w}, r)
	})
}

// applyFrameHeaders makes a header set refuse framing: X-Frame-Options DENY,
// and `frame-ancestors 'none'` in every Content-Security-Policy value, adding
// one only when there is none.
func applyFrameHeaders(h http.Header) {
	h.Set(frameOptionsHeader, frameOptionsDenyAll)
	values := h.Values(cspHeader)
	if len(values) == 0 {
		h.Set(cspHeader, frameAncestorsNone)
		return
	}
	// Merged into each value, then the redundant ones dropped: a value that
	// says nothing but `frame-ancestors 'none'` (the one set before the
	// handler ran, or a handler's own framing policy once replaced) adds
	// nothing when another value already carries it, and a repeat adds
	// nothing at all. So a handler's policy never travels with a second
	// header beside it.
	var merged []string
	seen := map[string]bool{}
	for _, v := range values {
		m := withFrameAncestorsNone(v)
		if !seen[m] {
			seen[m] = true
			merged = append(merged, m)
		}
	}
	if len(merged) > 1 {
		kept := merged[:0]
		for _, m := range merged {
			if m != frameAncestorsNone {
				kept = append(kept, m)
			}
		}
		merged = kept
	}
	h[http.CanonicalHeaderKey(cspHeader)] = merged
}

// withFrameAncestorsNone returns a policy whose frame-ancestors directive is
// 'none': any frame-ancestors the policy had is dropped (directive names are
// case-insensitive) and the 'none' one appended, the rest kept as written.
func withFrameAncestorsNone(policy string) string {
	var kept []string
	for _, d := range strings.Split(policy, ";") {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		name := strings.ToLower(strings.Fields(d)[0])
		if name == "frame-ancestors" {
			continue
		}
		kept = append(kept, d)
	}
	return strings.Join(append(kept, frameAncestorsNone), "; ")
}

// frameGuardWriter re-applies the headers when the response is sent, so a
// handler cannot unset or loosen them.
type frameGuardWriter struct {
	http.ResponseWriter
	sent bool
}

func (w *frameGuardWriter) WriteHeader(code int) {
	if !w.sent {
		w.sent = true
		applyFrameHeaders(w.ResponseWriter.Header())
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *frameGuardWriter) Write(b []byte) (int, error) {
	if !w.sent {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController and the WebSocket library reach the
// connection underneath (Flush, Hijack, deadlines).
func (w *frameGuardWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush and Hijack are passed through for callers that type-assert rather
// than unwrap.
func (w *frameGuardWriter) Flush() {
	if !w.sent {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *frameGuardWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}
