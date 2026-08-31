package notify

// ── THESE ARE COMPLETE AND DELIBERATELY UNWIRED ─────────────────────────────
//
// Nothing calls `Send`, and that is a cutover constraint rather than an
// oversight. Two things stand between here and a caller:
//
//  1. THE CALLER IS NOT PORTED. `src/alerter.js` is 692 lines and EVENT-DRIVEN —
//     `evaluate(event, data)` runs per collector event, not on a timer. This
//     port's `internal/alert` holds the formatting and the row shaping, none of
//     the decision.
//
//  2. WIRING IT WHILE NODE RUNS WOULD SEND EVERYTHING TWICE. Both apps run
//     collectors against the same routers and would evaluate the same
//     conditions, and the cooldown that might have interlocked them is an
//     IN-MEMORY Map (`_deliver`'s `cooldownMap`) rather than a shared row — so
//     neither engine sees the other's sends.
//
// That last point is why this blocker is worse than the settings one. A reverted
// write is a lost edit; a duplicated notification is outbound, and an operator
// cannot un-receive two of every alert from the system that exists to tell them
// when something is wrong.
//
// PORT-QUEUE.md Part 25 records it; CLAUDE.md carries it as cutover blocker 5.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// RequestTimeout is the live `req.setTimeout(10000, …)`.
//
// The other channels enforce ten seconds too, and the live comment explains what
// it is for: `send()` awaits each channel in turn, so a black-holed host stalls
// every later channel behind it — and holds the test-notification HTTP request
// open for the same period.
const RequestTimeout = 10 * time.Second

// Doer is the HTTP client. An interface so a test can answer without a network.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// DefaultClient bounds the whole exchange, not just the connect.
var DefaultClient = &http.Client{Timeout: RequestTimeout}

// bodyLimit is how much of an error body is read before giving up on it.
//
// `Reason` truncates to 160 characters, but the read happens first: a server
// answering with a gigabyte of HTML would otherwise be buffered in full before
// anything trimmed it.
const bodyLimit = 64 << 10

// Post performs one described request and returns the live error text on
// failure.
//
// ── THERE WAS A `withReason` PARAMETER HERE, AND IT IS GONE ────────────────
//
// Telegram and Pushbullet failures included a reason pulled from the body and
// ntfy's did not: `sendNtfy` rejected with a bare `HTTP <status>` even though
// `_reason`'s own comment said ntfy returns one. The port reproduced that
// asymmetry deliberately rather than improving it, and filed it upstream as
// ../MikroDash/ToDo.md §4.
//
// The live side has since fixed it — `sendNtfy` appends `_reason(buf)` like the
// other two — so the flag had one value left, and a flag with one value is not a
// flag. `tools/notify-send-cases.js` now READS which branch the live source
// takes rather than encoding a transcription of it, which is how this stayed
// wrong quietly: every other generator lifts, and that one had a hand-written
// exception.
func Post(ctx context.Context, c Doer, r Request) error {
	u := fmt.Sprintf("%s://%s", r.Scheme, net.JoinHostPort(r.Host, fmt.Sprint(r.Port)))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u+r.Path, bytes.NewReader(r.Body))
	if err != nil {
		return err
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		// NOT NECESSARILY AN HTTP FAILURE — this also catches DNS and timeout
		// errors, which the live app's old "error: HTTP <message>" prefix
		// mislabelled. The message is passed through as it is.
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// EVERY channel now, ntfy included. The bare-status form this used to have
	// for ntfy is gone with the flag that selected it.
	return fmt.Errorf("HTTP %d%s", resp.StatusCode, Reason(string(raw)))
}

// Mailer sends one email. Satisfied by internal/mailer.
type Mailer func(title, body string) error

