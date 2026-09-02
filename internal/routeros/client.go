// Package routeros speaks the RouterOS binary API.
//
// THE WIRE PROTOCOL IS github.com/go-routeros/routeros/v3. This package is the
// adapter: it owns the vocabulary the rest of the port speaks — Cmd, Reply,
// Trap, Config — and nothing else. There is no framing code here any more.
//
// ── WHY THIS CHANGED, BECAUSE IT REVERSES A DOCUMENTED DECISION ──────────────
//
// The port began with a hand-written client. The stated reason was that four
// "protocol realities" made a general-purpose library unsafe, and that
// go-routeros got two of them wrong. That argument was tested against the three
// routers this project targets, on RouterOS 7.24. THE VERSION QUALIFIES EVERY
// CLAIM BELOW: "not reproduced" is not "never true". The evidence, in short:
//
//  1. `!empty` NOT followed by `!done`, hanging a client that waits for one.
//     NOT REPRODUCED. 16 of 16 empty replies across three routers sent `!done`
//     10–30 µs after `!empty`.
//  2. A reply arriving in several blocks, one `!done` per interface, on
//     wifi-qcom hardware. NOT REPRODUCED, on the exact hardware named: the AX3
//     answered the registration table with 30 clients across 8 interfaces in ONE
//     block with ONE `!done`, six times out of six, rows interleaved by uptime
//     rather than grouped by interface. A client returning on the first `!done`
//     got 30 of 30 — go-routeros included.
//  3. `/file/read` needing raw bytes. REAL, and inapplicable to Go: `string(b)`
//     does not transcode, so a backup read through go-routeros came back
//     byte-exact. The hazard was Node's UTF-8 decode, not the protocol.
//  4. Trailing packets for tags already torn down. REAL, and go-routeros
//     handles it — async.go's "cannot find tag for this sentence, ignore".
//
// THE ONE CONDITION THAT CARRIES OVER, AND IT IS NOT OPTIONAL: (4) is handled in
// ASYNC MODE ONLY. Synchronous mode keeps no tag map, so there is nothing for a
// stray sentence to miss against. Dial therefore starts async mode. Without it,
// cancelling a stream would eventually take down a connection every collector
// shares — the original finding, and it still stands.
//
// WHAT IS KNOWINGLY GIVEN UP. go-routeros returns on the first `!done`, and this
// wrapper cannot see block boundaries. If a future RouterOS build, or hardware
// not tested here, really does answer in several blocks, this returns the first
// and looks correct. That risk was measured rather than assumed, and the
// alternative — a 20 ms settle window on every call — cost about 7× on small
// reads (23.1 ms against 3.3 ms for /system/resource/print on the AX3) to defend
// against a behaviour observed on none of the three routers.
//
// The claims above are version-qualified on purpose: they were tested on 7.24
// and the original findings describe 7.23. "Not reproduced" is not "never true".
package routeros

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	ros "github.com/go-routeros/routeros/v3"
)

// Reply is one `!re` sentence: a RouterOS row.
type Reply map[string]string

// Trap is an error the router reported, as opposed to a transport failure.
//
// The distinction is load-bearing: every collector latches "no such command" to
// stop asking for a menu that does not exist, and would latch wrongly if a dead
// connection presented the same way.
type Trap struct {
	Message  string
	Category string
}

func (t *Trap) Error() string {
	if t.Category != "" {
		return "routeros: " + t.Message + " (category " + t.Category + ")"
	}
	return "routeros: " + t.Message
}

// Absent reports whether a trap means "this build does not have that menu",
// which every collector treats as a reason to stop asking rather than an error.
func (t *Trap) Absent() bool {
	m := strings.ToLower(t.Message)
	return strings.Contains(m, "no such command") ||
		strings.Contains(m, "unknown command") ||
		strings.Contains(m, "not supported")
}

// Denied reports whether the API user simply may not see this. The Node
// collectors latch on it exactly as they latch on Absent, because a read-only
// user is the documented deployment.
func (t *Trap) Denied() bool {
	m := strings.ToLower(t.Message)
	return strings.Contains(m, "not enough permissions") ||
		strings.Contains(m, "permission denied") ||
		strings.Contains(m, "no permissions")
}

// Config is what it takes to reach a router.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	TLS      bool
	// InsecureTLS matches the existing deployment: RouterOS ships a self-signed
	// certificate and the Node side sets rejectUnauthorized:false for it.
	InsecureTLS bool
	DialTimeout time.Duration

	// Debug turns on the library's protocol tracing — the port of live's
	// `debug: Settings.load().rosDebug` at `src/index.js:444`, which sets
	// node-routeros's own `debug` flag.
	//
	// ── IT IS A SETTING AN OPERATOR CAN SEE ───────────────────────────────
	//
	// "RouterOS debug" is a checkbox on the Settings page. It was rendered,
	// validated and persisted here and read by NOBODY until 2026-08-29 — the
	// same shape as `topN` and the retention policy, and found by the audit
	// written after those.
	//
	// Label is what the line is attributed to. Live tags the client with
	// `ros.routerLabel = routerCfg.label || routerCfg.host` for exactly this:
	// three routers tracing at once are unreadable otherwise.
	Debug bool
	Label string
}

