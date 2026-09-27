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
//	WRITE ACCESS TO THE terminal PAGE, per device, through the normal matrix -
//	and that is the whole of it. See terminalGateNote for what that one check
//	already carries and why it replaced three.
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/rawcmd"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
)

const (
	// terminalMaxLine bounds what arrives in one go, not its meaning. RouterOS
	// refuses a script over 64 kB, and this is a quarter of that - generous for
	// a PASTED BLOCK, which is the case that made 4 KiB too tight, and still far
	// under what the device will take. A length bound is not vetting: nothing
	// here reads the characters.
	terminalMaxLine = 16 * 1024

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

	// Completion is interactive: a person is waiting with a finger on Tab, so
	// it gets a short bound of its own rather than a command's thirty seconds.
	terminalCompleteTimeout = 5 * time.Second
	// What one Tab may list. The console shows every candidate; at the root
	// that is 83 rows, and past a screenful nobody is reading anyway.
	terminalMaxCandidates = 120
)

// terminalRefusal is the SAME sentence whichever of the three gates stopped it,
// so nobody can map an installation by watching which refusal they get. Which
// gate it was goes in the audit row.
const terminalRefusal = "You do not have permission to run commands on this device."

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
	// Cwd is the menu the line was TYPED AT, which is not always the menu the
	// session is in afterwards: `/ip address` is typed at the root and lands
	// somewhere else. Carried per entry so the echo shows the prompt you
	// actually typed at, and so a replayed scrollback is still faithful after
	// walking about.
	Cwd string `json:"cwd"`
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
	// Cwd is the menu the session is in, so the prompt can show it. Carried on
	// every frame rather than sent as its own event: it changes only when a
	// frame is being sent anyway, and a separate event could arrive out of
	// order with the line that caused it.
	Cwd string `json:"cwd"`
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
	Running bool   `json:"running"`
	Cwd     string `json:"cwd"`
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
	// identity is the device's own `/system/identity`, read once with the
	// greeting rather than on every page focus. The browser has no other
	// source: the device dropdown carries MikroDash's LABEL, which is a
	// different string, and a prompt quietly showing the wrong one is a
	// plausible-looking wrong answer.
	// cwd is the menu the operator has walked into, in display form
	// ("/ip/address") or "" at the root. The SERVER keeps it, not the browser:
	// it is part of the session, so it survives a page switch the same way the
	// scrollback does, and the browser only has to draw it.
	cwd string
	// completing is the one-at-a-time latch for Tab. A held key repeats, and
	// each repeat is a read on the device.
	completing bool
	identity   string
	// greeted latches the opening banner, so it is written once per device
	// session and not again on every focus. Cleared by reset (a device switch
	// or a closed socket) and NOT by wipe, because a terminal you have cleared
	// does not print its login banner again until you reconnect.
	greeted bool
}

// ── the gate ────────────────────────────────────────────────────────────────

// terminalGateNote is the gate, answered WITHOUT auditing: "" when this viewer
// may use the page here, otherwise the note saying why not.
//
// ── ONE CHECK, AND IT IS THE PERMISSION MATRIX ──────────────────────────────
//
// There was an install-wide `terminalEnabled` switch here as well, and a
// requirement that the caller be a global administrator. Both are gone, at the
// operator's direction, and removing them made this more coherent rather than
// less: a page key in this app IS a permission key, so "may this person use the
// Terminal on this device" already had an answer, and the other two were a
// second and third mechanism for the one job. Worse, keeping the administrator
// clause would have made GRANTING somebody the Terminal permission do nothing
// at all, which is the most confusing of the three possible designs.
//
// What that one check already carries, from canPageIn:
//
//	sign-in switched off      write is refused outright, so a command nobody
//	                          can be held to is one nobody can send
//	RBAC unavailable          write fails CLOSED
//	no audit database         write is refused: no trail, no writes
//	the coarse page union     ANDed with the resolver, never substituted
//
// So the properties the old gate spelled out by hand are properties of the
// matrix, and they hold for this page the same way they hold for every other.
// The page is not granted to anyone by default; an Administrator has it because
// builtin roles carry every page, and anybody else has it because somebody
// deliberately said so.
func (cn *conn) terminalGateNote(sc connScope) string {
	if !cn.canPageIn(sc, "terminal", "write") {
		return "no terminal write access on this device"
	}
	return ""
}

