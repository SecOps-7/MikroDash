package roscache

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

// pusher is a Reader that can also stream, and lets a test push rows by hand.
type pusher struct {
	mu     sync.Mutex
	onRow  func(routeros.Reply)
	opens  int
	stops  int
	refuse error
	reads  int
	// cmds is every command the channel has been opened with, in order. The
	// merge tests are ABOUT the command, so a fake that discards it could only
	// check that a reopen happened and not that it opened the right thing.
	cmds []routeros.Cmd
}

func (p *pusher) Do(routeros.Cmd) ([]routeros.Reply, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads++
	return []routeros.Reply{{"from": "a read"}}, nil
}

func (p *pusher) Stream(cmd routeros.Cmd, onRow func(routeros.Reply)) (func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refuse != nil {
		return nil, p.refuse
	}
	p.opens++
	p.cmds = append(p.cmds, cmd)
	p.onRow = onRow
	return func() {
		p.mu.Lock()
		p.stops++
		p.mu.Unlock()
	}, nil
}

func (p *pusher) push(rows ...routeros.Reply) {
	p.mu.Lock()
	fn := p.onRow
	p.mu.Unlock()
	for _, r := range rows {
		fn(r)
	}
}

func (p *pusher) counts() (opens, stops, reads int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.opens, p.stops, p.reads
}

func (p *pusher) lastCmd() routeros.Cmd {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.cmds) == 0 {
		return routeros.Cmd{}
	}
	return p.cmds[len(p.cmds)-1]
}

func byName(r routeros.Reply) string { return r["name"] }

// fill is a single-owner stream fill: a Join with no Merge, which is how a menu
// declares that it has one owner. See the note on `Join.Merge`.
func fill(t *testing.T, c *Cache, menu string) func() {
	t.Helper()
	stop, err := c.JoinStream(Join{
		Menu: menu, Cmd: routeros.Cmd{Path: menu}, KeyOf: byName, Boundary: time.Hour,
	})
	if err != nil {
		t.Fatalf("JoinStream(%s): %v", menu, err)
	}
	return stop
}

// fillEvery was the timing-injection form; `StreamTimings` replaced it. This is
// the same thing said through the public seam.
func fillTimed(t *testing.T, c *Cache, menu string, keyOf func(routeros.Reply) string,
	boundary, check, stale time.Duration) (func(), error) {
	t.Helper()
	c.StreamTimings(check, stale)
	return c.JoinStream(Join{
		Menu: menu, Cmd: routeros.Cmd{}, KeyOf: keyOf, Boundary: boundary,
	})
}

// TestAStreamedMenuAnswersGetWithoutReading is the whole premise of B.1: the
// scheduler is unchanged, and `Get` answers from pushed rows.
func TestAStreamedMenuAnswersGetWithoutReading(t *testing.T) {
	p := &pusher{}
	c := New(p)
	defer fill(t, c, "/interface/monitor-traffic")()

	p.push(routeros.Reply{"name": "ether1", "rx-bits-per-second": "100"})

	rows, err := c.Get("/interface/monitor-traffic", nil, time.Second)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(rows) != 1 || rows[0]["rx-bits-per-second"] != "100" {
		t.Fatalf("Get returned %v, want the pushed row", rows)
	}
	if _, _, reads := p.counts(); reads != 0 {
		t.Errorf("%d read(s) issued for a streamed menu — the whole point is that "+
			"the router is already sending it", reads)
	}
}

// TestAKeyRepeatEndsTheRound.
//
// ── THIS TEST USED TO ASSERT A ROLLING MAP, AND B.6 IS WHY IT CHANGED ───────
//
// It was `TestARowReplacesItsOwnKeyAndOnlyThat`: three pushes of two names left
// two rows, with the second `ether1` replacing the first. That was right for a
// map that only ever accumulates, and it is wrong now.
//
// A `/print =interval=N` re-prints the WHOLE table each round with no separator,
// so a repeated key means the round has RESTARTED. The second `ether1` therefore
// belongs to the next round, and serving it would publish a table missing
// `ether2` — a page that flickers a row out and back on every interval.
//
// So the completed round is served whole, and the new one accumulates unseen.
func TestAKeyRepeatEndsTheRound(t *testing.T) {
	p := &pusher{}
	c := New(p)
	defer fill(t, c, "/interface/monitor-traffic")()

	p.push(
		routeros.Reply{"name": "ether1", "rx-bits-per-second": "100"},
		routeros.Reply{"name": "ether2", "rx-bits-per-second": "200"},
		routeros.Reply{"name": "ether1", "rx-bits-per-second": "999"}, // round 2 begins
	)

	rows, _ := c.Get("/interface/monitor-traffic", nil, time.Second)
	if len(rows) != 2 {
		t.Fatalf("%d rows, want the 2 of the completed round", len(rows))
	}
	if rows[0]["rx-bits-per-second"] != "100" || rows[1]["rx-bits-per-second"] != "200" {
		t.Errorf("served %v; a partial round was published, so the page would lose a "+
			"row and get it back on every interval", rows)
	}

	// Round two completes: now the newer reading is the one served.
	p.push(
		routeros.Reply{"name": "ether2", "rx-bits-per-second": "888"},
		routeros.Reply{"name": "ether1"}, // round 3 begins, publishing round 2
	)
	rows, _ = c.Get("/interface/monitor-traffic", nil, time.Second)
	if len(rows) != 2 || rows[0]["rx-bits-per-second"] != "999" {
		t.Errorf("after the second round completed, served %v — want the newer readings", rows)
	}
}

