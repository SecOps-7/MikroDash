package routeros

// THE TAG WATCHER MUST FRAME WHAT THIS APP WRITES EXACTLY AS THE ROUTER DOES.
//
// `/cancel` names a command by the tag tagWatch read off the wire. A watcher
// that lost its place would hand back the wrong tag, cancelling some other
// command while the stuck one kept its router slot. So, like fatalWatch's tests,
// the stream is written in small chunks across every length encoding, and the
// sentence count is checked against the library's own reader.

import (
	"bytes"
	"strings"
	"testing"
)

// written is a connection that keeps every byte written to it.
type written struct{ bytes.Buffer }

func (w *written) Close() error { return nil }

func TestTagWatchReadsEachSentencesTagAcrossEveryLengthEncoding(t *testing.T) {
	stream := frames(t,
		[]string{"/login", "=name=u", "=password=p"}, // no tag at all
		[]string{"/ip/address/print", ".tag=r1"},
		[]string{"/x", "=comment=" + strings.Repeat("x", 200), ".tag=r2"},    // 2-byte length
		[]string{"/x", "=comment=.tag=r99 is only text here", ".tag=l3"},     // the prefix inside a value
		[]string{"/x", "=big=" + strings.Repeat("y", 30000), ".tag=r4"},      // 3-byte length
		[]string{"/x", "=huge=" + strings.Repeat("z", 2_200_000), ".tag=r5"}, // 4-byte length
		[]string{"/cancel", "=tag=r4", ".tag=r6"},
	)
	conn := &written{}
	w := newTagWatch(conn)
	for rest, size := stream, 0; len(rest) > 0; {
		size = size%7 + 1
		n := size
		if n > len(rest) {
			n = len(rest)
		}
		if _, err := w.Write(rest[:n]); err != nil {
			t.Fatal(err)
		}
		rest = rest[n:]
	}

	if !bytes.Equal(conn.Bytes(), stream) {
		t.Fatal("the watcher changed what was written")
	}
	if got, want := w.sentencesSeen(), librarySentences(t, stream); got != want {
		t.Fatalf("the watcher counted %d sentences and the library read %d: it has lost its "+
			"place, so a tag it reports may belong to another command", got, want)
	}
	for i, want := range []string{"", "r1", "r2", "l3", "r4", "r5", "r6"} {
		select {
		case got := <-w.sent:
			if got != want {
				t.Errorf("sentence %d went out tagged %q and the watcher read %q", i, want, got)
			}
		default:
			t.Fatalf("the watcher reported %d sentence(s), and 7 were written", i)
		}
	}
}
