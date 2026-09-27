package server

// The Terminal page: one RouterOS console line at a time, run on the selected
// router, with the router's own text sent back.
//
// ── THE SECOND PATH THAT LEAVES THE REGISTRY, AND THE FURTHER ONE ───────────
//
// `ai_raw.go` is the first: a command the model composed, which no resource
// describes and no guard can evaluate. This is that, one step further out.
// `ai_raw.go` still runs its text through internal/rawcmd, which is an
// ALLOW-LIST of shapes - twenty verbs, and a refusal for `;`, `[`, `$`, a
// leading `:` and every scripting construct.
//
// NOTHING HERE PARSES THE LINE AT ALL.
//
// internal/rawcmd is not on this path and must not be added to it. The typed
// text becomes `=script=<text>` and goes to the router byte for byte, because
// the whole point of choosing /execute over reusing that parser was that
// `/interface print detail where name~"ether"` and `:put [/system resource get
// uptime]` are the commands people actually type, and rawcmd refuses both by
// design. A terminal that accepted a fifth of the console would be a worse
// answer than no terminal. The only bounds applied to the input are that it is
// not empty and not absurdly long, neither of which reads what it says.
//
// So there is no guard, no read-back, no undo and no page in the permission
// matrix that maps the menu it reaches. What there is instead:
//
//	a SIGNED-IN GLOBAL ADMINISTRATOR   not merely someone who may write a page,
//	                                   and not "sign-in is off, so everyone is"
//	the terminalEnabled SETTING        default false; see store.TerminalEnabled
//	WRITE ACCESS TO THE terminal PAGE  per router, through the normal matrix
//
// ── AND A FOURTH GATE THAT IS NOT OURS, WHICH IS THE STRONGEST ──────────────
//
// RouterOS applies the signed-in user's own group policies to anything
// /execute runs. The credential MikroDash stores is what decides whether a line
// may write, and the README's recommended group is `read,api,test` with `write`
// and `policy` explicitly off. On an install that followed it the Terminal is
// read-only BECAUSE THE ROUTER REFUSES, not because MikroDash vetted anything -
// which is a stronger guarantee than any denylist here could be, and is why
// there is no denylist here. A refusal from the router is rendered as what it
// is: the router's own sentence.
//
// ── MEASURED, NOT ASSUMED ───────────────────────────────────────────────────
//
// Probed against a RouterOS 7.24.4 CHR before this file was written, because
// the documentation says `/execute` blocks under `as-string` and does not say
// where the text lands:
//
//   - the console text arrives in the `!done` `ret=` word and NOWHERE ELSE -
//     zero `!re` sentences - so Cmd.Ret is the whole of the reply handling;
//   - without `=as-string=yes` the reply is a background job id ("*116"), so
//     the flag is not optional: without it this page would render job ids;
//   - THE ROUTER REPORTS A COMMAND ERROR AS OUTPUT, WITH A NIL ERROR.
//     `/nope/print` comes back as "syntax error (line 1 column 6)" and
//     `:error "boom"` as "boom (:error; line 1)", both with err == nil. See
//     the audit outcome below, which is narrower than it looks because of this;
//   - a command that overruns its timeout is cancelled on the router and THE
//     SHARED CONNECTION SURVIVES: `:delay 30s` bounded at 5s timed out, and the
//     next command on the same connection answered. That is the property that
//     makes this safe to run on the connection every collector shares, and it
//     is the one to re-measure if the timeout or the RouterOS floor changes;
//   - lines are CRLF-separated, and `/export` is ~4.5 KB.

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/rawcmd"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
	"mikrodash/internal/store"
)

