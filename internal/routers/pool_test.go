package routers

import (
	"errors"
	"sync"
	"testing"
	"time"

	"mikrodash/internal/collection"
	"mikrodash/internal/routeros"
)

// fakeConn answers every command with nothing, which is enough: this file tests
// the POOL — connect, drop, classify, suspend, resume, tear down — and the
// collectors' own payloads are pinned by their own corpora.
type fakeConn struct {
	streams []string
	stopped []string
	mu      sync.Mutex
	up      bool
	closed  int
	cmds    []string
}

func (c *fakeConn) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	c.mu.Lock()
	c.cmds = append(c.cmds, cmd.Path)
	c.mu.Unlock()
	return nil, nil
}

// sawPrefix reports whether any command issued so far starts with p — which is
// how "was this collector started" is observed from outside the package.
func (c *fakeConn) sawPrefix(p string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.cmds {
		if len(s) >= len(p) && s[:len(p)] == p {
			return true
		}
	}
	return false
}

// Stream is the history pair's half of the connection. A stub that RECORDS the
// command rather than ignoring it, so a test can assert which streams a pooled
// session opened — the difference between "the ping collector was started" and
// "a ping collector exists" is the whole point of the history wiring.
func (c *fakeConn) Stream(cmd routeros.Cmd, _ func(routeros.Reply)) (func(), error) {
	c.mu.Lock()
	c.streams = append(c.streams, cmd.Path)
	c.mu.Unlock()
	// THE STOP IS RECORDED TOO. Without it a test can only see that a stream was
	// opened, never that it was closed — and "the old router keeps recording
	// after an activation" is exactly a stream that was opened and not closed.
	// A mutation making every pooled router record survived until this existed.
	return func() {
		c.mu.Lock()
		c.stopped = append(c.stopped, cmd.Path)
		c.mu.Unlock()
	}, nil
}

// streamStopped reports whether a stream on the given path was stopped.
func (c *fakeConn) streamStopped(p string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.stopped {
		if s == p {
			return true
		}
	}
	return false
}

// sawStream reports whether a stream was opened on the given path.
func (c *fakeConn) sawStream(p string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.streams {
		if s == p {
			return true
		}
	}
	return false
}

func (c *fakeConn) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.up
}
func (c *fakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed++
	c.up = false
	return nil
}
func (c *fakeConn) drop() {
	c.mu.Lock()
	c.up = false
	c.mu.Unlock()
}
func (c *fakeConn) closes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

type dialLog struct {
	mu    sync.Mutex
	calls int
	err   error
	conns []*fakeConn
	// byHost maps the dialled host to the conn handed back, so a test can ask
	// what ONE router did. Added with continuous history, where the whole
	// question is which router opened a stream and which did not.
	byHost map[string]*fakeConn
}

// conn returns the connection dialled for the given host, or nil.
func (d *dialLog) conn(host string) *fakeConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.byHost[host]
}

func (d *dialLog) dial(cfg routeros.Config) (Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.err != nil {
		return nil, d.err
	}
	c := &fakeConn{up: true}
	d.conns = append(d.conns, c)
	if d.byHost == nil {
		d.byHost = map[string]*fakeConn{}
	}
	d.byHost[cfg.Host] = c
	return c, nil
}
func (d *dialLog) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}
func (d *dialLog) fail(err error) {
	d.mu.Lock()
	d.err = err
	d.mu.Unlock()
}
func (d *dialLog) last() *fakeConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.conns) == 0 {
		return nil
	}
	return d.conns[len(d.conns)-1]
}

func cfg(id string) RouterConfig {
	return RouterConfig{ID: id, Label: id, Host: "198.51.100.9", Port: 8728, User: "dash"}
}

// waitFor polls a condition rather than sleeping a fixed time, so the test is
// not a race that passes on a fast machine.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPoolConnectsAndReportsSummaries(t *testing.T) {
	d := &dialLog{}
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil)
	defer p.Close()

	act := p.Sync([]RouterConfig{cfg("a"), cfg("b")}, nil)
	if len(act.Start) != 2 || len(act.Stop) != 0 {
		t.Fatalf("sync decided %+v", act)
	}
	waitFor(t, "both sessions connected", func() bool {
		for _, s := range p.Summaries() {
			if !s.Connected {
				return false
			}
		}
		return len(p.Summaries()) == 2
	})

	sum := p.Summaries()
	// SORTED, so a caller diffing two payloads is not reading map order.
	if sum[0].RouterID != "a" || sum[1].RouterID != "b" {
		t.Fatalf("summaries not sorted: %s, %s", sum[0].RouterID, sum[1].RouterID)
	}
	for _, s := range sum {
		if s.LastError != "" {
			t.Errorf("%s: connected but LastError = %q", s.RouterID, s.LastError)
		}
	}
}

