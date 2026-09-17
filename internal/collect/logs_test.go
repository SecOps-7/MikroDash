package collect

import (
	"mikrodash/internal/hub"
	"sync"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

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

// ── PHASE 4.1: THE SECOND SEQUENCE DERIVATION ──────────────────────────────
//
// A log line is the clearest case of a fold: each row is an EVENT, it matters
// once, and there is no "current value of the log" to map over. That is also why
// this menu can never back a rolling cache entry — `roscache.unrollable` refuses
// it for exactly this reason.

func TestFoldLogKeepsTheMostRecentLines(t *testing.T) {
	var ring []LogEntry
	for i := 0; i < 10; i++ {
		_, ring = FoldLog(ring, 3, routeros.Reply{
			"message": string(rune('a' + i)), "topics": "info",
		}, int64(i))
	}
	if len(ring) != 3 {
		t.Fatalf("ring holds %d, want 3", len(ring))
	}
	// THE LAST THREE, NOT THE FIRST. A ring that trimmed from the back would
	// freeze on the oldest lines and never show anything that just happened,
	// which is the entire purpose of a log tail.
	want := []string{"h", "i", "j"}
	for i, w := range want {
		if ring[i].Message != w {
			t.Errorf("ring[%d] = %q, want %q — the ring is trimming from the wrong "+
				"end, so the page shows the oldest lines for ever", i, ring[i].Message, w)
		}
	}
}

// TestFoldLogDoesNotWriteThroughToThePrior.
//
// `append` into a slice with spare capacity writes THROUGH to the caller's
// backing array, and these rings always have spare capacity once trimmed. The
// equivalent test for `FoldPing` passed a mutation for a whole round because it
// was written with slice literals, whose capacity equals their length — so this
// one is built with room to spare on purpose.
func TestFoldLogDoesNotWriteThroughToThePrior(t *testing.T) {
	prior := append(make([]LogEntry, 0, 8),
		LogEntry{Message: "one"}, LogEntry{Message: "two"})
	_, next := FoldLog(prior, 10, routeros.Reply{"message": "three"}, 1)

	if len(prior) != 2 {
		t.Errorf("the prior grew to %d", len(prior))
	}
	// The element PAST the prior's length is where an in-place append lands, and
	// a length check alone would miss it.
	if full := prior[:cap(prior)]; full[2].Message != "" {
		t.Errorf("the fold appended into the prior's spare capacity (found %q); the "+
			"caller's history was written through", full[2].Message)
	}
	if len(next) != 3 || next[2].Message != "three" {
		t.Errorf("next = %d entries ending %q", len(next), next[len(next)-1].Message)
	}
}

// TestFoldLogClassifiesFromTopics — the severity is derived, not carried, so a
// line's colour cannot disagree with its topics.
func TestFoldLogClassifiesFromTopics(t *testing.T) {
	e, _ := FoldLog(nil, 10, routeros.Reply{
		"message": "login failure", "topics": "system,error,critical", "time": "12:00:00",
	}, 42)
	if e.Severity == "" {
		t.Error("no severity derived from the topics")
	}
	if e.TS != 42 || e.Time != "12:00:00" || e.Message != "login failure" {
		t.Errorf("entry = %+v; the fold altered fields it should pass through", e)
	}
}

// TestFoldLogWithNoCapIsUnbounded — `entryOf` builds a single entry with size 0
// and must not have its ring trimmed to nothing.
func TestFoldLogWithNoCapIsUnbounded(t *testing.T) {
	var ring []LogEntry
	for i := 0; i < 5; i++ {
		_, ring = FoldLog(ring, 0, routeros.Reply{"message": "x"}, int64(i))
	}
	if len(ring) != 5 {
		t.Errorf("ring holds %d with no cap, want 5 — a zero size must mean "+
			"unbounded, not empty", len(ring))
	}
}

// logDoer answers /log/print and can hold a listen channel, counting both.
type logDoer struct {
	mu     sync.Mutex
	prints int
	opens  int
	stops  int
	rows   []routeros.Reply
}

func (d *logDoer) Connected() bool { return true }

func (d *logDoer) Do(routeros.Cmd) ([]routeros.Reply, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prints++
	return d.rows, nil
}

func (d *logDoer) Stream(routeros.Cmd, func(routeros.Reply)) (func(), error) {
	d.mu.Lock()
	d.opens++
	d.mu.Unlock()
	return func() {
		d.mu.Lock()
		d.stops++
		d.mu.Unlock()
	}, nil
}

func (d *logDoer) counts() (prints, opens, stops int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.prints, d.opens, d.stops
}

func logRows(msgs ...string) []routeros.Reply {
	out := make([]routeros.Reply, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, routeros.Reply{"message": m, "topics": "system,info", "time": "10:00:00"})
	}
	return out
}