const (
	// terminalMaxLine bounds the TYPED LINE, not its meaning. RouterOS refuses a
	// script over 64 kB; 4 KiB is far under that and far over what a person
	// types. A length bound is not vetting - nothing here reads the characters.
	terminalMaxLine = 4096

	// terminalTimeout bounds one command. `reader.Do` defaults to 15s, which is
	// sized for a collector's menu print; a typed line is not. `/ping count=10`
	// is ten seconds by definition and `/export` is seconds on a loaded router,
	// so 15s would fail the ordinary cases and read as a broken page. It is not
	// free: it holds one of the router's eight roslimit slots for its length.
	terminalTimeout = 30 * time.Second

	// What one command's output may cost. NOT ai_raw's 50 rows and 16 KiB:
	// those bound what a MODEL is sent as one message. The consumer here is a
	// person with a scrolling pane, and truncating an `/export` at fifty lines
	// would break the one job this page exists to do.
	terminalMaxLines = 2000
	terminalMaxBytes = 128 * 1024

	// What this connection keeps for the selected router. The binding cap in
	// practice is the byte one, because it is what a single replay frame must
	// marshal without being dropped.
	terminalScrollbackEntries = 50
	terminalScrollbackBytes   = 256 * 1024
)

// terminalRefusal is the SAME sentence whichever of the three gates stopped it,
// so nobody can map an installation by watching which refusal they get. Which
// gate it was goes in the audit row.
const terminalRefusal = "The Terminal is not available. It is off unless a global " +
	"administrator has switched it on for this installation and granted you write " +
	"access to this page on this device."