// ── ReleaseAll GIVES THE FLEET BACK, WHICH Suspend DOES NOT ────────────────
//
// `syncAlertPool` excludes every router this pool reports in `Summaries()`, and
// a SUSPENDED session is still reported — so once anybody had opened the Devices
// page, the overview pool owned the whole fleet, stopped collecting the moment
// they left, and the alert pool was locked out of all of it. No alert evaluation
// and no continuous history for any router until something else re-ran the sync.
//
// The distinction pinned here is the one the Suspend test asserts in the other
// direction: Suspend keeps its sockets on purpose, so ReleaseAll has to exist
// rather than Suspend quietly changing meaning.
func TestReleaseAllGivesUpEveryRouter(t *testing.T) {
	d := &dialLog{}
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil)
	defer p.Close()

	p.Sync([]RouterConfig{cfg("a"), cfg("b")}, nil)
	waitFor(t, "two tracked", func() bool { return len(p.Tracked()) == 2 })

	p.ReleaseAll()

	// BOTH, because they answer different questions: Tracked is what the pool
	// thinks it holds, and Summaries is what `syncAlertPool` reads to decide
	// whether a router is already covered. A release that emptied one and not
	// the other would leave the alert pool still locked out.
	if n := len(p.Tracked()); n != 0 {
		t.Errorf("%d router(s) still tracked after ReleaseAll", n)
	}
	if n := len(p.Summaries()); n != 0 {
		t.Errorf("%d router(s) still in Summaries — syncAlertPool would keep excluding them", n)
	}

	// And the pool is still USABLE: returning to the Devices page re-dials,
	// which is what the first visit does anyway.
	p.Resume()
	p.Sync([]RouterConfig{cfg("a"), cfg("b")}, nil)
	waitFor(t, "two tracked again", func() bool { return len(p.Tracked()) == 2 })
}

// A router the MAIN pool takes over must be STOPPED, not left running: the live
// teardown fires when a router is excluded OR gone, and the two loops are not
// symmetrical. This is the case that separates them.
func TestPoolStopsARouterSomebodyOpened(t *testing.T) {
	d := &dialLog{}
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil)
	defer p.Close()

	p.Sync([]RouterConfig{cfg("a"), cfg("b")}, nil)
	waitFor(t, "two tracked", func() bool { return len(p.Tracked()) == 2 })

	act := p.Sync([]RouterConfig{cfg("a"), cfg("b")}, map[string]bool{"a": true})
	if len(act.Stop) != 1 || act.Stop[0] != "a" {
		t.Fatalf("expected a stop for the opened router, got %+v", act)
	}
	if tracked := p.Tracked(); tracked["a"] {
		t.Error("the opened router is still tracked")
	}
	if len(p.Summaries()) != 1 {
		t.Errorf("summaries still report %d routers", len(p.Summaries()))
	}
}

func TestPoolDropsARemovedRouter(t *testing.T) {
	d := &dialLog{}
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil)
	defer p.Close()

	p.Sync([]RouterConfig{cfg("a"), cfg("b")}, nil)
	waitFor(t, "two tracked", func() bool { return len(p.Tracked()) == 2 })

	act := p.Sync([]RouterConfig{cfg("a")}, nil)
	if len(act.Stop) != 1 || act.Stop[0] != "b" {
		t.Fatalf("expected b torn down, got %+v", act)
	}
	waitFor(t, "one tracked", func() bool { return len(p.Tracked()) == 1 })
}

// A FAILED DIAL IS EXPLAINED, not swallowed. This is issue #92: an offline card
// that cannot say why sends the operator to the container logs.
func TestPoolClassifiesADialFailure(t *testing.T) {
	d := &dialLog{}
	d.fail(errors.New("dial tcp 198.51.100.9:8728: connect: connection refused"))
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil)
	defer p.Close()

	p.Sync([]RouterConfig{cfg("a")}, nil)
	waitFor(t, "the failure to be recorded", func() bool {
		s := p.Summaries()
		return len(s) == 1 && s[0].LastError != ""
	})
	s := p.Summaries()[0]
	if s.Connected {
		t.Error("reported connected after a failed dial")
	}
	want := "Connection refused — is RouterOS reachable at 198.51.100.9?"
	if s.LastError != want {
		t.Errorf("LastError\n  got  %q\n  want %q", s.LastError, want)
	}
}

