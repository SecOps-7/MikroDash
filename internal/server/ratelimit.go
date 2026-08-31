package server

// A fixed-window rate limiter, matching what `express-rate-limit` does on the
// routes this server has taken over.
//
// ── WHY THIS EXISTS AT ALL, AND WHY IT IS SO SMALL ──────────────────────────
//
// The live app declares fifteen limiters. Fourteen of them guard endpoints this
// server still PROXIES to Node, which applies them itself — so porting those
// would mean building a second gate in front of one that already works.
//
// The routes this server actually owns are the report endpoints, and the live app
// limits exactly three: POST, PUT and DELETE on `/api/reports/schedules`, at 30 a
// minute. The five report READS and both schedule GETs carry no limiter there, so
// they carry none here — a port that added protection the original does not have
// is still a port that behaves differently, and the difference would stay
// invisible until somebody's dashboard started returning 429.
//
// (`_sendNowLimiter`, 5/min, guards Send Now — `POST /api/reports/schedules/{id}/run`.
// This line said it guarded "an endpoint this port does not implement" until
// 2026-08-29, long after `reports.go` registered that route and
// `reports-schedules.ts` wired the button. It cost a tick: a sweep read it as
// evidence Send Now was still dead and wrote that into two other files, when one
// curl would have shown a 401 rather than a 404.)
//
// ── MATCHING THE OBSERVABLE CONTRACT ────────────────────────────────────────
//
// Taken from the real thing rather than its documentation — express-rate-limit
// 8.6.1, driven with max=1 and read off the wire:
//
//	RateLimit-Limit: 1        RateLimit-Policy: 1;w=60
//	RateLimit-Remaining: 0    RateLimit-Reset: 60
//	429 → also Retry-After: 60, body "Too many requests, please try again later."
//
// The body is PLAIN TEXT, not this API's `{ok:false,error}` envelope. That is a
// wart and it is the original's: a client parsing every failure as JSON already
// has to cope with it today.

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a fixed window per key.
//
// FIXED, not sliding, because that is what the MemoryStore behind
// express-rate-limit does: the window opens on a key's first request and every
// request until it closes shares it. A sliding window would be fairer and would
// answer differently at the boundary.
type rateLimiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	hits map[string]*rateWindow
}

type rateWindow struct {
	count int
	reset time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window, now: time.Now, hits: map[string]*rateWindow{}}
}

// take records a request and reports whether it is allowed, with the remaining
// allowance and the seconds until the window closes.
func (l *rateLimiter) take(key string) (ok bool, remaining int, resetSec int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	w := l.hits[key]
	if w == nil || !now.Before(w.reset) {
		w = &rateWindow{reset: now.Add(l.window)}
		l.hits[key] = w
		// Opening a window is the moment to discard the closed ones. Without this
		// the map grows by one entry per distinct address for ever — a slow leak
		// that only shows on a server exposed to the internet, which is exactly
		// where it would matter.
		for k, v := range l.hits {
			if k != key && !now.Before(v.reset) {
				delete(l.hits, k)
			}
		}
	}
	w.count++

	remaining = l.max - w.count
	if remaining < 0 {
		remaining = 0
	}
	// Whole seconds, rounded UP: express-rate-limit reports 60 for a fresh
	// 60-second window rather than 59.
	resetSec = int((w.reset.Sub(now) + time.Second - 1) / time.Second)
	if resetSec < 0 {
		resetSec = 0
	}
	return w.count <= l.max, remaining, resetSec
}

// limit wraps a handler.
//
// The key is the client address, resolved the same way the audit trail resolves
// it — X-Forwarded-For first, because behind a reverse proxy RemoteAddr is the
// proxy for everybody and a limiter keyed on it would throttle the whole site as
// one caller.
func (l *rateLimiter) limit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := clientIPOf(r)
		ok, remaining, resetSec := l.take(key)

		h := w.Header()
		h.Set("RateLimit-Limit", fmt.Sprint(l.max))
		h.Set("RateLimit-Policy", fmt.Sprintf("%d;w=%d", l.max, int(l.window.Seconds())))
		h.Set("RateLimit-Remaining", fmt.Sprint(remaining))
		h.Set("RateLimit-Reset", fmt.Sprint(resetSec))

		if !ok {
			h.Set("Retry-After", fmt.Sprint(resetSec))
			h.Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("Too many requests, please try again later."))
			return
		}
		next(w, r)
	}
}