// TermEntry is one line somebody typed and what came back.
type TermEntry struct {
	// Seq is this connection's run counter, monotonic and never reused, so a
	// gap in what the page received is a frame the hub dropped.
	Seq int   `json:"seq"`
	At  int64 `json:"at"`
	// Command is the line AS SHOWN - masked, never as sent. See maskTypedLine.
	Command   string   `json:"command"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
	Ms        int      `json:"ms"`
	// Code is "" for a line that reached the router, and otherwise one of
	// denied, unavailable, busy, empty, toolong, limited, timeout, failed,
	// stopped. The browser turns it into a sentence; the server does not write
	// prose the browser then has to render.
	Code    string `json:"code"`
	Message string `json:"message"`
}

// TermOutputPayload is `term:output`: one run, echoed and then answered.
type TermOutputPayload struct {
	Entry TermEntry `json:"entry"`
	// Running marks the echo frame, sent before the command leaves, so a
	// thirty-second command appears on the page when it is sent rather than
	// only when it answers.
	Running bool `json:"running"`
	Done    bool `json:"done"`
}

// TermScrollbackPayload is `term:scrollback`: the whole pane, and what this
// viewer may do with it.
type TermScrollbackPayload struct {
	RouterID string      `json:"routerId"`
	Entries  []TermEntry `json:"entries"`
	// MayRun is all three gates answered at once, so the input is drawn
	// disabled rather than accepting a line that would only be refused.
	MayRun bool `json:"mayRun"`
	// Why is the same refusal sentence the executor returns, never which gate.
	Why string `json:"why"`
	// Identity is the router's own `/system/identity`, for the prompt. The
	// browser has no other source for it: the dropdown carries MikroDash's
	// label, which is a different string, and a prompt that quietly showed the
	// wrong one would be a plausible-looking wrong answer.
	Identity string `json:"identity"`
	User     string `json:"user"`
	Trimmed  bool   `json:"trimmed"`
	// Running is whether a command is in flight, so a reconnect mid-command
	// does not draw an idle prompt.
	Running bool `json:"running"`
}

// terminalExec is the one method this file needs of a session, so a test can
// drive it without a router. Mirrors secScanReader.
type terminalExec interface {
	Exec(cmd routeros.Cmd) ([]routeros.Reply, error)
}

// termState is this connection's scrollback.
//
// ── WHY THE SERVER KEEPS THE PANE AT ALL ────────────────────────────────────
//
// The hub DROPS a frame a slow browser cannot take (internal/hub/hub.go), and
// its own header says why that is safe: every other payload is a complete
// snapshot, a change re-emits, and opening a page replays the last one.
// `term:output` is none of those. It is a delta, sent once, and nothing
// re-derives it - so a dropped frame would be a command whose output is gone
// for good, with nothing on the page saying so.
//
// So the server holds the last commands for the router THIS connection has
// selected and replays them on page:focus. That one mechanism is three things
// at once: drop recovery, survival across a page switch, and survival across a
// reconnect - which is why it is preferred over a sequence-number-and-resend
// protocol that would do only the first.
//
// PER SOCKET, like `proposals`: a terminal session is a conversation with the
// person at this browser, and replaying it into another tab is not something
// anyone asked for.
//
// ── ITS OWN MUTEX, AND NOT THE LOOP ─────────────────────────────────────────
//
// ws.go's inbox loop owns sess/routerID/rsession and background work reads them
// through scope(). This is neither: it is WRITTEN by the run goroutine, which
// is off the loop because a run blocks for up to thirty seconds, and READ by
// the loop in resumePage. Posting the append back onto the loop would put the
// output behind whatever else the browser has queued, which is the very thing
// running off the loop avoids; and releaseRouter runs on the disconnect path
// after the loop has closed, while a run may still be in flight.
type termState struct {
	mu sync.Mutex
	// router is the router these entries belong to; "" when there are none.
	router  string
	entries []TermEntry
	bytes   int
	seq     int
	quit    chan struct{}
	running bool
	trimmed bool
}

// ── the gate ────────────────────────────────────────────────────────────────

// terminalGateNote is the three checks, answered WITHOUT auditing: "" when this
// viewer passes all of them, otherwise the note saying which one refused.
//
// SIGNED IN, AND A GLOBAL ADMINISTRATOR. `(*Server).isGlobalAdmin` answers true
// when sign-in is switched off entirely, which is right for reading the
// principal graph and wrong here for the reason ai_raw.go's rawGateNote gives:
// a command nobody can be held to is one nobody should be able to send. The
// AuthMode clause is that same compensation, borrowed rather than reinvented.
//
// The order refuses earliest: identity first (no I/O), then the install setting
// (one settings read), then the per-router grant (three indexed selects).
func (cn *conn) terminalGateNote(sc connScope) (store.Settings, string) {
	if sc.sess == nil || sc.sess.AuthMode == "none" || !cn.srv.isGlobalAdmin(sc.sess) {
		return nil, "not a signed-in global administrator"
	}
	settings, err := cn.srv.mergedSettings()
	if err != nil {
		return nil, "unreadable"
	}
	if !store.TerminalEnabled(settings) {
		return nil, "terminalEnabled is off"
	}
	if !cn.canPageIn(sc, "terminal", "write") {
		return nil, "no terminal write access on this router"
	}
	return settings, ""
}

// terminalGate is terminalGateNote plus the audit row a refusal earns.
func (cn *conn) terminalGate(rec *audit.Recorder, sc connScope) string {
	_, note := cn.terminalGateNote(sc)
	if note == "unreadable" {
		return "The settings could not be read, so nothing was run."
	}
	if note != "" {
		rec.Denied(audit.Event{
			Action: "terminal.run", TargetType: "router",
			TargetID: sc.routerID, RouterID: sc.routerID, Note: note,
		})
		return terminalRefusal
	}
	if sc.rs == nil {
		return "No device is selected, so nothing was run."
	}
	return ""
}

// ── masking ─────────────────────────────────────────────────────────────────

// maskTypedLine hides what LOOKS like a secret in the line somebody typed, for
// the audit row and for the echo the page draws. What is SENT to the router is
// untouched.
//
// ── A PATTERN, NOT A PARSE, AND BEST EFFORT ─────────────────────────────────
//
// `audit.IsCredentialField` and `rawcmd.Sensitive` both answer "is this FIELD
// NAME a secret" and both want a parsed field. Nothing here parses. What is
// borrowed is the name predicate alone, applied to every `name=value` token a
// regexp can see - and that is the whole of what is possible. A line that says
// `:local p "hunter2"; /user/add password=$p`, or that builds the value by
// concatenation, defeats it and always will.
//
// THIS IMPORT IS NOT VETTING. `rawcmd.Sensitive` is a name predicate over
// RouterOS's published list with its own test; `Parse`, `IsReadVerb` and
// `APIPath` are never called from this file and must not be.
//
// Said plainly because the alternative is implying a guarantee: the CONTROL on
// this page is the gate, not the mask. The mask stops the ordinary accident of
// `password=x` being written into `audit_events`, which cannot be withdrawn.
// terminalPairRe finds `name=value` tokens, quoted or bare. It is the whole of
// what maskTypedLine can see, and the comment above says why that is the limit.
var terminalPairRe = regexp.MustCompile(`(?i)[A-Za-z][A-Za-z0-9-]*=(?:"(?:[^"\\]|\\.)*"|\S+)`)