// Send delivers to every configured channel and COLLECTS the failures.
//
// It does not stop at the first: a broken Telegram token must not silence email.
// Each failure is prefixed with its channel and they are joined with "; ", so
// the operator learns which one failed rather than that "notification failed".
//
// The order is the live one — Telegram, Pushbullet, SMTP, ntfy — and it is
// visible in the joined message, so it is not free to change.
func Send(ctx context.Context, c Doer, s Settings, mail Mailer, title, body string) error {
	var errs []string
	try := func(name string, fn func() error) {
		if err := fn(); err != nil {
			errs = append(errs, name+": "+err.Error())
		}
	}

	str := func(k string) string { v, _ := s[k].(string); return v }

	if truthy(s["telegramEnabled"]) && str("telegramBotToken") != "" && str("telegramChatId") != "" {
		try("Telegram", func() error {
			return Post(ctx, c, TelegramRequest(str("telegramBotToken"), str("telegramChatId"), title, body))
		})
	}
	if truthy(s["pushbulletEnabled"]) && str("pushbulletApiKey") != "" {
		try("Pushbullet", func() error {
			return Post(ctx, c, PushbulletRequest(str("pushbulletApiKey"), title, body))
		})
	}
	if truthy(s["smtpEnabled"]) && str("smtpHost") != "" && str("smtpFrom") != "" && str("smtpTo") != "" {
		try("SMTP", func() error {
			if mail == nil {
				return errors.New("no mailer configured")
			}
			return mail(title, body)
		})
	}
	if truthy(s["ntfyEnabled"]) && str("ntfyUrl") != "" {
		try("ntfy", func() error {
			r, err := NtfyRequest(str("ntfyUrl"), str("ntfyToken"), title, body)
			if err != nil {
				return err
			}
			// FALSE: ntfy's failures carry no reason. See Post.
			return Post(ctx, c, r)
		})
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// TestTitle and TestBody are what a channel test sends.
const (
	TestTitle = "MikroDash Test"
	TestBody  = "Test notification from MikroDash — your alert channel is working correctly."
)

// preconditions are the fields each channel needs, IN THE ORDER THEY ARE
// CHECKED — the operator is told about the first missing one, so the order is
// part of what they see.
var preconditions = map[Channel][][2]string{
	Telegram: {
		{"telegramBotToken", "Telegram Bot Token is not configured"},
		{"telegramChatId", "Telegram Chat ID is not configured"},
	},
	Pushbullet: {{"pushbulletApiKey", "Pushbullet API Key is not configured"}},
	SMTP: {
		{"smtpHost", "SMTP Host is not configured"},
		{"smtpFrom", "SMTP From address is not configured"},
		{"smtpTo", "SMTP To address is not configured"},
	},
	Ntfy: {{"ntfyUrl", "ntfy topic URL is not configured"}},
}

// Precondition reports why a channel cannot be tested, or "" when it can.
//
// IT REFUSES BEFORE SENDING, and names the field. "Telegram Bot Token is not
// configured" is something an operator can act on; a 401 from Telegram is not,
// and neither is "test failed".
func Precondition(s Settings, ch Channel) string {
	list, ok := preconditions[ch]
	if !ok {
		return "Unknown notification channel"
	}
	for _, kv := range list {
		if v, _ := s[kv[0]].(string); v == "" {
			return kv[1]
		}
	}
	return ""
}

// TestChannel sends one channel's test notification.
func TestChannel(ctx context.Context, c Doer, s Settings, ch Channel, mail Mailer) error {
	if msg := Precondition(s, ch); msg != "" {
		return errors.New(msg)
	}
	str := func(k string) string { v, _ := s[k].(string); return v }
	switch ch {
	case Telegram:
		return Post(ctx, c, TelegramRequest(str("telegramBotToken"), str("telegramChatId"), TestTitle, TestBody))
	case Pushbullet:
		return Post(ctx, c, PushbulletRequest(str("pushbulletApiKey"), TestTitle, TestBody))
	case SMTP:
		if mail == nil {
			return errors.New("no mailer configured")
		}
		return mail(TestTitle, TestBody)
	case Ntfy:
		r, err := NtfyRequest(str("ntfyUrl"), str("ntfyToken"), TestTitle, TestBody)
		if err != nil {
			return err
		}
		return Post(ctx, c, r)
	}
	return errors.New("Unknown notification channel")
}
