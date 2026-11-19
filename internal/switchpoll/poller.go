// Package switchpoll keeps every enabled SwOS switch read (#142): one worker
// per switch, reading through internal/swos, feeding what it learns to the
// same places a router's collectors feed - the connectivity tracker, history,
// the alert rules - through callbacks, so this package knows nothing of the
// server.
//
// ── CADENCE ────────────────────────────────────────────────────────────────
//
//	link.b and the stats page  every 30 s, every 2 s while a modal peeks
//	sys.b, sfp.b, poe.b, hosts  every 5 min, and on the first read
//
// The switch's own page polls at 1 s, so even the peek cadence is inside what
// it was built for. One request is in flight per switch (the client's lock).
//
// ── A PANIC IN ONE WORKER STOPS ONE READ, NOT THE SERVER ───────────────────
//
// Every read decodes an untrusted reply. internal/swos refuses what it can see
// is malformed, and each tick is recovered as well: a bug that reaches a
// panic costs that switch one reading and is logged.
package switchpoll

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"mikrodash/internal/collect"
	"mikrodash/internal/store"
	"mikrodash/internal/swos"
)

const (
	Slow   = 30 * time.Second
	Fast   = 2 * time.Second
	Config = 5 * time.Minute
)

// Getter reads one page. *swos.Client is one; tests use a fake.
type Getter interface {
	Get(ctx context.Context, page string) (swos.Value, error)
}

// Deps is everything outside this package the poller touches.
type Deps struct {
	// Dial makes a client for a switch.
	Dial func(sw store.Switch) (Getter, error)
	// Online reports a read that worked (true) or failed (false).
	Online func(id string, up bool, now time.Time)
	// Traffic is one port's rates, as a router interface's traffic sample.
	Traffic func(id string, s collect.TrafficSample)
	// Ports is the port list, shaped as a router's interface status, for the
	// interface up/down rule.
	Ports func(sw store.Switch, p collect.IfStatusPayload)
	// Identity is what sys.b said, after every sys read.
	Identity func(id string, s swos.Switch)
	// Changed says a switch's state moved, so pages can be told.
	Changed func(id string)
	Now     func() time.Time
}

// State is the latest reading of one switch.
type State struct {
	// Known is whether any read has finished, either way.
	Known  bool
	Online bool
	// LastError says why the last read failed, in words for a card.
	LastError string
	Switch    *swos.Switch
	// Rates and Counters are per port, in port order.
	Rates    []swos.PortRates
	Counters []swos.Counters
	// Hosts is the number of MACs in the switch's host table, nil until read.
	Hosts     *int
	UpdatedMs int64
}

// Poller runs the workers.
type Poller struct {
	deps Deps
	mu   sync.Mutex
	w    map[string]*worker
}

func New(d Deps) *Poller {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Poller{deps: d, w: map[string]*worker{}}
}

type worker struct {
	p    *Poller
	sw   store.Switch
	stop chan struct{}
	wake chan struct{}

	mu    sync.Mutex
	peeks int
	state State

	// owned by the worker goroutine
	client   Getter
	scheme   swos.Scheme
	sys      swos.Value
	sfp, poe swos.Value
	lastSys  time.Time
	prevAt   time.Time
}

// sameLogin is whether two records would dial the same switch the same way.
func sameLogin(a, b store.Switch) bool {
	return a.Host == b.Host && a.Port == b.Port && a.Scheme == b.Scheme &&
		a.Username == b.Username && a.Password == b.Password
}

// Sync makes the running workers match the list: a worker for every switch
// not disabled, restarted when its login changed, stopped when it is gone.
func (p *Poller) Sync(list []store.Switch) {
	p.mu.Lock()
	defer p.mu.Unlock()
	keep := map[string]bool{}
	for _, sw := range list {
		if sw.Disabled {
			continue
		}
		keep[sw.ID] = true
		if w, ok := p.w[sw.ID]; ok {
			if sameLogin(w.sw, sw) {
				w.mu.Lock()
				w.sw = sw // flags such as alerts and reporting
				w.mu.Unlock()
				continue
			}
			close(w.stop)
		}
		w := &worker{p: p, sw: sw, stop: make(chan struct{}), wake: make(chan struct{}, 1)}
		p.w[sw.ID] = w
		go w.run()
	}
	for id, w := range p.w {
		if !keep[id] {
			close(w.stop)
			delete(p.w, id)
		}
	}
}

// Stop ends every worker.
func (p *Poller) Stop() { p.Sync(nil) }

// IDs is every switch being polled.
func (p *Poller) IDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.w))
	for id := range p.w {
		out = append(out, id)
	}
	return out
}

// Has is whether a switch is being polled.
func (p *Poller) Has(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.w[id]
	return ok
}

