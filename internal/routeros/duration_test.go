package routeros

import "testing"

func TestDurationSeconds(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 0},
		{"00:00:30", 30},
		{"01:02:03", 3723},
		{"1d", 86400},
		{"1d2h3m4s", 93784},
		{"45s", 45},
		{"2h30m", 9000},
		{"garbage", 0},
		// Measured on RouterOS 7.24 BGP sessions: milliseconds are not minutes,
		// weeks count, and hold-time and keepalive-time use the same spelling.
		{"24s930ms", 24},
		{"4s70ms", 4},
		{"2w3d", 1468800},
		{"3m", 180},
		{"1m", 60},
		{"infinity", 0},
		{"90", 90},
	} {
		if got := DurationSeconds(tc.in); got != tc.want {
			t.Errorf("DurationSeconds(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
