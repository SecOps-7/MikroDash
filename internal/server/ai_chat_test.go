package server

import (
	"errors"
	"strings"
	"testing"

	"mikrodash/internal/aiprovider"
)

// The tool loop, without an endpoint and without a router.
//
// What is worth pinning here is not the HTTP — `internal/aiprovider` already
// asserts on the request it builds. It is the three ways this loop can go wrong
// quietly: an endpoint that ignores tools looking like a failure, a result fed
// back against the wrong call, and a model that keeps asking never stopping.

// callOf records what the loop asked for and replays scripted answers.
type callOf struct {
	sawTools [][]any
	sawMsgs  [][]aiprovider.ChatMessage
	replies  []aiprovider.Reply
	err      error
}

func (c *callOf) call(m []aiprovider.ChatMessage, tl []any) (aiprovider.Reply, error) {
	// COPIED, because the loop appends to the same backing array between calls
	// and a retained slice header would show later state as if it were earlier.
	c.sawMsgs = append(c.sawMsgs, append([]aiprovider.ChatMessage(nil), m...))
	c.sawTools = append(c.sawTools, tl)
	if c.err != nil {
		return aiprovider.Reply{}, c.err
	}
	i := len(c.sawMsgs) - 1
	if i < len(c.replies) {
		return c.replies[i], nil
	}
	return c.replies[len(c.replies)-1], nil
}

func toolCall(id, name string) aiprovider.ToolCall {
	var tc aiprovider.ToolCall
	tc.ID, tc.Type = id, "function"
	tc.Function.Name = name
	return tc
}

// TestAIChatLoopAnswersWhenTheEndpointIgnoresTools.
//
// Many OpenAI-compatible servers accept `tools` and answer in prose anyway. That
// is advisory chat, which is what this page was before tools existed — it must
// not read as an error, and it must not cost a second request.
func TestAIChatLoopAnswersWhenTheEndpointIgnoresTools(t *testing.T) {
	c := &callOf{replies: []aiprovider.Reply{{Text: "The router looks fine.", Finish: "stop"}}}
	ran := 0
	got, err := aiChatLoop(nil, []any{"a-tool"}, c.call, func(aiprovider.ToolCall) string {
		ran++
		return "should not happen"
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "The router looks fine." {
		t.Errorf("got %q", got)
	}
	if ran != 0 {
		t.Errorf("ran %d tools for a reply that asked for none", ran)
	}
	if len(c.sawMsgs) != 1 {
		t.Errorf("made %d requests for a one-shot answer", len(c.sawMsgs))
	}
	if len(c.sawTools[0]) != 1 {
		t.Error("the tools were not advertised on the first request")
	}
}

// TestAIChatLoopFeedsEachResultBackAgainstItsCallID.
//
// A model may ask for several tools at once. A result appended without its id is
// matched by POSITION by the endpoint, so one menu's rows arrive labelled as
// another's — and the model then answers confidently about the wrong table.
func TestAIChatLoopFeedsEachResultBackAgainstItsCallID(t *testing.T) {
	c := &callOf{replies: []aiprovider.Reply{
		{ToolCalls: []aiprovider.ToolCall{toolCall("c1", "list_dnsStatic"), toolCall("c2", "list_fwFilter")}},
		{Text: "done"},
	}}
	got, err := aiChatLoop(nil, nil, c.call, func(tc aiprovider.ToolCall) string {
		return "rows for " + tc.Function.Name
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "done" {
		t.Errorf("got %q", got)
	}
	second := c.sawMsgs[1]
	var assistants, tools int
	byID := map[string]string{}
	for _, m := range second {
		switch m.Role {
		case "assistant":
			assistants++
			if len(m.ToolCalls) != 2 {
				t.Errorf("the assistant turn carried %d calls, so the endpoint cannot match the results", len(m.ToolCalls))
			}
		case "tool":
			tools++
			byID[m.ToolCallID] = m.Content
		}
	}
	if assistants != 1 || tools != 2 {
		t.Fatalf("second request carried %d assistant and %d tool turns", assistants, tools)
	}
	if byID["c1"] != "rows for list_dnsStatic" || byID["c2"] != "rows for list_fwFilter" {
		t.Errorf("results are not keyed by call id: %v", byID)
	}
}

// TestAIChatLoopStopsAtTheCapAndAsksWithNothingToCall.
//
// A model that keeps asking must be stopped, and stopping silently would leave
// the operator a blank page after a long wait. The final request offers no tools,
// which is what forces prose out of what has already been read.
func TestAIChatLoopStopsAtTheCapAndAsksWithNothingToCall(t *testing.T) {
	always := aiprovider.Reply{ToolCalls: []aiprovider.ToolCall{toolCall("c1", "list_dnsStatic")}}
	c := &callOf{replies: []aiprovider.Reply{always}}
	ran := 0
	got, err := aiChatLoop(nil, []any{"a-tool"}, c.call, func(aiprovider.ToolCall) string {
		ran++
		return "rows"
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.sawMsgs) != aiMaxToolIterations+1 {
		t.Fatalf("made %d requests; the cap is %d plus one final ask", len(c.sawMsgs), aiMaxToolIterations)
	}
	if ran != aiMaxToolIterations {
		t.Errorf("ran %d tools across %d rounds", ran, aiMaxToolIterations)
	}
	if c.sawTools[len(c.sawTools)-1] != nil {
		t.Error("the final request still advertised tools, so the model can ask again for ever")
	}
	// The scripted reply carries no text, so the honest sentence is what is left.
	if strings.TrimSpace(got) == "" {
		t.Error("the cap produced a blank answer, which reads as a broken page")
	}
	if !strings.Contains(got, "one thing at a time") {
		t.Errorf("the cap message does not tell the operator what to do: %q", got)
	}
}

// TestAIChatLoopPrefersTheModelsOwnFinalAnswer over the canned sentence.
func TestAIChatLoopPrefersTheModelsOwnFinalAnswer(t *testing.T) {
	loop := aiprovider.Reply{ToolCalls: []aiprovider.ToolCall{toolCall("c1", "list_dnsStatic")}}
	c := &callOf{}
	for i := 0; i < aiMaxToolIterations; i++ {
		c.replies = append(c.replies, loop)
	}
	c.replies = append(c.replies, aiprovider.Reply{Text: "Here is what I found."})
	got, err := aiChatLoop(nil, nil, c.call, func(aiprovider.ToolCall) string { return "rows" })
	if err != nil {
		t.Fatal(err)
	}
	if got != "Here is what I found." {
		t.Errorf("got %q; the model answered and the canned sentence overrode it", got)
	}
}

// TestAIChatLoopReturnsTheTransportError rather than an empty answer. The caller
// sanitises it and shows it; swallowing it gives the operator nothing to act on.
func TestAIChatLoopReturnsTheTransportError(t *testing.T) {
	c := &callOf{err: errors.New("dial tcp 198.51.100.7:443: refused")}
	got, err := aiChatLoop(nil, nil, c.call, func(aiprovider.ToolCall) string { return "" })
	if err == nil {
		t.Fatal("a refused connection returned no error")
	}
	if got != "" {
		t.Errorf("returned text %q alongside an error", got)
	}
}
