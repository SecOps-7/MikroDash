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
	logPrintCmd  = routeros.Cmd{Path: "/log/print", Args: []string{"=.proplist=time,topics,message"}}
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
}

// The two rooms this collector serves. The page and the dashboard card show the
// same lines, and a viewer can hold both.
const logRooms = "page-logs,dash-card-logs"

func NewLogs(ros Reader, emit Emit) *Logs {
	return &Logs{ros: ros, emit: emit, size: logHistorySize()}
}

// push appends one entry, dropping the oldest when the ring is full.
func (l *Logs) push(e LogEntry) {
	l.history = append(l.history, e)
	if len(l.history) > l.size {
		l.history = l.history[len(l.history)-l.size:]
	}
}

func entryOf(row routeros.Reply, now int64) LogEntry {
	topics := row["topics"]
	return LogEntry{
		TS: now, Time: row["time"], Topics: topics,
		Message: row["message"], Severity: classifyLog(topics),
	}
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
	out := l.snapshot()
	l.mu.Unlock()
	l.emit(logRooms, "logs:history", out)
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
		l.emit(logRooms, "logs:new", e)
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
func (l *Logs) Reconnected() {
	l.Stop()
	l.mu.Lock()
	l.history = nil
	l.mu.Unlock()
	l.Start()
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

func (l *Logs) Suspend() {}
func (l *Logs) Resume()  {}

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