// Peek speeds a switch up while a modal is open on it (on) and slows it down
// again when the modal closes (off). Counted, so two viewers are two peeks.
func (p *Poller) Peek(id string, on bool) {
	p.mu.Lock()
	w := p.w[id]
	p.mu.Unlock()
	if w == nil {
		return
	}
	w.mu.Lock()
	if on {
		w.peeks++
	} else if w.peeks > 0 {
		w.peeks--
	}
	w.mu.Unlock()
	if on {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

// State is a switch's latest reading; ok is false for a switch not polled.
func (p *Poller) State(id string) (State, bool) {
	p.mu.Lock()
	w := p.w[id]
	p.mu.Unlock()
	if w == nil {
		return State{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state, true
}

// Switch is the record a worker was last synced with.
func (p *Poller) Switch(id string) (store.Switch, bool) {
	p.mu.Lock()
	w := p.w[id]
	p.mu.Unlock()
	if w == nil {
		return store.Switch{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sw, true
}

func (w *worker) run() {
	for {
		w.tick()
		w.mu.Lock()
		every := Slow
		if w.peeks > 0 {
			every = Fast
		}
		w.mu.Unlock()
		select {
		case <-w.stop:
			return
		case <-w.wake:
		case <-time.After(every):
		}
	}
}

// tick is one read, recovered.
func (w *worker) tick() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[switch] %s: a read panicked and was dropped: %v", w.sw.Label, r)
		}
	}()
	select {
	case <-w.stop:
		return
	default:
	}
	err := w.read()
	now := w.p.deps.Now()
	w.mu.Lock()
	w.state.Known = true
	w.state.Online = err == nil
	w.state.LastError = ""
	if err != nil {
		w.state.LastError = describe(err)
		// The next read starts with a fresh login and a fresh look at sys.b.
		w.client, w.scheme = nil, ""
	}
	w.state.UpdatedMs = now.UnixMilli()
	w.mu.Unlock()
	if f := w.p.deps.Online; f != nil {
		f(w.sw.ID, err == nil, now)
	}
	if f := w.p.deps.Changed; f != nil {
		f(w.sw.ID)
	}
}

var errNotSwOS = errors.New("did not answer as SwOS")

// describe is a failure in words an operator can act on.
func describe(err error) string {
	switch {
	case errors.Is(err, swos.ErrAuth):
		return "The switch refused the username or password"
	case errors.Is(err, errNotSwOS), errors.Is(err, swos.ErrNotFound):
		return "This device did not answer as a SwOS switch"
	case errors.Is(err, context.DeadlineExceeded):
		return "The switch did not answer in time"
	}
	return err.Error()
}

func (w *worker) read() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*swos.Timeout)
	defer cancel()
	if w.client == nil {
		c, err := w.p.deps.Dial(w.sw)
		if err != nil {
			return err
		}
		w.client = c
	}
	now := w.p.deps.Now()
	rebooted, sysRead := false, false
	if w.scheme == "" || now.Sub(w.lastSys) >= Config {
		sys, err := w.client.Get(ctx, "sys.b")
		if err != nil {
			return err
		}
		scheme, ok := swos.DetectScheme(sys)
		if !ok {
			return errNotSwOS
		}
		if w.sys.Kind == swos.Obj {
			prev := swos.BuildSwitch(scheme, w.sys, swos.Value{}, swos.Value{}, swos.Value{})
			cur := swos.BuildSwitch(scheme, sys, swos.Value{}, swos.Value{}, swos.Value{})
			rebooted = swos.Rebooted(prev.UptimeSec, cur.UptimeSec)
		}
		w.scheme, w.sys, w.lastSys, sysRead = scheme, sys, now, true
		// Pages a switch may not have: a missing one is simply absent.
		w.sfp, _ = w.client.Get(ctx, "sfp.b")
		w.poe, _ = w.client.Get(ctx, "poe.b")
		if hosts, err := w.client.Get(ctx, "!dhost.b"); err == nil && hosts.Kind == swos.Arr {
			n := len(hosts.Items)
			w.mu.Lock()
			w.state.Hosts = &n
			w.mu.Unlock()
		}
	}
	link, err := w.client.Get(ctx, "link.b")
	if err != nil {
		return err
	}
	stats, err := w.client.Get(ctx, swos.StatsPage(w.scheme))
	if errors.Is(err, swos.ErrNotFound) && w.scheme == swos.SchemeSwOS {
		// Full SwOS 2.18's page reads stats.b; a later build may serve only the
		// read-only name the other families use.
		stats, err = w.client.Get(ctx, "!stats.b")
	}
	if err != nil {
		return err
	}
	sw := swos.BuildSwitch(w.scheme, w.sys, link, w.sfp, w.poe)
	cur := swos.ReadCounters(w.scheme, stats)

	w.mu.Lock()
	rates := swos.BuildPorts(w.state.Counters, cur, now.Sub(w.prevAt), rebooted)
	w.state.Switch, w.state.Counters, w.state.Rates = &sw, cur, rates
	w.prevAt = now
	rec := w.sw
	w.mu.Unlock()

	if f := w.p.deps.Identity; f != nil && sysRead {
		f(rec.ID, sw)
	}
	if f := w.p.deps.Traffic; f != nil {
		for i, p := range sw.Ports {
			if i >= len(rates) {
				break
			}
			f(rec.ID, collect.TrafficSample{
				IfName: p.Name, TS: now.UnixMilli(),
				RxMbps: rates[i].RxBps / 1e6, TxMbps: rates[i].TxBps / 1e6,
				Running: p.Up, Disabled: !p.Enabled,
			})
		}
	}
	if f := w.p.deps.Ports; f != nil {
		ifs := make([]collect.Interface, 0, len(sw.Ports))
		for _, p := range sw.Ports {
			ifs = append(ifs, collect.Interface{Name: p.Name, Type: "ether", Running: p.Up, Disabled: !p.Enabled})
		}
		f(rec, collect.IfStatusPayload{TS: now.UnixMilli(), RouterID: rec.ID, Interfaces: ifs})
	}
	return nil
}
