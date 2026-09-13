package routeros

// fatalWatch catches the reason a router gives for ending a session.
//
// ── WHY IT EXISTS ───────────────────────────────────────────────────────────
//
// "If the API connection must be closed, RouterOS sends a `!fatal` with a
// reason in a description and then closes the connection" (the RouterOS API
// documentation). go-routeros's async loop routes each sentence by its `.tag`,
// and a `!fatal` for the whole session has none — so the library discards it
// (async.go: "cannot find tag for this sentence, ignore") and all that reaches
// this package is the EOF that follows. On 2026-09-12 that EOF was the logged
// reason for all 60 of a day's drops across four routers, with nothing to say
// whether the router had given a reason.
//
// So the connection is wrapped, and the bytes the router sends are decoded a
// second time, beside the library, for that one sentence. It is a side channel:
// nothing here can change what the library reads.
//
// ── IT DECODES WORDS, IT DOES NOT MATCH BYTES ───────────────────────────────
//
// A value can hold anything, including the literal bytes of a `!fatal` word. So
// the stream is framed exactly as `proto/reader.go`'s readLength frames it, and a
// sentence is `!fatal` only when its FIRST WORD is. Only those sentences' words
// are ever copied; every other word's bytes are counted past, not kept.
//
// Only bytes the ROUTER sends pass through here. What this app writes — the
// login among it — never does; tagWatch reads that side, for a different word.
// The framing itself is framer's, shared by both.

import (
	"io"
	"strings"
	"sync"
)

const (
	fatalWord = "!fatal"
	// fatalReasonCap bounds what is kept of a reason. RouterOS sends a short
	// sentence; a router sending more is not owed the memory.
	fatalReasonCap = 512
)

type fatalWatch struct {
	io.ReadWriteCloser

	mu sync.Mutex
	in framer

	// The sentence in progress.
	isFatal    bool
	reason     string
	haveReason bool

	sentences  int
	seen       bool
	seenReason string
}

func newFatalWatch(rwc io.ReadWriteCloser) *fatalWatch {
	w := &fatalWatch{ReadWriteCloser: rwc}
	w.in = framer{
		max: fatalReasonCap,
		// Only two kinds of word are ever copied: a first word that could be
		// `!fatal`, and the words of a sentence that is.
		keep: func(index int, n int64) bool {
			return (index == 0 && n == int64(len(fatalWord))) || (w.isFatal && !w.haveReason)
		},
		onWord:     w.endWord,
		onSentence: w.endSentence,
	}
	return w
}

// Read passes the router's bytes through untouched, noting them on the way.
func (w *fatalWatch) Read(p []byte) (int, error) {
	n, err := w.ReadWriteCloser.Read(p)
	if n > 0 {
		w.mu.Lock()
		w.in.feed(p[:n])
		w.mu.Unlock()
	}
	return n, err
}

// Fatal is the reason from the last `!fatal` the router sent, and whether it
// sent one. A `!fatal` cut short by the close still counts: the router said it
// was ending the session, and gave as much of the reason as arrived.
func (w *fatalWatch) Fatal() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.isFatal {
		return w.reason, true
	}
	return w.seenReason, w.seen
}

// sentencesSeen is how many complete sentences were framed. A test compares it
// with the library's own reader to prove the two agree on where words fall.
func (w *fatalWatch) sentencesSeen() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sentences
}

func (w *fatalWatch) endWord(index int, word []byte) {
	if index == 0 {
		// An uncopied first word arrives as nil, which is not `!fatal`.
		w.isFatal = string(word) == fatalWord
		return
	}
	if !w.isFatal || w.haveReason {
		return
	}
	s := string(word)
	switch {
	case strings.HasPrefix(s, "=message="):
		w.reason, w.haveReason = s[len("=message="):], true
	case strings.HasPrefix(s, "=") || strings.HasPrefix(s, "."):
		// Another attribute, or a tag: not the reason.
	default:
		w.reason, w.haveReason = s, true
	}
}

func (w *fatalWatch) endSentence() {
	w.sentences++
	if w.isFatal {
		w.seen, w.seenReason = true, w.reason
	}
	w.isFatal, w.reason, w.haveReason = false, "", false
}
