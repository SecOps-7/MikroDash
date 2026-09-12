package routeros

// THE WATCHER MUST READ THE STREAM EXACTLY AS THE LIBRARY DOES.
//
// `fatalWatch` sits under go-routeros's reader and decodes the same bytes a
// second time, looking for the `!fatal` sentence RouterOS sends before it closes
// a session. go-routeros's async loop discards that sentence (it carries no tag),
// so without this the only record of a router ending the session is the EOF
// that follows.
//
// A watcher that lost its place would report nothing, or report a `!fatal` that
// was never sent. So every case is fed in small chunks, splitting words and
// length prefixes across reads, and the sentence count is checked against the
// library's own reader over the same bytes.

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/go-routeros/routeros/v3/proto"
)

// frames encodes sentences with the library's own writer.
func frames(t *testing.T, sentences ...[]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := proto.NewWriter(&buf)
	for _, words := range sentences {
		w.BeginSentence()
		for _, word := range words {
			w.WriteWord(word)
		}
		if err := w.EndSentence(); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

// chunked hands out its bytes 1 to 7 at a time, so a word or a length prefix is
// split across reads.
type chunked struct {
	r    io.Reader
	size int
}

func (c *chunked) Read(p []byte) (int, error) {
	c.size = c.size%7 + 1
	if len(p) > c.size {
		p = p[:c.size]
	}
	return c.r.Read(p)
}
func (c *chunked) Write(p []byte) (int, error) { return len(p), nil }
func (c *chunked) Close() error                { return nil }

func watchAll(t *testing.T, stream []byte) *fatalWatch {
	t.Helper()
	w := newFatalWatch(&chunked{r: bytes.NewReader(stream)})
	if _, err := io.Copy(io.Discard, w); err != nil {
		t.Fatal(err)
	}
	return w
}

func librarySentences(t *testing.T, stream []byte) int {
	t.Helper()
	r := proto.NewReader(bytes.NewReader(stream))
	n := 0
	for {
		if _, err := r.ReadSentence(); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("the library could not read the test stream: %v", err)
			}
			return n
		}
		n++
	}
}

func TestFatalWatchFindsTheReasonAcrossEveryLengthEncoding(t *testing.T) {
	stream := frames(t,
		[]string{"!re", ".tag=1", "=name=" + strings.Repeat("x", 200)},       // 2-byte length
		[]string{"!re", ".tag=1", "=comment=!fatal is only text here"},       // the word, but not a sentence
		[]string{"!re", ".tag=2", "=big=" + strings.Repeat("y", 30000)},      // 3-byte length
		[]string{"!re", ".tag=2", "=huge=" + strings.Repeat("z", 2_200_000)}, // 4-byte length
		[]string{"!done", ".tag=1"},
		[]string{"!fatal", "=message=session terminated on request"},
	)
	w := watchAll(t, stream)

	if got, want := w.sentencesSeen(), librarySentences(t, stream); got != want {
		t.Fatalf("the watcher counted %d sentences and the library read %d: it has lost its "+
			"place in the stream, so anything it reports is unreliable", got, want)
	}
	reason, seen := w.Fatal()
	if !seen || reason != "session terminated on request" {
		t.Errorf("Fatal() = (%q, %v), want (\"session terminated on request\", true)", reason, seen)
	}
}

// The documentation says only that `!fatal` carries "a reason in a description",
// not in which form, so a bare reason word is accepted as well as `=message=`.
func TestFatalWatchAcceptsABareReasonWord(t *testing.T) {
	w := watchAll(t, frames(t, []string{"!re", ".tag=1", "=a=b"}, []string{"!fatal", "not logged in"}))
	if reason, seen := w.Fatal(); !seen || reason != "not logged in" {
		t.Errorf("Fatal() = (%q, %v), want (\"not logged in\", true)", reason, seen)
	}
}

func TestFatalWatchReportsNothingWhenNoFatalWasSent(t *testing.T) {
	// A VALUE holding the exact bytes of a `!fatal` word, length prefix and all,
	// is still a value. Matching bytes rather than decoding words would fire here.
	fake := "\x06!fatal\x00"
	w := watchAll(t, frames(t,
		[]string{"!re", ".tag=1", "=comment=" + fake},
		[]string{"!done", ".tag=1"},
	))
	if reason, seen := w.Fatal(); seen {
		t.Errorf("Fatal() = (%q, true) for a stream with no !fatal sentence", reason)
	}
}

func TestFatalWatchKeepsAFatalWithNoReasonOrCutShort(t *testing.T) {
	if reason, seen := watchAll(t, frames(t, []string{"!fatal"})).Fatal(); !seen || reason != "" {
		t.Errorf("a reasonless !fatal: Fatal() = (%q, %v), want (\"\", true)", reason, seen)
	}
	// The router closed mid-sentence, before the terminating empty word.
	cut := frames(t, []string{"!fatal", "=message=going away"})
	cut = cut[:len(cut)-1]
	if reason, seen := watchAll(t, cut).Fatal(); !seen || reason != "going away" {
		t.Errorf("a !fatal cut short by the close: Fatal() = (%q, %v), want (\"going away\", true)", reason, seen)
	}
}