// terminalGate is terminalGateNote plus the audit row a refusal earns.
func (cn *conn) terminalGate(rec *audit.Recorder, sc connScope) string {
	if note := cn.terminalGateNote(sc); note != "" {
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

// ── walking the menus ───────────────────────────────────────────────────────

// terminalPathRe is what a line must look like to be worth asking about.
//
// A SOUND filter, not a clever one: every menu path is letters, digits, `/`,
// `-`, `_`, `.` and spaces, so anything carrying `=`, a bracket, a quote, a `$`
// or a leading `:` is certainly a command and needs no question asked of the
// device. That is what keeps the extra read off `/ip address add address=...`
// and onto the handful of lines that might be navigation.
var terminalPathRe = regexp.MustCompile(`^[A-Za-z0-9/._\- ]+$`)

func terminalPathShaped(line string) bool { return terminalPathRe.MatchString(line) }

// terminalResolve turns what was typed into what will run, against the menu the
// session is in. A leading `/` is absolute, as it is in the console.
func terminalResolve(cwd, line string) string {
	if cwd == "" || strings.HasPrefix(line, "/") {
		return line
	}
	return cwd + "/" + line
}

// terminalMenuPath is the display form of a menu: slash-separated, however it
// was typed.
//
// The console takes `/ip address` and `/ip/address` as the same place, and
// people type both. Showing back exactly what was typed would put `/ip address`
// in the prompt, which reads as a command rather than a location. Safe to do
// unconditionally here because this is only ever called for a line the device
// has already said IS a menu, and a menu path has no arguments to mangle.
func terminalMenuPath(resolved string) string {
	out := strings.Join(strings.Fields(strings.ReplaceAll(resolved, "/", " ")), "/")
	if out == "" {
		return ""
	}
	return "/" + out
}

// terminalUp is what `..` does.
func terminalUp(cwd string) string {
	i := strings.LastIndex(cwd, "/")
	if i <= 0 {
		return ""
	}
	return cwd[:i]
}

// terminalIsMenu asks the device whether a line names a menu rather than a
// command.
//
// ── THE DEVICE ANSWERS THIS TOO, AND THE SIGNAL IS MEASURED ─────────────────
//
// Completion for the line WITH A TRAILING SPACE says what could come next, and
// that is the whole tell. Measured on a RouterOS 7.24.4 CHR:
//
//	`/ip address `        12 cmd + 1 dir   the verbs of a menu
//	`/ip address print `  19 arg           the arguments of a command
//	`ip `                 30 dir           the submenus of a menu
//	`/system reboot `     nothing shown    a leaf command
//
// So a menu offers commands or directories and never arguments. `request=self`
// and `request=child` were tried first and return nothing at all over the API,
// which is why this is read off completion rather than asked directly.
func terminalIsMenu(rs terminalExec, resolved string) (bool, error) {
	rows, err := rs.Exec(routeros.Cmd{
		Path:    "/console/inspect",
		Args:    []string{"=request=completion", "=input=" + resolved + " "},
		Timeout: terminalCompleteTimeout,
	})
	if err != nil {
		return false, err
	}
	var verbs, args int
	for _, r := range rows {
		if r["show"] != "true" {
			continue
		}
		switch r["style"] {
		case "cmd", "dir":
			verbs++
		case "arg":
			args++
		}
	}
	return verbs > 0 && args == 0, nil
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
	// WHERE THE LINE WAS TYPED, captured before navigation can move it.
	from := cn.term.where()

	// ── NAVIGATION IS NOT A COMMAND ────────────────────────────────────────
	//
	// `/` and `..` are answered here without touching the device at all, and a
	// path-shaped line costs one read to ask whether it is a menu. Anything
	// carrying `=`, a bracket or a quote is certainly a command and is not
	// asked about, which is what keeps this off the lines people actually run.
	//
	// Walking into a menu writes NO AUDIT ROW. Nothing ran on the device: the
	// one read it costs is the same kind of read Tab makes, and auditing a
	// change of prompt would fill the trail with rows that record nothing
	// having been done. The command that eventually runs is audited in full,
	// with the resolved path, so the trail still says what happened and where.
	if line == "/" || line == ".." {
		cn.term.end()
		at := cn.term.where()
		if line == "/" {
			at = ""
		} else {
			at = terminalUp(at)
		}
		cn.term.goTo(at)
		cn.termEcho(line, from, at)
		return
	}
	resolved := terminalResolve(from, line)
	if terminalPathShaped(line) {
		if menu, err := terminalIsMenu(sc.rs, resolved); err == nil && menu {
			cn.term.end()
			cn.term.goTo(terminalMenuPath(resolved))
			cn.termEcho(line, from, cn.term.where())
			return
		}
	}

	shown := maskTypedLine(line)
	seq := cn.term.nextSeq()
	quit := cn.term.arm()
	// The echo first, so a long command shows as soon as it is sent.
	EvTermOutput.Send(cn.srv.hub, cn.c, TermOutputPayload{
		Entry:   TermEntry{Seq: seq, At: nowMillis(), Command: shown, Lines: []string{}, Cwd: from},
		Running: true, Cwd: from,
	})
	go func() {
		defer cn.term.end()
		cn.termWork(sc, rec, seq, resolved, shown, from, quit)
	}()
}

// termWork is the off-loop half: the command, the audit row, the result.
func (cn *conn) termWork(sc connScope, rec *audit.Recorder, seq int, line, shown, from string, quit <-chan struct{}) {
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
		Truncated: truncated, Ms: ms, Cwd: from,
	}
	if err != nil {
		entry.Code, entry.Message = terminalFailure(err)
	}
	cn.term.append(sc.routerID, entry)
	EvTermOutput.Send(cn.srv.hub, cn.c, TermOutputPayload{
		Entry: entry, Done: true, Cwd: cn.term.where()})
}

// termRefuse sends a refusal the page can render in the pane, so a command that
// was not run still leaves a mark rather than vanishing.
func (cn *conn) termRefuse(code, msg string) {
	EvTermOutput.Send(cn.srv.hub, cn.c, TermOutputPayload{
		Entry: TermEntry{At: nowMillis(), Lines: []string{}, Code: code, Message: msg},
		Done:  true, Cwd: cn.term.where(),
	})
}

// termEcho draws a prompt line for something that changed the menu rather than
// running anything. It carries a seq so a replay replaces it rather than
// stacking a second copy, and it is appended to the pane like any other line.
func (cn *conn) termEcho(line, from, at string) {
	e := TermEntry{Seq: cn.term.nextSeq(), At: nowMillis(), Command: line,
		Lines: []string{}, Cwd: from}
	cn.term.append(cn.term.deviceID(), e)
	EvTermOutput.Send(cn.srv.hub, cn.c, TermOutputPayload{Entry: e, Done: true, Cwd: at})
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
		Done: true, Cwd: cn.term.where(),
	})
}

