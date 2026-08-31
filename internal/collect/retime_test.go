package collect

// The live poll re-tune: `pollInterval` and `pollLoop.retime`.
//
// A settings save changes a running collector's period
// (`collection.PollRetunes` decides which and to what). These cover the
// mechanism underneath: that the new period is visible to the payload, that it
// reaches the PENDING timer rather than only the next one, and that writing it
// from another goroutine is safe.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

func TestPollIntervalIsReadableAndWritable(t *testing.T) {
	p := newPollInterval(5000)
	if p.ms() != 5000 {
		t.Fatalf("ms() = %d, want 5000", p.ms())
	}
	if p.duration() != 5*time.Second {
		t.Errorf("duration() = %v, want 5s", p.duration())
	}
	p.set(250)
	if p.ms() != 250 {
		t.Errorf("ms() = %d after set(250)", p.ms())
	}
	// NOT re-clamped here. The clamp lives in `pollLoop.bounded` and in each
	// constructor, and clamping twice against two different ranges is how a
	// fleet-wide interval would silently become a per-collector one.
	if p.duration() != 250*time.Millisecond {
		t.Errorf("duration() = %v; set must store what it is given", p.duration())
	}
}

// TestRetimeAppliesToThePendingTimer.
//
// This is the whole reason `retime` exists. `delay` is re-read on every
// schedule, so a changed period already affects the NEXT tick without help —
// what it does not touch is the timer already counting down. An operator moving
// a poll from a long period to a short one expects the short one, not to wait
// out the remainder first.
func TestRetimeAppliesToThePendingTimer(t *testing.T) {
	interval := newPollInterval(30000) // long enough that it will never fire
	runs := make(chan struct{}, 8)

	loop := newPollLoop(
		func() { runs <- struct{}{} },
		func() time.Duration { return interval.duration() },
	)
	loop.start()
	t.Cleanup(loop.stop)

	// The first run happens immediately: `lastRun` is the zero time, so a whole
	// interval has "already elapsed".
	select {
	case <-runs:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop never ran, so nothing below is about re-timing")
	}

	// With 30s pending, nothing more should arrive.
	select {
	case <-runs:
		t.Fatal("the loop ran again on a 30s interval, so this test cannot tell a " +
			"re-timed timer from an ordinary one")
	case <-time.After(150 * time.Millisecond):
	}

	interval.set(50)
	loop.retime()

	select {
	case <-runs:
	case <-time.After(2 * time.Second):
		t.Error("the pending timer was not re-armed -- the operator would wait out the " +
			"remaining 30 seconds before the new interval took effect")
	}
}

// TestRetimeMeasuresFromNow, not from the last run.
//
// `start` counts elapsed time since the last run on purpose, because page
// navigation calls stop/start freely. A settings save is a different event with
// a different answer: the live side does `clearInterval` + `setInterval`, so the
// next run is a FULL new period from the save. Measuring from `lastRun` instead
// would fire immediately whenever the new period is shorter than the time
// already elapsed — an instant burst across every collector on every router the
// moment an operator presses Save.
func TestRetimeMeasuresFromNow(t *testing.T) {
	interval := newPollInterval(30000)
	runs := make(chan struct{}, 8)
	loop := newPollLoop(
		func() { runs <- struct{}{} },
		func() time.Duration { return interval.duration() },
	)
	loop.start()
	t.Cleanup(loop.stop)

	<-runs // the immediate first run
	time.Sleep(300 * time.Millisecond)

	// A new period SHORTER than the 300ms already elapsed. Measuring from
	// `lastRun` would make this due immediately.
	interval.set(600)
	loop.retime()

	select {
	case <-runs:
		t.Error("the loop fired at once -- retime measured from lastRun, which turns a " +
			"fleet-wide save into a burst of simultaneous polls")
	case <-time.After(350 * time.Millisecond):
		// Still waiting, which is right: 600ms from the retime, not from the run.
	}

	select {
	case <-runs:
	case <-time.After(2 * time.Second):
		t.Error("the loop never fired after re-timing")
	}
}

