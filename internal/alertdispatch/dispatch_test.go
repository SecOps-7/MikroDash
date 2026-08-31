package alertdispatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mikrodash/internal/alert"
	"mikrodash/internal/notify"
)

type corpus struct {
	Messages []struct {
		Why       string         `json:"why"`
		Settings  map[string]any `json:"settings"`
		AlertType string         `json:"alertType"`
		Vars      map[string]any `json:"vars"`
		IsUp      bool           `json:"isUp"`
		Title     string         `json:"title"`
		Body      string         `json:"body"`
	} `json:"messages"`
	Cooldowns []struct {
		Why      string         `json:"why"`
		Settings map[string]any `json:"settings"`
		Steps    []struct {
			At         int64  `json:"at"`
			SubjectKey string `json:"subjectKey"`
			Recipient  *struct {
				ID       string         `json:"id"`
				Settings map[string]any `json:"settings"`
			} `json:"recipient"`
		} `json:"steps"`
		Result []struct {
			At      int64  `json:"at"`
			Sent    bool   `json:"sent"`
			MapSize int    `json:"mapSize"`
			Subject string `json:"subjectKey"`
		} `json:"result"`
	} `json:"cooldowns"`
}

func load(t *testing.T) corpus {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "alert-dispatch-cases.json"))
	if err != nil {
		t.Fatalf("no corpus — run: node tools/alert-dispatch-cases.js: %v", err)
	}
	var c corpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Messages) == 0 || len(c.Cooldowns) == 0 {
		t.Fatal("the corpus is empty — these tests would pass against nothing")
	}
	return c
}

// THE MESSAGE, against the live assembly.
func TestTheMessageMatchesLive(t *testing.T) {
	c := load(t)
	for _, m := range c.Messages {
		t.Run(m.Why, func(t *testing.T) {
			detail, _ := m.Vars["detail"].(string)
			subject, _ := m.Vars["subject"].(string)
			got := Build(notify.Settings(m.Settings), "Test Router", "12:34:56",
				alert.Fired{AlertType: m.AlertType, Detail: detail, Subject: subject, Up: m.IsUp})
			if got.Title != m.Title {
				t.Errorf("title = %q, live = %q", got.Title, m.Title)
			}
			if got.Body != m.Body {
				t.Errorf("body  = %q, live = %q", got.Body, m.Body)
			}
		})
	}
}

// THE COOLDOWN, replayed as the SEQUENCE the corpus recorded.
//
// `Allow` rather than `Deliver`, because the corpus records the DECISION and the
// map size — what the live `_deliver` did before reaching `notifier.send`.
// Nothing here can send: there is no transport wired at all.
func TestTheCooldownMatchesLive(t *testing.T) {
	c := load(t)
	for _, run := range c.Cooldowns {
		t.Run(run.Why, func(t *testing.T) {
			var now int64
			d := New(true, notify.Settings(run.Settings), nil, nil, func() int64 { return now })
			for i, step := range run.Steps {
				now = step.At
				var r *Recipient
				if step.Recipient != nil {
					r = &Recipient{ID: step.Recipient.ID,
						Settings: notify.Settings(step.Recipient.Settings)}
				}
				got := d.Allow(r, step.SubjectKey)
				want := run.Result[i]
				if got != want.Sent {
					t.Errorf("step %d (at %d, %s): allowed=%v, live sent=%v",
						i, step.At, step.SubjectKey, got, want.Sent)
				}
				if len(d.cooldowns) != want.MapSize {
					t.Errorf("step %d: %d cooldown entries, live had %d — the cooldown is "+
						"stamped in a different place from the live one",
						i, len(d.cooldowns), want.MapSize)
				}
			}
		})
	}
}

// ── the switch ──────────────────────────────────────────────────────────────

// A DISABLED DISPATCHER SENDS NOTHING AND LEAVES NO TRACE.
//
// The second half is the part worth pinning: if it consumed the cooldown while
// disabled, then turning it on would find every subject already warm and the
// first real alert of each kind would be silently swallowed.
func TestADisabledDispatcherLeavesNoTrace(t *testing.T) {
	sent := 0
	d := New(false, notify.Settings{"telegramEnabled": true, "telegramBotToken": "x",
		"telegramChatId": "1"}, nil, nil, func() int64 { return realInstant })
	d.sendFn = func(context.Context, notify.Settings, string, string) error { sent++; return nil }

	r := &Recipient{ID: "_install", Settings: d.settings}
	for i := 0; i < 3; i++ {
		if d.Deliver(context.Background(), r, "cpu", Message{Title: "T", Body: "B"}) {
			t.Fatal("a disabled dispatcher reported a send")
		}
	}
	if sent != 0 {
		t.Errorf("the transport was called %d time(s) while disabled", sent)
	}
	if len(d.cooldowns) != 0 {
		t.Errorf("%d cooldown entries were stamped while disabled — turning the dispatch "+
			"on later would find them warm and swallow the first alert of each kind",
			len(d.cooldowns))
	}
	if d.Enabled() {
		t.Error("Enabled() is true on a disabled dispatcher")
	}
}