// termClear answers `term:clear`: the operator emptied the pane.
//
// THE SERVER MUST FORGET IT TOO. The browser clearing its own screen is not
// enough, because the pane it draws is replayed from here on the next page
// focus - so a Clear that only reached the browser would be undone by walking
// to another page and back, which is exactly the kind of quiet nonsense that
// makes people stop trusting a control.
func (cn *conn) termClear() {
	cn.term.wipe()
	cn.termResume()
}

// termResume sends the pane and what this viewer may do with it. Called from
// resumePage, so it is how a dropped frame is recovered as well as how the pane
// survives a page switch.
func (cn *conn) termResume() {
	sc := cn.scope()
	// NO AUDIT: opening a page is not an attempt to run anything.
	note := cn.terminalGateNote(sc)
	// THE GREETING IS WRITTEN ONCE PER DEVICE SESSION, and only for somebody who
	// may actually use the page - reading a device's version to decorate a pane
	// that is about to say "not available" would be two channels spent on a
	// refusal.
	if note == "" && sc.rs != nil && cn.term.claimGreeting(sc.routerID) {
		identity, lines := terminalGreeting(sc.rs)
		cn.term.greet(sc.routerID, identity, TermEntry{
			Seq: cn.term.nextSeq(), At: nowMillis(), Lines: lines,
		})
	}
	routerID, entries, trimmed, running := cn.term.snapshot()
	out := TermScrollbackPayload{
		RouterID: sc.routerID, Entries: entries, MayRun: note == "",
		Trimmed: trimmed, Running: running, Cwd: cn.term.where(),
	}
	if note != "" {
		out.Why = terminalRefusal
	}
	// A pane kept for a router this socket has since left is not this pane.
	if routerID != "" && routerID != sc.routerID {
		out.Entries = []TermEntry{}
	}
	if sc.sess != nil {
		out.User = sc.sess.Username
	}
	out.Identity = cn.term.who()
	EvTermScrollback.Send(cn.srv.hub, cn.c, out)
}