func maskTypedLine(s string) string {
	return terminalPairRe.ReplaceAllStringFunc(s, func(tok string) string {
		i := strings.Index(tok, "=")
		if i < 0 || !rawcmd.Sensitive(strings.ToLower(tok[:i])) {
			return tok
		}
		return tok[:i+1] + audit.Set
	})
}

// ── running one line ────────────────────────────────────────────────────────

// runTerminalLine sends ONE typed line to the router's console and returns what
// came back, capped.
//
// The reply shape is measured, not guessed: `as-string` puts the console text
// in the `!done` `ret` word, which `Client.Do` copies into `Cmd.Ret`. Rows are
// read too, so a router that answers some other way is VISIBLE rather than
// silently rendered as nothing.
func runTerminalLine(rs terminalExec, line string) (out []string, truncated bool, err error) {
	var ret string
	rows, err := rs.Exec(routeros.Cmd{
		Path:    "/execute",
		Args:    []string{"=script=" + line, "=as-string=yes"},
		Timeout: terminalTimeout,
		Ret:     &ret,
	})
	if err != nil {
		return []string{}, false, err
	}
	text := ret
	if text == "" {
		var b strings.Builder
		for _, r := range rows {
			b.WriteString(r["ret"])
		}
		text = b.String()
	}
	lines, truncated := terminalLines(text)
	return lines, truncated, nil
}

// terminalLines splits the router's text and caps it. CRLF, measured.
func terminalLines(text string) ([]string, bool) {
	out := []string{}
	if text == "" {
		return out, false
	}
	used, truncated := 0, false
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSuffix(ln, "\r")
		if len(out) >= terminalMaxLines || used+len(ln) > terminalMaxBytes {
			truncated = true
			break
		}
		used += len(ln)
		out = append(out, ln)
	}
	return out, truncated
}

// terminalFailure turns an error into a code the browser has a sentence for.
// The router's own wording is kept: a policy refusal is the most useful thing
// this page can say, and rewriting it would lose which policy was missing.
func terminalFailure(err error) (string, string) {
	var trap *routeros.Trap
	if errors.As(err, &trap) {
		return "failed", "The router refused it: " + safe.Message(trap.Message)
	}
	if errors.Is(err, errWriteRateLimited) {
		return "limited", "Too many commands in the last minute. Wait a moment."
	}
	msg := safe.Message(err.Error())
	if strings.Contains(msg, "timed out") || strings.Contains(msg, "deadline") {
		return "timeout", "The device did not answer in 30 seconds, so the command was cancelled."
	}
	return "failed", msg
}

// ── dispatch ────────────────────────────────────────────────────────────────

type terminalRunReq struct {
	Line string `json:"line"`
}