// A REALISTIC INSTANT, and it matters more than it looks.
//
// The window test is `now - last < window`, and an absent stamp is zero — so a
// clock reading 1000 is only one second past the epoch and INSIDE a sixty-second
// window. The first draft of these tests used exactly that and read as "an
// enabled dispatcher did not send", which looks like a broken switch and was a
// broken fixture.
//
// The live code has the identical expression and never meets it, because
// `Date.now()` is ~1.7e12. Faithful, and unreachable in production — but only
// because the clock is real.
const realInstant int64 = 1699996400000

// A NIL DISPATCHER is inert rather than a panic — that is what the server holds
// when the operator has not switched dispatch on.
func TestANilDispatcherIsInert(t *testing.T) {
	var d *Dispatcher
	if d.Enabled() {
		t.Error("a nil dispatcher reports enabled")
	}
	if d.Deliver(context.Background(), &Recipient{ID: "x"}, "k", Message{}) {
		t.Error("a nil dispatcher reported a send")
	}
}

// AN ENABLED one does send, so the test above is not passing for want of a
// working path.
func TestAnEnabledDispatcherSends(t *testing.T) {
	var got Message
	sent := 0
	s := notify.Settings{"telegramEnabled": true, "telegramBotToken": "x", "telegramChatId": "1"}
	d := New(true, s, nil, nil, func() int64 { return realInstant })
	d.sendFn = func(_ context.Context, _ notify.Settings, title, body string) error {
		sent++
		got = Message{Title: title, Body: body}
		return nil
	}
	if !d.Deliver(context.Background(), &Recipient{ID: "_install", Settings: s}, "cpu",
		Message{Title: "T", Body: "B"}) {
		t.Fatal("an enabled dispatcher did not send")
	}
	if sent != 1 || got.Title != "T" || got.Body != "B" {
		t.Errorf("sent %d time(s) with %+v", sent, got)
	}
	// AND THE SECOND IS HELD by the cooldown, at the same instant.
	if d.Deliver(context.Background(), &Recipient{ID: "_install", Settings: s}, "cpu",
		Message{Title: "T", Body: "B"}) {
		t.Error("a second send at the same instant was allowed")
	}
}

// A TRANSPORT FAILURE is reported as "did not send" and does not stop anything
// else — but the cooldown IS already spent, matching the live order where the
// stamp happens before the send.
func TestATransportFailureIsReportedNotPanicked(t *testing.T) {
	s := notify.Settings{"telegramEnabled": true, "telegramBotToken": "x", "telegramChatId": "1"}
	d := New(true, s, nil, nil, func() int64 { return realInstant })
	d.sendFn = func(context.Context, notify.Settings, string, string) error {
		return context.DeadlineExceeded
	}
	if d.Deliver(context.Background(), &Recipient{ID: "_install", Settings: s}, "cpu", Message{}) {
		t.Error("a failed send reported success")
	}
	if len(d.cooldowns) != 1 {
		t.Errorf("%d cooldown entries after a failed send — the live order stamps before "+
			"sending, so a failure still spends the window", len(d.cooldowns))
	}
}

// ── recipients ──────────────────────────────────────────────────────────────

// PER-USER FAN-OUT IS OFF UNLESS THE INSTALL SWITCH SAYS OTHERWISE, and the
// switch ships absent — which must read as off.
func TestPerUserRecipientsAreGatedOnTheInstallSwitch(t *testing.T) {
	called := 0
	perUser := func(string) ([]Recipient, error) {
		called++
		return []Recipient{{ID: "user:u1"}}, nil
	}

	off := New(true, notify.Settings{}, nil, nil, func() int64 { return 1 })
	if got := off.Recipients("r-1", perUser); len(got) != 1 || got[0].ID != "_install" {
		t.Errorf("with the switch absent: %v", got)
	}
	if called != 0 {
		t.Error("the per-user lookup ran with the switch off")
	}

	on := New(true, notify.Settings{"userNotifyEnabled": true}, nil, nil, func() int64 { return 1 })
	got := on.Recipients("r-1", perUser)
	if len(got) != 2 || got[0].ID != "_install" || got[1].ID != "user:u1" {
		t.Errorf("with the switch on: %v", got)
	}
}

// A FAILING per-user lookup must never cost the install its own notification.
func TestAFailingPerUserLookupKeepsTheInstallRecipient(t *testing.T) {
	d := New(true, notify.Settings{"userNotifyEnabled": true}, nil, nil, func() int64 { return 1 })
	got := d.Recipients("r-1", func(string) ([]Recipient, error) {
		return nil, context.DeadlineExceeded
	})
	if len(got) != 1 || got[0].ID != "_install" {
		t.Errorf("a failing per-user lookup produced %v — the install destination is the "+
			"one an operator actually relies on", got)
	}
}
