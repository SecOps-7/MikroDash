// Package aiprovider talks to an OpenAI-compatible endpoint the operator chose.
//
// ── THE WIRE FORMAT, NOT THE SERVICE ────────────────────────────────────────
//
// There is no provider hostname anywhere in this package, and there must never
// be one. The operator supplies a base URL, which may be a hosted service, a
// gateway, or Ollama on the same machine — and the fully-local case is the one
// worth protecting, because it is the configuration that sends nothing about the
// operator's network to anybody. An `api.openai.com` default would quietly make
// the privacy-preserving setup the awkward one.
//
// ── THE CALLER CHOOSES THE DESTINATION, SO THE CLIENT IS NARROW ─────────────
//
// This is the second place in the app where a setting decides where the server
// connects OUT to; `internal/notify` is the first, and `POST /api/settings/
// test-notification` is gated three ways for exactly this reason. Private
// addresses are deliberately ALLOWED here — refusing them would break the local
// model case this feature exists to support — so the boundary cannot be "is the
// address routable". It is instead everything below: two schemes only, no
// credentials in the URL, a bounded body, a bounded time, and no redirect
// carrying the key anywhere the operator did not name.
package aiprovider

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Doer is the HTTP client. An interface so a test can answer without a network,
// matching `notify.Doer` — the tests that matter here assert on the REQUEST, and
// a model's reply is not a thing to assert on anyway.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// DefaultTimeout is used when the setting is absent or out of range.
//
// LONGER THAN `notify.RequestTimeout`, and deliberately. Ten seconds is right
// for posting a short message to a notification service; a local model on a CPU
// can take a minute to answer a first token, and a timeout tuned for the hosted
// case would make self-hosting look broken.
const DefaultTimeout = 60 * time.Second

// bodyLimit caps what is read from a reply.
//
// A model endpoint is a stranger that returns text, so an unbounded read is an
// unbounded allocation driven by whatever the operator pointed us at — including
// a misconfiguration that answers with something enormous and unrelated.
const bodyLimit = 2 << 20

// Config is one endpoint, built from the merged settings by the caller.
//
// Built by the CALLER rather than read from the store here, so this package owes
// nothing to `internal/store` and a test can drive it with a literal.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	// Headers is the operator's extra headers, one `Name: value` per line, for
	// the gateways that require them.
	Headers     string
	TimeoutMs   int
	TLSInsecure bool
}

// Timeout is the configured bound, or DefaultTimeout when it is unusable.
func (c Config) Timeout() time.Duration {
	if c.TimeoutMs < 1000 || c.TimeoutMs > 600000 {
		return DefaultTimeout
	}
	return time.Duration(c.TimeoutMs) * time.Millisecond
}

// Client builds the HTTP client for one Config.
//
// ── A CLIENT PER CONFIG, NOT A PACKAGE-LEVEL ONE ────────────────────────────
//
// `notify` can share one, because its timeout is a constant and it never skips
// verification. Here both are the operator's to set, and a shared client would
// mean the first request's TLS decision silently governing every later one — so
// turning verification off for a lab endpoint would leave it off for a hosted
// one until the process restarted.
//
// REDIRECTS ARE REFUSED, NOT FOLLOWED. Go attaches the request's headers to a
// redirected request, so following one would hand the API key to whatever host
// the response named. That is the operator's credential going somewhere they did
// not configure, decided by the endpoint rather than by them.
func (c Config) Client() *http.Client {
	tr := &http.Transport{}
	if c.TLSInsecure {
		// EXPLICIT OPT-IN ONLY, mirroring `routerTlsInsecure`. A lab endpoint
		// with a self-signed certificate is a real case; a blanket skip is not.
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // operator opt-in
	}
	return &http.Client{
		Timeout:   c.Timeout(),
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("the endpoint redirected; MikroDash does not follow redirects, " +
				"because that would send your API key to a host you did not configure")
		},
	}
}