// TestRetimeLeavesAStoppedLoopStopped.
//
// `start` computes a fresh delay when the page is next opened. Re-arming a timer
// for a collector nobody is watching would undo what `stop` is for — and a
// settings save re-tunes EVERY collector, including the ones no browser has
// open.
func TestRetimeLeavesAStoppedLoopStopped(t *testing.T) {
	interval := newPollInterval(50)
	runs := make(chan struct{}, 8)
	loop := newPollLoop(
		func() { runs <- struct{}{} },
		func() time.Duration { return interval.duration() },
	)
	// Never started.
	loop.retime()

	// LONGER THAN THE FLOOR. `bounded` clamps to a minimum of 500ms, so the 50ms
	// asked for above is really 500 — and an earlier version of this test waited
	// only 300ms, which a re-armed loop would not have beaten either. The
	// mutation "retime re-arms a stopped loop" survived because of it.
	select {
	case <-runs:
		t.Error("a stopped loop started polling after a re-tune")
	case <-time.After(900 * time.Millisecond):
	}

	// And it still starts normally afterwards, so the guard did not break it.
	loop.start()
	t.Cleanup(loop.stop)
	select {
	case <-runs:
	case <-time.After(2 * time.Second):
		t.Error("the loop would not start after a re-tune on a stopped loop")
	}
}

// TestConcurrentRetuneIsRaceFree.
//
// Run with -race, this is the reason `pollInterval` is atomic rather than
// guarded by the collector's mutex: `packages`, `routing` and `talkers` have no
// mutex at all, so "hold the collector's lock" would be a rule with three
// exceptions in exactly the files where its absence is hardest to notice.
//
// A STARTING GATE, not a plain loop: without one the writers can finish before
// the readers begin and the test proves nothing about concurrency.
func TestConcurrentRetuneIsRaceFree(t *testing.T) {
	interval := newPollInterval(1000)
	loop := newPollLoop(func() { _ = interval.ms() }, interval.duration)
	loop.start()
	t.Cleanup(loop.stop)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			<-start
			for j := 0; j < 200; j++ {
				interval.set(500 + n)
				loop.retime()
			}
		}(i)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 200; j++ {
				_ = interval.ms()
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := interval.ms(); got < 500 || got > 507 {
		t.Errorf("interval = %d after the storm; want one of the values written", got)
	}
}

// stubReader is a Reader that never connects, so `Tick` returns early without
// touching a router. Enough to see the loop RUN, which is all this needs.
type stubReader struct{ ticks chan struct{} }

func (s *stubReader) Do(routeros.Cmd) ([]routeros.Reply, error) { return nil, nil }
func (s *stubReader) Connected() bool {
	select {
	case s.ticks <- struct{}{}:
	default:
	}
	return false
}

// TestSetPollMsRetimesARunningCollector.
//
// `TestSetPollMsChangesWhatThePayloadReports` covers the stored value; this
// covers the other half. A mutation that stored the period and skipped `retime`
// survived until this existed — the payload would have reported the new period
// while the collector kept polling at the old one, which is the version of this
// bug nobody would report because the number on screen looks right.
func TestSetPollMsRetimesARunningCollector(t *testing.T) {
	r := &stubReader{ticks: make(chan struct{}, 8)}
	s := NewSystem(r, func(string, string, any) {}, 60000)
	s.loop.start()
	t.Cleanup(s.loop.stop)

	<-r.ticks // the immediate first run

	// Nothing more on a 60-second period.
	select {
	case <-r.ticks:
		t.Fatal("the collector ran again on a 60s period, so this cannot tell a re-timed " +
			"loop from an ordinary one")
	case <-time.After(200 * time.Millisecond):
	}

	s.SetPollMs(500) // the floor, so the wait below stays short

	select {
	case <-r.ticks:
	case <-time.After(3 * time.Second):
		t.Error("the running collector was not re-timed -- it would keep polling at the " +
			"old period while its payload reported the new one")
	}
}

// TestSetPollMsChangesWhatThePayloadReports.
//
// Twenty-one of the twenty-four collectors send `pollMs` to the browser, and the
// live ones send the value the settings route mutated. A re-tune that changed
// only the timer would leave the page showing the old period while polling at
// the new one.
func TestSetPollMsChangesWhatThePayloadReports(t *testing.T) {
	s := NewSystem(nil, func(string, string, any) {}, 5000)
	if got := s.pollMs.ms(); got != 5000 {
		t.Fatalf("constructed with %d, want 5000", got)
	}
	s.SetPollMs(12000)
	if got := s.pollMs.ms(); got != 12000 {
		t.Errorf("pollMs = %d after SetPollMs(12000) -- the payload would report the "+
			"old period while polling at the new one", got)
	}
}

