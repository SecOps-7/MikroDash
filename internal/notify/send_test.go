package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type sendCorpus struct {
	// NtfyHasReason records which branch the generator READ out of the live
	// sendNtfy, so a revert upstream is a loud failure here rather than a quiet
	// regeneration.
	NtfyHasReason bool `json:"ntfyHasReason"`
	Reasons       []struct {
		Raw string `json:"raw"`
		Out string `json:"out"`
	} `json:"reasons"`
	Errors []struct {
		Channel string `json:"channel"`
		Status  int    `json:"status"`
		Raw     string `json:"raw"`
		Out     string `json:"out"`
	} `json:"errors"`
	Preconditions []struct {
		Channel  string            `json:"channel"`
		Settings map[string]string `json:"settings"`
		Missing  string            `json:"missing"`
		Out      *string           `json:"out"`
	} `json:"preconditions"`
}

func loadSendCorpus(t *testing.T) sendCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/notify-send-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c sendCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Errors) == 0 || len(c.Preconditions) == 0 {
		t.Fatal("corpus is empty -- these tests would pass against nothing")
	}
	return c
}

// stubDoer answers every request with one canned response.
type stubDoer struct {
	status int
	body   string
	err    error
	seen   []*http.Request
}

func (s *stubDoer) Do(r *http.Request) (*http.Response, error) {
	s.seen = append(s.seen, r)
	if s.err != nil {
		return nil, s.err
	}
	return &http.Response{
		StatusCode: s.status,
		Body:       io.NopCloser(strings.NewReader(s.body)),
	}, nil
}

func TestReasonMatchesLive(t *testing.T) {
	for _, tc := range loadSendCorpus(t).Reasons {
		if got := Reason(tc.Raw); got != tc.Out {
			t.Errorf("Reason(%q) = %q, live %q", tc.Raw, got, tc.Out)
		}
	}
}

// TestTheErrorTextMatchesLive.
//
// ── THIS USED TO ASSERT AN ASYMMETRY, AND THE ASYMMETRY IS GONE ────────────
//
// It carried `sawBare`, which required at least one case where ntfy DISCARDED a
// reason it had been given — the port's rule that a reproduced quirk must be
// asserted to still exist, so that fixing it upstream fails the suite rather
// than leaving a stale note.
//
// It fired. The live `sendNtfy` now appends `_reason(buf)` like the other two
// transports (../MikroDash/ToDo.md §4), the corpus reads that branch out of the
// source rather than transcribing it, and `Post` no longer takes a flag to
// select between them. `sawBare` is DELETED rather than inverted: there is no
// second shape to see any more.
func TestTheErrorTextMatchesLive(t *testing.T) {
	c := loadSendCorpus(t)
	if !c.NtfyHasReason {
		t.Fatal("the corpus says the live ntfy transport does NOT include a reason. If " +
			"that fix was reverted upstream, Post needs its flag back and this test " +
			"needs its sawBare half back -- do not simply regenerate")
	}
	sawReason := false
	seen := map[string]bool{}
	for _, tc := range c.Errors {
		d := &stubDoer{status: tc.Status, body: tc.Raw}
		err := Post(context.Background(), d,
			Request{Scheme: "https", Host: "x", Port: 443, Path: "/y"})
		if err == nil {
			t.Fatalf("status %d produced no error", tc.Status)
		}
		if err.Error() != tc.Out {
			t.Errorf("%s %d: %q, live %q", tc.Channel, tc.Status, err.Error(), tc.Out)
		}
		seen[tc.Channel] = true
		if strings.Contains(tc.Out, "—") {
			sawReason = true
		}
	}
	if !sawReason {
		t.Error("no case shows a reason being included, so that half is untested")
	}
	// BOTH channels must appear, or "every channel reports its reason" is being
	// checked against one of them.
	for _, ch := range []string{"http", "ntfy"} {
		if !seen[ch] {
			t.Errorf("the corpus has no %s case, so the claim that every channel "+
				"reports its reason is checked against fewer than every channel", ch)
		}
	}
}

func TestA2xxIsNotAnError(t *testing.T) {
	for _, status := range []int{200, 201, 204, 299} {
		d := &stubDoer{status: status, body: `{"error":"ignored"}`}
		if err := Post(context.Background(), d, Request{Scheme: "https", Host: "x", Port: 443}); err != nil {
			t.Errorf("status %d reported %v", status, err)
		}
	}
	for _, status := range []int{199, 300, 301, 400} {
		d := &stubDoer{status: status}
		if err := Post(context.Background(), d, Request{Scheme: "https", Host: "x", Port: 443}); err == nil {
			t.Errorf("status %d was treated as success", status)
		}
	}
}

// TestATransportFailureIsPassedThroughUnlabelled.
//
// A DNS failure or a timeout is not an HTTP failure, and the live app's older
// "error: HTTP <message>" prefix mislabelled exactly those.
func TestATransportFailureIsPassedThroughUnlabelled(t *testing.T) {
	d := &stubDoer{err: errors.New("dial tcp: lookup ntfy.sh: no such host")}
	err := Post(context.Background(), d, Request{Scheme: "https", Host: "x", Port: 443})
	if err == nil {
		t.Fatal("a transport failure produced no error")
	}
	if strings.HasPrefix(err.Error(), "HTTP") {
		t.Errorf("a DNS failure was labelled as HTTP: %q", err)
	}
}

