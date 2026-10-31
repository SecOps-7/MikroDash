package server

// THE AGENT OVERVIEW CARD FILLS, OR SAYS WHY NOT (2026-10-31).
//
// A model that reasons before it answers spent the card's 120-token budget
// thinking and returned `finish_reason: "length"` with empty content; the card
// cached that blank line for three hours and logged nothing.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/aiprovider"
)

func TestABlankAnswerIsAFailureNotALine(t *testing.T) {
	for _, c := range []struct {
		why    string
		reply  aiprovider.Reply
		inFail string
	}{
		{"the budget spent thinking", aiprovider.Reply{Text: "", Finish: "length"}, "whole token budget"},
		{"nothing at all", aiprovider.Reply{Text: "", Finish: "stop"}, "returned no text"},
		{"only whitespace", aiprovider.Reply{Text: " \n\t ", Finish: "stop"}, "returned no text"},
	} {
		line, fail := overviewReply(c.reply)
		if line != "" || !strings.Contains(fail, c.inFail) {
			t.Errorf("%s: line %q, fail %q; want no line and a failure naming %q", c.why, line, fail, c.inFail)
		}
	}
}

func TestTheLineIsOneBoundedLine(t *testing.T) {
	line, fail := overviewReply(aiprovider.Reply{Text: "  Routing normally, WAN up  \nSecond paragraph", Finish: "stop"})
	if fail != "" || line != "Routing normally, WAN up" {
		t.Errorf("got line %q fail %q, want the first line, trimmed", line, fail)
	}
	long, _ := overviewReply(aiprovider.Reply{Text: strings.Repeat("é", 500), Finish: "stop"})
	if n := len([]rune(long)); n != overviewLineMax+1 || !strings.HasSuffix(long, "…") {
		t.Errorf("a 500-character answer became %d characters, want %d and an ellipsis", n, overviewLineMax+1)
	}
}

func TestAFailureIsRetriedSoon(t *testing.T) {
	if got := overviewRetry(3 * time.Hour); got != overviewRetryAfter {
		t.Errorf("after a failure the card waits %v, want %v rather than the 3 h interval", got, overviewRetryAfter)
	}
	if got := overviewRetry(2 * time.Minute); got != 2*time.Minute {
		t.Errorf("an interval shorter than the retry gave %v", got)
	}
}

// overviewTick asks with the chat's budget and goes through overviewReply, or the
// helpers above are not what the card does.
func TestTheCardAsksWithTheChatsBudgetAndChecksTheAnswer(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "ai_overview.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	budget, checked, retried := false, false, false
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "overviewTick" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				if fun.Sel.Name == "Complete" && len(call.Args) >= 5 {
					if c, ok := call.Args[4].(*ast.CallExpr); ok {
						if s, ok := c.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "ReplyTokens" {
							budget = true
						}
					}
				}
			case *ast.Ident:
				switch fun.Name {
				case "overviewReply":
					checked = true
				case "overviewRetry":
					retried = true
				}
			}
			return true
		})
	}
	if !budget {
		t.Error("overviewTick does not ask with cfg.ReplyTokens(), so a model that reasons first spends the budget thinking")
	}
	if !checked {
		t.Error("overviewTick does not pass the answer through overviewReply, so a blank answer is cached as a line")
	}
	if !retried {
		t.Error("overviewTick does not retry a failure soon, so the card stays empty for the whole interval")
	}
}
