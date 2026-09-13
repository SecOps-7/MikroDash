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
// login among it — never does.

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

	// Length prefix in progress: extra bytes still to read, and the value so far.
	lenExtra int
	lenHigh  int64
	lenShift uint
	lenAcc   int64

	// Word content in progress.
	inWord   bool
	wordLeft int64
	word     []byte
	keep     bool

	// The sentence in progress.
	wordIndex  int
	isFatal    bool
	reason     string
	haveReason bool

	sentences  int
	seen       bool
	seenReason string
}

func newFatalWatch(rwc io.ReadWriteCloser) *fatalWatch {
	return &fatalWatch{ReadWriteCloser: rwc}
}

// Read passes the router's bytes through untouched, noting them on the way.
func (w *fatalWatch) Read(p []byte) (int, error) {
	n, err := w.ReadWriteCloser.Read(p)
	if n > 0 {
		w.mu.Lock()
		w.feed(p[:n])
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

func (w *fatalWatch) feed(p []byte) {
	for len(p) > 0 {
		if w.inWord {
			k := w.wordLeft
			if int64(len(p)) < k {
				k = int64(len(p))
			}
			if w.keep {
				room := fatalReasonCap - len(w.word)
				if room > int(k) {
					room = int(k)
				}
				if room > 0 {
					w.word = append(w.word, p[:room]...)
				}
			}
			p = p[k:]
			w.wordLeft -= k
			if w.wordLeft == 0 {
				w.inWord = false
				w.endWord()
			}
			continue
		}

		b := int64(p[0])
		p = p[1:]
		if w.lenExtra > 0 {
			w.lenAcc = w.lenAcc<<8 | b
			w.lenExtra--
			if w.lenExtra == 0 {
				w.beginWord(w.lenHigh<<w.lenShift | w.lenAcc)
			}
			continue
		}
		// The first byte of a length, decoded as proto/reader.go's readLength does.
		switch {
		case b&0x80 == 0x00:
			w.beginWord(b)
		case b&0xC0 == 0x80:
			w.startLength(b&0x3F, 1)
		case b&0xE0 == 0xC0:
			w.startLength(b&0x1F, 2)
		case b&0xF0 == 0xE0:
			w.startLength(b&0x0F, 3)
		case b&0xF8 == 0xF0:
			w.startLength(0, 4)
		default:
			// A reserved control byte. The library reads the byte itself as the
			// length, and so does this, so the two stay in step.
			w.beginWord(b)
		}
	}
}

func (w *fatalWatch) startLength(high int64, extra int) {
	w.lenHigh, w.lenExtra, w.lenShift, w.lenAcc = high, extra, uint(8*extra), 0
}

func (w *fatalWatch) beginWord(n int64) {
	if n == 0 {
		w.endSentence()
		return
	}
	w.inWord, w.wordLeft, w.word = true, n, w.word[:0]
	// Only two kinds of word are ever copied: a first word that could be
	// `!fatal`, and the words of a sentence that is.
	w.keep = (w.wordIndex == 0 && n == int64(len(fatalWord))) || (w.isFatal && !w.haveReason)
}

func (w *fatalWatch) endWord() {
	if w.wordIndex == 0 {
		w.isFatal = w.keep && string(w.word) == fatalWord
	} else if w.isFatal && !w.haveReason {
		word := string(w.word)
		switch {
		case strings.HasPrefix(word, "=message="):
			w.reason, w.haveReason = word[len("=message="):], true
		case strings.HasPrefix(word, "=") || strings.HasPrefix(word, "."):
			// Another attribute, or a tag: not the reason.
		default:
			w.reason, w.haveReason = word, true
		}
	}
	w.wordIndex++
}

func (w *fatalWatch) endSentence() {
	w.sentences++
	if w.isFatal {
		w.seen, w.seenReason = true, w.reason
	}
	w.wordIndex, w.isFatal, w.reason, w.haveReason = 0, false, "", false
}
