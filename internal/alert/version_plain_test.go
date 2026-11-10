package alert

import "testing"

// TestAPlainVersionReadsAsItIs: RouterOS 7.25 dropped the channel label
// ("system - remove the channel label from the RouterOS version"), so the
// update alert's text sees `7.25.1` where it used to see `7.24.5 (stable)`.
func TestAPlainVersionReadsAsItIs(t *testing.T) {
	for in, want := range map[string]string{
		"7.25.1":          "7.25.1",
		"7.24.5 (stable)": "7.24.5",
		"":                "unknown",
	} {
		if got := cleanVersion(in); got != want {
			t.Errorf("cleanVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
