package notify

import (
	"errors"
	"strings"
	"testing"
)

// EVERY SCHEME, BUILT AND INSPECTED WITHOUT A NETWORK.
//
// The request builders here are pure by design, which is the property that made
// hand-rolling these worth doing: a fixture can assert the exact host, path and
// body a provider will receive. A library that sends would need an HTTP server
// per provider to pin the same thing.
func TestEverySchemeBuildsTheRequestItsProviderExpects(t *testing.T) {
	cases := []struct {
		name, url        string
		scheme, host     string
		port             int
		pathHas, bodyHas string
	}{
		{
			name: "telegram", url: "tgram://111:AAA/-100123",
			scheme: "https", host: "api.telegram.org", port: 443,
			pathHas: "/sendMessage", bodyHas: `"chat_id":"-100123"`,
		},
		{
			name: "pushbullet", url: "pbul://o.ABCDEF",
			scheme: "https", host: "api.pushbullet.com", port: 443,
			pathHas: "/v2/pushes", bodyHas: `"type":"note"`,
		},
		{
			name: "ntfy plain", url: "ntfy://ntfy.example.net/mikrodash",
			scheme: "http", host: "ntfy.example.net", port: 80,
			pathHas: "/mikrodash", bodyHas: "the detail",
		},
		{
			name: "ntfy tls", url: "ntfys://ntfy.example.net/mikrodash",
			scheme: "https", host: "ntfy.example.net", port: 443,
			pathHas: "/mikrodash", bodyHas: "the detail",
		},
		{
			name: "discord", url: "discord://123456/wh-token",
			scheme: "https", host: "discord.com", port: 443,
			pathHas: "/api/webhooks/123456/wh-token", bodyHas: `"content"`,
		},
		{
			name: "slack", url: "slack://token-a/token-b/token-c",
			scheme: "https", host: "hooks.slack.com", port: 443,
			pathHas: "/services/token-a/token-b/token-c", bodyHas: `"text"`,
		},
		{
			name: "gotify", url: "gotify://gotify.example.net/AppToken",
			scheme: "http", host: "gotify.example.net", port: 80,
			pathHas: "/message?token=AppToken", bodyHas: `"message"`,
		},
		{
			name: "gotify tls with port", url: "gotifys://gotify.example.net:8443/AppToken",
			scheme: "https", host: "gotify.example.net", port: 8443,
			pathHas: "/message?token=AppToken", bodyHas: `"message"`,
		},
		{
			name: "pushover", url: "pover://user-key/api-token",
			scheme: "https", host: "api.pushover.net", port: 443,
			pathHas: "/1/messages.json", bodyHas: "token=api-token",
		},
		{
			name: "generic json", url: "jsons://hooks.example.net/mikrodash?src=md",
			scheme: "https", host: "hooks.example.net", port: 443,
			pathHas: "/mikrodash?src=md", bodyHas: `"title"`,
		},
		{
			name: "apprise", url: "apprise://apprise.example.net:8000/mykey",
			scheme: "http", host: "apprise.example.net", port: 8000,
			pathHas: "/notify/mykey", bodyHas: `"body"`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Parse(c.url, "Interface Down", "the detail")
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.url, err)
			}
			if r.Scheme != c.scheme || r.Host != c.host || r.Port != c.port {
				t.Errorf("got %s://%s:%d, want %s://%s:%d",
					r.Scheme, r.Host, r.Port, c.scheme, c.host, c.port)
			}
			if !strings.Contains(r.Path, c.pathHas) {
				t.Errorf("path %q does not contain %q", r.Path, c.pathHas)
			}
			if !strings.Contains(string(r.Body), c.bodyHas) {
				t.Errorf("body %q does not contain %q", r.Body, c.bodyHas)
			}
			// EVERY REQUEST CARRIES A BODY. A provider that received an empty
			// POST would answer 400, and the operator would see a delivery
			// failure with nothing in it to explain why.
			if len(r.Body) == 0 {
				t.Error("built an empty body")
			}
		})
	}
}

