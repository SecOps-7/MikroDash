package collect

import (
	"reflect"
	"testing"
	"time"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// mutateDeep is mutate reaching into what a queue row is made of: the limit and
// counter pairs are structs, and the counters are pointers to int. mutate skips
// both, and a skipped field reads as covered, which is the gap this test exists
// to close. It reports whether anything was changed.
func mutateDeep(f reflect.Value) bool {
	switch f.Kind() {
	case reflect.Struct:
		any := false
		for i := 0; i < f.NumField(); i++ {
			if f.Field(i).CanSet() && mutateDeep(f.Field(i)) {
				any = true
			}
		}
		return any
	case reflect.Ptr:
		switch f.Type().Elem().Kind() {
		case reflect.Int, reflect.Int64, reflect.String:
			v := reflect.New(f.Type().Elem())
			if !f.IsNil() {
				v.Elem().Set(f.Elem())
			}
			mutate(v.Elem())
			f.Set(v)
			return true
		}
	}
	return mutate(f)
}

// TestQueuesFingerprintCoversEveryRenderedField. The fingerprint was a
// hand-picked tuple, and this collector has no heartbeat, so an edit to a
// queue's priority, queue type, burst, a tree's limit-at, a move in the list or
// an `invalid` flag was re-read, hashed identically and never sent (survey,
// 2026-10-03). Every field moves it now, except the counters, which move on
// their own.
func TestQueuesFingerprintCoversEveryRenderedField(t *testing.T) {
	q := &Queues{}
	fill := func(row any) any {
		rv := reflect.ValueOf(row).Elem()
		for i := 0; i < rv.NumField(); i++ {
			mutateDeep(rv.Field(i))
		}
		return row
	}
	// The covered check, field by field, with mutateDeep in place of mutate.
	covered := func(label string, row any, exclude map[string]string, fp func(any) string) {
		t.Helper()
		rt := reflect.TypeOf(row).Elem()
		base := fp(row)
		for i := 0; i < rt.NumField(); i++ {
			name := rt.Field(i).Name
			fresh := reflect.New(rt)
			fresh.Elem().Set(reflect.ValueOf(row).Elem())
			// A deep copy of the pointers, or the mutation would reach the base row.
			fresh.Elem().Field(i).Set(reflect.ValueOf(deepCopy(fresh.Elem().Field(i).Interface())))
			if !mutateDeep(fresh.Elem().Field(i)) {
				continue
			}
			moved := fp(fresh.Interface()) != base
			if why, ok := exclude[name]; ok {
				if moved {
					t.Errorf("%s.%s moved the fingerprint and must not: %s", label, name, why)
				}
				continue
			}
			if !moved {
				t.Errorf("%s.%s does NOT move the fingerprint: an edit to it never reaches an open page", label, name)
			}
		}
	}
	counters := map[string]string{
		"Bytes": "a counter", "Packets": "a counter", "Dropped": "a counter", "QueuedBytes": "a counter",
		"RateWindowMs": "moves every tick",
		"RateBps":      "rounded to kbit, so a 7 bit/s nudge must not move it; checked below",
	}
	covered("SimpleQueue", fill(&SimpleQueue{}), counters, func(r any) string {
		return q.fingerprint(&QueuesPayload{Simple: []SimpleQueue{*r.(*SimpleQueue)}})
	})
	covered("TreeQueue", fill(&TreeQueue{}), counters, func(r any) string {
		return q.fingerprint(&QueuesPayload{Tree: []TreeQueue{*r.(*TreeQueue)}})
	})
	covered("QueuesPayload", fill(&QueuesPayload{}), map[string]string{
		"TS": "the time of the reading, not a change",
	}, func(r any) string { return q.fingerprint(r.(*QueuesPayload)) })

	// The rates: a visible change moves it, a sub-kbit one does not.
	rate := func(bps float64) string {
		return q.fingerprint(&QueuesPayload{
			Simple: []SimpleQueue{{RateBps: RatePair{Up: &bps}}},
			Tree:   []TreeQueue{{RateBps: &bps}},
		})
	}
	if rate(2_000_000) == rate(3_000_000) {
		t.Error("a rate going from 2 to 3 Mbit/s does not move the fingerprint")
	}
	if rate(2_000_000) != rate(2_000_100) {
		t.Error("a 100 bit/s wobble moves the fingerprint, which defeats the dirty check")
	}
	// The counters really are left out: a busy queue alone must not emit.
	busy := func(n int) string {
		return q.fingerprint(&QueuesPayload{
			Simple: []SimpleQueue{{Bytes: IntPair{Up: &n, Down: &n}}},
			Tree:   []TreeQueue{{Bytes: &n}},
		})
	}
	if busy(1) != busy(2) {
		t.Error("a byte counter moving alone moves the fingerprint")
	}
}

// deepCopy copies a value through its pointers, so mutating the copy cannot
// reach the original.
func deepCopy(v any) any {
	src := reflect.ValueOf(v)
	dst := reflect.New(src.Type()).Elem()
	copyInto(dst, src)
	return dst.Interface()
}

func copyInto(dst, src reflect.Value) {
	switch src.Kind() {
	case reflect.Ptr:
		if src.IsNil() {
			return
		}
		p := reflect.New(src.Type().Elem())
		copyInto(p.Elem(), src.Elem())
		dst.Set(p)
	case reflect.Struct:
		dst.Set(src)
		for i := 0; i < src.NumField(); i++ {
			if dst.Field(i).CanSet() {
				copyInto(dst.Field(i), src.Field(i))
			}
		}
	default:
		dst.Set(src)
	}
}

func fasttrackRule(id string, disabled string) routeros.Reply {
	return routeros.Reply{".id": id, "chain": "forward", "action": "fasttrack-connection",
		"disabled": disabled, "dynamic": "false"}
}

// TestTheFastTrackBannerSeesARuleChangedOnTheRouter. The banner read the
// firewall collector's held filter table, which is refreshed only while the
// Firewall page is open on its Filter tab, so with only the Queues page open a
// FastTrack rule added or disabled in Winbox never moved it (survey,
// 2026-10-03). A copy older than queuesFasttrackEvery is re-read now, and a
// fresh one is not.
func TestTheFastTrackBannerSeesARuleChangedOnTheRouter(t *testing.T) {
	r := &fwRouter{filter: []routeros.Reply{fwRow("*1", "accept", "established", "1")}}
	f := NewFirewall(r, hub.Relay{}, 5000)
	clock := time.Unix(1_800_000_000, 0)
	f.now = func() time.Time { return clock }
	q := NewQueues(r, hub.Relay{}, f, 5000)

	// Firewall collection off: the collector never started, so "cannot say",
	// and nothing is read on its behalf.
	if got := q.fasttrack(); got.State != "unknown" {
		t.Errorf("an unstarted firewall gave %q, want unknown", got.State)
	}
	if n := r.reads["/ip/firewall/filter/print"]; n != 0 {
		t.Errorf("an unstarted firewall read the filter table %d times", n)
	}

	f.Start()
	if got := q.fasttrack(); got.State != "clear" {
		t.Fatalf("the control: no FastTrack rule, got %q", got.State)
	}

	// In Winbox: the default FastTrack rule is added.
	r.mu.Lock()
	r.filter = append(r.filter, fasttrackRule("*2", "false"))
	r.reads = nil
	r.mu.Unlock()

	clock = clock.Add(30 * time.Second)
	q.fasttrack()
	if n := r.reads["/ip/firewall/filter/print"]; n != 0 {
		t.Errorf("the filter table was re-read %d times inside the slow lane", n)
	}

	clock = clock.Add(queuesFasttrackEvery)
	if got := q.fasttrack(); got.State != "active" || got.Count != 1 {
		t.Errorf("a FastTrack rule added on the router never reached the banner: %+v", got)
	}

	// The Firewall page open on its Filter tab keeps the copy fresh, and the
	// banner then costs no read of its own.
	r.mu.Lock()
	r.filter[1] = fasttrackRule("*2", "true")
	r.reads = nil
	r.mu.Unlock()
	clock = clock.Add(queuesFasttrackEvery)
	f.pollActive()
	if got := q.fasttrack(); got.State != "clear" {
		t.Errorf("a disabled FastTrack rule still shows: %+v", got)
	}
	if n := r.reads["/ip/firewall/filter/print"]; n != 1 {
		t.Errorf("with the Filter tab fresh, the filter table was read %d times, want 1 (the tab's own)", n)
	}

	if queuesFasttrackEvery < 30*time.Second {
		t.Errorf("queuesFasttrackEvery is %s: configuration belongs on a lane of 30s or longer", queuesFasttrackEvery)
	}
}