const defaultDialTimeout = 15 * time.Second

// debugHandler is the slog handler to install, or NIL when tracing is off.
//
// A FUNCTION RATHER THAN AN INLINE `if`, so "off by default" is testable. The
// inline form could be mutated to install the handler unconditionally and no
// test could see it — every router would then trace every sentence to stderr,
// which is a performance and a disclosure problem at once: the sentences carry
// interface names, addresses and the arguments of write commands.
// The WRITER is a parameter so a test can read what the handler produces. It was
// `os.Stderr` inline, and the test for the router label could then only assert
// that a handler existed — which is a test that passes whatever the label says.
func debugHandler(cfg Config, w io.Writer) slog.Handler {
	if !cfg.Debug {
		return nil
	}
	label := cfg.Label
	if label == "" {
		// Live tags the client `routerCfg.label || routerCfg.host`. Three routers
		// tracing at once are unreadable without it.
		label = cfg.Host
	}
	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})
	return h.WithAttrs([]slog.Attr{slog.String("router", label)})
}

// Cmd is one command.
type Cmd struct {
	Path string
	Args []string
	// Timeout bounds a one-shot call. Zero means no bound, which is correct for
	// a stream and wrong for everything else.
	Timeout time.Duration
}

// words is the sentence go-routeros wants: the path, then each argument.
func (c Cmd) words() []string {
	out := make([]string, 0, len(c.Args)+1)
	out = append(out, c.Path)
	out = append(out, c.Args...)
	return out
}

// Client is one connection.
//
// Safe for concurrent use: async mode tags every call and demultiplexes the
// replies, which is the same property that makes a trailing packet for a
// torn-down tag something to discard rather than something to fear.
type Client struct {
	cfg Config
	c   *ros.Client

	mu     sync.Mutex
	closed bool
	// fatal records an error that ended the connection, so Connected() stops
	// claiming a session that is gone.
	fatal error
	// cancel ends the context async mode was started with. See Dial: without it
	// go-routeros parks a goroutine on context.Background() for the life of the
	// process, once per client ever dialled.
	cancel context.CancelFunc
}

// Dial connects, logs in and starts async mode.
func Dial(cfg Config) (*Client, error) {
	timeout := cfg.DialTimeout
	if timeout == 0 {
		timeout = defaultDialTimeout
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	var (
		inner *ros.Client
		err   error
	)
	if cfg.TLS {
		// RouterOS ships a self-signed certificate. InsecureSkipVerify mirrors
		// the Node deployment's rejectUnauthorized:false rather than inventing a
		// stricter policy every existing router would fail.
		inner, err = ros.DialTLSTimeout(addr, cfg.Username, cfg.Password,
			&tls.Config{InsecureSkipVerify: cfg.InsecureTLS}, timeout) //nolint:gosec // see above
	} else {
		inner, err = ros.DialTimeout(addr, cfg.Username, cfg.Password, timeout)
	}
	if err != nil {
		return nil, err
	}

	cl := &Client{cfg: cfg, c: inner}

	// ── PROTOCOL TRACING, OFF UNLESS THE OPERATOR ASKED ───────────────────
	//
	// go-routeros logs every sentence it sends and every tag it assigns at
	// slog's Debug level (`run.go:33`, `listen.go:71`). With no handler
	// installed those calls go to a logger that drops them, so this costs
	// nothing when the setting is off — which is why the guard is on installing
	// the handler rather than inside it.
	//
	// STDERR AND A LEVEL OF Debug, because the default handler's level is Info
	// and would silently discard every line this exists to produce.
	if h := debugHandler(cfg, os.Stderr); h != nil {
		inner.SetLogHandler(h)
	}

	// ASYNC MODE IS NOT A PERFORMANCE CHOICE. It is what gives the client a tag
	// map, and therefore somewhere for a sentence addressed to a cancelled tag
	// to be discarded. In sync mode there is no map, and protocol reality 4 has
	// nothing catching it.
	// ── AND IT IS STARTED WITH A CONTEXT WE CAN CANCEL ────────────────────
	//
	// `Async()` is `AsyncContext(context.Background())`, and `asyncLoop` opens
	// with `go func() { <-ctx.Done(); c.r.Cancel() }()`. On Background that
	// goroutine parks FOR EVER, holding a reference to the client -- so every
	// connection this process ever dialled stayed reachable from a live
	// goroutine stack, and the socket's finalizer could never run even after
	// Close.
	//
	// One parked goroutine per dial is small; unbounded over an uptime measured
	// in weeks, across reconnects, is not. Cancelling in Close ends it and also
	// unblocks the reader, which is the tidier shutdown anyway.
	ctx, cancel := context.WithCancel(context.Background())
	cl.cancel = cancel
	errC := inner.AsyncContext(ctx)
	go func() {
		// Async() closes this channel when the read loop ends. A closed channel
		// with no value is a clean shutdown; a value is what ended it.
		if e, ok := <-errC; ok && e != nil {
			cl.mu.Lock()
			if cl.fatal == nil {
				cl.fatal = e
			}
			cl.mu.Unlock()
		}
	}()

	return cl, nil
}

// Do issues a command and returns every row of the reply.
func (c *Client) Do(cmd Cmd) ([]Reply, error) {
	if err := c.err(); err != nil {
		return nil, err
	}

	ctx := context.Background()
	if cmd.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cmd.Timeout)
		defer cancel()
	}

	reply, err := c.c.RunArgsContext(ctx, cmd.words())
	if err != nil {
		return nil, c.wrap(err)
	}

	out := make([]Reply, 0, len(reply.Re))
	for _, sen := range reply.Re {
		if sen == nil {
			continue
		}
		// The library's map is handed straight through rather than copied. It
		// builds a fresh one per sentence and keeps no reference after the call
		// returns, and nothing downstream mutates a Reply — the collectors read
		// and project. Copying would duplicate every field of every row of a
		// 500-row connection table for no gain.
		out = append(out, Reply(sen.Map))
	}
	return out, nil
}