// terminalGreeting is what the pane says before anybody types: the device's
// name for the prompt, and the opening banner.
//
// TWO READS, ONCE PER DEVICE SESSION, not once per page focus. An earlier
// version read the identity every time `resumePage` ran, which is every time
// somebody moved to this page; latching it here is fewer channels on the
// device, which is the bottleneck this app is built around.
//
// Neither read is fatal. A device that will not answer still gets a banner and
// a prompt, with the parts that could not be read simply absent - a terminal
// that refuses to open because it could not print its own version would be a
// worse answer than one that opens without it.
func terminalGreeting(rs terminalExec) (identity string, lines []string) {
	if rows, err := rs.Exec(routeros.Cmd{Path: "/system/identity/print"}); err == nil && len(rows) > 0 {
		identity = rows[0]["name"]
	}
	var res routeros.Reply
	if rows, err := rs.Exec(routeros.Cmd{
		Path: "/system/resource/print",
		Args: []string{"=.proplist=version,board-name,architecture-name,uptime"},
	}); err == nil && len(rows) > 0 {
		res = rows[0]
	}
	return identity, terminalBanner(identity, res, time.Now().Year())
}

// mikrotikLogo is the block-letter wordmark a RouterOS console prints when you
// sign in.
//
// ── REPRODUCED, NOT RELAYED, AND THAT IS WORTH SAYING ───────────────────────
//
// A real console prints this itself. The RouterOS API cannot: the banner is
// written by the login path of the serial, telnet and SSH consoles, and no menu
// returns it - checked against a live device's serial console, which shows it
// only after a console login that the API account's policy does not permit. So
// these lines are ours, laid out to match what the device shows rather than
// read back from it, and a future reader should not go looking for the read
// that produces them.
var mikrotikLogo = []string{
	`  MMM      MMM       KKK                          TTTTTTTTTTT      KKK`,
	`  MMMM    MMMM       KKK                          TTTTTTTTTTT      KKK`,
	`  MMM MMMM MMM  III  KKK  KKK  RRRRRR     OOOOOO      TTT     III  KKK  KKK`,
	`  MMM  MM  MMM  III  KKKKK     RRR  RRR  OOO  OOO     TTT     III  KKKKK`,
	`  MMM      MMM  III  KKK KKK   RRRRRR    OOO  OOO     TTT     III  KKK KKK`,
	`  MMM      MMM  III  KKK  KKK  RRR  RRR   OOOOOO      TTT     III  KKK  KKK`,
}

// terminalBanner composes the opening lines. Every field is optional, because
// every one of them comes off a device that may not have answered.
func terminalBanner(identity string, res routeros.Reply, year int) []string {
	out := make([]string, 0, len(mikrotikLogo)+8)
	out = append(out, "")
	out = append(out, mikrotikLogo...)
	out = append(out, "")

	title := "  MikroTik RouterOS"
	if v := res["version"]; v != "" {
		title += " " + v
	}
	out = append(out, title+"  (c) 1999-"+strconv.Itoa(year)+"       https://help.mikrotik.com/")

	// The device's own line, built from whatever came back. Joined with a
	// separator only between the parts that exist, so a device that answered
	// half the read does not render a row of empty bullets.
	var facts []string
	for _, v := range []string{identity, res["board-name"], res["architecture-name"]} {
		if v != "" {
			facts = append(facts, v)
		}
	}
	if u := res["uptime"]; u != "" {
		facts = append(facts, "up "+u)
	}
	if len(facts) > 0 {
		out = append(out, "", "  "+strings.Join(facts, "  ·  "))
	}

	out = append(out, "",
		"  Lines typed here go to the device exactly as written. MikroDash does not check them,",
		"  and each one is recorded in the Audit Trail.",
		"")
	return out
}

// ── completion, which the DEVICE does for us ────────────────────────────────

// TermCompletion is one candidate, exactly as the device offered it.
type TermCompletion struct {
	// Text is what to insert. Offset is where it goes in the line, which the
	// device tells us rather than the browser inferring it from the prefix -
	// quoting and `[` substitution make that inference wrong.
	Text   string `json:"text"`
	Offset int    `json:"offset"`
	// Help is the device's own one-line description, and Style is how the
	// console would colour it: dir, cmd, arg, and so on.
	Help  string `json:"help"`
	Style string `json:"style"`
}

// TermCompletePayload answers `term:complete`.
type TermCompletePayload struct {
	// Line is the input the candidates were computed for. The browser drops an
	// answer that is not for what is currently typed, because a slow reply to
	// an older line would otherwise splice the wrong word in.
	Line       string           `json:"line"`
	Candidates []TermCompletion `json:"candidates"`
	Code       string           `json:"code"`
}

