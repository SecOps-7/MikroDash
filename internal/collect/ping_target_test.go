package collect

// A PING'S TARGET CAN CHANGE WHILE IT RUNS.
//
// The router form edits `pingTarget`, and the Dashboard session is held for as
// long as the router has alerting or recording on — on the hAP AX3, for good.
// A target read only when the session was built would never change on it
// without a restart. So the target is settable, and a change reopens the
// stream at the new host.

import (
	"strings"
	"sync"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// cmdStreamer records every command a stream was opened with.
type cmdStreamer struct {
	mu    sync.Mutex
	cmds  []routeros.Cmd
	stops int
}

func (c *cmdStreamer) Connected() bool { return true }

func (c *cmdStreamer) Stream(cmd routeros.Cmd, _ func(routeros.Reply)) (func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cmds = append(c.cmds, cmd)
	return func() {
		c.mu.Lock()
		c.stops++
		c.mu.Unlock()
	}, nil
}

func (c *cmdStreamer) snapshot() ([]routeros.Cmd, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]routeros.Cmd(nil), c.cmds...), c.stops
}

func addressOf(cmd routeros.Cmd) string {
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, "=address=") {
			return strings.TrimPrefix(a, "=address=")
		}
	}
	return ""
}

func TestSetTargetReopensTheStreamForTheNewHost(t *testing.T) {
	s := &cmdStreamer{}
	p := NewPing(s, hub.Relay{}, 5000, "198.51.100.1")
	p.Start()
	defer p.Stop()
	p.ProcessRow(routeros.Reply{"seq": "0", "time": "10ms"}, 1000) // a reading for the old host

	p.SetTarget("198.51.100.9")

	cmds, stops := s.snapshot()
	if len(cmds) != 2 || stops != 1 {
		t.Fatalf("%d open(s) and %d stop(s), want 2 and 1: the stream to the old host must be "+
			"stopped and one opened to the new host", len(cmds), stops)
	}
	if got := addressOf(cmds[1]); got != "198.51.100.9" {
		t.Errorf("the reopened stream pings %q, want 198.51.100.9", got)
	}
	h := p.History()
	if h.Target != "198.51.100.9" {
		t.Errorf("History().Target = %q, want the new host", h.Target)
	}
	if len(h.History) != 0 {
		t.Errorf("%d reading(s) kept from the old host: a chart would show another host's "+
			"latency under the new host's name", len(h.History))
	}
}

func TestSetTargetToTheSameHostChangesNothing(t *testing.T) {
	s := &cmdStreamer{}
	p := NewPing(s, hub.Relay{}, 5000, "198.51.100.1")
	p.Start()
	defer p.Stop()
	p.ProcessRow(routeros.Reply{"seq": "0", "time": "10ms"}, 1000)

	p.SetTarget("198.51.100.1")

	if cmds, stops := s.snapshot(); len(cmds) != 1 || stops != 0 {
		t.Errorf("setting the same target reopened the stream (%d opens, %d stops)", len(cmds), stops)
	}
	if n := len(p.History().History); n != 1 {
		t.Errorf("setting the same target dropped the history (%d readings, want 1)", n)
	}
}

func TestSetTargetOnAStoppedPingOpensNothing(t *testing.T) {
	s := &cmdStreamer{}
	p := NewPing(s, hub.Relay{}, 5000, "198.51.100.1")
	p.SetTarget("198.51.100.9")
	if cmds, _ := s.snapshot(); len(cmds) != 0 {
		t.Errorf("a ping that was never started opened %d stream(s) on a target change", len(cmds))
	}
	if got := p.History().Target; got != "198.51.100.9" {
		t.Errorf("History().Target = %q, want the new host for when it does start", got)
	}
}

func TestAnEmptyTargetFallsBackToTheDefault(t *testing.T) {
	p := NewPing(&cmdStreamer{}, hub.Relay{}, 5000, "198.51.100.1")
	p.SetTarget("")
	if got := p.History().Target; got != pingDefaultTgt {
		t.Errorf("History().Target = %q after an empty target, want %q", got, pingDefaultTgt)
	}
}