func TestPreconditionMatchesLive(t *testing.T) {
	for _, tc := range loadSendCorpus(t).Preconditions {
		s := Settings{}
		for k, v := range tc.Settings {
			s[k] = v
		}
		want := ""
		if tc.Out != nil {
			want = *tc.Out
		}
		if got := Precondition(s, Channel(tc.Channel)); got != want {
			t.Errorf("channel %q missing %q: %q, live %q", tc.Channel, tc.Missing, got, want)
		}
	}
}

// TestSendCollectsEveryFailureAndKeepsGoing is the behaviour the whole function
// exists for: a broken Telegram token must not silence email.
func TestSendCollectsEveryFailureAndKeepsGoing(t *testing.T) {
	s := Settings{
		"telegramEnabled": true, "telegramBotToken": "t", "telegramChatId": "c",
		"pushbulletEnabled": true, "pushbulletApiKey": "k",
		"ntfyEnabled": true, "ntfyUrl": "https://ntfy.sh/topic",
		"smtpEnabled": true, "smtpHost": "h", "smtpFrom": "f", "smtpTo": "to",
	}

	// Every HTTP channel fails; the mailer succeeds and MUST still be called.
	d := &stubDoer{status: 403, body: `{"description":"nope"}`}
	mailed := false
	err := Send(context.Background(), d, s, func(string, string) error { mailed = true; return nil }, "T", "B")
	if err == nil {
		t.Fatal("three failing channels produced no error")
	}
	if !mailed {
		t.Error("email was never attempted -- a failing channel stopped the others")
	}
	if len(d.seen) != 3 {
		t.Errorf("%d HTTP requests, want 3 (telegram, pushbullet, ntfy)", len(d.seen))
	}
	msg := err.Error()
	for _, name := range []string{"Telegram:", "Pushbullet:", "ntfy:"} {
		if !strings.Contains(msg, name) {
			t.Errorf("the error does not name %s: %q", name, msg)
		}
	}
	if strings.Contains(msg, "SMTP:") {
		t.Errorf("email succeeded but is reported as failed: %q", msg)
	}
	// The order is the live one and is visible in the message.
	if strings.Index(msg, "Telegram:") > strings.Index(msg, "Pushbullet:") ||
		strings.Index(msg, "Pushbullet:") > strings.Index(msg, "ntfy:") {
		t.Errorf("the channels are reported out of order: %q", msg)
	}
	// ...and EVERY half carries its reason.
	//
	// The second of these used to be `if strings.Contains(msg, "ntfy: HTTP 403 —")`
	// — ntfy's half carrying no reason where Telegram's did. That was the live
	// asymmetry, asserted so that fixing it upstream would fail here rather than
	// leave a stale note behind. It fired; ../MikroDash/ToDo.md §4 is fixed, and
	// the assertion is inverted rather than deleted because there is still a real
	// property to state: the collected message must not silently lose one
	// channel's explanation while keeping another's.
	for _, ch := range []string{"Telegram", "Pushbullet", "ntfy"} {
		if !strings.Contains(msg, ch+": HTTP 403 — nope") {
			t.Errorf("%s's reason was dropped: %q", ch, msg)
		}
	}
}

// TestSendSkipsChannelsThatAreNotFullyConfigured: a channel ticked without its
// credentials must not be attempted. The live comment records what happened when
// the two halves disagreed — "a channel ticked without a token consumed the
// alert cooldown, sent nothing, and logged nothing".
func TestSendSkipsChannelsThatAreNotFullyConfigured(t *testing.T) {
	for _, s := range []Settings{
		{"telegramEnabled": true, "telegramBotToken": "t"}, // no chat id
		{"telegramEnabled": true, "telegramChatId": "c"},   // no token
		{"pushbulletEnabled": true},                        // no key
		{"ntfyEnabled": true},                              // no url
		{"telegramBotToken": "t", "telegramChatId": "c"},   // not enabled
	} {
		d := &stubDoer{status: 200}
		if err := Send(context.Background(), d, s, nil, "T", "B"); err != nil {
			t.Errorf("%v: %v", s, err)
		}
		if len(d.seen) != 0 {
			t.Errorf("%v: %d requests were sent for an unconfigured channel", s, len(d.seen))
		}
	}
}

func TestTestChannelRefusesBeforeSending(t *testing.T) {
	d := &stubDoer{status: 200}
	err := TestChannel(context.Background(), d, Settings{}, Telegram, nil)
	if err == nil || err.Error() != "Telegram Bot Token is not configured" {
		t.Errorf("err = %v", err)
	}
	if len(d.seen) != 0 {
		t.Error("a channel with no credentials was still contacted")
	}
	if err := TestChannel(context.Background(), d, Settings{}, Channel("nonsense"), nil); err == nil {
		t.Error("an unknown channel was accepted")
	}
}
