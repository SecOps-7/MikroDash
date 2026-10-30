package session

// AN UNREACHABLE ROUTER IS LOGGED ONCE, THEN HOURLY (2026-10-30).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"
)

const dns = "could not connect to router os: dial tcp: lookup chr-test on 127.0.0.11:53: server misbehaving"

func TestAnOutageIsLoggedOnceThenHourly(t *testing.T) {
	var n dialNoise
	t0 := time.Date(2026, 10, 30, 15, 54, 35, 0, time.UTC)
	var lines []string
	for i := 0; i <= 3*720; i++ { // three hours of attempts, one every 5 s
		if l := n.failed(t0.Add(time.Duration(i)*5*time.Second), dns); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 4 {
		t.Fatalf("three hours of the same failure wrote %d lines, want 4 (the first and one an hour):\n%s",
			len(lines), strings.Join(lines, "\n"))
	}
	if lines[0] != dns {
		t.Errorf("the first line is %q, want the error itself", lines[0])
	}
	if want := "still unreachable after 1h0m0s (721 attempts): " + dns; lines[1] != want {
		t.Errorf("the first reminder is %q, want %q", lines[1], want)
	}
}

func TestADifferentReasonIsLoggedAndAFreshPortIsNot(t *testing.T) {
	var n dialNoise
	t0 := time.Now()
	n.failed(t0, dns)
	if l := n.failed(t0.Add(5*time.Second), "dial tcp 198.51.100.7:8728: connect: connection refused"); l == "" {
		t.Error("a new reason (the name now resolves, the port refuses) was not logged")
	}
	reset := func(port string) string {
		return "read tcp 172.23.0.2:" + port + "->198.51.100.7:8728: read: connection reset by peer"
	}
	if l := n.failed(t0.Add(10*time.Second), reset("41234")); l == "" {
		t.Error("a reset connection was not logged when it first appeared")
	}
	if l := n.failed(t0.Add(15*time.Second), reset("41250")); l != "" {
		t.Errorf("only the local port changed and it was logged again: %q", l)
	}
}

func TestTheRecoverySaysHowLongTheOutageWas(t *testing.T) {
	var n dialNoise
	t0 := time.Now()
	for i := 0; i < 1600; i++ {
		n.failed(t0.Add(time.Duration(i)*5*time.Second), dns)
	}
	if got := n.recovered(t0.Add(8000 * time.Second)); got != "after 2h13m0s unreachable (1600 attempts)" {
		t.Errorf("the recovery reads %q", got)
	}
	// A new outage starts from the beginning: its first failure is logged.
	if l := n.failed(t0.Add(9000*time.Second), dns); l != dns {
		t.Errorf("the next outage's first failure logged %q", l)
	}
	// One failed attempt is already in the log; the reconnect adds nothing.
	if got := n.recovered(t0.Add(9005 * time.Second)); got != "" {
		t.Errorf("a single failed attempt produced %q on recovery", got)
	}
}

// connectLoop must go through dialNoise for both the failure and the reconnect
// line, or the policy above is never reached on a real router.
func TestTheDialLoopUsesTheOutagePolicy(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "session.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "connectLoop" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
					calls[sel.Sel.Name] = true
				}
			}
			return true
		})
	}
	for _, want := range []string{"failed", "recovered"} {
		if !calls[want] {
			t.Errorf("connectLoop does not call dialNoise.%s, so an unreachable router logs every attempt", want)
		}
	}
}
