package cfgtpl

import "testing"

// TestAPlainVersionReadsAsItIs: RouterOS 7.25 dropped the channel label
// ("system - remove the channel label from the RouterOS version"), and a
// template's target check reads the major.minor out of either form.
func TestAPlainVersionReadsAsItIs(t *testing.T) {
	for in, want := range map[string]string{
		"7.25.1":          "7.25",
		"7.24.5 (stable)": "7.24",
		"7.25":            "7.25",
	} {
		if got := majorMinor(in); got != want {
			t.Errorf("majorMinor(%q) = %q, want %q", in, got, want)
		}
	}
}