// Stream subscribes to a /listen or an `=interval=` print, calling onRow for
// every row until stop is called.
//
// stop is idempotent and waits for the delivery goroutine, so a caller that
// stops a stream and then reads its accumulator cannot race a late row into it.
func (c *Client) Stream(cmd Cmd, onRow func(Reply)) (stop func(), err error) {
	return c.StreamUntilDone(cmd, onRow, nil)
}

// StreamUntilDone is Stream, plus notification that the stream ENDED BY ITSELF.
//
// Stream's four callers are all `/listen` subscriptions, which run until they
// are stopped — for them there is no such event and `onDone` would never fire.
// The frequency scan is different: it is a bounded command that finishes, and
// the difference between "it ended" and "we stopped it" decides whether a
// `/cancel` is written to a device that has already finished scanning.
//
// `onDone` fires exactly once, and NOT when stop() is what ended the stream:
// the flag is set inside the same Once that performs the cancel, so a caller
// cannot see both.
func (c *Client) StreamUntilDone(cmd Cmd, onRow func(Reply), onDone func()) (stop func(), err error) {
	if err := c.err(); err != nil {
		return nil, err
	}

	lr, err := c.c.ListenArgs(cmd.words())
	if err != nil {
		return nil, c.wrap(err)
	}

	var stopped atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sen := range lr.Chan() {
			if sen == nil || onRow == nil {
				continue
			}
			onRow(Reply(sen.Map))
		}
		if onDone != nil && !stopped.Load() {
			onDone()
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			// BEFORE the cancel, so the delivery goroutine cannot observe the
			// channel closing and call onDone for a stream the caller stopped.
			stopped.Store(true)
			// The cancel's own reply is discarded. It can legitimately fail —
			// the connection may already be gone — and a stop that reported that
			// would make every teardown path handle an error it can do nothing
			// about.
			_, _ = lr.Cancel()
			<-done
		})
	}, nil
}

// wrap turns a library error into this package's vocabulary.
//
// A DeviceError is the router speaking: `!trap` or `!fatal`, with a message and
// sometimes a category. Everything else is transport, and a transport failure
// ends the connection — recorded so Connected() stops claiming otherwise.
func (c *Client) wrap(err error) error {
	if err == nil {
		return nil
	}
	var de *ros.DeviceError
	if errors.As(err, &de) && de.Sentence != nil {
		return &Trap{
			Message:  de.Sentence.Map["message"],
			Category: de.Sentence.Map["category"],
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("routeros: timed out: %w", err)
	}
	c.mu.Lock()
	if c.fatal == nil {
		c.fatal = err
	}
	c.mu.Unlock()
	return err
}

func (c *Client) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("routeros: connection closed")
	}
	return c.fatal
}

func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	cancel := c.cancel
	c.mu.Unlock()
	// FIRST, so async mode's parked `<-ctx.Done()` goroutine ends and releases
	// its reference to the client. Idempotent: the `closed` guard above means
	// this runs once, and a CancelFunc is safe to call twice anyway.
	if cancel != nil {
		cancel()
	}
	return c.c.Close()
}

// Connected reports whether this client still has a usable session.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && c.fatal == nil
}

// ValidUTF8 reports whether every value in a row is valid UTF-8.
//
// Kept as a conformance check rather than a decode step. Go strings hold bytes
// and this package never transcodes, so a value that is not UTF-8 arrives
// intact — which is exactly what /file/read needs, and what the Node client had
// to work around. This says so out loud instead of leaving it to be
// rediscovered.
func ValidUTF8(r Reply) bool {
	for _, v := range r {
		if !utf8.ValidString(v) {
			return false
		}
	}
	return true
}
