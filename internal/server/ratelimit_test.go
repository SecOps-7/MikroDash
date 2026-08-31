package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The contract these assert was read off express-rate-limit 8.6.1 on the wire,
// not from its documentation — see the file header for the capture.

func hit(t *testing.T, h http.HandlerFunc, ip string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set("X-Forwarded-For", ip)
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestRateLimitAllowsUpToMaxThenRefuses(t *testing.T) {
	l := newRateLimiter(3, time.Minute)
	h := l.limit(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for i := 1; i <= 3; i++ {
		w := hit(t, h, "198.51.100.7")
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: %d, want 200", i, w.Code)
		}
		if got, want := w.Header().Get("RateLimit-Remaining"), []string{"2", "1", "0"}[i-1]; got != want {
			t.Errorf("request %d: RateLimit-Remaining %q, want %q", i, got, want)
		}
	}

	w := hit(t, h, "198.51.100.7")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("the 4th request: %d, want 429", w.Code)
	}
	// The body is PLAIN TEXT, matching the original. A JSON envelope here would
	// be an improvement and a difference.
	if got := w.Body.String(); got != "Too many requests, please try again later." {
		t.Errorf("429 body %q", got)
	}
	if got := w.Header().Get("Retry-After"); got == "" {
		t.Error("a 429 must carry Retry-After")
	}
	if got, want := w.Header().Get("RateLimit-Policy"), "3;w=60"; got != want {
		t.Errorf("RateLimit-Policy %q, want %q", got, want)
	}
}

// TestRateLimitIsPerClient is the one that matters behind a reverse proxy: keyed
// on RemoteAddr instead, one busy caller would throttle everybody.
func TestRateLimitIsPerClient(t *testing.T) {
	l := newRateLimiter(1, time.Minute)
	h := l.limit(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	if w := hit(t, h, "198.51.100.7"); w.Code != http.StatusOK {
		t.Fatalf("first client, first request: %d", w.Code)
	}
	if w := hit(t, h, "198.51.100.7"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("first client, second request: %d, want 429", w.Code)
	}
	if w := hit(t, h, "198.51.100.8"); w.Code != http.StatusOK {
		t.Fatalf("a DIFFERENT client was refused: %d", w.Code)
	}
}

// TestRateLimitWindowIsFixed pins the shape: the window opens on a key's first
// request and everything until it closes shares it — which is what the
// MemoryStore does, and it answers differently from a sliding window at the
// boundary.
func TestRateLimitWindowIsFixed(t *testing.T) {
	now := time.Unix(1767225600, 0)
	l := newRateLimiter(2, time.Minute)
	l.now = func() time.Time { return now }
	h := l.limit(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	hit(t, h, "198.51.100.7")
	now = now.Add(59 * time.Second) // still inside the window
	hit(t, h, "198.51.100.7")
	if w := hit(t, h, "198.51.100.7"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("third request inside the window: %d, want 429", w.Code)
	}
	now = now.Add(2 * time.Second) // the window has closed
	w := hit(t, h, "198.51.100.7")
	if w.Code != http.StatusOK {
		t.Fatalf("after the window closed: %d, want 200", w.Code)
	}
	if got, want := w.Header().Get("RateLimit-Reset"), "60"; got != want {
		t.Errorf("a fresh window reports Reset %q, want %q", got, want)
	}
}

// TestRateLimitForgetsClosedWindows pins the sweep. Without it the map grows by
// one entry per distinct address for ever — invisible until the server is
// exposed to the internet, which is where it would matter.
func TestRateLimitForgetsClosedWindows(t *testing.T) {
	now := time.Unix(1767225600, 0)
	l := newRateLimiter(5, time.Minute)
	l.now = func() time.Time { return now }
	for i := 0; i < 50; i++ {
		l.take("198.51.100." + string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	if len(l.hits) < 2 {
		t.Fatalf("expected several live windows, got %d", len(l.hits))
	}
	now = now.Add(2 * time.Minute)
	l.take("198.51.100.7")
	if len(l.hits) != 1 {
		t.Errorf("%d windows survived after the sweep, want 1", len(l.hits))
	}
}
