package collect

import "testing"

// The severity classification, including the two branches no fixture reaches.
//
// The captured router's 500 lines are `info` and `warning` only, so `error` and
// `debug` are unexercised by the differential gate — and the ORDER of the
// branches is not visible there either. A row carries several topics at once, so
// "system,error,warning" has to read as an error and not as a warning: the first
// branch that matches wins, and reordering them would silently reclassify rows
// the page colours.
func TestClassifyLog(t *testing.T) {
	cases := []struct{ topics, want string }{
		{"system,info,account", "info"},
		{"system,error,critical", "error"},
		{"system,critical", "error"},
		{"firewall,warning", "warning"},
		{"debug,dhcp", "debug"},
		{"", "info"},
		{"SYSTEM,ERROR", "error"},           // matched case-insensitively
		{"system,error,warning", "error"},   // error outranks warning
		{"system,warning,debug", "warning"}, // warning outranks debug
		{"interface,link", "info"},          // nothing matched
	}
	for _, c := range cases {
		if got := classifyLog(c.topics); got != c.want {
			t.Errorf("classifyLog(%q) = %q, want %q", c.topics, got, c.want)
		}
	}
}

// The ring buffer drops the OLDEST, and the depth is the live default.
func TestLogRingDropsOldest(t *testing.T) {
	l := &Logs{size: 3}
	for _, m := range []string{"a", "b", "c", "d", "e"} {
		l.push(LogEntry{Message: m})
	}
	got := ""
	for _, e := range l.history {
		got += e.Message
	}
	if got != "cde" {
		t.Errorf("ring holds %q, want %q", got, "cde")
	}
	if n := logHistorySize(); n != 500 {
		t.Errorf("default history size is %d, want 500", n)
	}
}