// terminalComplete asks the device to complete a partial line.
//
// ── THE COMPLETION IS THE DEVICE'S, NOT OURS ────────────────────────────────
//
// `/console/inspect request=completion` runs the console's own completion
// engine - the same one an SSH session uses - and hands back the candidates.
// That matters more than it sounds:
//
//   - it is RIGHT FOR THIS DEVICE'S VERSION, by construction. A table shipped
//     in MikroDash would drift the moment a router upgraded, and would drift
//     silently, offering a menu that is not there;
//   - it knows DYNAMIC values. `interface=` answers with this device's own
//     interfaces, comments and all. Nothing shipped here could;
//   - it carries the help text and the style, so the list reads like the
//     console's rather than like a list of strings we invented.
//
// Measured on a RouterOS 7.24.4 CHR before this was written: `/ip add` offers
// `address` at offset 4; `/ip address add interface=` offers that device's six
// interfaces, two of them annotated with their comments.
//
// Rows whose `show` is not true are the console's own syntax furniture -
// whitespace, `{`, `;`, `[` - which it uses for highlighting rather than for a
// completion list, so they are dropped. `preference` is its ranking, highest
// first.
func terminalComplete(rs terminalExec, line string) ([]TermCompletion, error) {
	rows, err := rs.Exec(routeros.Cmd{
		Path:    "/console/inspect",
		Args:    []string{"=request=completion", "=input=" + line},
		Timeout: terminalCompleteTimeout,
	})
	if err != nil {
		return []TermCompletion{}, err
	}
	type ranked struct {
		c    TermCompletion
		pref int
	}
	out := make([]ranked, 0, len(rows))
	for _, r := range rows {
		if r["show"] != "true" || r["completion"] == "" {
			continue
		}
		pref, _ := strconv.Atoi(r["preference"])
		off, _ := strconv.Atoi(r["offset"])
		out = append(out, ranked{TermCompletion{
			Text: r["completion"], Offset: off, Help: r["text"], Style: r["style"],
		}, pref})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].pref > out[j].pref })
	cands := make([]TermCompletion, 0, len(out))
	for _, r := range out {
		if len(cands) >= terminalMaxCandidates {
			break
		}
		cands = append(cands, r.c)
	}
	return cands, nil
}

// termComplete answers `term:complete`. On the read loop; the device read is not.
func (cn *conn) termComplete(raw json.RawMessage) {
	var req terminalRunReq
	if json.Unmarshal(raw, &req) != nil {
		return
	}
	line := req.Line
	sc := cn.scope()
	if cn.terminalGateNote(sc) != "" || sc.rs == nil {
		return // no candidates, and no audit: pressing Tab is not an attempt
	}
	// ONE AT A TIME. Tab repeats easily and each press is a channel on the
	// device; a held key would otherwise queue a read per repeat.
	if !cn.term.claimComplete() {
		return
	}
	go func() {
		defer cn.term.releaseComplete()
		cands, err := terminalComplete(sc.rs, line)
		out := TermCompletePayload{Line: line, Candidates: cands}
		if err != nil {
			out.Code = "failed"
		}
		EvTermComplete.Send(cn.srv.hub, cn.c, out)
	}()
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

// reset drops everything, on a device switch and on disconnect. The greeting
// latch goes with it, so the next device prints its own banner.
func (s *termState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.router = ""
	s.entries = nil
	s.bytes = 0
	s.trimmed = false
	s.identity = ""
	s.greeted = false
	s.cwd = ""
}

// wipe is the operator pressing Clear. The pane empties and the greeting latch
// STAYS SET: a terminal you have just cleared does not print its login banner
// again, and one that did would undo the clear you asked for.
func (s *termState) wipe() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	s.bytes = 0
	s.trimmed = false
}

// claimGreeting reports whether this call owns writing the banner, and takes
// that right in the same breath. Taken under the lock so two focuses landing
// together cannot both read the device and both append.
func (s *termState) claimGreeting(routerID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.greeted {
		return false
	}
	if s.router != "" && s.router != routerID {
		return false
	}
	s.greeted = true
	return true
}

// greet records the device's name and writes the banner as the pane's first
// entry. It carries a real seq, like any other entry, so a replay REPLACES it
// rather than drawing a second copy.
func (s *termState) greet(routerID, identity string, e TermEntry) {
	s.mu.Lock()
	s.identity = identity
	s.mu.Unlock()
	s.append(routerID, e)
}

// claimComplete takes the right to ask the device for candidates, if it is
// free. Tab repeats; without this a held key queues one read per repeat.
func (s *termState) claimComplete() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completing {
		return false
	}
	s.completing = true
	return true
}

func (s *termState) releaseComplete() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completing = false
}

// where is the menu the operator is in, for the prompt.
func (s *termState) where() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

func (s *termState) goTo(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cwd = p
}

// deviceID is the device the pane belongs to. Named apart from the `router`
// field it reads, which is what a first attempt collided with.
func (s *termState) deviceID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.router
}

// who is the device's own name, for the prompt.
func (s *termState) who() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.identity
}
