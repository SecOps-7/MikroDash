package aiprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// stub captures the request and answers with canned bytes.
//
// THE TESTS ASSERT ON THE REQUEST, NOT THE REPLY. A model's answer is
// non-deterministic and asserting on one would pin nothing; what has to be true
// every time is where the request went, what it carried, and what it did not.
type stub struct {
	got         *http.Request
	body        []byte
	status      int
	err         error
	contentType string
}

func (s *stub) Do(r *http.Request) (*http.Response, error) {
	s.got = r
	if s.err != nil {
		return nil, s.err
	}
	st := s.status
	if st == 0 {
		st = 200
	}
	b := s.body
	if b == nil {
		b = []byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}
	ct := s.contentType
	if ct == "" {
		ct = "application/json"
	}
	return &http.Response{
		StatusCode: st,
		Body:       io.NopCloser(bytes.NewReader(b)),
		Header:     http.Header{"Content-Type": []string{ct}},
	}, nil
}

func cfg() Config {
	return Config{BaseURL: "http://198.51.100.10:11434/v1", Model: "a-model"}
}

// TestTheRequestGoesToTheConfiguredEndpoint.
//
// The acceptance criterion this feature was asked for by name: no provider
// hostname anywhere. A default that crept in would be invisible on a machine
// where the operator had configured one anyway, so the check is that the URL is
// built from the setting and nothing else.
func TestTheRequestGoesToTheConfiguredEndpoint(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"http://198.51.100.10:11434/v1", "http://198.51.100.10:11434/v1/chat/completions"},
		{"https://gateway.example.invalid/v1", "https://gateway.example.invalid/v1/chat/completions"},
		// A TRAILING SLASH IS THE SAME ENDPOINT. Refusing it would be a
		// configuration error nobody can see on the page.
		{"http://198.51.100.10:11434/v1/", "http://198.51.100.10:11434/v1/chat/completions"},
		// A base with no path at all is legitimate for some gateways.
		{"http://198.51.100.10:8080", "http://198.51.100.10:8080/chat/completions"},
		// Query and fragment are dropped rather than carried into the path.
		{"http://198.51.100.10:11434/v1?x=1#y", "http://198.51.100.10:11434/v1/chat/completions"},
	} {
		s := &stub{}
		c := cfg()
		c.BaseURL = tc.base
		if _, err := Complete(context.Background(), s, c, TestPrompt, 1); err != nil {
			t.Fatalf("%s: %v", tc.base, err)
		}
		if got := s.got.URL.String(); got != tc.want {
			t.Errorf("%s -> %s, want %s", tc.base, got, tc.want)
		}
		if s.got.Method != http.MethodPost {
			t.Errorf("%s: method %s, want POST", tc.base, s.got.Method)
		}
	}
}

// TestAnEmptyEndpointRefusesRatherThanFallingBack — the same criterion from the
// other side. A built-in default reached only when the setting is empty would
// pass every case above.
func TestAnEmptyEndpointRefusesRatherThanFallingBack(t *testing.T) {
	for _, base := range []string{"", "   "} {
		s := &stub{}
		c := cfg()
		c.BaseURL = base
		if _, err := Complete(context.Background(), s, c, TestPrompt, 1); err == nil {
			t.Errorf("an empty endpoint was accepted; it must refuse, never fall back")
		}
		if s.got != nil {
			t.Errorf("an empty endpoint still produced a request to %s", s.got.URL)
		}
	}
}