// A BOT TOKEN CONTAINS A COLON, and url.Parse reads a colon in the host as a
// port. Taking `u.Host` would truncate every Telegram URL at the colon and send
// to a bot id with no token — a 404 from Telegram, and nothing in the message to
// say why.
func TestATelegramTokenSurvivesItsColon(t *testing.T) {
	r, err := Parse("tgram://8123456789:AAH-rEaLlYlOnGtOkEn_x/-1001234567890", "T", "B")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Path, "8123456789") ||
		!strings.Contains(r.Path, "AAH-rEaLlYlOnGtOkEn_x") {
		t.Errorf("the token did not survive parsing: %q", r.Path)
	}
}

// AN UNKNOWN SCHEME IS AN ERROR, NOT A SKIP. A channel holding a URL nothing can
// send delivers silently nothing, and the operator finds out during the incident
// it was meant to report.
func TestAnUnknownSchemeIsRefused(t *testing.T) {
	for _, raw := range []string{"matrix://server/room", "https://example.net/hook", "nonsense"} {
		if _, err := Parse(raw, "T", "B"); !errors.Is(err, ErrUnknownScheme) {
			t.Errorf("Parse(%q) error = %v, want ErrUnknownScheme", raw, err)
		}
		if err := Validate(raw); err == nil {
			t.Errorf("Validate(%q) accepted a URL nothing can send", raw)
		}
	}
}

// A SCHEME WITH TOO FEW PARTS IS ALSO REFUSED, and says what it wanted. A
// discord URL missing its token would otherwise POST to a webhook path that does
// not exist.
func TestATruncatedURLIsRefusedAndSaysTheForm(t *testing.T) {
	cases := map[string]string{
		"discord://only-id":     "webhook_id",
		"slack://a/b":           "token_a",
		"pover://user-key":      "user_key",
		"gotify://host-only":    "host",
		"ntfy://host-only":      "host",
		"apprise://host-only":   "config_key",
		"tgram://token-no-chat": "chat_id",
	}
	for raw, wantMention := range cases {
		err := Validate(raw)
		if err == nil {
			t.Errorf("Validate(%q) accepted a truncated URL", raw)
			continue
		}
		if !strings.Contains(err.Error(), wantMention) {
			t.Errorf("Validate(%q) said %q, which does not mention %q",
				raw, err, wantMention)
		}
	}
}

// THE ADVERTISED LIST IS THE ACCEPTED LIST, both ways. A scheme in `Schemes`
// that Parse rejects is help text promising something that fails; a scheme Parse
// accepts and `Schemes` omits never reaches the operator at all.
func TestSchemesMatchesWhatParseAccepts(t *testing.T) {
	for _, s := range Schemes {
		// A URL with enough parts for any of them.
		if err := Validate(s + "://a/b/c"); err != nil {
			t.Errorf("Schemes advertises %q and Parse refuses it: %v", s, err)
		}
	}
	// And nothing outside the list is accepted — checked against the names this
	// app used to speak, plus a few near-misses.
	for _, s := range []string{"telegram", "pushbullet", "gotifyss", "gotify2", "gotif"} {
		if err := Validate(s + "://a/b/c"); err == nil {
			t.Errorf("Parse accepts %q, which Schemes does not advertise", s)
		}
	}
}

// ONE TEXT FIELD MUST NOT LOSE HALF THE MESSAGE. Discord and Slack have a single
// content field; the title holds the alert type and the body the detail.
func TestSingleFieldProvidersKeepBothHalves(t *testing.T) {
	for _, raw := range []string{"discord://id/token", "slack://a/b/c"} {
		r, err := Parse(raw, "Interface Down", "ether3 on Office")
		if err != nil {
			t.Fatal(err)
		}
		got := string(r.Body)
		if !strings.Contains(got, "Interface Down") || !strings.Contains(got, "ether3 on Office") {
			t.Errorf("%s dropped half the message: %s", raw, got)
		}
	}
}
