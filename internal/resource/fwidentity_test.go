package resource

// The firewall identity exists in TWO implementations, and they have to agree.
//
// The server round-trips it to decide "is this still the row I was looking at
// when I clicked", and the browser writes it onto each row as `data-identity`.
// If the two spell it differently, every firewall write is refused as stale —
// or worse, one that should be refused is not.
//
// RouterOS REUSES `*N` ids after a delete, which is why an id alone is not
// enough and this composite exists at all.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestFirewallIdentityMatchesTheBrowsers(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "pages", "firewall.ts"))
	if err != nil {
		t.Fatalf("read firewall.ts: %v", err)
	}
	src := string(b)

	m := regexp.MustCompile(`(?s)export function fwIdentity\([^)]*\): string \{\s*return \[(.*?)\]\.join\('([^']*)'\)`).
		FindStringSubmatch(src)
	if m == nil {
		t.Fatal("fwIdentity is no longer a single `[...].join(...)` — the shapes can no " +
			"longer be compared, which is worse than them differing")
	}

	// The separator, spelled as an ESCAPE in the TypeScript source rather than
	// as a literal control character: a literal is invisible in a diff and is
	// silently lost by any tool that normalises the file.
	if m[2] != `\u0001` {
		t.Errorf("browser separator source is %q, want the six characters \\u0001", m[2])
	}
	if IdentityOfSeparator != "\u0001" {
		t.Errorf("server separator is %q, want U+0001", IdentityOfSeparator)
	}

	// The FIELDS, in order.
	var fields []string
	for _, f := range regexp.MustCompile(`r\.(\w+)`).FindAllStringSubmatch(m[1], -1) {
		fields = append(fields, f[1])
	}
	want := strings.Join(fwIdentity, ",")
	if got := strings.Join(fields, ","); got != want {
		t.Errorf("identity fields differ:\n  browser: %s\n  server:  %s", got, want)
	}
}