// TestTheEndpointIsRefusedWhenItCannotBeMadeSafe.
//
// Private addresses are deliberately allowed — the local model case is the whole
// point — so the boundary is the SHAPE of the URL, not where it points.
func TestTheEndpointIsRefusedWhenItCannotBeMadeSafe(t *testing.T) {
	for _, tc := range []struct{ base, why string }{
		{"file:///etc/passwd", "a non-HTTP scheme"},
		{"unix:///var/run/x.sock", "a non-HTTP scheme"},
		{"ftp://198.51.100.10/v1", "a non-HTTP scheme"},
		{"/v1", "no host"},
		{"https://user:pass@198.51.100.10/v1", "credentials in the URL"},
	} {
		s := &stub{}
		c := cfg()
		c.BaseURL = tc.base
		if _, err := Complete(context.Background(), s, c, TestPrompt, 1); err == nil {
			t.Errorf("%s (%s) was accepted", tc.base, tc.why)
		}
		if s.got != nil {
			t.Errorf("%s (%s) still produced a request", tc.base, tc.why)
		}
	}
}

// TestTheKeyIsSentOnlyWhenThereIsOne.
//
// An empty `Authorization: Bearer ` is not the same as no header: some proxies
// reject it, and a local endpoint needs none at all. This is the difference
// between "works out of the box against Ollama" and "works only with a dummy
// key", which is the kind of thing reported as the feature being broken.
func TestTheKeyIsSentOnlyWhenThereIsOne(t *testing.T) {
	s := &stub{}
	c := cfg()
	if _, err := Complete(context.Background(), s, c, TestPrompt, 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.got.Header["Authorization"]; ok {
		t.Errorf("an Authorization header was sent with no key configured: %q",
			s.got.Header.Get("Authorization"))
	}

	s = &stub{}
	c.APIKey = "  NOT-A-REAL-KEY  "
	if _, err := Complete(context.Background(), s, c, TestPrompt, 1); err != nil {
		t.Fatal(err)
	}
	// TRIMMED on the way out. A pasted key picks up whitespace, and a header
	// value is not the place to preserve it.
	if got := s.got.Header.Get("Authorization"); got != "Bearer NOT-A-REAL-KEY" {
		t.Errorf("Authorization = %q", got)
	}
}

// TestTheRequestBodyNamesTheModel — the model name is the failure operators
// actually hit, because the names share no vocabulary between providers.
func TestTheRequestBodyNamesTheModel(t *testing.T) {
	s := &stub{}
	c := cfg()
	if _, err := Complete(context.Background(), s, c, TestPrompt, 7); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(s.got.Body)
	body := string(b)
	for _, want := range []string{`"model":"a-model"`, `"max_tokens":7`, `"stream":false`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s does not contain %s", body, want)
		}
	}

	// AND A MISSING MODEL REFUSES rather than letting the endpoint guess.
	s = &stub{}
	c.Model = "   "
	if _, err := Complete(context.Background(), s, c, TestPrompt, 1); err == nil {
		t.Error("an empty model was accepted")
	} else if s.got != nil {
		t.Error("an empty model still produced a request")
	}
}

