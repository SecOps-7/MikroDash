package server

// A RECOVERY GOES ONLY TO A CHANNEL THAT WAS SENT THE BREACH.
//
// On 2026-10-22 Telegram, set to alert at 90% CPU, sent "CPU back to normal"
// twice and never the high-CPU alert before either: the spikes were 71% and
// 72%, under its threshold, and every recovery went to every channel
// regardless. These drive the real send path against a real database and a
// fake HTTP client, so what is asserted is what would have left the server.

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mikrodash/internal/alert"
	"mikrodash/internal/alertdispatch"
	"mikrodash/internal/db"
	"mikrodash/internal/notify"
)

// sentTo records which host every request went to: each channel below has its
// own host, so the host says which channel a message reached.
type sentTo struct {
	mu   sync.Mutex
	hits []string
}

func (s *sentTo) Do(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_, _ = io.ReadAll(r.Body)
	}
	s.mu.Lock()
	s.hits = append(s.hits, r.URL.Hostname())
	s.mu.Unlock()
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
}

// waitFor returns the hits once there are `n`, or whatever there is after a
// short wait (the sends run on a goroutine), and empties the record.
func (s *sentTo) waitFor(n int) []string {
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		have := len(s.hits)
		s.mu.Unlock()
		if have >= n || time.Now().After(deadline) {
			time.Sleep(50 * time.Millisecond) // and nothing more arrives
			s.mu.Lock()
			got := append([]string{}, s.hits...)
			s.hits = nil
			s.mu.Unlock()
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func channelRow(id string, cpu, cooldown int) db.NotifyChannel {
	return db.NotifyChannel{
		ID: id, Owner: db.InstallOwner, Name: id, Kind: notify.KindWebhook, Enabled: 1,
		Config:  `{"urls":["json://` + id + `.example/hook"]}`,
		Events:  `["high_cpu","ping_loss"]`,
		Routers: `[]`, IfaceTypes: `[]`,
		Tuning: `{"cpu":` + strconv.Itoa(cpu) + `,"pingLoss":30,"cooldownSec":` +
			strconv.Itoa(cooldown) + `}`,
	}
}

func recoveryServer(d *db.DB, clock *int64) (*Server, *sentTo) {
	tr := &sentTo{}
	return &Server{
		auditDB:  d,
		dispatch: alertdispatch.New(true, notify.Settings{}, tr, tr, nil, func() int64 { return *clock }),
	}, tr
}

func highCPU(v float64) []alert.Fired {
	return []alert.Fired{{AlertType: "High CPU", Detail: "CPU high", Value: v}}
}

var cpuNormal = []alert.Fired{{Up: true, AlertType: "CPU Normal", ResolveType: "high_cpu",
	Detail: "CPU back to normal"}}

func TestARecoveryGoesOnlyToChannelsThatGotTheBreach(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, c := range []db.NotifyChannel{channelRow("at90", 90, 60), channelRow("at70", 70, 60)} {
		if err := d.UpsertNotifyChannel(c); err != nil {
			t.Fatal(err)
		}
	}
	clock := int64(1_000_000)
	s, tr := recoveryServer(d, &clock)

	// The real spike: 71%, which only the channel set to 70 asked about.
	s.dispatchFired("r1", "AX3", highCPU(71))
	if got := tr.waitFor(1); len(got) != 1 || got[0] != "at70.example" {
		t.Fatalf("the 71%% breach reached %v, want only at70.example", got)
	}
	clock += 2_000
	s.dispatchFired("r1", "AX3", cpuNormal)
	if got := tr.waitFor(1); len(got) != 1 || got[0] != "at70.example" {
		t.Fatalf("the recovery reached %v, want only at70.example: a channel set to 90 was "+
			"told CPU recovered from a breach it was never sent", got)
	}

	// CONTROL: a breach loud enough for both reaches both, and so does its recovery.
	clock += 300_000
	s.dispatchFired("r1", "AX3", highCPU(95))
	if got := tr.waitFor(2); len(got) != 2 {
		t.Fatalf("a 95%% breach reached %v, want both channels", got)
	}
	clock += 2_000
	s.dispatchFired("r1", "AX3", cpuNormal)
	if got := tr.waitFor(2); len(got) != 2 {
		t.Fatalf("its recovery reached %v, want both channels", got)
	}
}

// A breach the cooldown held back was not sent, so its recovery is not either.
func TestABreachHeldBackByTheCooldownGetsNoRecovery(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.UpsertNotifyChannel(channelRow("tg", 70, 120)); err != nil {
		t.Fatal(err)
	}
	clock := int64(1_000_000)
	s, tr := recoveryServer(d, &clock)

	s.dispatchFired("r1", "AX3", highCPU(80)) // sent, and the cooldown starts
	clock += 1_000
	s.dispatchFired("r1", "AX3", cpuNormal) // sent
	if got := tr.waitFor(2); len(got) != 2 {
		t.Fatalf("the first episode reached %v, want breach and recovery", got)
	}

	clock += 99_000 // 100 s after the first breach: inside its 120 s cooldown
	s.dispatchFired("r1", "AX3", highCPU(80))
	clock += 100_000 // and well past the first recovery's cooldown
	s.dispatchFired("r1", "AX3", cpuNormal)
	if got := tr.waitFor(1); len(got) != 0 {
		t.Fatalf("a breach the cooldown held back was followed by %v: a recovery of an alert "+
			"the channel was never sent", got)
	}
}

// Who was sent the breach is in the database, so a restart mid-episode still
// sends the recovery to the channel that heard the breach.
func TestWhoHeardTheBreachSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertNotifyChannel(channelRow("tg", 70, 60)); err != nil {
		t.Fatal(err)
	}
	clock := int64(1_000_000)
	s, tr := recoveryServer(d, &clock)
	s.dispatchFired("r1", "AX3", highCPU(85))
	if got := tr.waitFor(1); len(got) != 1 {
		t.Fatalf("the breach reached %v", got)
	}
	_ = d.Close()

	d2, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	clock += 60_000
	s2, tr2 := recoveryServer(d2, &clock)
	s2.dispatchFired("r1", "AX3", cpuNormal)
	if got := tr2.waitFor(1); len(got) != 1 || got[0] != "tg.example" {
		t.Fatalf("after a restart the recovery reached %v, want tg.example", got)
	}
}