// termRun answers `term:run`. On the read loop; the command itself is not.
func (cn *conn) termRun(raw json.RawMessage) {
	sc := cn.scope()
	// BUILT ON THE LOOP and carried into the goroutine: audit.New reads
	// cn.sess, which only the loop may touch. The value is immutable after
	// construction, so passing it across is safe where calling it there is not.
	rec := cn.recorder()

	var req terminalRunReq
	if json.Unmarshal(raw, &req) != nil {
		return
	}
	line := strings.TrimRight(req.Line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return
	}
	if len(line) > terminalMaxLine {
		cn.termRefuse("toolong", "That line is too long to send.")
		return
	}
	if refusal := cn.terminalGate(rec, sc); refusal != "" {
		cn.termRefuse("denied", refusal)
		return
	}
	if !cn.term.begin() {
		cn.termRefuse("busy", "A command is still running.")
		return
	}

	shown := maskTypedLine(line)
	seq := cn.term.nextSeq()
	quit := cn.term.arm()
	// The echo first, so a long command shows as soon as it is sent.
	EvTermOutput.Send(cn.srv.hub, cn.c, TermOutputPayload{
		Entry:   TermEntry{Seq: seq, At: nowMillis(), Command: shown, Lines: []string{}},
		Running: true,
	})
	go func() {
		defer cn.term.end()
		cn.termWork(sc, rec, seq, line, shown, quit)
	}()
}

// termWork is the off-loop half: the command, the audit row, the result.
func (cn *conn) termWork(sc connScope, rec *audit.Recorder, seq int, line, shown string, quit <-chan struct{}) {
	started := time.Now()
	var out []string
	var truncated bool
	// THE RATE LIMIT IS THE WRITE LIMIT, and it applies whether or not this line
	// changes anything - for ai_raw.go's reason, that a loop of prints is as
	// much load on a router as a loop of writes and this path has no collector
	// cache behind it, and for one more that is stronger here: WE CANNOT TELL.
	// The text is not parsed, so "is this a read" has no answer on this path,
	// and the only honest limit is the stricter one.
	err := cn.inWriteQueueOn(sc, func() error {
		var e error
		out, truncated, e = runTerminalLine(sc.rs, line)
		return e
	})
	ms := int(time.Since(started).Milliseconds())

	// AUDITED WHETHER OR NOT IT WORKED, with the masked text, before the result
	// is delivered: the record of what was attempted is the point.
	//
	// ── WHAT `outcome` MEANS HERE, WHICH IS NARROWER THAN IT LOOKS ──────────
	//
	// It says whether MikroDash got a reply, NOT whether the command worked.
	// The router reports a command error as output with a nil error - measured:
	// `/nope/print` returns "syntax error (line 1 column 6)" and succeeds at the
	// protocol level. Deriving a truthful outcome would mean pattern-matching
	// the router's prose, which would be wrong in both directions. The honest
	// claim is the narrow one, and it is written here rather than implied.
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	rec.Record(audit.Event{
		Action: "terminal.run", TargetType: "router", TargetID: sc.routerID,
		TargetName: sc.rs.Label, RouterID: sc.routerID, Outcome: outcome,
		Note: "ran a line on the device's terminal",
		Extra: []audit.KV{
			{Key: "command", Value: shown},
			{Key: "lines", Value: len(out)},
			{Key: "truncated", Value: truncated},
			{Key: "ms", Value: ms},
			{Key: "via", Value: "terminal"},
		},
	})

	select {
	case <-quit:
		// Stopped, or the router was switched, or the socket closed. The frame
		// is dropped and nothing is appended: a result from one device must not
		// land in another's pane.
		return
	default:
	}
	entry := TermEntry{
		Seq: seq, At: nowMillis(), Command: shown, Lines: out,
		Truncated: truncated, Ms: ms,
	}
	if err != nil {
		entry.Code, entry.Message = terminalFailure(err)
	}
	cn.term.append(sc.routerID, entry)
	EvTermOutput.Send(cn.srv.hub, cn.c, TermOutputPayload{Entry: entry, Done: true})
}

// termRefuse sends a refusal the page can render in the pane, so a command that
// was not run still leaves a mark rather than vanishing.
func (cn *conn) termRefuse(code, msg string) {
	EvTermOutput.Send(cn.srv.hub, cn.c, TermOutputPayload{
		Entry: TermEntry{At: nowMillis(), Lines: []string{}, Code: code, Message: msg},
		Done:  true,
	})
}

