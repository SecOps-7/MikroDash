package server

import "testing"

// TestAPlainVersionReadsAsItIs: RouterOS 7.25 dropped the channel label
// ("system - remove the channel label from the RouterOS version"). The CAPsMAN
// version check compares a manager with its CAPs, and on a mixed fleet one side
// reports `7.25.1` while the other still reports `7.24.5 (stable)`.
func TestAPlainVersionReadsAsItIs(t *testing.T) {
	for in, want := range map[string]string{
		"7.25.1":          "7.25.1",
		"7.24.5 (stable)": "7.24.5",
	} {
		if got := bareVersion(in); got != want {
			t.Errorf("bareVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
