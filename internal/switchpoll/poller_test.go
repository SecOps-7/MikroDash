package switchpoll

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mikrodash/internal/collect"
	"mikrodash/internal/store"
	"mikrodash/internal/swos"
)

const lite = "../../testdata/fixtures/swos/lite-css610-2.21"

// fakeSwitch answers from the real CSS610 captures. Its first stats read is the
// EARLIER capture and later ones the later capture, so rates are compared.
type fakeSwitch struct {
	mu    sync.Mutex
	stats int
	err   error
	sys   string // overrides sys.b when set
}

func (f *fakeSwitch) Get(_ context.Context, page string) (swos.Value, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return swos.Value{}, f.err
	}
	if page == "sys.b" && f.sys != "" {
		return swos.Parse([]byte(f.sys))
	}
	name := page
	if page == "!stats.b" {
		f.stats++
		if f.stats == 1 {
			name = "!stats.b.sample2"
		}
	}
	b, err := os.ReadFile(filepath.Join(lite, name))
	if err != nil {
		return swos.Value{}, swos.ErrNotFound
	}
	return swos.Parse(b)
}

type recorder struct {
	mu       sync.Mutex
	changed  chan string
	online   []bool
	traffic  []collect.TrafficSample
	ports    []collect.IfStatusPayload
	identity []swos.Switch
}

func newPoller(t *testing.T, f *fakeSwitch) (*Poller, *recorder) {
	t.Helper()
	r := &recorder{changed: make(chan string, 64)}
	p := New(Deps{
		Dial: func(store.Switch) (Getter, error) { return f, nil },
		Online: func(_ string, up bool, _ time.Time) {
			r.mu.Lock()
			r.online = append(r.online, up)
			r.mu.Unlock()
		},
		Traffic: func(_ string, s collect.TrafficSample) {
			r.mu.Lock()
			r.traffic = append(r.traffic, s)
			r.mu.Unlock()
		},
		Ports: func(_ store.Switch, pl collect.IfStatusPayload) {
			r.mu.Lock()
			r.ports = append(r.ports, pl)
			r.mu.Unlock()
		},
		Identity: func(_ string, s swos.Switch) {
			r.mu.Lock()
			r.identity = append(r.identity, s)
			r.mu.Unlock()
		},
		Changed: func(id string) { r.changed <- id },
	})
	t.Cleanup(p.Stop)
	return p, r
}

func waitChanged(t *testing.T, r *recorder) {
	t.Helper()
	select {
	case <-r.changed:
	case <-time.After(5 * time.Second):
		t.Fatal("no read finished")
	}
}

func TestASwitchIsReadAndFedToHistoryAndAlerts(t *testing.T) {
	p, r := newPoller(t, &fakeSwitch{})
	p.Sync([]store.Switch{{ID: "sw1", Label: "Core", Host: "198.51.100.2"}})
	waitChanged(t, r)
	st, ok := p.State("sw1")
	if !ok || !st.Known || !st.Online || st.Switch == nil || st.Switch.Model != "CSS610-8P-2S+" {
		t.Fatalf("state %+v", st)
	}
	if st.Hosts == nil || *st.Hosts != 29 {
		t.Errorf("hosts = %v, want 29", st.Hosts)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.online) != 1 || !r.online[0] {
		t.Errorf("online reports %v", r.online)
	}
	if len(r.traffic) != 10 || r.traffic[0].IfName != "p1_uplink-a" || r.traffic[1].RxMbps <= 0 {
		t.Errorf("traffic: %d samples, first %+v", len(r.traffic), r.traffic)
	}
	if len(r.ports) != 1 || len(r.ports[0].Interfaces) != 10 || r.ports[0].Interfaces[3].Running {
		t.Errorf("ports payload: %+v", r.ports)
	}
	if len(r.identity) != 1 || r.identity[0].Serial != "SWTEST00001" {
		t.Errorf("identity: %+v", r.identity)
	}
}

// TestAPeekReadsAgainAtOnce: the modal's fast cadence, not the 30-second one.
func TestAPeekReadsAgainAtOnce(t *testing.T) {
	p, r := newPoller(t, &fakeSwitch{})
	p.Sync([]store.Switch{{ID: "sw1", Host: "198.51.100.2"}})
	waitChanged(t, r)
	p.Peek("sw1", true)
	waitChanged(t, r) // well inside Slow: the peek woke it
	st, _ := p.State("sw1")
	// The second read compared two captures, so the counters are the later ones.
	if len(st.Rates) != 10 || st.Rates[0].Reset {
		t.Errorf("rates after a second read: %+v", st.Rates)
	}
	p.Peek("sw1", false)
}

func TestAFailedReadSaysWhy(t *testing.T) {
	for _, c := range []struct {
		f    *fakeSwitch
		want string
	}{
		{&fakeSwitch{err: swos.ErrAuth}, "The switch refused the username or password"},
		{&fakeSwitch{sys: "{}"}, "This device did not answer as a SwOS switch"},
	} {
		p, r := newPoller(t, c.f)
		p.Sync([]store.Switch{{ID: "sw1", Host: "198.51.100.2"}})
		waitChanged(t, r)
		st, _ := p.State("sw1")
		if st.Online || !st.Known || st.LastError != c.want {
			t.Errorf("state %+v, want the error %q", st, c.want)
		}
	}
}

func TestSyncStopsWhatIsGoneAndSkipsWhatIsDisabled(t *testing.T) {
	p, r := newPoller(t, &fakeSwitch{})
	p.Sync([]store.Switch{{ID: "a", Host: "198.51.100.2"}, {ID: "b", Host: "198.51.100.3", Disabled: true}})
	waitChanged(t, r)
	if !p.Has("a") || p.Has("b") {
		t.Errorf("a %v b %v: a disabled switch is not polled", p.Has("a"), p.Has("b"))
	}
	p.Sync(nil)
	if p.Has("a") {
		t.Error("a removed switch is still polled")
	}
}
