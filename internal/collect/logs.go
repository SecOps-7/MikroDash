package collect

// Logs collector.
//
//	/log/print    the backlog, once, at connect
//	/log/listen   a PUSH STREAM: RouterOS sends each entry as it is written
//
// THE ONLY STREAMING COLLECTOR IN THE PORT, and the one place where a stream is
// clearly right. Everything else here polls, because a poll costs one channel
// for a moment and a stream holds one open — but a log has no "current state" to
// read: polling /log/print would mean re-reading the whole buffer every tick and
// keeping a seen-set to work out what was new. The listen channel makes both
// unnecessary, which is exactly what the Node original says it is for.
//
// The history is a RING BUFFER, not a growing slice. A busy router writes
// continuously, and the page shows a window; keeping everything would be a leak
// with a log level attached to it.

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

// Declared as a Cmd so the proplist drift gate can compare it against what
// logs.js asks for. /log/listen carries no proplist — it pushes whole rows.
var (
	logPrintCmd = routeros.Cmd{Path: "/log/print", Args: []string{"=.proplist=time,topics,message"}}
	// SET B: A STREAM, and the only one identified by its path rather than by an
	// argument. See acquisition.go.
	logListenCmd = routeros.Cmd{Path: "/log/listen"}
)

// Streamer is the half of a router connection that keeps a channel open.
//
// Separate from Reader because ONE collector needs it and the replay harness
// cannot provide it: a fixture records answers to reads, and a recorded stream
// is replayed as successive reads instead. A Reader that does not implement this
// simply gets no live tail, which is the honest degradation — the backlog still
// loads and the page still renders.
type Streamer interface {
	Stream(routeros.Cmd, func(routeros.Reply)) (func(), error)
}