// ── THE LEDGER ──────────────────────────────────────────────────────────────

// retunable maps each collector the LIVE settings route re-tunes to the Go type
// that answers for it. The two vocabularies differ — `conns` is `Connections`,
// `ifStatus` is `IfStatus` — and no rule derives one from the other, so this is
// written out and then CHECKED against the lifted table.
var retunable = map[string]string{
	"bandwidth": "Bandwidth", "bridges": "Bridges", "capsman": "Capsman",
	"conns": "Connections", "dhcpNetworks": "DHCPNetworks", "dns": "DNS",
	"firewall": "Firewall", "ifStatus": "IfStatus", "packages": "Packages",
	"ping": "Ping", "ppp": "PPP", "queues": "Queues", "rosusers": "RosUsers",
	"routing": "Routing", "system": "System", "talkers": "Talkers",
	"topology": "Topology", "vlans": "Vlans", "vpn": "VPN", "wan": "Wan",
	"wifi": "Wifi", "wireless": "Wireless",
}

// notRetunable is the live target with no Go counterpart, and why.
var notRetunable = map[string]string{
	"arp": "this port has NO ARP collector. The live one exists to fill a cache that " +
		"other collectors read through getByIP/getByMAC — the port record notes it " +
		"alongside `conns` and `traffic` as collectors that 'fill a cache and emit " +
		"elsewhere'. There is nothing here to re-tune, and inventing one so this table " +
		"could be complete would be a collector with no caller.",
}

// TestEveryReTunedCollectorHasASetter.
//
// A LEDGER, failing in both directions. `collection.PollRetunes` names a
// collector for every poll key the operator can change; a name with nothing
// behind it means the setting saves, the payload reports the new period, and the
// collector keeps polling at the old one — with nothing logged anywhere.
//
// The reverse matters too: an entry here for a collector the live route does NOT
// re-tune is a claim about upstream that has gone stale.
func TestEveryReTunedCollectorHasASetter(t *testing.T) {
	b, err := os.ReadFile("../../testdata/settings-apply-cases.json")
	if err != nil {
		t.Fatalf("read the lifted table: %v", err)
	}
	var doc struct {
		PollMap map[string]string `json:"pollMap"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.PollMap) == 0 {
		t.Fatal("the lifted poll map is empty, so this test would pass against nothing")
	}

	live := map[string]bool{}
	for _, name := range doc.PollMap {
		live[name] = true
	}

	// Every live target is either mapped or recorded.
	for name := range live {
		_, mapped := retunable[name]
		_, excused := notRetunable[name]
		if !mapped && !excused {
			t.Errorf("the live route re-tunes %q and nothing here answers for it -- the "+
				"setting would save, the payload would report the new period, and the "+
				"collector would keep polling at the old one", name)
		}
		if mapped && excused {
			t.Errorf("%q is both mapped and excused", name)
		}
	}
	// And nothing here claims a target the live route does not have.
	for name := range retunable {
		if !live[name] {
			t.Errorf("%q is mapped here but the live route does not re-tune it -- this "+
				"table has drifted from upstream", name)
		}
	}
	for name := range notRetunable {
		if !live[name] {
			t.Errorf("%q is excused here but the live route does not re-tune it either, "+
				"so the entry describes nothing", name)
		}
	}
}

// TestEveryMappedTypeActuallyHasTheMethod.
//
// The map above is strings; this is what makes them true. A type renamed, or a
// setter never added, fails here rather than at the moment an operator saves.
func TestEveryMappedTypeActuallyHasTheMethod(t *testing.T) {
	setters := map[string]bool{}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`func \(\w+ \*(\w+)\) SetPollMs\(`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			setters[m[1]] = true
		}
	}
	if len(setters) == 0 {
		t.Fatal("no SetPollMs was found at all, so this test proves nothing")
	}

	for name, typ := range retunable {
		if !setters[typ] {
			t.Errorf("%s (for the live %q) has no SetPollMs", typ, name)
		}
	}
	// A setter on a type nothing maps is not an error — it may be reached another
	// way — but the count is reported so a surprise is visible.
	if len(setters) != len(retunable) {
		t.Logf("%d types have SetPollMs and %d are mapped", len(setters), len(retunable))
	}
}
