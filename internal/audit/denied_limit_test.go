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

func TestOneDeniedRowPerActionPerMinute(t *testing.T) {
	c := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewDeniedLimiter(c.now)
	if ok, n := l.Allow("198.51.100.7", "DELETE /api/routers/{id}"); !ok || n != 0 {
		t.Fatalf("the first refusal was not allowed: %v %d", ok, n)
	}
	for i := 0; i < 5; i++ {
		if ok, _ := l.Allow("198.51.100.7", "DELETE /api/routers/{id}"); ok {
			t.Fatalf("repeat %d of the same action in one minute was allowed", i+1)
		}
	}
	// Another action from the same address is its own row, and reports the
	// five that were counted.
	ok, n := l.Allow("198.51.100.7", "POST /api/users")
	if !ok || n != 5 {
		t.Fatalf("another action: allowed %v, suppressed %d, want true and 5", ok, n)
	}
	// Another address has its own budget.
	if ok, n := l.Allow("198.51.100.8", "DELETE /api/routers/{id}"); !ok || n != 0 {
		t.Errorf("another address shared the budget: %v %d", ok, n)
	}
}

func TestThirtyDeniedRowsPerAddressPerMinute(t *testing.T) {
	c := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewDeniedLimiter(c.now)
	for i := 0; i < 30; i++ {
		if ok, _ := l.Allow("198.51.100.7", "action-"+strconv.Itoa(i)); !ok {
			t.Fatalf("row %d of 30 was refused", i+1)
		}
	}
	for i := 30; i < 500; i++ {
		if ok, _ := l.Allow("198.51.100.7", "action-"+strconv.Itoa(i)); ok {
			t.Fatalf("row %d was allowed past the cap of 30", i+1)
		}
	}
	// The next window: allowed again, and it reports what the last one counted.
	c.t = c.t.Add(deniedWindow)
	ok, n := l.Allow("198.51.100.7", "action-0")
	if !ok || n != 470 {
		t.Fatalf("after the window: allowed %v, suppressed %d, want true and 470", ok, n)
	}
	if ok, n := l.Allow("198.51.100.7", "action-1"); !ok || n != 0 {
		t.Errorf("the count was reported twice: %v %d", ok, n)
	}
}

func TestTheLimiterHoldsABoundedNumberOfAddresses(t *testing.T) {
	c := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewDeniedLimiter(c.now)
	for i := 0; i < maxDeniedAddrs*2; i++ {
		c.t = c.t.Add(time.Millisecond)
		l.Allow("10.0."+strconv.Itoa(i/256)+"."+strconv.Itoa(i%256), "x")
	}
	if n := l.tracked(); n > maxDeniedAddrs {
		t.Errorf("%d addresses tracked, the bound is %d", n, maxDeniedAddrs)
	}
	var nilLimiter *DeniedLimiter
	if ok, _ := nilLimiter.Allow("a", "b"); !ok {
		t.Error("a nil limiter refused")
	}
}

func TestDeniedThroughTheLimiter(t *testing.T) {
	c := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	sink := &memSink{}
	marks := 0
	rec := New(sink, Actor{Name: "anonymous", IP: "198.51.100.7"}, func() int64 { return 1 }).
		WithDeniedLimit(NewDeniedLimiter(c.now)).OnDenied(func() { marks++ })
	for i := 0; i < 3; i++ {
		rec.Denied(Event{Action: "DELETE /api/routers/{id}", Note: "HTTP 403"})
	}
	if len(sink.rows) != 1 {
		t.Fatalf("%d rows written for three identical refusals in a minute, want 1", len(sink.rows))
	}
	if marks != 3 {
		t.Errorf("OnDenied ran %d times, want 3: a counted refusal is still handled", marks)
	}
	if sink.rows[0].Outcome != "denied" {
		t.Errorf("outcome %q", sink.rows[0].Outcome)
	}
	rec.Denied(Event{Action: "POST /api/users", Note: "HTTP 401"})
	if len(sink.rows) != 2 || !strings.Contains(sink.rows[1].Detail, "and 2 more refused") {
		t.Errorf("the next row does not report the two counted: %+v", sink.rows)
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