// An UNCLASSIFIED failure must not put raw driver text on the page.
func TestPoolNeverStoresAnUnclassifiedReason(t *testing.T) {
	d := &dialLog{}
	d.fail(errors.New("/data/secret.key: something nobody predicted"))
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil)
	defer p.Close()

	p.Sync([]RouterConfig{cfg("a")}, nil)
	waitFor(t, "the failure to be recorded", func() bool {
		s := p.Summaries()
		return len(s) == 1 && s[0].LastError != ""
	})
	if got := p.Summaries()[0].LastError; got != "Connection failed" {
		t.Errorf("stored %q; an unclassified reason must become the generic string", got)
	}
}

// A failed dial RETRIES rather than giving up once.
func TestPoolRetriesAFailedDial(t *testing.T) {
	d := &dialLog{}
	d.fail(errors.New("dial tcp: connect: connection refused"))
	p := NewPool(d.dial, 5*time.Millisecond, nil, nil)
	defer p.Close()

	p.Sync([]RouterConfig{cfg("a")}, nil)
	waitFor(t, "several dial attempts", func() bool { return d.count() >= 3 })
}

// A DROPPED LINK is noticed and re-dialled. Without this the session would sit
// "connected" for ever and the page would read a stale payload as current.
func TestPoolReconnectsAfterADrop(t *testing.T) {
	d := &dialLog{}
	p := NewPool(d.dial, 5*time.Millisecond, nil, nil)
	defer p.Close()

	p.Sync([]RouterConfig{cfg("a")}, nil)
	waitFor(t, "connected", func() bool {
		s := p.Summaries()
		return len(s) == 1 && s[0].Connected
	})
	first := d.last()
	first.drop()

	waitFor(t, "a re-dial", func() bool { return d.count() >= 2 })
	waitFor(t, "connected again", func() bool {
		s := p.Summaries()
		return len(s) == 1 && s[0].Connected
	})
	if first.closes() == 0 {
		t.Error("the dropped connection was never closed")
	}
}

// SUSPEND STOPS COLLECTING WITHOUT DISCONNECTING, so Resume costs nothing.
func TestSuspendKeepsTheConnection(t *testing.T) {
	d := &dialLog{}
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil)
	defer p.Close()

	p.Sync([]RouterConfig{cfg("a")}, nil)
	waitFor(t, "connected", func() bool {
		s := p.Summaries()
		return len(s) == 1 && s[0].Connected
	})
	before := d.count()
	p.Suspend()

	if !p.Summaries()[0].Connected {
		t.Error("Suspend disconnected — it is supposed to stop collecting only")
	}
	p.Resume()
	if d.count() != before {
		t.Errorf("Resume re-dialled (%d → %d); the socket was supposed to still be open",
			before, d.count())
	}
}

// A session STARTED WHILE SUSPENDED must not begin collecting.
func TestASessionAddedWhileSuspendedStaysSuspended(t *testing.T) {
	d := &dialLog{}
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil)
	defer p.Close()

	p.Suspend()
	p.Sync([]RouterConfig{cfg("a")}, nil)
	waitFor(t, "connected", func() bool {
		s := p.Summaries()
		return len(s) == 1 && s[0].Connected
	})
	if !p.Summaries()[0].Connected {
		t.Fatal("did not connect while suspended — suspension is not disconnection")
	}
}

func TestCloseTearsEverythingDown(t *testing.T) {
	d := &dialLog{}
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil)
	p.Sync([]RouterConfig{cfg("a"), cfg("b")}, nil)
	waitFor(t, "two tracked", func() bool { return len(p.Tracked()) == 2 })

	p.Close()
	if len(p.Tracked()) != 0 {
		t.Errorf("still tracking %d after Close", len(p.Tracked()))
	}
	// And a Sync after Close must not resurrect anything.
	if act := p.Sync([]RouterConfig{cfg("a")}, nil); len(act.Start) != 0 {
		t.Errorf("Sync after Close started %v", act.Start)
	}
}

