package server

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"mikrodash/internal/diag"
	"mikrodash/internal/geo"
	"mikrodash/internal/routeros"
)

// A CONTINUOUS PING KEEPS REPORTING PAST ITS TRIM. Progress used to be sent when
// the rows grew; a trimmed run stops growing at its bound, so that rule would
// have frozen the page at the hundredth reply. It counts rows received instead.
func TestAContinuousPingKeepsReportingPastItsTrim(t *testing.T) {
	fastProgress(t, 5*time.Millisecond)
	f := newFakeStream()
	var frames, lastSent atomic.Int32
	ch := runAsync(func() (*diag.PingResult, string, string) {
		return runPing(f, "198.51.100.1", 0, true, nil, func(p *diag.PingResult) {
			frames.Add(1)
			lastSent.Store(int32(p.Sent))
		})
	})
	f.wait(t)
	total := diag.PingKeepReplies + 30
	for i := 0; i < total; i++ {
		n := strconv.Itoa(i + 1)
		f.row(routeros.Reply{"seq": strconv.Itoa(i), "host": "198.51.100.1", "time": "4ms",
			"sent": n, "received": n, "packet-loss": "0"})
		if i >= diag.PingKeepReplies {
			time.Sleep(12 * time.Millisecond)
		}
	}
	time.Sleep(30 * time.Millisecond)
	if got := lastSent.Load(); got != int32(total) {
		t.Errorf("the last progress frame says sent %d, want %d: progress stopped at the trim", got, total)
	}
	// Reaching the hour is the run's end, not a failure.
	f.end(fmt.Errorf("routeros: timed out: %w", context.DeadlineExceeded))
	o := outcome(t, ch)
	if o.code != "" || o.res == nil || o.res.Sent != total || len(o.res.Replies) != diag.PingKeepReplies {
		t.Errorf("a continuous run at its cap answered %q %v, want the run with %d kept replies", o.code, o.res, diag.PingKeepReplies)
	}
	if f.cmd.Timeout != diag.ContinuousMax {
		t.Errorf("a continuous ping is bounded by %v, want %v", f.cmd.Timeout, diag.ContinuousMax)
	}
}

// A FIXED RUN'S TIMEOUT IS STILL A FAILURE: only a continuous one's is its cap.
func TestAFixedRunsTimeoutIsStillAFailure(t *testing.T) {
	f := newFakeStream()
	ch := runAsync(func() (*diag.PingResult, string, string) {
		return runPing(f, "198.51.100.1", 4, false, nil, nil)
	})
	f.wait(t)
	f.end(fmt.Errorf("routeros: timed out: %w", context.DeadlineExceeded))
	if o := outcome(t, ch); o.code != "failed" {
		t.Errorf("a fixed ping past its timeout answered %q, want failed", o.code)
	}
}

// THE ASSISTANT KEEPS ITS OWN CAP when the page's is raised: a model waits for
// the whole run before it reads a reply.
func TestTheAssistantKeepsItsPingCap(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, 0}, {4, 4}, {10, 10}, {50, diag.AssistantPingMax}, {100, diag.AssistantPingMax}} {
		if got := assistantPingCount(tc.in); got != tc.want {
			t.Errorf("assistantPingCount(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
	if diag.AssistantPingMax >= diag.PingMaxCount {
		t.Error("the assistant's cap is not below the page's, so it caps nothing")
	}
}

// EVERY HOP THE DATABASE KNOWS IS PLACED; A PRIVATE ONE IS NOT. Needs the real
// DB-IP database (it is downloaded at image build), as internal/geo's own mmdb
// tests do: MIKRODASH_GEO_DIR names the directory.
func TestTracerouteHopsArePlaced(t *testing.T) {
	dir := os.Getenv("MIKRODASH_GEO_DIR")
	if dir == "" {
		t.Skip("MIKRODASH_GEO_DIR is not set; the mmdb is downloaded at image build")
	}
	if _, ok := geo.Shared(dir); !ok {
		t.Skipf("no geo database in %s", dir)
	}
	r := diag.TracerouteResult{Hops: []diag.Hop{
		{Hop: 1, Address: "192.168.88.1"}, {Hop: 2, TimedOut: true}, {Hop: 3, Address: "8.8.8.8"}}}
	locateHops(&r)
	if h := r.Hops[0]; h.Lat != nil || h.Country != "" {
		t.Errorf("a private hop was placed: %+v", h)
	}
	if h := r.Hops[1]; h.Lat != nil {
		t.Errorf("a hop with no address was placed: %+v", h)
	}
	if h := r.Hops[2]; h.Lat == nil || h.Lon == nil || h.Country == "" {
		t.Errorf("a public hop was not placed: %+v", h)
	}
}