// TestCustomHeadersReachTheRequestAndReservedOnesDoNot.
func TestCustomHeadersReachTheRequestAndReservedOnesDoNot(t *testing.T) {
	s := &stub{}
	c := cfg()
	c.APIKey = "NOT-A-REAL-KEY"
	c.Headers = "X-Tenant: acme\n\n  X-Trace : on  \n"
	if _, err := Complete(context.Background(), s, c, TestPrompt, 1); err != nil {
		t.Fatal(err)
	}
	if got := s.got.Header.Get("X-Tenant"); got != "acme" {
		t.Errorf("X-Tenant = %q", got)
	}
	if got := s.got.Header.Get("X-Trace"); got != "on" {
		t.Errorf("X-Trace = %q — the name and value should be trimmed", got)
	}
	// The key survives the custom-header block. Setting the custom headers last
	// would let one of them replace it silently.
	if got := s.got.Header.Get("Authorization"); got != "Bearer NOT-A-REAL-KEY" {
		t.Errorf("Authorization = %q — a custom header overwrote the key", got)
	}

	for _, bad := range []string{"Authorization: Bearer x", "content-type: text/plain", "no-colon-here"} {
		if _, err := ParseHeaders(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// TestAProviderErrorIsReportedInItsOwnWords.
//
// "HTTP 401" tells an operator nothing they can act on. The provider's own
// message is what turns a failed Test button into a fixable configuration.
func TestAProviderErrorIsReportedInItsOwnWords(t *testing.T) {
	s := &stub{status: 401, body: []byte(`{"error":{"message":"Incorrect API key provided"}}`)}
	_, err := Complete(context.Background(), s, cfg(), TestPrompt, 1)
	if err == nil || !strings.Contains(err.Error(), "Incorrect API key provided") {
		t.Errorf("err = %v, want the provider's own message", err)
	}

	// SOME SERVERS ANSWER 200 WITH AN ERROR OBJECT. Treating that as success
	// would show a blank answer and no reason for it.
	s = &stub{status: 200, body: []byte(`{"error":{"message":"model not found"}}`)}
	if _, err := Complete(context.Background(), s, cfg(), TestPrompt, 1); err == nil {
		t.Error("a 200 carrying an error object was read as success")
	}

	// AND NO CHOICES IS NOT AN EMPTY ANSWER.
	s = &stub{status: 200, body: []byte(`{"choices":[]}`)}
	if _, err := Complete(context.Background(), s, cfg(), TestPrompt, 1); err == nil {
		t.Error("an empty choices array was read as success")
	}
}

// TestTheTimeoutIsBoundedAndFallsBackWhenUnusable — a zero or absurd setting
// must not mean "no timeout", which would hold a request open for ever against
// an endpoint that never answers.
func TestTheTimeoutIsBoundedAndFallsBackWhenUnusable(t *testing.T) {
	for _, tc := range []struct {
		ms   int
		want time.Duration
	}{
		{0, DefaultTimeout},
		{-1, DefaultTimeout},
		{999, DefaultTimeout},
		{600001, DefaultTimeout},
		{1000, time.Second},
		{45000, 45 * time.Second},
	} {
		if got := (Config{TimeoutMs: tc.ms}).Timeout(); got != tc.want {
			t.Errorf("TimeoutMs %d -> %v, want %v", tc.ms, got, tc.want)
		}
	}
	if c := (Config{TimeoutMs: 45000}).Client(); c.Timeout != 45*time.Second {
		t.Errorf("the client's timeout is %v", c.Timeout)
	}
}

// TestARedirectIsRefused.
//
// Go re-attaches the request's headers to a redirected request, so following one
// would send the API key to whatever host the response named — the operator's
// credential going somewhere they did not configure, chosen by the endpoint.
func TestARedirectIsRefused(t *testing.T) {
	c := (Config{}).Client()
	if c.CheckRedirect == nil {
		t.Fatal("no CheckRedirect — a redirect would carry the key onward")
	}
	if err := c.CheckRedirect(&http.Request{}, nil); err == nil {
		t.Error("a redirect was permitted")
	}
}

// TestVerificationIsOnUnlessTheOperatorTurnedItOff.
func TestVerificationIsOnUnlessTheOperatorTurnedItOff(t *testing.T) {
	tr, ok := (Config{}).Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("unexpected transport")
	}
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("verification is off by default")
	}

	tr, _ = (Config{TLSInsecure: true}).Client().Transport.(*http.Transport)
	if tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("the explicit opt-in did not take effect")
	}
}