// TestLogsSuspendGivesUpTheChannel — phase 3.4.
//
// ── IT WAS AN EMPTY METHOD FOR THE WHOLE LIFE OF THE PORT ──────────────────
//
// `logs` was the one page-fed collector nothing could stop: `/log/listen` was
// held open from connect to teardown on every router, whether or not anybody had
// ever opened the Logs page. `Suspend()` and `Resume()` were `{}`, which reads as
// a deliberate design rather than a gap — and because the page switchboard only
// ran from a frame the browser sent, nothing ever called them to find out.
//
// A channel per router is the resource this project conserves, so the property
// is pinned rather than left to the methods looking plausible.
func TestLogsSuspendGivesUpTheChannel(t *testing.T) {
	d := &logDoer{rows: logRows("one", "two", "three")}
	l := NewLogs(d, hub.Relay{})
	l.Start()

	if _, opens, _ := d.counts(); opens != 1 {
		t.Fatalf("Start opened %d channel(s), want 1", opens)
	}
	l.Suspend()
	if _, _, stops := d.counts(); stops != 1 {
		t.Errorf("Suspend released the channel %d time(s), want 1 — the router keeps "+
			"pushing log lines to a collector nobody is watching", stops)
	}
	l.Resume()
	if _, opens, _ := d.counts(); opens != 2 {
		t.Errorf("Resume opened %d channel(s) in total, want 2", opens)
	}
}

// TestLogsResumeIsIdempotent.
//
// ── WHY THIS IS REQUIRED AND NOT MERELY TIDY ───────────────────────────────
//
// `applyDemand` calls `ResumeCollector` for every wanted collector on every focus
// and every blur, so a viewer flipping between pages reaches Resume several times
// a minute. `Start` is `LoadInitial` + `Listen`, and `LoadInitial` is a full
// `/log/print`: resuming unconditionally would be one of those per navigation,
// and a second channel each time if `Listen` were not itself guarded.
func TestLogsResumeIsIdempotent(t *testing.T) {
	d := &logDoer{rows: logRows("one", "two")}
	l := NewLogs(d, hub.Relay{})
	l.Start()
	prints, opens, _ := d.counts()

	for range 5 {
		l.Resume()
	}
	p2, o2, _ := d.counts()
	if p2 != prints {
		t.Errorf("five Resumes on a running listener issued %d extra /log/print "+
			"read(s); a viewer flipping pages would do that all day", p2-prints)
	}
	if o2 != opens {
		t.Errorf("five Resumes opened %d extra channel(s)", o2-opens)
	}
}

// TestLogsResumeReloadsWithoutDuplicating.
//
// The ring develops a gap while nothing is listening, so the backlog has to come
// from the router again — and `push` does not deduplicate, so reading
// `/log/print` on top of a ring that still holds the same lines would show every
// one of them twice. A ring with a hole in the middle is worse still, because the
// page renders it as continuous.
func TestLogsResumeReloadsWithoutDuplicating(t *testing.T) {
	d := &logDoer{rows: logRows("one", "two", "three")}
	l := NewLogs(d, hub.Relay{})
	l.Start()
	if got := len(l.Last()); got != 3 {
		t.Fatalf("the backlog holds %d line(s) after Start, want 3", got)
	}

	l.Suspend()
	l.Resume()
	if got := len(l.Last()); got != 3 {
		t.Errorf("the backlog holds %d line(s) after a suspend and resume, want 3 — "+
			"the reload was appended to the ring it should have replaced", got)
	}
	if prints, _, _ := d.counts(); prints != 2 {
		t.Errorf("%d /log/print read(s) across Start and Resume, want 2 — a resume "+
			"after a real suspend MUST reload, or the ring keeps its gap", prints)
	}
}

// TestLogsReloadForAReaderOffThePage. The assistant's log tool reads the ring
// while nobody is on the Logs page. A suspended ring is reloaded without
// duplicating and without opening a channel; a listening one is left alone and
// reports itself current.
func TestLogsReloadForAReaderOffThePage(t *testing.T) {
	d := &logDoer{rows: logRows("one", "two", "three")}
	l := NewLogs(d, hub.Relay{})
	if l.ReadingTS() != 0 {
		t.Errorf("a never-loaded ring reports reading time %d, want 0", l.ReadingTS())
	}
	l.Start()
	if age := time.Now().UnixMilli() - l.ReadingTS(); age > 1000 {
		t.Errorf("a listening ring reports a reading %dms old, want current", age)
	}
	l.Reload()
	if prints, _, _ := d.counts(); prints != 1 {
		t.Errorf("Reload while listening issued a /log/print (%d total); the ring was already complete", prints)
	}

	l.Suspend()
	suspendedAt := l.ReadingTS()
	l.Reload()
	if got := len(l.Last()); got != 3 {
		t.Errorf("after a reload the ring holds %d line(s), want 3 (appended, not replaced?)", got)
	}
	if prints, opens, _ := d.counts(); prints != 2 || opens != 1 {
		t.Errorf("reload while suspended: %d prints, %d channel opens; want 2 prints and no new channel", prints, opens)
	}
	if l.ReadingTS() < suspendedAt {
		t.Errorf("a reload did not advance the reading time")
	}
}
