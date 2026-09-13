package routeros

// tagWatch reads the tag each command goes out with.
//
// ── WHY IT EXISTS ───────────────────────────────────────────────────────────
//
// A command that never gets a reply has to be CANCELLED, and RouterOS cancels by
// tag: `/cancel =tag=<tag>`. go-routeros's async RunArgsContext picks the tag
// itself (`r` and a counter, run.go) and never says what it picked, so the only
// place it can still be read is the bytes on their way to the router.
//
// So the connection is wrapped on the write side too, and each sentence this
// app writes is framed for its `.tag=` word. Like fatalWatch it is a side
// channel: it copies a few short words and changes nothing that is written.
//
// ── WHICH SENTENCE IS WHOSE ─────────────────────────────────────────────────
//
// The library writes a whole sentence under its writer's lock and flushes it
// once (proto/writer.go), so sentences reach the connection whole and in order.
// That says nothing about which caller wrote one; `Client.send` does, by letting
// only one command be written at a time.

import (
	"bytes"
	"io"
	"sync"
)

// tagCap bounds the words copied while looking for a tag. The library's tags are
// a letter and a counter; a longer word is counted past unread.
const tagCap = 64

var tagPrefix = []byte(".tag=")

type tagWatch struct {
	io.ReadWriteCloser

	mu  sync.Mutex
	out framer
	tag string // of the sentence in progress

	// sent receives the tag of every sentence written, "" for one without.
	// Buffered and never blocked on: a sentence nobody waits for, like the login,
	// must not stall the writer. `Client.send` empties it before each command.
	sent      chan string
	sentences int
}

func newTagWatch(rwc io.ReadWriteCloser) *tagWatch {
	w := &tagWatch{ReadWriteCloser: rwc, sent: make(chan string, 8)}
	w.out = framer{
		max: tagCap,
		// Never the first word: that is the command's path.
		keep: func(index int, n int64) bool { return index > 0 && n <= tagCap },
		onWord: func(_ int, word []byte) {
			if bytes.HasPrefix(word, tagPrefix) {
				w.tag = string(word[len(tagPrefix):])
			}
		},
		onSentence: func() {
			w.sentences++
			select {
			case w.sent <- w.tag:
			default:
			}
			w.tag = ""
		},
	}
	return w
}

// Write passes this app's bytes through untouched, noting them on the way.
func (w *tagWatch) Write(p []byte) (int, error) {
	n, err := w.ReadWriteCloser.Write(p)
	if n > 0 {
		w.mu.Lock()
		w.out.feed(p[:n])
		w.mu.Unlock()
	}
	return n, err
}

// forget drops the tags of sentences nobody claimed, so the next tag read is the
// next sentence written.
func (w *tagWatch) forget() {
	for {
		select {
		case <-w.sent:
		default:
			return
		}
	}
}

// sentencesSeen is how many complete sentences were framed. A test compares it
// with the library's own reader to prove the two agree on where words fall.
func (w *tagWatch) sentencesSeen() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sentences
}