// TestARowThatLEAVESTheTableIsForgotten is the whole point of B.6, and the
// property whose ABSENCE kept nine menus refused.
//
// Without a round boundary the entry can only accumulate: a closed connection, a
// departed client or an expired lease stays for the life of the session, on a
// page that looks populated and is wrong.
func TestARowThatLeavesTheTableIsForgotten(t *testing.T) {
	p := &pusher{}
	c := New(p)
	defer fill(t, c, "/interface/monitor-traffic")()

	// Round one: three rows.
	p.push(routeros.Reply{"name": "a"}, routeros.Reply{"name": "b"}, routeros.Reply{"name": "c"})
	// Round two: `b` has gone. The repeat of `a` closes round one.
	p.push(routeros.Reply{"name": "a"}, routeros.Reply{"name": "c"})
	// Round three begins, publishing round two.
	p.push(routeros.Reply{"name": "a"})

	rows, _ := c.Get("/interface/monitor-traffic", nil, time.Second)
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r["name"])
	}
	if len(names) != 2 || names[0] != "a" || names[1] != "c" {
		t.Errorf("served %v, want [a c] — the row that left the table was not "+
			"forgotten, which is the unbounded growth that kept the connection "+
			"table, the registration tables and the lease table on the polled path", names)
	}
}

