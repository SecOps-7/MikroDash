package server

import "testing"

// The router downloads only an http(s) file into a name MikroDash chose, and
// never one that would run or install once it landed.
func TestAFetchIsOneFileUnderASafeName(t *testing.T) {
	for _, c := range []struct{ url, name string }{
		{"https://example.com/lists/block.txt", "block.txt"},
		{"http://198.51.100.1/a%20b/../cert.pem?token=x", "cert.pem"},
		{"https://example.com/.hidden", "hidden"},
		{"https://example.com/we ird;name.rsc", "we_ird_name.rsc"},
	} {
		if _, name, p := fetchTarget(c.url); p != "" || name != c.name {
			t.Errorf("%s: name %q problem %q, want %q", c.url, name, p, c.name)
		}
	}
	for _, bad := range []string{
		"", "ftp://example.com/f.txt", "file:///etc/passwd", "https:///f.txt",
		"https://user:pw@example.com/f.txt", "https://example.com/",
		"https://example.com/setup.auto.rsc", "https://example.com/SETUP.AUTO.RSC",
		"https://example.com/routeros-7.24-arm64.npk", "https://example.com/f\x01.txt",
	} {
		if _, name, p := fetchTarget(bad); p == "" {
			t.Errorf("%q was accepted as %q", bad, name)
		}
	}
}
