package backups

import "testing"

// TestAPlainVersionReadsAsItIs: RouterOS 7.25 dropped the channel label
// ("system - remove the channel label from the RouterOS version"). ShortVersion
// is what a backup records and what the restore guard compares (the restore's
// live read in internal/server uses it too), so a 7.25.1 backup must match its
// own 7.25.1 router and a 7.24.5 one must still reduce to its number.
func TestAPlainVersionReadsAsItIs(t *testing.T) {
	for in, want := range map[string]string{
		"7.25.1":          "7.25.1",
		"7.24.5 (stable)": "7.24.5",
	} {
		if got := ShortVersion(in); got != want {
			t.Errorf("ShortVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
