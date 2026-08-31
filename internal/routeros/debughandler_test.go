package routeros

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// TRACING IS OFF UNLESS ASKED FOR.
//
// The guard used to be an inline `if cfg.Debug` around `SetLogHandler`, which no
// test could reach: mutating it to install the handler unconditionally left every
// test green. That matters more than a missing log line. Every sentence
// go-routeros sends would go to stderr for every router, continuously —
// interface names, addresses and the arguments of write commands included — so
// the failure is a performance problem and a disclosure one at once.
func TestTracingIsOffUnlessEnabled(t *testing.T) {
	if h := debugHandler(Config{Host: "198.51.100.1"}, io.Discard); h != nil {
		t.Error("a config with Debug unset installed a log handler; every router would " +
			"trace every sentence to stderr")
	}
	if h := debugHandler(Config{Host: "198.51.100.1", Debug: false}, io.Discard); h != nil {
		t.Error("Debug:false installed a log handler")
	}
	if h := debugHandler(Config{Host: "198.51.100.1", Debug: true}, io.Discard); h == nil {
		t.Fatal("Debug:true installed no handler, so the setting does nothing — " +
			"which is the state this was written to fix")
	}
}

// THE TRACE SAYS WHICH ROUTER IT CAME FROM, and this reads the actual output.
//
// Live tags the client `routerCfg.label || routerCfg.host`: three routers
// tracing at once are unreadable otherwise, and the fallback is what keeps a
// router with no label attributable.
//
// An earlier version of this test asserted only that a handler existed, which
// would have passed with the label dropped, the fallback missing, or the
// attribute named something else.
func TestTheTraceIsAttributedToARouter(t *testing.T) {
	for _, c := range []struct{ name, label, host, want string }{
		{"the label when it has one", "hAP AX3", "198.51.100.1", "hAP AX3"},
		{"the host when it does not", "", "198.51.100.1", "198.51.100.1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			h := debugHandler(Config{Debug: true, Label: c.label, Host: c.host}, &buf)
			if h == nil {
				t.Fatal("no handler")
			}
			slog.New(h).Debug("RunArgsContext", slog.String("sentence", "/system/resource/print"))
			out := buf.String()
			if !strings.Contains(out, `router=`) {
				t.Fatalf("the trace carries no router attribute: %q", out)
			}
			if !strings.Contains(out, c.want) {
				t.Errorf("the trace is attributed to something other than %q: %q", c.want, out)
			}
		})
	}
}

// AND IT LOGS AT DEBUG LEVEL, which is the level go-routeros uses.
//
// `slog`'s default handler level is Info, so a handler built without an explicit
// level would silently discard every line this feature exists to produce — the
// setting would be on, the handler installed, and nothing written.
func TestTheTraceHandlerAcceptsDebugLevel(t *testing.T) {
	var buf bytes.Buffer
	h := debugHandler(Config{Debug: true, Host: "198.51.100.1"}, &buf)
	slog.New(h).Debug("set tag", slog.String("tag", "r1"))
	if buf.Len() == 0 {
		t.Error("a Debug-level line was dropped. go-routeros logs every sentence at " +
			"Debug (run.go:33, listen.go:71), so the setting would do nothing.")
	}
}
