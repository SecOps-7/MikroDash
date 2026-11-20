package audit

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

type memSink struct{ rows []DBEvent }

func (m *memSink) InsertAuditEvent(e DBEvent) error { m.rows = append(m.rows, e); return nil }

var (
	anon  = Actor{Name: "anonymous", IP: "198.51.100.7"}
	alice = Actor{ID: "u-1", Name: "alice", IP: "198.51.100.7"} // same address, behind one proxy
)

func TestOneDeniedRowPerActionAndTargetPerMinute(t *testing.T) {
	c := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	var sums []DeniedSummary
	l := NewDeniedLimiter(c.now, func(s DeniedSummary) { sums = append(sums, s) })
	if !l.Allow(anon, "DELETE /api/routers/{id}", "/api/routers/a") {
		t.Fatal("the first refusal was not allowed")
	}
	for i := 0; i < 5; i++ {
		if l.Allow(anon, "DELETE /api/routers/{id}", "/api/routers/a") {
			t.Fatalf("repeat %d of the same action and target was allowed", i+1)
		}
	}
	// Another target of the same action is its own row.
	if !l.Allow(anon, "DELETE /api/routers/{id}", "/api/routers/b") {
		t.Error("a refusal on another target was suppressed")
	}
	// Another actor at the SAME address has its own budget.
	if !l.Allow(alice, "DELETE /api/routers/{id}", "/api/routers/a") {
		t.Error("an anonymous flood used up a signed-in user's budget at the same address")
	}
	if len(sums) != 0 {
		t.Fatalf("a summary before the window closed: %+v", sums)
	}
	c.t = c.t.Add(deniedWindow)
	l.Flush(false)
	if len(sums) != 1 || sums[0].Actor != anon || sums[0].Counts["DELETE /api/routers/{id}"] != 5 {
		t.Fatalf("summary after the window: %+v, want anonymous with 5 counted", sums)
	}
	if !strings.Contains(sums[0].Note(), "5 refused attempts not recorded one by one: DELETE /api/routers/{id} x5") {
		t.Errorf("summary note: %q", sums[0].Note())
	}
}

func TestThirtyDeniedRowsPerActorPerMinute(t *testing.T) {
	c := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	var sums []DeniedSummary
	l := NewDeniedLimiter(c.now, func(s DeniedSummary) { sums = append(sums, s) })
	for i := 0; i < 30; i++ {
		if !l.Allow(anon, "action-"+strconv.Itoa(i), "") {
			t.Fatalf("row %d of 30 was refused", i+1)
		}
	}
	for i := 30; i < 500; i++ {
		if l.Allow(anon, "action-"+strconv.Itoa(i), "") {
			t.Fatalf("row %d was allowed past the cap of 30", i+1)
		}
	}
	// The actor's next refusal, a minute on, closes the window: the summary
	// comes out then, and the new window allows again.
	c.t = c.t.Add(deniedWindow)
	if !l.Allow(anon, "action-0", "") {
		t.Fatal("refused in the next window")
	}
	if len(sums) != 1 || sums[0].Total() != 470 {
		t.Fatalf("summary: %+v, want one with 470 counted", sums)
	}
}

func TestNothingCountedIsLost(t *testing.T) {
	c := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	total := 0
	l := NewDeniedLimiter(c.now, func(s DeniedSummary) { total += s.Total() })
	// Fill past the bound with actors that each have something counted.
	for i := 0; i < maxDeniedKeys+100; i++ {
		c.t = c.t.Add(time.Millisecond)
		a := Actor{Name: "anonymous", IP: "10.0." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256)}
		l.Allow(a, "x", "")
		l.Allow(a, "x", "") // counted
	}
	if n := l.tracked(); n > maxDeniedKeys {
		t.Errorf("%d pairs tracked, the bound is %d", n, maxDeniedKeys)
	}
	// Shutdown flushes the rest.
	l.Flush(true)
	if total != maxDeniedKeys+100 {
		t.Errorf("summaries account for %d counted refusals, want %d: eviction or flush dropped some",
			total, maxDeniedKeys+100)
	}
	var nilLimiter *DeniedLimiter
	if !nilLimiter.Allow(anon, "b", "") {
		t.Error("a nil limiter refused")
	}
	nilLimiter.Flush(true)
}

func TestDeniedThroughTheLimiter(t *testing.T) {
	c := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	sink := &memSink{}
	marks := 0
	l := NewDeniedLimiter(c.now, func(s DeniedSummary) { WriteDeniedSummary(sink, s, func() int64 { return 2 }) })
	rec := New(sink, anon, func() int64 { return 1 }).WithDeniedLimit(l).OnDenied(func() { marks++ })
	for i := 0; i < 3; i++ {
		rec.Denied(Event{Action: "DELETE /api/routers/{id}", TargetID: "/api/routers/a", Note: "HTTP 403"})
	}
	if len(sink.rows) != 1 {
		t.Fatalf("%d rows written for three identical refusals in a minute, want 1", len(sink.rows))
	}
	if marks != 3 {
		t.Errorf("OnDenied ran %d times, want 3: a counted refusal is still handled", marks)
	}
	l.Flush(true)
	if len(sink.rows) != 2 {
		t.Fatalf("%d rows after the flush, want the refusal and one summary", len(sink.rows))
	}
	sum := sink.rows[1]
	if sum.Action != DeniedSummaryAction || sum.Outcome != "denied" || sum.ActorName != "anonymous" ||
		!strings.Contains(sum.Detail, "2 refused attempts not recorded one by one") {
		t.Errorf("summary row: %+v", sum)
	}
	// Without a limiter every refusal is written, as before.
	plain := &memSink{}
	r2 := New(plain, Actor{IP: "198.51.100.9"}, func() int64 { return 1 })
	r2.Denied(Event{Action: "a"})
	r2.Denied(Event{Action: "a"})
	if len(plain.rows) != 2 {
		t.Errorf("a recorder without a limiter wrote %d rows, want 2", len(plain.rows))
	}
}
