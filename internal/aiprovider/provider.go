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
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
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
	Headers   string
	TimeoutMs int
	// TLSPin is the SHA-256 fingerprint (64 lowercase hex characters) of the one
	// certificate this endpoint may present, for an endpoint on the operator's
	// own network with a self-signed certificate. Empty means ordinary
	// verification. It replaced a blanket "skip verification" switch (code
	// scanning alert 164): a pin accepts that certificate and refuses any other,
	// so a man in the middle is still refused.
	TLSPin string
	// MaxTokens is the chat's reply budget (aiMaxTokens). See ReplyTokens.
	MaxTokens int
}

// The chat's reply budget: the default and the range the setting accepts.
//
// 8192 BY DEFAULT, because a reasoning model thinks inside this budget: at 1024
// a security question to one spent all of it thinking and wrote nothing
// (2026-09-18). The operator can raise it for a model that thinks longer.
const (
	DefaultReplyTokens = 8192
	MinReplyTokens     = 1024
	MaxReplyTokens     = 65536
)

// ReplyTokens is the configured budget, or DefaultReplyTokens when it is unset
// or out of range — as Timeout treats an unusable timeout.
func (c Config) ReplyTokens() int {
	if c.MaxTokens < MinReplyTokens || c.MaxTokens > MaxReplyTokens {
		return DefaultReplyTokens
	}
	return c.MaxTokens
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
// ── A CLIENT PER CONFIG, NOT A PACKAGE-LEVEL ONE (the transports are shared) ──
//
// `notify` can share one, because its timeout is a constant and it never skips
// verification. Here both are the operator's to set, and a shared client would
// mean the first request's TLS decision silently governing every later one — so
// pinning a lab endpoint's certificate would pin it for a hosted one until the
// process restarted.
//
// REDIRECTS ARE REFUSED, NOT FOLLOWED. Go attaches the request's headers to a
// redirected request, so following one would hand the API key to whatever host
// the response named. That is the operator's credential going somewhere they did
// not configure, decided by the endpoint rather than by them.
func (c Config) Client() *http.Client {
	tr := verifyingTransport
	if c.TLSPin != "" {
		tr = pinnedTransport(c.TLSPin)
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

// ONE TRANSPORT PER TLS CHOICE, shared by every client. A transport per request
// left each round's keep-alive connection and its goroutines idle until the
// endpoint hung up. Separate ones for the reason above: the TLS decision lives
// on the transport, and a lab endpoint's pin must not reach a hosted one.
// Cloned from http.DefaultTransport for its proxy, dial and idle settings.
var verifyingTransport = http.DefaultTransport.(*http.Transport).Clone()

// The pinned transport, for the one pin in use. ONE, not a map: a pin changes
// when the operator trusts a new certificate, and Test Connection can try pins
// that are never saved; a map would keep every one's idle connections for ever.
// A different pin replaces it, and the old one's idle connections are closed.
var (
	pinnedMu  sync.Mutex
	pinnedPin string
	pinnedTr  *http.Transport
)

// pinnedTransport verifies by the PIN instead of the chain.
//
// ── WHY InsecureSkipVerify IS STILL SET ─────────────────────────────────────
//
// It is how Go hands verification to the caller: with it set, VerifyConnection
// runs on every handshake, full and resumed, and is the ONLY check. That check
// refuses any certificate whose SHA-256 is not the pin, so nothing is skipped:
// the chain check is replaced by a stricter one, exactly one certificate. A
// self-signed lab certificate often has no usable SAN, which is why a RootCAs
// pool holding it would not do: Go's hostname check would refuse it anyway.
func pinnedTransport(pin string) *http.Transport {
	pinnedMu.Lock()
	defer pinnedMu.Unlock()
	if pinnedTr != nil && pinnedPin == pin {
		return pinnedTr
	}
	if pinnedTr != nil {
		pinnedTr.CloseIdleConnections()
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // replaced by VerifyConnection's pin check below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the endpoint presented no certificate")
			}
			if Fingerprint(cs.PeerCertificates[0]) != pin {
				return &PinMismatchError{Cert: cs.PeerCertificates[0]}
			}
			return nil
		},
	}
	pinnedPin, pinnedTr = pin, tr
	return tr
}

// Fingerprint is a certificate's SHA-256, as 64 lowercase hex characters: the
// form a pin is stored in.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// PinMismatchError is a pinned endpoint presenting a different certificate.
type PinMismatchError struct{ Cert *x509.Certificate }

func (e *PinMismatchError) Error() string {
	return "the endpoint's certificate is not the one you trusted"
}

// PresentedCertificate is the certificate an endpoint presented when it was
// refused: not trusted by the system, or not the pinned one. nil for any other
// failure. Go reports the refused chain in its verification error, so showing
// it to the operator needs no second, unverified connection.
func PresentedCertificate(err error) *x509.Certificate {
	var ve *tls.CertificateVerificationError
	if errors.As(err, &ve) && len(ve.UnverifiedCertificates) > 0 {
		return ve.UnverifiedCertificates[0]
	}
	var pe *PinMismatchError
	if errors.As(err, &pe) {
		return pe.Cert
	}
	return nil
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
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	// Tools is OMITTED WHEN EMPTY, and that omission is what makes degrading to
	// advisory free rather than a branch: an endpoint that does not speak
	// tool-calling receives exactly the request it received before this existed.
	Tools     []toolSpec `json:"tools,omitempty"`
	MaxTokens int        `json:"max_tokens,omitempty"`
	Stream    bool       `json:"stream"`
}

// toolSpec wraps one callable in the envelope the wire format expects.
type toolSpec struct {
	Type     string `json:"type"`
	Function any    `json:"function"`
}

// toolSpecs wraps each advertised callable. `any` for the function body because
// the catalogue lives in internal/aitools and this package owes it nothing —
// which is also what keeps the wire format here and the tool list there from
// having to agree about a Go type.
func toolSpecs(tools []any) []toolSpec {
	out := make([]toolSpec, 0, len(tools))
	for _, t := range tools {
		out = append(out, toolSpec{Type: "function", Function: t})
	}
	return out
}

// ToolCall is one call the model asked for.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name string `json:"name"`
		// Arguments is a JSON STRING, not an object — the wire format encodes it
		// that way, and a model may emit invalid JSON inside it. The caller
		// parses it and must treat a failure as a refusal rather than a crash.
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ChatMessage is one turn.
//
// ── FOUR ROLES, AND THE TOOL ONE CARRIES AN ID ──────────────────────────────
//
// A `role: "tool"` message answers a specific call and must name it, or an
// endpoint cannot pair the result with the request that asked for it. Both
// tool fields are omitempty so an ordinary turn serialises exactly as before.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// chatReply is the part of the response this app reads.
type chatReply struct {
	Choices []struct {
		Message      ChatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
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
// Reply is one answer: prose, or a request to call tools, never usefully both.
//
// ── WHY A STRUCT RATHER THAN A SECOND RETURN ────────────────────────────────
//
// A model that wants to call something returns `finish_reason: "tool_calls"`
// and, on most endpoints, empty content. Returning `(string, error)` forced the
// caller to read an empty answer as a failure, which is exactly what an endpoint
// that does not speak tool-calling produces for a different reason — and
// conflating the two is how "degrade to advisory" turns into "report a broken
// endpoint".
type Reply struct {
	Text      string
	ToolCalls []ToolCall
	// Finish is the endpoint's own word for why it stopped. Carried rather than
	// interpreted: `length`, `stop` and `tool_calls` all mean something, and a
	// caller that only wanted the text should not have to know which.
	Finish string
}

// Complete performs one non-streaming chat completion.
//
// `tools` may be nil, and when it is, the request carries no `tools` key at all
// — see chatRequest. So an endpoint with no tool support sees the request it saw
// before tools existed, and the caller degrades by simply getting prose back.
func Complete(ctx context.Context, c Doer, cfg Config, msgs []ChatMessage, maxTokens int,
	tools ...any) (Reply, error) {
	req, err := newChatRequest(ctx, cfg, msgs, maxTokens, false, tools)
	if err != nil {
		return Reply{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return Reply{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	return parseReply(resp.StatusCode, raw)
}

// Stream performs one chat completion with `stream: true`, handing each piece of
// prose to `onText` as it arrives, and returns the whole reply as `Complete`
// would.
//
// ── WHY, AND WHAT IT CHANGES ────────────────────────────────────────────────
//
// The chat page used to wait for the entire answer and show it in one go, so a
// long answer read as a page that had stopped. Streaming changes when the text
// is SEEN and nothing about what it IS: the returned Reply is assembled from the
// same deltas and is what the caller acts on, saves and replays.
//
// ── AN ENDPOINT THAT DOES NOT STREAM STILL WORKS ────────────────────────────
//
// Some OpenAI-compatible servers ignore `stream` and answer with ordinary JSON.
// That is read by Content-Type rather than assumed, and parsed exactly as
// `Complete` parses it, with `onText` simply never called. So turning streaming
// on cannot break an endpoint that worked before.
//
// ── TOOL CALLS ARRIVE IN PIECES ─────────────────────────────────────────────
//
// A streamed tool call comes as fragments keyed by `index`: the id and name in
// one delta, the JSON arguments split across many. They are joined per index and
// returned in index order, so the caller sees the same ToolCalls a
// non-streaming reply would have carried.
func Stream(ctx context.Context, c Doer, cfg Config, msgs []ChatMessage, maxTokens int,
	onText func(string), tools ...any) (Reply, error) {
	req, err := newChatRequest(ctx, cfg, msgs, maxTokens, true, tools)
	if err != nil {
		return Reply{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return Reply{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body := io.LimitReader(resp.Body, bodyLimit)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 ||
		!strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		raw, _ := io.ReadAll(body)
		return parseReply(resp.StatusCode, raw)
	}

	var (
		text   strings.Builder
		finish string
		calls  = map[int]*ToolCall{}
		order  []int
	)
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), bodyLimit)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue // blank separators, `event:` and `:` comment lines
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue // a keep-alive or a line this app does not read
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return Reply{}, errors.New(chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.Delta.Content != "" {
			text.WriteString(ch.Delta.Content)
			if onText != nil {
				onText(ch.Delta.Content)
			}
		}
		for _, tc := range ch.Delta.ToolCalls {
			call, ok := calls[tc.Index]
			if !ok {
				call = &ToolCall{Type: "function"}
				calls[tc.Index] = call
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				call.ID = tc.ID
			}
			if tc.Type != "" {
				call.Type = tc.Type
			}
			call.Function.Name += tc.Function.Name
			call.Function.Arguments += tc.Function.Arguments
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			finish = *ch.FinishReason
		}
	}
	if err := sc.Err(); err != nil {
		return Reply{}, err
	}

	sort.Ints(order)
	out := Reply{Text: text.String(), Finish: finish}
	for _, i := range order {
		out.ToolCalls = append(out.ToolCalls, *calls[i])
	}
	if out.Text == "" && len(out.ToolCalls) == 0 && finish == "" {
		// A stream that closed having said nothing at all. Reported, for the
		// reason `parseReply` refuses a reply with no choices: a blank answer
		// with no explanation is the worst thing to show an operator.
		return Reply{}, errors.New("the endpoint's stream ended without a reply; check the model name")
	}
	return out, nil
}

// streamChunk is one `data:` event of a streamed reply.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// newChatRequest builds the POST both calls send. `stream` is the only
// difference, plus the Accept header that says a stream is welcome.
func newChatRequest(ctx context.Context, cfg Config, msgs []ChatMessage, maxTokens int,
	stream bool, tools []any) (*http.Request, error) {
	u, err := endpoint(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("no model is configured")
	}
	extra, err := ParseHeaders(cfg.Headers)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(chatRequest{
		Model: cfg.Model, Messages: msgs, MaxTokens: maxTokens, Stream: stream,
		Tools: toolSpecs(tools),
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream, application/json")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	// OMITTED WHEN EMPTY, not sent blank. A local endpoint that needs no key
	// will reject `Authorization: Bearer ` from some proxies, and an empty
	// credential header is not the same as no credential.
	if k := strings.TrimSpace(cfg.APIKey); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	return req, nil
}

// parseReply reads a whole JSON reply, or the error it carries.
func parseReply(status int, raw []byte) (Reply, error) {
	var parsed chatReply
	_ = json.Unmarshal(raw, &parsed)

	if status < 200 || status >= 300 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return Reply{}, fmt.Errorf("HTTP %d: %s", status, parsed.Error.Message)
		}
		return Reply{}, fmt.Errorf("HTTP %d", status)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		// SOME SERVERS ANSWER 200 WITH AN ERROR OBJECT. Treating that as a
		// successful empty reply would show the operator a blank answer and no
		// reason for it.
		return Reply{}, errors.New(parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return Reply{}, errors.New("the endpoint returned no choices; check the model name")
	}
	ch := parsed.Choices[0]
	// EMPTY CONTENT IS NOT AN ERROR when the model asked to call something. It
	// is the normal shape of a tool-calling turn.
	return Reply{
		Text:      ch.Message.Content,
		ToolCalls: ch.Message.ToolCalls,
		Finish:    ch.FinishReason,
	}, nil
}

// TestPrompt is what the Test button sends. The answer is never read; only
// whether one arrived.
var TestPrompt = []ChatMessage{{Role: "user", Content: "Reply with the single word: ok"}}

// testTokens is the budget for that one exchange.
//
// ── IT WAS 1, AND THAT BROKE THE BUTTON AGAINST REAL GATEWAYS ──────────────
//
// One token is enough for a small local model to say "ok", and asking for more
// looked like spending the operator's money to learn nothing extra. It is not
// enough for a reasoning-capable model behind a proxy: the model spends the
// single token on internal output, returns a completion with no usable content,
// and the gateway answers HTTP 502 `upstream_empty_response`.
//
// Measured against a live OpenAI-compatible gateway on 2026-09-16: the identical
// request failed at `max_tokens: 1` and returned "ok" at 64. So the button
// reported a broken endpoint for a configuration that works, and the error named
// the operator's provider rather than this request — the worst shape of wrong,
// because it sends them to debug something that is fine.
//
// Sixteen is still a rounding error in cost and leaves room for a model that
// thinks before it speaks.
const testTokens = 16

// TestEndpoint checks one configuration end to end, returning nil when it works.
func TestEndpoint(ctx context.Context, c Doer, cfg Config) error {
	// NO TOOLS ON THE TEST. The button answers "can this endpoint hold a
	// conversation", and advertising a catalogue would make it also answer "does
	// it support tool-calling" — a different question, whose failure would be
	// reported as a broken endpoint.
	_, err := Complete(ctx, c, cfg, TestPrompt, testTokens)
	return err
}