// TestTheTestButtonAsksForEnoughTokensToGetAnAnswer.
//
// ── FOUND LIVE, NOT BY THIS SUITE ──────────────────────────────────────────
//
// `TestEndpoint` asked for ONE token. That is enough for a small local model to
// say "ok", and NOT enough for a reasoning-capable model behind a gateway: the
// model spends the single token on internal output, the completion comes back
// with no usable content, and the gateway answers HTTP 502
// `upstream_empty_response`.
//
// Measured against a live OpenAI-compatible gateway on 2026-09-16 — the
// identical request failed at `max_tokens: 1` and returned "ok" at 64. So the
// Test button reported a broken endpoint for a configuration that worked, and
// the error named the operator's provider rather than this request, which sends
// them to debug something that is fine.
//
// The stub cannot reproduce that (it answers whatever it is told to), so this
// pins the REQUEST instead: the one property that would have prevented it.
func TestTheTestButtonAsksForEnoughTokensToGetAnAnswer(t *testing.T) {
	s := &stub{}
	if err := TestEndpoint(context.Background(), s, cfg()); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(s.got.Body)
	var req struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(b, &req); err != nil {
		t.Fatalf("the test request is not JSON: %v", err)
	}
	if req.MaxTokens < 8 {
		t.Errorf("the Test button asks for max_tokens=%d — a reasoning model spends that "+
			"on internal output and the gateway returns an empty completion, so the button "+
			"calls a working endpoint broken", req.MaxTokens)
	}
}

// TestAStreamIsDeliveredPieceByPieceAndAssembled.
//
// The deltas reach `onText` in order as they arrive, and the returned Reply is
// the same text joined: what the page shows while streaming and what is saved
// and replayed afterwards cannot disagree.
func TestAStreamIsDeliveredPieceByPieceAndAssembled(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		": keep-alive\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\", router\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	s := &stub{body: []byte(sse), contentType: "text/event-stream; charset=utf-8"}
	var pieces []string
	r, err := Stream(context.Background(), s, cfg(), TestPrompt, 64, func(p string) { pieces = append(pieces, p) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pieces, "|") != "Hello|, router" {
		t.Errorf("onText received %q", pieces)
	}
	if r.Text != "Hello, router" || r.Finish != "stop" {
		t.Errorf("assembled reply %+v", r)
	}
	var sent map[string]any
	b, _ := io.ReadAll(s.got.Body)
	_ = json.Unmarshal(b, &sent)
	if sent["stream"] != true {
		t.Errorf("the request asked for stream=%v", sent["stream"])
	}
}

// TestStreamedToolCallsAreJoinedByIndex. Arguments arrive split, and two calls
// interleave; each must come back whole, in index order.
func TestStreamedToolCallsAreJoinedByIndex(t *testing.T) {
	sse := `data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","type":"function","function":{"name":"list_dns","arguments":""}}]}}]}` + "\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"change_row","arguments":"{\"resource\":"}}]}}]}` + "\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"dnsStatic\"}"}}]}}]}` + "\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n" +
		"data: [DONE]\n"
	s := &stub{body: []byte(sse), contentType: "text/event-stream"}
	called := false
	r, err := Stream(context.Background(), s, cfg(), TestPrompt, 64, func(string) { called = true })
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("onText was called for a turn that carried no prose")
	}
	if len(r.ToolCalls) != 2 || r.ToolCalls[0].ID != "a" || r.ToolCalls[1].ID != "b" {
		t.Fatalf("tool calls %+v", r.ToolCalls)
	}
	if got := r.ToolCalls[0].Function.Arguments; got != `{"resource":"dnsStatic"}` {
		t.Errorf("arguments were not joined: %q", got)
	}
	if r.Finish != "tool_calls" {
		t.Errorf("finish %q", r.Finish)
	}
}

// TestAnEndpointThatIgnoresStreamStillAnswers. The control for the two above: a
// plain JSON reply to a streaming request is read as a whole reply.
func TestAnEndpointThatIgnoresStreamStillAnswers(t *testing.T) {
	s := &stub{} // JSON body, application/json
	called := false
	r, err := Stream(context.Background(), s, cfg(), TestPrompt, 64, func(string) { called = true })
	if err != nil || r.Text != "ok" {
		t.Fatalf("reply %+v err %v", r, err)
	}
	if called {
		t.Error("onText was called for a non-streamed reply")
	}
	// And an error status is still the provider's own words.
	s = &stub{status: 401, body: []byte(`{"error":{"message":"bad key"}}`)}
	if _, err := Stream(context.Background(), s, cfg(), TestPrompt, 64, nil); err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Errorf("an HTTP 401 produced %v", err)
	}
}