// LogEntry is one line as the page renders it.
type LogEntry struct {
	// TS is when THIS PROCESS saw the entry, not when the router wrote it —
	// `Time` is the router's own stamp, in the router's timezone, with no
	// offset. Both travel because the page sorts on one and shows the other.
	TS       int64  `json:"ts"`
	Time     string `json:"time"`
	Topics   string `json:"topics"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// logHistorySize is the ring buffer's depth, from the same environment variable
// the Node collector reads so a deployment that raised it keeps its setting.
func logHistorySize() int {
	if v := os.Getenv("LOG_HISTORY_SIZE"); v != "" {
		// parseInt semantics, matching the original: a leading number wins and
		// anything unparseable falls back to the default rather than to zero.
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return 500
}

// classifyLog maps RouterOS's topic list to the four severities the page styles.
//
// SUBSTRING MATCHING ON THE WHOLE LIST, in this order, because a row carries
// several topics at once — "system,error,critical" is one string, and the first
// branch that matches wins. Reproduced exactly: reordering these would reclassify
// rows, and the page colours them.
func classifyLog(topicsRaw string) string {
	t := strings.ToLower(topicsRaw)
	switch {
	case strings.Contains(t, "critical") || strings.Contains(t, "error"):
		return "error"
	case strings.Contains(t, "warning"):
		return "warning"
	case strings.Contains(t, "debug"):
		return "debug"
	}
	return "info"
}

// Logs is the collector.
type Logs struct {
	ros  Reader
	emit Emit
	size int

	mu      sync.Mutex
	history []LogEntry
	stop    func()
	// loadedAt is when the backlog was last read whole (ms). While the channel is
	// open the ring is complete up to now; once it is closed, this is the last
	// moment the ring is known to have had no gap. See ReadingTS.
	loadedAt int64
}

// The two rooms this collector serves. The page and the dashboard card show the
// same lines, and a viewer can hold both.
// logRooms is kept as the name this file has always used; the VALUE now comes
// from the one declaration in rooms.go, so the guard and the emit agree.
var logRooms = logsRooms.Join()

func NewLogs(ros Reader, emit Emit) *Logs {
	return &Logs{ros: ros, emit: emit, size: logHistorySize()}
}

// FoldLog folds one log row into the history ring and returns the entry it made
// and the next state.
//
// ── PHASE 4.1: THE SECOND SEQUENCE DERIVATION, SAME SHAPE AS THE FIRST ─────
//
//	table     func(prior, []Reply) (payload, prior)   map over the current state
//	sequence  func(prior,   Reply) (payload, prior)   fold one element in
//
// A log line is the clearest case of the second: each row is an EVENT, it
// matters once, and there is no "current value of the log" to map over. That is
// also why this menu can never back a rolling cache entry -- see
// `roscache.unrollable`, where it is refused for exactly this reason.
//
// PURE, AND THE COPY IS NOT DEFENSIVE. `append` into a slice with spare capacity
// writes THROUGH to the caller's backing array, and these rings always have
// spare capacity once they have been sliced down. `FoldPing`'s own test passed a
// mutation for a whole round because it was written with slice literals, whose
// capacity equals their length; this avoids the same trap by construction.
func FoldLog(prior []LogEntry, size int, row routeros.Reply, now int64) (LogEntry, []LogEntry) {
	topics := row["topics"]
	e := LogEntry{
		TS: now, Time: row["time"], Topics: topics,
		Message: row["message"], Severity: classifyLog(topics),
	}
	return e, appendCapped(prior, e, size)
}

// appendCapped is the ring rule, in one place.
//
// THE LAST `size`, not the first: a ring that has overflowed must yield its most
// RECENT lines. Trimming from the front is what makes it a ring rather than a
// buffer that stops accepting, and it is the same rule `LoadInitial` applies to
// the backlog -- `/log/print` answers oldest first, so a router with more history
// than the ring holds must give up its oldest.
//
// It COPIES rather than appending in place. A slice with spare capacity -- which
// every one of these has once it has been trimmed -- would otherwise be written
// through, and the caller's view of its own history would change under it.
func appendCapped(prior []LogEntry, e LogEntry, size int) []LogEntry {
	next := append(append(make([]LogEntry, 0, len(prior)+1), prior...), e)
	if size > 0 && len(next) > size {
		next = next[len(next)-size:]
	}
	return next
}

// push appends one entry, dropping the oldest when the ring is full.
//
// The collector's half: hand the carried ring to the fold and keep what comes
// back. Everything that can be got wrong lives in FoldLog.
func (l *Logs) push(e LogEntry) {
	l.history = appendCapped(l.history, e, l.size)
}

func entryOf(row routeros.Reply, now int64) LogEntry {
	e, _ := FoldLog(nil, 0, row, now)
	return e
}

// LoadInitial reads the backlog and emits it whole.
//
// The LAST `size` rows, not the first: /log/print answers oldest first, and a
// router with more history than the ring holds must yield its most recent lines,
// not its oldest. A row with no message is dropped rather than rendered blank.
func (l *Logs) LoadInitial() {
	if !l.ros.Connected() {
		return
	}
	rows, err := l.ros.Do(logPrintCmd)
	if err != nil {
		return
	}
	if len(rows) > l.size {
		rows = rows[len(rows)-l.size:]
	}
	now := time.Now().UnixMilli()
	l.mu.Lock()
	for _, row := range rows {
		if row["message"] == "" {
			continue
		}
		l.push(entryOf(row, now))
	}
	l.loadedAt = now
	out := l.snapshot()
	l.mu.Unlock()
	EvLogsHistory.Emit(l.emit, logRooms, out)
}

// Listen opens the push channel. A Reader that cannot stream gets the backlog
// and nothing further, which is what the replay harness sees.
func (l *Logs) Listen() {
	s, ok := l.ros.(Streamer)
	if !ok || !l.ros.Connected() {
		return
	}
	l.mu.Lock()
	if l.stop != nil {
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()

	stop, err := s.Stream(logListenCmd, func(row routeros.Reply) {
		if row["message"] == "" {
			return
		}
		e := entryOf(row, time.Now().UnixMilli())
		l.mu.Lock()
		l.push(e)
		l.mu.Unlock()
		// ONE ENTRY PER FRAME, not the whole history. A busy router writes
		// several lines a second and the page appends; re-sending the buffer
		// each time would be the same data over and over.
		EvLogsNew.Emit(l.emit, logRooms, e)
	})
	if err != nil {
		return
	}
	l.mu.Lock()
	l.stop = stop
	l.mu.Unlock()
}

func (l *Logs) Start() {
	l.LoadInitial()
	l.Listen()
}

// Reconnected drops the buffer and reloads. The router that came back may have
// rebooted, in which case its log starts again and the lines held here describe
// a different uptime.
//
// EXPRESSED THROUGH Resume, so the two paths cannot drift: coming back from a
// reconnect and coming back from a suspend need the same three things in the
// same order, and they used to be written out twice.
func (l *Logs) Reconnected() {
	l.Stop()
	l.Resume()
}

func (l *Logs) Stop() {
	l.mu.Lock()
	stop := l.stop
	l.stop = nil
	l.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// Suspend gives up the push channel.
//
// ── IT WAS A NO-OP FOR THE WHOLE LIFE OF THE PORT ──────────────────────────
//
// `logs` was the one page-fed collector nothing could stop. It held
// `/log/listen` open from connect to teardown on every router, whether or not
// anybody had ever opened the Logs page -- and because the page switchboard only
// ran from a frame the browser sent, an empty pair of methods here looked like a
// deliberate design rather than a gap. The comment that justified it said the
// channel was the point; what it did not say is that the channel is exactly the
// resource this project conserves.
//
// ── THE TRADE, STATED, BECAUSE IT IS NOT FREE ──────────────────────────────
//
// Suspending costs a `/log/print` on the next resume: the ring develops a gap
// while nothing is listening, so the backlog has to come from the router again.
// That is one command per visit, which is what every other page-gated collector
// already pays on focus -- and CLAUDE.md is explicit that fewer channels is the
// win and command count is not the bottleneck. A router that nobody watches the
// logs of now holds no channel for them at all.
func (l *Logs) Suspend() { l.Stop() }

// Resume re-opens the channel, and RELOADS FIRST.
//
// The ring is dropped rather than appended to. `push` does not deduplicate, so
// reading `/log/print` on top of a ring that still holds the same lines would
// show every one of them twice; and a ring with a gap in the middle is worse
// than an empty one, because the page renders it as continuous.
//
// IDEMPOTENT, and that is required rather than tidy: `applyDemand` calls
// `ResumeCollector` for every wanted collector on every focus and blur, so a
// viewer flipping between pages reaches this several times a minute. Reloading
// each time would be a `/log/print` per navigation.
func (l *Logs) Resume() {
	l.mu.Lock()
	running := l.stop != nil
	if running {
		l.mu.Unlock()
		return
	}
	l.history = nil
	l.mu.Unlock()
	l.Start()
}

// ReadingTS is how current the ring is, in ms: now while the channel is open,
// because every line reaches it as the router writes it; otherwise the time of
// the last full backlog read, after which the ring has a gap. Zero means the
// ring has never been loaded.
//
// For a reader outside the page (the assistant's `list_logs`), which has to know
// whether the lines it would hand over are the router's recent log or an old
// snapshot with a hole in it.
func (l *Logs) ReadingTS() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stop != nil {
		return time.Now().UnixMilli()
	}
	return l.loadedAt
}

// Reload replaces the ring with the router's current backlog WITHOUT opening the
// channel, for a reader that needs recent lines while nobody is on the Logs page.
//
// A NO-OP WHILE LISTENING: the ring is already complete, and reading the backlog
// on top of it would duplicate every line, since `push` does not deduplicate.
// Cleared first for the same reason `Resume` clears.
func (l *Logs) Reload() {
	l.mu.Lock()
	if l.stop != nil {
		l.mu.Unlock()
		return
	}
	l.history = nil
	l.mu.Unlock()
	l.LoadInitial()
}

// snapshot copies the ring under the caller's lock.
func (l *Logs) snapshot() []LogEntry {
	out := make([]LogEntry, len(l.history))
	copy(out, l.history)
	return out
}

// Last is the history, for the page replay and for the differential gate.
func (l *Logs) Last() []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.history == nil {
		return nil
	}
	return l.snapshot()
}
