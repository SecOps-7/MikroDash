package roscache

import (
	"sync"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

// ender is a pusher that can also tell the cache the ROUTER ended a stream
// (DoneStreamer), the way RouterOS ends a monitor-traffic stream whose
// interface was removed (`!trap`, then `!done`; CHR Test, 7.24.1).
type ender struct {
	pusher
	emu    sync.Mutex
	onDone func(error)
}

func (e *ender) StreamUntilDone(cmd routeros.Cmd, onRow func(routeros.Reply), onDone func(error)) (func(), error) {
	stop, err := e.Stream(cmd, onRow)
	if err != nil {
		return nil, err
	}
	e.emu.Lock()
	e.onDone = onDone
	e.emu.Unlock()
	return stop, nil
}

// end is the router ending the stream that is open now.
func (e *ender) end() {
	e.emu.Lock()
	fn := e.onDone
	e.onDone = nil
	e.emu.Unlock()
	if fn != nil {
		fn(nil)
	}
}

func waitOpens(t *testing.T, e *ender, want int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if opens, _, _ := e.counts(); opens >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	opens, _, _ := e.counts()
	t.Fatalf("opens = %d after %s, want %d", opens, within, want)
}

// A stream the router ends is reopened within a second, not after the ten
// seconds of silence the watchdog waits for, and counted as a restart. A
// stream WE stop is not reopened.
func TestAStreamTheRouterEndsIsReopenedAtOnce(t *testing.T) {
	e := &ender{}
	c := New(e)
	rel := joinIfaces(t, c, "ether1", 1, nil)
	defer rel()
	waitOpens(t, e, 1, time.Second)

	e.end()
	waitOpens(t, e, 2, 1500*time.Millisecond)
	if st, ok := c.StreamStats("/interface/monitor-traffic", ""); !ok || st.Restarts != 1 {
		t.Errorf("stats after the reopen: %+v %v, want one restart", st, ok)
	}

	// Ended again at once (the interface is still gone): the wait doubles, so
	// a reopen that keeps failing does not spin.
	e.end()
	time.Sleep(700 * time.Millisecond)
	if opens, _, _ := e.counts(); opens != 2 {
		t.Errorf("opens = %d 0.7 s after a quick second end; the back-off is gone", opens)
	}
	waitOpens(t, e, 3, 1500*time.Millisecond)

	// Our own stop: no onDone, and nothing reopens.
	rel()
	time.Sleep(700 * time.Millisecond)
	if opens, _, _ := e.counts(); opens != 3 {
		t.Errorf("opens = %d after the last holder left; a stopped stream was reopened", opens)
	}
}

// A holder on its own channel neither merges with the menu's holders nor is
// touched by them, reads go to the menu's channel only, and the menu is
// listed as streamed once.
func TestASeparateChannelIsItsOwn(t *testing.T) {
	p := &pusher{}
	c := New(p)
	relShared := joinIfaces(t, c, "ether1,wlan1", 5, nil)
	relChart, err := c.JoinStream(Join{
		Menu: "/interface/monitor-traffic", Channel: "chart",
		Cmd: routeros.Cmd{Path: "/interface/monitor-traffic", Args: []string{
			"=interface=ether1", "=interval=1"}},
		KeyOf: byName, Boundary: time.Second, Merge: mergeIfaces,
	})
	if err != nil {
		t.Fatal(err)
	}
	opens, stops, _ := p.counts()
	if opens != 2 || stops != 0 {
		t.Errorf("opens=%d stops=%d; a separate channel must be opened beside the "+
			"shared one, not merged into it", opens, stops)
	}
	if got := p.lastCmd().Args; got[0] != "=interface=ether1" || got[1] != "=interval=1" {
		t.Errorf("the separate channel opened with %v, want its own command", got)
	}
	if got := c.StreamedMenus(); len(got) != 1 || got[0] != "/interface/monitor-traffic" {
		t.Errorf("StreamedMenus = %v, want the menu once", got)
	}
	if _, ok := c.StreamStats("/interface/monitor-traffic", "chart"); !ok {
		t.Error("no stats for the separate channel")
	}

	relShared()
	if _, stops, _ = p.counts(); stops != 1 {
		t.Errorf("stops=%d after the shared holder left; it must close only its own", stops)
	}
	if _, ok := c.StreamStats("/interface/monitor-traffic", "chart"); !ok {
		t.Error("the separate channel closed with the shared one")
	}
	if c.Streaming("/interface/monitor-traffic") {
		t.Error("a read is answered by the separate channel; it is nobody's read")
	}
	relChart()
	if _, stops, _ = p.counts(); stops != 2 {
		t.Errorf("stops=%d after the chart holder left", stops)
	}
}
