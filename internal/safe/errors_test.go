package safe

// The corpus comes from RUNNING the live sanitizeErr, so this pins the port
// against the implementation rather than against my reading of four regexes.
//
// Redaction is the kind of function where both failure directions hurt: too
// little and the router's address reaches a browser, too much and the operator
// is left with a message that says nothing. The corpus carries near-misses for
// exactly that reason — a version string is not an address, and a single `/` is
// not a path.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

type sanCase struct {
	Input string `json:"input"`
	Want  string `json:"want"`
}

func loadSanCases(t *testing.T) []sanCase {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "sanitize-cases.json"))
	if err != nil {
		t.Fatalf("no cases — run: node tools/sanitize-cases.js: %v", err)
	}
	var corpus struct {
		Cases []sanCase `json:"cases"`
	}
	if err := json.Unmarshal(body, &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Cases) < 20 {
		t.Fatalf("only %d cases — the corpus shrank", len(corpus.Cases))
	}
	return corpus.Cases
}

func TestMessageMatchesTheLiveImplementation(t *testing.T) {
	for _, c := range loadSanCases(t) {
		if got := Message(c.Input); got != c.Want {
			t.Errorf("Message(%q)\n got %q\nwant %q", c.Input, got, c.Want)
		}
	}
}

// TestNoAddressSurvivesSanitising is the property the whole thing exists for,
// asserted independently of the corpus: whatever the substitutions do, a dotted
// quad must not come out the other side. Stated separately because a corpus can
// only fail on cases somebody thought of.
func TestNoAddressSurvivesSanitising(t *testing.T) {
	for _, msg := range []string{
		"dial tcp 10.0.0.2:8729: connect: connection refused",
		"read tcp 172.16.5.4:51234->192.168.88.1:8728: read: connection reset by peer",
		"x509: certificate is valid for 10.0.0.2, not router.local",
		"lookup 198.51.100.7 on 8.8.8.8:53: no such host",
	} {
		got := Message(msg)
		for _, part := range strings.Fields(got) {
			if strings.Count(part, ".") >= 3 && strings.ContainsAny(part, "0123456789") &&
				!strings.Contains(part, "[") {
				t.Errorf("Message(%q) = %q — %q still looks like an address", msg, got, part)
			}
		}
	}
}

// TestTheLimitIsInUtf16CodeUnits. A byte or rune count would cut a string of
// astral characters at a different place, and the original counts UTF-16.
func TestTheLimitIsInUtf16CodeUnits(t *testing.T) {
	// Each emoji is ONE rune, FOUR bytes and TWO UTF-16 code units.
	in := strings.Repeat("\U0001F600", 120)
	got := Message(in)
	if n := len(utf16.Encode([]rune(got))); n != 200 {
		t.Errorf("result is %d UTF-16 units, want exactly 200", n)
	}
	if len([]rune(got)) != 100 {
		t.Errorf("result is %d runes; 200 UTF-16 units of astral characters is 100 runes",
			len([]rune(got)))
	}
}

// TestRedactionCanLengthenPastTheLimit — `[addr]` is longer than some addresses,
// so the cut happens AFTER substitution and a message that fitted before may not
// after. The corpus carries the case; this names why it is there.
func TestTruncationHappensAfterSubstitution(t *testing.T) {
	in := strings.Repeat("1.2.3.4 ", 40) // 320 chars in, each address 7 chars
	got := Message(in)
	if n := len(utf16.Encode([]rune(got))); n != 200 {
		t.Fatalf("result is %d units, want 200", n)
	}
	if strings.Contains(got, "1.2.3.4") {
		t.Error("an address survived; substitution must run before the cut")
	}
}

// TestAnEmptyMessageStaysEmpty — the original answers null for a null error and
// this has no nullable string. The only caller passes err.Error().
func TestAnEmptyMessageStaysEmpty(t *testing.T) {
	if got := Message(""); got != "" {
		t.Errorf("Message(\"\") = %q", got)
	}
}

// TestTheStoredErrorIsSanitised covers the APPLICATION, not the function.
//
// Every test above drives `sanitizeErr` directly, so all of them pass with the
// call site reverted to `err.Error()` — which is exactly what happened while
// this file was being written: a mutation run timed out, its restore never ran,
// and the tree kept the raw assignment with a comment above it claiming
// otherwise. A green suite said nothing, because nothing reached the field.
//
// The source is checked rather than the behaviour because reaching `lastErr`
// needs a dial to a router that is not there, and a test that waits out a retry
// loop is one nobody will keep. That is a weaker check and worth naming as one:
// it proves the call site still sanitises, not that the value is unreachable by
// any other route.
func TestTheStoredErrorIsSanitised(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "session", "session.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	if !strings.Contains(src, "s.lastErr = safe.Message(err.Error())") {
		t.Error("session.go no longer sanitises the error it stores in lastErr; " +
			"announce() puts that field in router:status as `reason`, and the shell " +
			"renders it as the banner text")
	}
	if strings.Contains(src, "s.lastErr = err.Error()") {
		t.Error("session.go stores a RAW error message in lastErr")
	}
}

// The hostname rule, and the ORDER it runs in.
//
// Upstream `51aac86`, reported from this port. `sanitizeErr` had no direct
// coverage on either side — one incidental scan asserting a route calls it — and
// it is the last thing between a driver error and the browser.
func TestAHostnameIsRedacted(t *testing.T) {
	for _, c := range []struct{ why, in, want string }{
		// THE CASE THAT SURVIVED: a bare name in a resolver error. No leading
		// slash, so the path rule never saw it; not an address, so the IPv4 rule
		// never saw it.
		{"a resolver error names the host",
			"getaddrinfo ENOTFOUND build-server.internal",
			"getaddrinfo ENOTFOUND [host]"},
		{"no such host", "dial tcp: lookup ntfy.example.org: no such host",
			"dial tcp: lookup [host]: no such host"},
		// ── THE ORDERING, ASSERTED RATHER THAN DESCRIBED ──────────────────
		//
		// Run before the email rule this yields `ops@[host]`, which still names
		// the domain. The whole address must go.
		{"an address is redacted WHOLE, not half", "smtp auth failed for ops@corp.example.com",
			"smtp auth failed for [email]"},
		{"a subdomain chain", "connect to a.b.c.example.co.uk failed",
			"connect to [host] failed"},
	} {
		t.Run(c.why, func(t *testing.T) {
			if got := Message(c.in); got != c.want {
				t.Errorf("Message(%q)\n  got  %q\n  want %q", c.in, got, c.want)
			}
		})
	}
}

// A bare word with no dot is not a hostname and must survive, or every error
// message becomes unreadable.
func TestOrdinaryWordsAreNotRedactedAsHosts(t *testing.T) {
	for _, in := range []string{
		"connection refused",
		"invalid user name or password",
		"cannot log in",
	} {
		if got := Message(in); got != in {
			t.Errorf("Message(%q) = %q — an ordinary message was mangled", in, got)
		}
	}
}