// termStop answers `term:stop`.
//
// IT STOPS MIKRODASH WAITING, and says so rather than implying more. There is
// no cancellation on `Session.Exec`: the only bound is the command's own
// timeout, so closing quit makes the result stop mattering - the frame is
// dropped and nothing is appended - while the command runs on the router until
// it answers or the timeout fires.
func (cn *conn) termStop() {
	if !cn.term.disarm() {
		return
	}
	EvTermOutput.Send(cn.srv.hub, cn.c, TermOutputPayload{
		Entry: TermEntry{At: nowMillis(), Lines: []string{}, Code: "stopped",
			Message: "Stopped waiting. The command may still be running on the device; " +
				"its output will not be shown."},
		Done: true,
	})
}

// termResume sends the pane and what this viewer may do with it. Called from
// resumePage, so it is how a dropped frame is recovered as well as how the pane
// survives a page switch.
func (cn *conn) termResume() {
	sc := cn.scope()
	// NO AUDIT: opening a page is not an attempt to run anything.
	_, note := cn.terminalGateNote(sc)
	routerID, entries, trimmed, running := cn.term.snapshot()
	out := TermScrollbackPayload{
		RouterID: sc.routerID, Entries: entries, MayRun: note == "",
		Trimmed: trimmed, Running: running,
	}
	switch note {
	case "":
	case "unreadable":
		out.Why = "The settings could not be read."
	default:
		out.Why = terminalRefusal
	}
	// A pane kept for a router this socket has since left is not this pane.
	if routerID != "" && routerID != sc.routerID {
		out.Entries = []TermEntry{}
	}
	if sc.sess != nil {
		out.User = sc.sess.Username
	}
	if out.MayRun && sc.rs != nil {
		out.Identity = terminalIdentity(sc.rs)
	}
	EvTermScrollback.Send(cn.srv.hub, cn.c, out)
}

// terminalIdentity reads the router's own name for the prompt. One cheap read
// per page focus, and "" on failure - the page falls back rather than showing a
// name that might be another device's.
func terminalIdentity(rs terminalExec) string {
	rows, err := rs.Exec(routeros.Cmd{Path: "/system/identity/print"})
	if err != nil || len(rows) == 0 {
		return ""
	}
	return rows[0]["name"]
}

// ── the scrollback ──────────────────────────────────────────────────────────

func (s *termState) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

func (s *termState) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
}

func (s *termState) nextSeq() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}

func (s *termState) arm() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := make(chan struct{})
	s.quit = q
	return q
}

// disarm closes the run in flight, and reports whether there was one.
func (s *termState) disarm() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quit == nil {
		return false
	}
	close(s.quit)
	s.quit = nil
	return true
}

// append adds one finished entry, evicting oldest-first until BOTH caps hold.
// A routerID that is not the one the pane holds is discarded: a result that
// lands after a router switch belongs to neither pane.
func (s *termState) append(routerID string, e TermEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.router == "" {
		s.router = routerID
	} else if s.router != routerID {
		return
	}
	s.entries = append(s.entries, e)
	s.bytes += entryBytes(e)
	for len(s.entries) > terminalScrollbackEntries || (s.bytes > terminalScrollbackBytes && len(s.entries) > 1) {
		s.bytes -= entryBytes(s.entries[0])
		s.entries = s.entries[1:]
		s.trimmed = true
	}
}

func entryBytes(e TermEntry) int {
	n := len(e.Command) + len(e.Message)
	for _, l := range e.Lines {
		n += len(l) + 1
	}
	return n
}

// snapshot is a copy for the replay, never the live slice.
func (s *termState) snapshot() (string, []TermEntry, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TermEntry, len(s.entries))
	copy(out, s.entries)
	return s.router, out, s.trimmed, s.running
}

// clear drops everything, on a router switch and on disconnect.
func (s *termState) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.router = ""
	s.entries = nil
	s.bytes = 0
	s.trimmed = false
}
