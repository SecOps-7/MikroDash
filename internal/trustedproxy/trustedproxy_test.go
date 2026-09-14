package trustedproxy

import "testing"

func mustParse(t *testing.T, s string) []string {
	t.Helper()
	p, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	out := make([]string, len(p))
	for i := range p {
		out[i] = p[i].String()
	}
	return out
}

func TestParseReadsAddressesAndRanges(t *testing.T) {
	got := mustParse(t, "10.0.0.0/8, 172.18.0.5\n2001:db8::/32 ::ffff:192.0.2.9")
	want := []string{"10.0.0.0/8", "172.18.0.5/32", "2001:db8::/32", "192.0.2.9/32"}
	if len(got) != len(want) {
		t.Fatalf("Parse = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %s, want %s", i, got[i], want[i])
		}
	}
	if got := mustParse(t, "  "); len(got) != 0 {
		t.Errorf("an empty list parsed as %v", got)
	}
}

// TRUSTING EVERY ADDRESS IS REFUSED. It is the forgeable behaviour this package
// replaces, and one line of configuration must not restore it.
func TestParseRefusesTrustingEveryone(t *testing.T) {
	for _, s := range []string{"0.0.0.0/0", "::/0", "10.0.0.1, 0.0.0.0/0", "not-an-ip", "10.0.0.0/33"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) was accepted", s)
		}
	}
}

func TestClient(t *testing.T) {
	trusted, _ := Parse("10.0.0.0/8, 172.18.0.2")
	for _, tc := range []struct {
		name, remote string
		xff          []string
		want         string
	}{
		{"an untrusted peer is the client", "198.51.100.7:51234", nil, "198.51.100.7"},
		{"an untrusted peer cannot forward", "198.51.100.7:51234", []string{"203.0.113.9"}, "198.51.100.7"},
		{"a trusted peer forwards its client", "172.18.0.2:4000", []string{"203.0.113.9"}, "203.0.113.9"},
		{"a forged leftmost entry is not the client", "172.18.0.2:4000",
			[]string{"6.6.6.6, 203.0.113.9"}, "203.0.113.9"},
		{"trusted hops are walked past", "10.0.0.1:4000",
			[]string{"203.0.113.9, 10.0.0.7"}, "203.0.113.9"},
		{"several headers are one list", "10.0.0.1:4000",
			[]string{"6.6.6.6", "203.0.113.9"}, "203.0.113.9"},
		{"a malformed hop stops at the last trusted address", "10.0.0.1:4000",
			[]string{"203.0.113.9, junk"}, "10.0.0.1"},
		{"a trusted peer with no header is itself", "10.0.0.1:4000", nil, "10.0.0.1"},
		{"ipv4-mapped peers are normalised", "[::ffff:198.51.100.7]:51234", nil, "198.51.100.7"},
		{"a peer without a port", "198.51.100.7", nil, "198.51.100.7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Client(tc.remote, tc.xff, trusted); got != tc.want {
				t.Errorf("Client = %q, want %q", got, tc.want)
			}
		})
	}
	if got := Client("172.18.0.2:4000", []string{"203.0.113.9"}, nil); got != "172.18.0.2" {
		t.Errorf("with nothing trusted, a forwarded address was believed: %q", got)
	}
}