// A ROUTER REMOVED WHILE ITS DIAL IS IN FLIGHT must not come up behind the
// teardown. The live pool carries a `destroyed` flag for exactly this, and
// without it a removed router opens a connection nothing will close and starts
// collectors that poll for ever.
//
// It also pins that `Sync` DOES NOT WAIT for the dial: an earlier version of
// destroy() blocked until the goroutine left, which would stall the Routers page
// for as long as an unreachable router takes to time out.
func TestARouterRemovedMidDialNeverComesUp(t *testing.T) {
	release := make(chan struct{})
	var conn *fakeConn
	var mu sync.Mutex
	entered := 0
	dial := func(routeros.Config) (Conn, error) {
		mu.Lock()
		entered++
		mu.Unlock()
		<-release // hold the dial open
		mu.Lock()
		defer mu.Unlock()
		conn = &fakeConn{up: true}
		return conn, nil
	}

	p := NewPool(dial, 10*time.Millisecond, nil, nil)
	defer p.Close()
	p.Sync([]RouterConfig{cfg("a")}, nil)

	// WAIT UNTIL THE DIAL IS ACTUALLY IN FLIGHT. Without this the teardown can
	// win the race to the top of the connect loop, where `stop` is already closed
	// and the session returns WITHOUT dialling — correct behaviour, but a
	// different path, and the mid-dial guard below would then never be reached.
	// The first version of this test did exactly that and failed on a connection
	// that was never opened.
	waitFor(t, "the dial to be entered", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return entered == 1
	})

	// Tear down WHILE the dial is blocked. This call must return promptly; if it
	// waits for the goroutine it cannot, because the goroutine is in the dial.
	start := time.Now()
	p.Sync([]RouterConfig{}, nil)
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("Sync blocked for %s waiting on an in-flight dial", waited)
	}
	if len(p.Tracked()) != 0 {
		t.Fatal("still tracked after teardown")
	}

	close(release) // let the dial complete, after the teardown
	waitFor(t, "the session goroutine to leave", func() bool {
		mu.Lock()
		c := conn
		mu.Unlock()
		return c != nil && c.closes() > 0
	})
	if len(p.Summaries()) != 0 {
		t.Error("a destroyed session still reports a summary")
	}
}

// #105: A COLLECTOR THE OPERATOR TURNED OFF IS NEVER STARTED.
//
// Observed by what reaches the router rather than by inspecting the session:
// `ifStatus` polls `/interface/print`, so its absence from the command log is
// the evidence.
//
// THE PREFIX HAS TO BE `/interface/print`, NOT `/interface/`. `dhcpLeases`
// issues `/interface/vlan/print`, so the broader prefix matches a collector that
// is still running and the test fails against correct wiring — which is exactly
// what the first version of it did. `system` and `dhcpLeases` are `disableable: false` in the registry —
// protected, because other collectors read them — so `ifStatus` is the only one
// of this pool's three that CAN be turned off, and the guards on the other two
// are unreachable today by design rather than by accident.
func TestADisabledCollectorIsNeverStarted(t *testing.T) {
	withOff := func(off []string) *fakeConn {
		d := &dialLog{}
		cfgA := cfg("a")
		if off != nil {
			cfgA.Collection = &collection.Router{Off: off}
		}
		p := NewPool(d.dial, 10*time.Millisecond, nil, nil)
		defer p.Close()
		p.Sync([]RouterConfig{cfgA}, nil)
		waitFor(t, "connected", func() bool {
			s := p.Summaries()
			return len(s) == 1 && s[0].Connected
		})
		// The system collector starts unconditionally, so waiting for ITS first
		// command is a deterministic point at which ifStatus would also have
		// started if it were going to.
		waitFor(t, "the first poll", func() bool {
			c := d.last()
			return c != nil && c.sawPrefix("/system/")
		})
		return d.last()
	}

	on := withOff(nil)
	if !on.sawPrefix("/interface/print") {
		t.Fatal("ifStatus never polled with nothing disabled — the observation is not working, " +
			"so the disabled case below would prove nothing")
	}

	off := withOff([]string{"ifStatus"})
	if off.sawPrefix("/interface/print") {
		t.Error("ifStatus polled the router after being turned off for this router")
	}
	if !off.sawPrefix("/system/") {
		t.Error("system stopped polling too — turning one collector off disabled another")
	}
}
