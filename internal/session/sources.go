package session

// Who asked the router for a command, for the API Diagnostics card.
//
// ── ONE CHOKE POINT, AND THE CALLER IS READ OFF THE STACK ───────────────────
//
// Every command this process sends a router passes through this package: the
// collectors through `reader.Do` and `reader.Stream`, every page and feature
// through `Session.Exec` and `Session.StreamUntilDone`. Counting there covers a
// feature added tomorrow without it doing anything, which a counting call per
// feature would not.
//
// ATTRIBUTION needs one more fact, the feature that asked, and the obvious way to
// get it (a label on every call) is eighty-odd edits that the next feature forgets
// to make. So the two feature entry points read their caller's file off the stack
// instead, and the server, which owns those files, names them (`SetSourceNamer`).
// The OUTERMOST named frame wins, because that is who started the work: the AI
// agent's writes run through the resource write path, and they are the agent's.
//
// It costs one `runtime.Callers` per on-demand command, which is microseconds
// against a network round trip, and no RouterOS command at all.

import (
	"runtime"
	"sync/atomic"
)

// SourceCollectors is every read the collector layer makes: the cache's polls
// and stream fills, the priming reads, the ping and log channels.
const SourceCollectors = "Collectors"

// SourceOther is an on-demand command whose caller no namer recognised. The
// server's ledger (TestEveryRouterCallerHasASource) keeps it empty.
const SourceOther = "Other"

var sourceNamer atomic.Pointer[func(file string) string]

// SetSourceNamer installs the function that names a source file's feature, or
// returns "" for a file that is not one. Called once, by the server.
func SetSourceNamer(fn func(file string) string) { sourceNamer.Store(&fn) }

// callerSource names the feature whose code called into this package.
func callerSource() string {
	p := sourceNamer.Load()
	if p == nil {
		return SourceOther
	}
	var pcs [48]uintptr
	// Skip runtime.Callers, callerSource and the exported entry point.
	n := runtime.Callers(3, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	src := ""
	for {
		f, more := frames.Next()
		if s := (*p)(f.File); s != "" {
			src = s // innermost first, so the last one named is the outermost
		}
		if !more {
			break
		}
	}
	if src == "" {
		return SourceOther
	}
	return src
}