// TestAQuietGapEndsTheRound. A repeat is a whole interval away for a small
// table, so silence is the other signal: rows arrive in a burst and stop.
func TestAQuietGapEndsTheRound(t *testing.T) {
	p := &pusher{}
	c := New(p)
	stop, err := fillTimed(t, c, "/interface/monitor-traffic", byName, 20*time.Millisecond, 5*time.Millisecond, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	p.push(routeros.Reply{"name": "a"}, routeros.Reply{"name": "b"})
	// No repeat is coming; the watchdog tick must close the round on the gap.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f := c.fillFor("/interface/monitor-traffic")
		f.mu.Lock()
		done := f.rounds > 0
		f.mu.Unlock()
		if done {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	f := c.fillFor("/interface/monitor-traffic")
	f.mu.Lock()
	rounds := f.rounds
	f.mu.Unlock()
	if rounds == 0 {
		t.Error("a round that went quiet was never closed. For a table read once a " +
			"minute the next repeat is a minute away, so silence has to end it.")
	}
	if rows, _ := c.Get("/interface/monitor-traffic", nil, time.Second); len(rows) != 2 {
		t.Errorf("%d rows after the quiet round closed, want 2", len(rows))
	}
}

// TestTheSnapshotOrderIsStable. The collectors fingerprint their payloads to
// suppress redundant emits, so an unstable order makes every payload look
// changed — a page that redraws constantly and a dirty check that means nothing.
func TestTheSnapshotOrderIsStable(t *testing.T) {
	p := &pusher{}
	c := New(p)
	defer fill(t, c, "/interface/monitor-traffic")()

	// Pushed in an order that is not sorted, from a map that has no order.
	p.push(
		routeros.Reply{"name": "sfp1"},
		routeros.Reply{"name": "ether1"},
		routeros.Reply{"name": "bridge"},
	)

	want := []string{"bridge", "ether1", "sfp1"}
	// TWENTY TIMES, because Go's map iteration is randomised PER RANGE: one
	// agreeing pair proves nothing.
	for i := 0; i < 20; i++ {
		rows, _ := c.Get("/interface/monitor-traffic", nil, time.Second)
		for j, w := range want {
			if rows[j]["name"] != w {
				t.Fatalf("pass %d: rows[%d] = %q, want %q — the order is not stable",
					i, j, rows[j]["name"], w)
			}
		}
	}
}

// TestARowsAreEventsMenuIsRefused is the gate the design's safety rests on.
//
// Feeding `/tool/ping` a rolling map keeps only the latest measurement, so the
// collector's loss statistic — built by counting EVERY row — would read 0% for
// ever. Nothing errors, nothing logs, and every test in the tree still passes.
// That is why this refuses rather than warns.
func TestARowsAreEventsMenuIsRefused(t *testing.T) {
	c := New(&pusher{})
	for _, menu := range []string{"/tool/ping", "/log/listen"} {
		stop, err := c.JoinStream(Join{Menu: menu, Cmd: routeros.Cmd{Path: menu}, KeyOf: byName, Boundary: time.Hour})
		if err == nil {
			stop()
			t.Errorf("%s was accepted for stream-filling. Its rows are distinct "+
				"elements, not readings of one value, so a rolling map loses them "+
				"SILENTLY — which is the one failure this gate exists for.", menu)
		}
	}
}

// TestAFillNeedsAKeyFunction. An implicit key is how every row lands in one
// bucket and the entry quietly becomes "the last row the router sent".
func TestAFillNeedsAKeyFunction(t *testing.T) {
	c := New(&pusher{})
	if _, err := c.JoinStream(Join{Menu: "/interface/print", Cmd: routeros.Cmd{}, KeyOf: nil, Boundary: time.Hour}); err == nil {
		t.Error("a fill with no key function was accepted")
	}
}

// TestAnUnkeyableRowIsCountedNotDropped. A menu whose rows this cannot name is
// one a rolling map cannot represent; the count is how that is discoverable
// rather than appearing later as a page missing a row.
func TestAnUnkeyableRowIsCountedNotDropped(t *testing.T) {
	p := &pusher{}
	c := New(p)
	defer fill(t, c, "/interface/monitor-traffic")()

	p.push(routeros.Reply{"no-name-here": "x"}, routeros.Reply{"name": "ether1"})

	f := c.fillFor("/interface/monitor-traffic")
	f.mu.Lock()
	n := f.unkeyed
	f.mu.Unlock()
	if n != 1 {
		t.Errorf("unkeyed = %d, want 1 — a row this cannot name was dropped without trace", n)
	}
	if rows, _ := c.Get("/interface/monitor-traffic", nil, time.Second); len(rows) != 1 {
		t.Errorf("%d rows; the unkeyed row should not be in the map either", len(rows))
	}
}

// TestStoppingAFillReleasesTheChannelAndFallsBackToReading. A menu nobody
// streams any more must go back to being polled, not keep answering from a
// frozen map.
func TestStoppingAFillReleasesTheChannelAndFallsBackToReading(t *testing.T) {
	p := &pusher{}
	c := New(p)
	stop := fill(t, c, "/interface/monitor-traffic")
	p.push(routeros.Reply{"name": "ether1"})

	stop()
	stop() // idempotent, like every other release in this package

	if opens, stops, _ := p.counts(); opens != 1 || stops != 1 {
		t.Errorf("opens=%d stops=%d, want 1 and 1 — a double stop closed the channel twice",
			opens, stops)
	}
	if got := c.StreamedMenus(); len(got) != 0 {
		t.Errorf("still streaming %v after the stop", got)
	}
	if _, err := c.Get("/interface/monitor-traffic", nil, time.Second); err != nil {
		t.Fatalf("Get after the stop: %v", err)
	}
	if _, _, reads := p.counts(); reads != 1 {
		t.Errorf("%d read(s) after the fill was stopped, want 1 — the menu did not go "+
			"back to being polled and would answer from a frozen map for ever", reads)
	}
}

// TestARefusedStreamIsNotLeftRegistered. Otherwise the menu answers Get from an
// empty map for ever, and a page shows nothing while the cache reports success.
func TestARefusedStreamIsNotLeftRegistered(t *testing.T) {
	p := &pusher{refuse: errors.New("no")}
	c := New(p)
	if _, err := c.JoinStream(Join{Menu: "/x", Cmd: routeros.Cmd{}, KeyOf: byName, Boundary: time.Hour}); err == nil {
		t.Fatal("a refused stream reported success")
	}
	if got := c.StreamedMenus(); len(got) != 0 {
		t.Errorf("a refused stream left %v registered; Get would answer from an empty map", got)
	}
}

// TestTheSameMenuIsNotFilledTwice. Two fills on one menu means two channels on
// the router for one answer, and the second silently wins every Get.
func TestTheSameMenuIsNotFilledTwice(t *testing.T) {
	c := New(&pusher{})
	stop := fill(t, c, "/interface/monitor-traffic")
	defer stop()
	if _, err := c.JoinStream(Join{Menu: "/interface/monitor-traffic", Cmd: routeros.Cmd{}, KeyOf: byName, Boundary: time.Hour}); err == nil {
		t.Error("a second fill of the same menu was accepted — two channels, one answer")
	}
}

// ── THE WATCHDOG ────────────────────────────────────────────────────────────
//
// A polled entry fails LOUDLY: the read errors, and the error is cached and
// surfaced. A pushed entry can go SILENT — the router stops sending, or the
// channel wedges — and the cache would serve its last value for ever with
// nothing noticing. `traffic` already carried this recovery for its one stream;
// here it covers every stream, which is the point of moving it.
func TestASilentChannelIsRestarted(t *testing.T) {
	p := &pusher{}
	c := New(p)
	// A SMALL boundary, because staleness is now derived from it: a stream is
	// silent when it has said nothing for two of its own intervals, never less
	// than the floor. A one-hour boundary would put the watchdog two hours away.
	stop, err := fillTimed(t, c, "/interface/monitor-traffic", byName, 2*time.Millisecond, 5*time.Millisecond, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	p.push(routeros.Reply{"name": "ether1", "rx-bits-per-second": "100"})

	// Say nothing for well past the staleness bound.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if opens, _, _ := p.counts(); opens > 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	opens, stops, _ := p.counts()
	if opens < 2 {
		t.Fatalf("the channel went silent and was never reopened (opens=%d). A stream "+
			"that stops delivering would serve its last value for ever.", opens)
	}
	if stops < 1 {
		t.Errorf("the old channel was not closed before reopening (stops=%d) — the "+
			"router would accumulate a channel per restart", stops)
	}

	// THE ROWS SURVIVE THE RESTART, and that is deliberate. They are the last
	// readings the router gave and they are what a page renders while the
	// channel comes back; dropping them would blank every card for the length
	// of a reconnect, which is the opposite of what this is for.
	rows, _ := c.Get("/interface/monitor-traffic", nil, time.Second)
	if len(rows) != 1 || rows[0]["rx-bits-per-second"] != "100" {
		t.Errorf("the rolling map was cleared by the restart: %v", rows)
	}
}

// TestAHealthyChannelIsNotRestarted — the other direction, or the test above
// passes against a watchdog that simply restarts everything on a timer.
func TestAHealthyChannelIsNotRestarted(t *testing.T) {
	p := &pusher{}
	c := New(p)
	stop, err := fillTimed(t, c, "/interface/monitor-traffic", byName, 2*time.Millisecond, 5*time.Millisecond, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	// A row every 20ms for 300ms: never silent for the 200ms bound.
	for i := 0; i < 15; i++ {
		p.push(routeros.Reply{"name": "ether1"})
		time.Sleep(20 * time.Millisecond)
	}
	if opens, _, _ := p.counts(); opens != 1 {
		t.Errorf("a channel delivering steadily was restarted %d time(s); the watchdog "+
			"is firing on a clock rather than on silence", opens-1)
	}
}

// TestAStreamedMenuStillFiresOnDeliver.
//
// ── A TRAP THIS DESIGN AVOIDS BY BEING ADDITIVE, PINNED SO IT STAYS AVOIDED ─
//
// `OnDeliver` is the dormancy supervisor's heartbeat: since phase 3.3 it rides
// the scheduler's deliveries instead of a ticker, and it is what decides a
// collector has stopped changing and may sleep. A stream filler that BYPASSED
// the scheduler — pushed rows straight to subscribers — would leave every
// streamed collector unjudged, so nothing would ever go dormant again. Nothing
// would fail; the router would just quietly never be left alone.
//
// This design does not bypass it: `Get` short-circuits for a streamed menu and
// the scheduler is otherwise UNCHANGED, so Invalidate/Get/deliver still runs at
// the subscriber's cadence and `deliver` still fires the hook. That is a
// consequence of the shape rather than a feature anyone wrote, which is exactly
// the kind of thing that is true until someone "optimises" the scheduler to skip
// streamed menus.
func TestAStreamedMenuStillFiresOnDeliver(t *testing.T) {
	p := &pusher{}
	c := New(p)
	defer fill(t, c, "/interface/monitor-traffic")()

	var mu sync.Mutex
	var beats []string
	c.OnDeliver(func(menu string) {
		mu.Lock()
		beats = append(beats, menu)
		mu.Unlock()
	})

	var got [][]routeros.Reply
	release := c.Subscribe("/interface/monitor-traffic", nil, Every(5*time.Millisecond),
		func(rows []routeros.Reply, _ error) {
			mu.Lock()
			got = append(got, rows)
			mu.Unlock()
		})
	defer release()

	p.push(routeros.Reply{"name": "ether1", "rx-bits-per-second": "100"})

	sched := NewScheduler(c, 2*time.Millisecond)
	sched.Start()
	defer sched.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n, d := len(got), len(beats)
		mu.Unlock()
		if n > 0 && d > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(beats) == 0 {
		t.Error("a streamed menu never fired OnDeliver. Dormancy rides that hook, so " +
			"every streamed collector would go unjudged and nothing would ever sleep — " +
			"and nothing would fail to say so.")
	}
	if len(got) == 0 {
		t.Fatal("the subscriber's onRows never ran for a streamed menu")
	}
	if len(got[0]) != 1 || got[0][0]["rx-bits-per-second"] != "100" {
		t.Errorf("the subscriber received %v, want the pushed row", got[0])
	}
	if _, _, reads := p.counts(); reads != 0 {
		t.Errorf("the scheduler issued %d read(s) for a streamed menu", reads)
	}
}

// TestOnlyRowsThatAreNotReadingsAreRefused.
//
// ── THE REFUSAL LIST HAS BEEN NARROWED TWICE, BY MEASUREMENT ───────────────
//
// This test used to name nine menus. Both reasons they were on it have been
// removed, and neither by argument:
//
//	CHURN      the entry could only accumulate, so a row that LEFT the table
//	           never left the map. B.6 found the round boundary and that reason
//	           went; the connection table came off the list and was verified live
//	           by its count going DOWN, which a broken implementation cannot do.
//	EMPTINESS  a table with no rows sends nothing, indistinguishable from a dead
//	           channel, so an emptied table held its last contents. The watchdog
//	           already ran the distinguishing experiment: silence that survives a
//	           deliberate reopen is evidence of emptiness, not breakage.
//
// WHAT REMAINS IS PERMANENT AND IS NOT ABOUT THE MECHANISM. `ping` and `logs`
// are refused because of what their rows ARE: a ping result is one measurement
// the collector counts into min/max/avg/loss, and a log line is one event. No
// amount of boundary or liveness detection makes the latest one stand for the
// others.
//
// So this test asserts the shape of the list rather than its contents: the two
// that can never be lifted are still there, and nothing has crept back in on a
// mechanical reason that has since been solved.
func TestOnlyRowsThatAreNotReadingsAreRefused(t *testing.T) {
	c := New(&pusher{})

	for _, menu := range []string{"/tool/ping", "/log/listen"} {
		stop, err := c.JoinStream(Join{Menu: menu, Cmd: routeros.Cmd{Path: menu}, KeyOf: byName, Boundary: time.Hour})
		if err == nil {
			stop()
			t.Errorf("%s was accepted. Its rows are distinct elements the collector "+
				"needs every one of, not readings of one value, so a rolling entry "+
				"loses them SILENTLY — 0%% loss for ever, or dropped log lines.", menu)
		}
	}

	// AND NOTHING ELSE. A menu refused for churn or for emptiness has been
	// refused for a reason this package has since solved, and the effect is a
	// collector left polling for no measured cause.
	for menu := range unrollableForTest() {
		if menu != "/tool/ping" && menu != "/log/listen" {
			t.Errorf("%s is refused. Both mechanical reasons for refusing a menu — "+
				"churn and emptiness — have been solved and measured, so a refusal "+
				"now needs to be about what the ROWS ARE. If this is a new and real "+
				"third reason, say so here.", menu)
		}
	}
}

// unrollableForTest exposes the list to this package's own tests.
func unrollableForTest() map[string]string { return unrollable }

// TestAnEmptiedTableEventuallyPublishesEmpty.
//
// ── SILENCE IS AMBIGUOUS, AND THE WATCHDOG IS THE EXPERIMENT ───────────────
//
// RouterOS sends nothing at all for a `/print =interval=N` on an empty table —
// measured, not assumed: the B.4 probe held `/ppp/active/print` open for three
// seconds on four routers and received nothing, and that menu is empty on all of
// them. So "no rows" and "dead channel" look identical.
//
// Holding the last contents was the safe reading, and it is what kept the
// registration tables, the lease table and `/ppp/active` on the polled path: an
// emptied table went on showing rows that had gone.
//
// The watchdog already performs the distinguishing experiment. It reopens a
// channel that has gone quiet, and a reopened channel on a router that answers
// delivers at once IF the table has rows. Silence surviving a deliberate restart
// is evidence of emptiness rather than of breakage.
func TestAnEmptiedTableEventuallyPublishesEmpty(t *testing.T) {
	p := &pusher{}
	c := New(p)
	stop, err := fillTimed(t, c, "/interface/monitor-traffic", byName, 5*time.Millisecond, 5*time.Millisecond, 15*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	// A complete round, so there is something to lose.
	p.push(routeros.Reply{"name": "a"}, routeros.Reply{"name": "b"})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rows, _ := c.Get("/interface/monitor-traffic", nil, time.Second); len(rows) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rows, _ := c.Get("/interface/monitor-traffic", nil, time.Second); len(rows) != 2 {
		t.Fatalf("%d rows before the silence; the test has nothing to observe", len(rows))
	}

	// Now say nothing at all, as an emptied table does.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rows, _ := c.Get("/interface/monitor-traffic", nil, time.Second); len(rows) == 0 {
			return // published empty, which is the point
		}
		time.Sleep(10 * time.Millisecond)
	}
	rows, _ := c.Get("/interface/monitor-traffic", nil, time.Second)
	t.Errorf("after a restart and a full staleness window of silence the entry still "+
		"holds %d row(s). An emptied table would show rows that have gone, which is "+
		"what kept five menus on the polled path.", len(rows))
}

// TestARowEndsTheSilenceRun — the other direction, so the rule above cannot be
// satisfied by an entry that simply empties itself on a timer.
func TestARowEndsTheSilenceRun(t *testing.T) {
	p := &pusher{}
	c := New(p)
	stop, err := fillTimed(t, c, "/interface/monitor-traffic", byName, 5*time.Millisecond, 5*time.Millisecond, 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	// Rows throughout, at a rate that never leaves a staleness window empty.
	for i := 0; i < 40; i++ {
		p.push(routeros.Reply{"name": "a"}, routeros.Reply{"name": "b"})
		time.Sleep(10 * time.Millisecond)
	}
	if rows, _ := c.Get("/interface/monitor-traffic", nil, time.Second); len(rows) == 0 {
		t.Error("a table delivering steadily was published as EMPTY. The rule is " +
			"firing on a clock rather than on silence, and every streamed page " +
			"would blank periodically.")
	}
}

// TestASlowMenuIsNotCalledDeadOrEmpty.
//
// ── THE BUG THIS PINS SHIPPED TO A PAGE AND NOT TO A TEST ──────────────────
//
// `streamStale` is ten seconds, which is right for a menu delivering every
// second or two and wrong for a slow one. `dhcpNetworks` runs at ten MINUTES, so
// its channel says nothing for ten minutes by design.
//
// With a fixed bound the watchdog called that death every ten seconds and
// reopened, and the empty-table rule — two staleness windows of silence — then
// concluded after twenty seconds that a ten-minute menu was empty. The DHCP page
// read "No DHCP networks on this device" while the router held three.
//
// The Go suite was green and the `=interval=` probe passed. Only the page was
// wrong, which is what "live verification is mandatory" is for.
func TestASlowMenuIsNotCalledDeadOrEmpty(t *testing.T) {
	p := &pusher{}
	c := New(p)
	// A "slow" menu in miniature: a 300ms cadence against a 10ms floor.
	stop, err := fillTimed(t, c, "/interface/monitor-traffic", byName, 300*time.Millisecond, 5*time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	p.push(routeros.Reply{"name": "a"}, routeros.Reply{"name": "b"})

	// Well past the FLOOR, and well inside one of this menu's own intervals.
	time.Sleep(150 * time.Millisecond)

	rows, _ := c.Get("/interface/monitor-traffic", nil, time.Second)
	if len(rows) != 2 {
		t.Errorf("a slow menu was emptied after %v of silence: %d rows, want 2. Its "+
			"cadence is 300ms, so silence shorter than that says nothing at all "+
			"about whether the table has rows.", 150*time.Millisecond, len(rows))
	}
	opens, _, _ := p.counts()
	if opens > 1 {
		t.Errorf("a slow menu's channel was reopened %d time(s) inside one of its own "+
			"intervals; the watchdog is measuring silence against a fixed bound "+
			"rather than against the cadence", opens-1)
	}
}

// TestAnUnwarmedStreamFallsThroughToARead.
//
// ── THE RACE THAT EMPTIED THE DHCP PAGE FOR TEN MINUTES ────────────────────
//
// `JoinStream` returns when the channel is OPEN; the first row arrives some
// milliseconds later. The scheduler can deliver inside that window, and `Get`
// answering "no rows" there is not a cheap wrong answer — it is published to the
// collector, which builds an EMPTY payload, and the next delivery is a whole
// cadence away.
//
// Measured on live hardware: the DHCP page read "0 leases" and "No DHCP networks
// on this device" while the router held 45 and 3. Both menus run at ten minutes,
// so one empty answer at startup persisted for ten minutes.
//
// The same race exists on a fast menu and self-corrects within a second, which
// is precisely why it would otherwise have been found much later and blamed on
// something else.
func TestAnUnwarmedStreamFallsThroughToARead(t *testing.T) {
	p := &pusher{}
	c := New(p)
	defer fill(t, c, "/interface/monitor-traffic")()

	// Nothing pushed yet: the channel is open and has delivered nothing.
	rows, err := c.Get("/interface/monitor-traffic", nil, time.Second)
	if err != nil {
		t.Fatalf("Get on an unwarmed stream: %v", err)
	}
	if _, _, reads := p.counts(); reads != 1 {
		t.Errorf("%d read(s) issued; an unwarmed stream must fall through to the "+
			"ordinary read path rather than publishing an empty table", reads)
	}
	// AND ONLY WHILE UNWARMED. An entry that has completed a round is
	// authoritative about its own emptiness; sending THAT back to the read path
	// would poll an empty table for ever while also holding a channel.
	if len(rows) != 1 || rows[0]["from"] != "a read" {
		t.Errorf("got %v, want the row a real read returns", rows)
	}

	// Once it has rows, the stream answers and the reads stop.
	p.push(routeros.Reply{"name": "ether1"})
	rows, _ = c.Get("/interface/monitor-traffic", nil, time.Second)
	if len(rows) != 1 || rows[0]["name"] != "ether1" {
		t.Fatalf("got %v, want the pushed row", rows)
	}
	if _, _, reads := p.counts(); reads != 1 {
		t.Errorf("%d reads; a warmed stream must answer without touching the router", reads)
	}
}

// ── SHARED FILLS ───────────────────────────────────────────────────────────
//
// `/interface/monitor-traffic` is the menu two collectors want, and they want
// different things from it. These tests are about the merge, the fan-out and the
// narrowing — everything a Join with a Merge adds over one without.

// splitList, joinList, sortStrings, atoiOr and itoa keep this file's merge rule
// standing on the standard library the collectors will use, without importing
// half of it into a test that is about the cache.

func splitList(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}
func joinList(v []string) string { return strings.Join(v, ",") }
func sortStrings(v []string)     { sort.Strings(v) }
func itoa(n int) string          { return strconv.Itoa(n) }
func atoiOr(v string, def int) int {
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// mergeIfaces is the monitor-traffic rule, in the shape the collectors use it:
// the union of every holder's `=interface=` list, and the finest `=interval=`.
func mergeIfaces(cmds []routeros.Cmd) routeros.Cmd {
	seen := map[string]bool{}
	var names []string
	best := 0
	for _, c := range cmds {
		for _, a := range c.Args {
			switch {
			case len(a) > 11 && a[:11] == "=interface=":
				for _, n := range splitList(a[11:]) {
					if !seen[n] {
						seen[n] = true
						names = append(names, n)
					}
				}
			case len(a) > 10 && a[:10] == "=interval=":
				if n := atoiOr(a[10:], 0); n > 0 && (best == 0 || n < best) {
					best = n
				}
			}
		}
	}
	sortStrings(names)
	if best == 0 {
		best = 1
	}
	return routeros.Cmd{Path: "/interface/monitor-traffic", Args: []string{
		"=interface=" + joinList(names), "=interval=" + itoa(best),
	}}
}

func joinIfaces(t *testing.T, c *Cache, ifaces string, sec int, onRow func(routeros.Reply)) func() {
	t.Helper()
	rel, err := c.JoinStream(Join{
		Menu: "/interface/monitor-traffic",
		Cmd: routeros.Cmd{Path: "/interface/monitor-traffic", Args: []string{
			"=interface=" + ifaces, "=interval=" + itoa(sec),
		}},
		KeyOf: byName, Boundary: time.Duration(sec) * time.Second,
		Merge: mergeIfaces, OnRow: onRow,
	})
	if err != nil {
		t.Fatalf("JoinStream(%s): %v", ifaces, err)
	}
	return rel
}

// TestTwoHoldersShareOneChannel is the whole point of the merge: the menu that
// was read on two channels per router is read on one.
func TestTwoHoldersShareOneChannel(t *testing.T) {
	p := &pusher{}
	c := New(p)

	relA := joinIfaces(t, c, "ether1,ether2", 5, nil) // the rates holder
	if got := p.lastCmd().Args[0]; got != "=interface=ether1,ether2" {
		t.Fatalf("first holder opened with %q", got)
	}
	relB := joinIfaces(t, c, "ether2,wlan1", 1, nil) // the chart holder

	opens, stops, _ := p.counts()
	if opens != 2 || stops != 1 {
		t.Errorf("opens=%d stops=%d; the second holder should have REPLACED the "+
			"channel, not added one", opens, stops)
	}
	// THE UNION, AND THE FINER INTERVAL. Neither holder's set contains the
	// other's, which is why this is a merge and not a subscription.
	if got := p.lastCmd().Args[0]; got != "=interface=ether1,ether2,wlan1" {
		t.Errorf("merged interface list = %q, want the union", got)
	}
	if got := p.lastCmd().Args[1]; got != "=interval=1" {
		t.Errorf("merged interval = %q; a merged channel must deliver at the finest "+
			"cadence any holder asked for, or the faster one silently loses "+
			"resolution", got)
	}

	// A HOLDER LEAVING NARROWS IT. A refcount that only ever widened would keep
	// the last viewer's selection open for the life of the session.
	relB()
	if got := p.lastCmd().Args[0]; got != "=interface=ether1,ether2" {
		t.Errorf("after the chart holder left, the channel carries %q", got)
	}
	opens, stops, _ = p.counts()
	if opens != 3 {
		t.Errorf("opens=%d after the narrowing, want 3", opens)
	}

	relA()
	_, stops, _ = p.counts()
	if stops != 3 {
		t.Errorf("stops=%d after the last holder left; the channel is still open", stops)
	}
	if c.Streaming("/interface/monitor-traffic") {
		t.Error("the fill survived its last holder")
	}
}

// TestASubsetHolderCostsNothing. The common case once the sets settle: a holder
// that wants less than is already open must not restart the channel, because a
// restart is a gap in every other holder's data.
func TestASubsetHolderCostsNothing(t *testing.T) {
	p := &pusher{}
	c := New(p)
	defer joinIfaces(t, c, "ether1,ether2,wlan1", 1, nil)()
	opens, _, _ := p.counts()

	rel := joinIfaces(t, c, "ether2", 1, nil)
	defer rel()
	if got, _, _ := p.counts(); got != opens {
		t.Errorf("a holder wanting a subset reopened the channel (%d -> %d); every "+
			"other holder loses a second of data for nothing", opens, got)
	}
}

// TestEveryHolderSeesEveryRow — the fan-out `traffic` needs.
//
// A snapshot is not enough for it: the chart is built from packets as they
// arrive, one point per interface per second, and a holder reading the rolling
// map on its own cadence would see only the latest.
func TestEveryHolderSeesEveryRow(t *testing.T) {
	p := &pusher{}
	c := New(p)
	var mu sync.Mutex
	var a, b []string
	relA := joinIfaces(t, c, "ether1", 1, func(r routeros.Reply) {
		mu.Lock()
		a = append(a, r["name"])
		mu.Unlock()
	})
	defer relA()
	relB := joinIfaces(t, c, "ether1", 1, func(r routeros.Reply) {
		mu.Lock()
		b = append(b, r["name"])
		mu.Unlock()
	})
	defer relB()

	p.push(
		routeros.Reply{"name": "ether1", "rx-bits-per-second": "1"},
		routeros.Reply{"name": "ether1", "rx-bits-per-second": "2"},
		routeros.Reply{"name": "ether1", "rx-bits-per-second": "3"},
	)
	mu.Lock()
	defer mu.Unlock()
	if len(a) != 3 || len(b) != 3 {
		t.Errorf("holders saw %d and %d rows, want 3 each — a chart built from a "+
			"snapshot would draw one point where the router sent three", len(a), len(b))
	}
}

// TestARowHookIsNotCalledUnderTheFillLock.
//
// ── THE DEADLOCK THIS FORBIDS HUNG THE SUITE ONCE ALREADY ──────────────────
//
// A holder's `OnRow` is another collector's delivery path — `traffic`'s takes its
// own mutex and emits to the hub. Calling it under `f.mu` puts two collector
// locks in one order here and invites the opposite order elsewhere, which is
// exactly what `syncRateChannel` and `Tick` did on 2026-09-09: the suite HUNG
// rather than failing, which is the worst way to find out.
//
// So the hook reaches back into the cache for a value that needs the same lock.
// Held, this never returns; the timeout is what turns a hang into a failure.
func TestARowHookIsNotCalledUnderTheFillLock(t *testing.T) {
	p := &pusher{}
	c := New(p)
	done := make(chan bool, 1)
	rel := joinIfaces(t, c, "ether1", 1, func(routeros.Reply) {
		// StreamStats takes f.mu. Under the fill's lock this deadlocks.
		_, ok := c.StreamStats("/interface/monitor-traffic")
		done <- ok
	})
	defer rel()

	go p.push(routeros.Reply{"name": "ether1"})
	select {
	case ok := <-done:
		if !ok {
			t.Error("StreamStats reported no fill from inside a row hook")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a row hook could not read the cache: absorb is holding the fill " +
			"lock across the fan-out, which is a deadlock waiting for a second " +
			"lock order")
	}
}

// TestAnUnsharedMenuRefusesASecondHolder.
//
// A `Join` with no `Merge` declares that this menu has one owner — two
// collectors quietly fighting over one channel is a whole class of bug — so
// sharing has to be opted into by BOTH holders rather than acquired by being
// second. This was a separate entry point, `FillFromStream`, until phase 6.2
// collapsed the two forms into one.
func TestAnUnsharedMenuRefusesASecondHolder(t *testing.T) {
	p := &pusher{}
	c := New(p)
	stop := fill(t, c, "/interface/monitor-traffic")
	defer stop()

	if _, err := c.JoinStream(Join{
		Menu:  "/interface/monitor-traffic",
		Cmd:   routeros.Cmd{Path: "/interface/monitor-traffic"},
		KeyOf: byName, Merge: mergeIfaces,
	}); err == nil {
		t.Error("a second holder took over a menu that declared no merge rule; the " +
			"single-owner guard is what stops two collectors sharing a channel by " +
			"accident")
	}
}

// TestJoinStreamRefusesWhatItCannotDo — a merge rule is not optional, because a
// shared channel without one is opened for whichever holder arrived last.
func TestJoinStreamRefusesWhatItCannotDo(t *testing.T) {
	c := New(&pusher{})
	base := Join{
		Menu:  "/interface/monitor-traffic",
		Cmd:   routeros.Cmd{Path: "/interface/monitor-traffic"},
		KeyOf: byName, Merge: mergeIfaces,
	}
	// A nil Merge is no longer refused: it DECLARES a single-owner menu, which
	// is what `TestAnUnsharedMenuRefusesASecondHolder` drives. Phase 6.2.
	noKey := base
	noKey.KeyOf = nil
	if _, err := c.JoinStream(noKey); err == nil {
		t.Error("a Join with no KeyOf was accepted")
	}
	unroll := base
	unroll.Menu = "/tool/ping"
	if _, err := c.JoinStream(unroll); err == nil {
		t.Error("a Join on an unrollable menu was accepted; rows that are distinct " +
			"measurements cannot back a rolling entry however they are held")
	}
}

// TestTheMergedBoundaryIsTheFinestHolderAsksFor.
//
// ── A SURVIVING MUTATION IS WHY THIS EXISTS ────────────────────────────────
//
// Deleting `tightenBoundaryLocked` from the join path left every other test
// green. The boundary is how long a quiet gap must be to end a round, and it is
// derived from the CADENCE — so a fill created by a five-second holder keeps a
// five-second boundary after a one-second holder widens the channel to one
// second. The round boundary is then five times the interval, and the staleness
// window that the empty-table rule doubles is wrong with it.
//
// It survives in practice on this menu because the repeat-key signal ends a
// round anyway — monitor-traffic re-sends every interface every interval — which
// is exactly the kind of "works by accident on the one menu we tried it on" that
// stops being true on the next.
//
// READ FROM THE FIELDS, and that is the honest description: the boundary is a
// scalar the mechanism consults, there is no cheaper observable that does not
// involve waiting out real seconds, and the test is in-package.
func TestTheMergedBoundaryIsTheFinestHolderAsksFor(t *testing.T) {
	p := &pusher{}
	c := New(p)
	defer joinIfaces(t, c, "ether1", 5, nil)()

	f := c.fillFor("/interface/monitor-traffic")
	if f == nil {
		t.Fatal("no fill after the first holder")
	}
	f.mu.Lock()
	first, firstStale := f.boundary, f.stale
	f.mu.Unlock()
	if first != 5*time.Second {
		t.Fatalf("the first holder's boundary is %v, want 5s", first)
	}

	defer joinIfaces(t, c, "ether1", 1, nil)()
	f.mu.Lock()
	after, afterStale := f.boundary, f.stale
	f.mu.Unlock()
	if after != time.Second {
		t.Errorf("after a 1s holder joined a 5s channel the boundary is %v, want 1s — "+
			"the merged channel delivers every second and a round that takes five to "+
			"close is four seconds of two rounds counted as one", after)
	}
	// The staleness floor must not RISE with the boundary falling, and must not
	// fall below the floor either: it is max(streamStale, 2*boundary).
	if afterStale < streamStale {
		t.Errorf("staleness fell to %v, below the %v floor", afterStale, streamStale)
	}
	if afterStale > firstStale {
		t.Errorf("staleness rose from %v to %v as the cadence got FINER",
			firstStale, afterStale)
	}
}