// endpoint resolves the chat-completions URL for a base.
//
// ── WHAT IS REFUSED, AND WHY EACH ───────────────────────────────────────────
//
// Only http and https: a `file://` or `unix://` base is not a mistake this
// should quietly attempt. No userinfo: `https://user:pass@host/v1` puts a
// credential somewhere it is logged by every proxy in between, and accepting it
// would mean supporting a shape we cannot make safe. A host is required, because
// a base of `/v1` would otherwise resolve against nothing and produce an error
// naming this process rather than the setting.
//
// The trailing slash is trimmed rather than rejected: `…/v1/` and `…/v1` are the
// same endpoint to every server that speaks this format, and refusing one of
// them would be a configuration error the operator cannot see on the page.
func endpoint(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", errors.New("no endpoint is configured")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", errors.New("the endpoint is not a valid URL")
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return "", errors.New("the endpoint must start with http:// or https://")
	}
	if u.Host == "" {
		return "", errors.New("the endpoint has no host")
	}
	if u.User != nil {
		return "", errors.New("the endpoint must not carry a username or password; " +
			"put the key in the API Key field")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/chat/completions"
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// ParseHeaders reads the operator's extra headers, one `Name: value` per line.
//
// Blank lines are skipped and a line with no colon is an error rather than being
// ignored: a header the operator typed and that is silently dropped produces a
// gateway rejection they cannot explain from this page.
//
// The four headers this package sets itself cannot be overridden, because a
// custom `Authorization` would mean two of them on the wire and the operator
// having no way to tell which one the gateway honoured.
func ParseHeaders(s string) (map[string]string, error) {
	out := map[string]string{}
	reserved := map[string]bool{
		"authorization": true, "content-type": true, "accept": true, "host": true,
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" {
			return nil, fmt.Errorf("header %q is not in the form Name: value", line)
		}
		if reserved[strings.ToLower(name)] {
			return nil, fmt.Errorf("%s is set by MikroDash and cannot be overridden here", name)
		}
		out[name] = value
	}
	return out, nil
}

// chatRequest is the request body. Only the fields this app sends.
type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
	Stream    bool          `json:"stream"`
}

// ChatMessage is one turn.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatReply is the part of the response this app reads.
type chatReply struct {
	Choices []struct {
		Message ChatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete performs one non-streaming chat completion and returns the reply text.
//
// ── THE ERROR IS THE PROVIDER'S OWN WORDS WHERE THERE ARE ANY ───────────────
//
// "HTTP 401" tells an operator nothing they can act on; "Incorrect API key
// provided" tells them everything. OpenAI-compatible servers put that in
// `error.message`, so it is read out and used when present — and the caller is
// responsible for running it through `safe.Message` before it reaches a browser
// or a log, because a transport error can carry the host and the port.
func Complete(ctx context.Context, c Doer, cfg Config, msgs []ChatMessage, maxTokens int) (string, error) {
	u, err := endpoint(cfg.BaseURL)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return "", errors.New("no model is configured")
	}
	extra, err := ParseHeaders(cfg.Headers)
	if err != nil {
		return "", err
	}

	body, err := json.Marshal(chatRequest{
		Model: cfg.Model, Messages: msgs, MaxTokens: maxTokens, Stream: false,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// OMITTED WHEN EMPTY, not sent blank. A local endpoint that needs no key
	// will reject `Authorization: Bearer ` from some proxies, and an empty
	// credential header is not the same as no credential.
	if k := strings.TrimSpace(cfg.APIKey); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}

	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	var parsed chatReply
	_ = json.Unmarshal(raw, &parsed)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, parsed.Error.Message)
		}
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		// SOME SERVERS ANSWER 200 WITH AN ERROR OBJECT. Treating that as a
		// successful empty reply would show the operator a blank answer and no
		// reason for it.
		return "", errors.New(parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("the endpoint returned no choices; check the model name")
	}
	return parsed.Choices[0].Message.Content, nil
}

// TestPrompt is what the Test button sends.
//
// ONE TOKEN, and a prompt whose answer nobody reads. The button verifies that
// the endpoint resolves, the key is accepted and the MODEL NAME EXISTS — which
// is the failure operators actually hit, since model names share no vocabulary
// between providers. Asking for more would spend the operator's money to tell
// them nothing extra.
var TestPrompt = []ChatMessage{{Role: "user", Content: "Reply with the single word: ok"}}

// TestEndpoint checks one configuration end to end, returning nil when it works.
func TestEndpoint(ctx context.Context, c Doer, cfg Config) error {
	_, err := Complete(ctx, c, cfg, TestPrompt, 1)
	return err
}
