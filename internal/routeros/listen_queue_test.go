package routeros

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-routeros/routeros/v3/proto"
)

// burstRouter logs a client in, answers a `listen` with five rows at once, and
// every other command with `!done` at once.
func burstRouter(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r, w := proto.NewReader(c), proto.NewWriter(c)
		say := func(words ...string) {
			w.BeginSentence()
			for _, wd := range words {
				w.WriteWord(wd)
			}
			_ = w.EndSentence()
		}
		if _, err := r.ReadSentence(); err != nil { // the /login
			return
		}
		say("!done")
		for {
			sen, err := r.ReadSentence()
			if err != nil {
				return
			}
			if strings.HasSuffix(sen.Word, "/listen") {
				for i := 0; i < 5; i++ {
					say("!re", "=name=row", ".tag="+sen.Tag)
				}
				continue
			}
			if sen.Word == "/cancel" {
				// As a router does: the cancelled command ends, then the cancel.
				say("!trap", "=category=2", "=message=interrupted", ".tag="+sen.Map["tag"])
				say("!done", ".tag="+sen.Map["tag"])
			}
			say("!done", ".tag="+sen.Tag)
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

// TestASlowStreamConsumerDoesNotStallTheConnection. The listener channel was
// unbuffered, so the connection's single reader goroutine could not hand a
// stream its next row until `onRow` returned from the last one; while it
// waited, no reply for any other command on the connection was read. A slow
// consumer (a hub emit, a collector lock) stalled every poll on that router
// (review loop).
func TestASlowStreamConsumerDoesNotStallTheConnection(t *testing.T) {
	cl := dialFake(t, burstRouter(t))
	stop, err := cl.Stream(Cmd{Path: "/interface/listen"}, func(Reply) { time.Sleep(200 * time.Millisecond) })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	time.Sleep(50 * time.Millisecond) // the burst has arrived; the consumer is on row one

	start := time.Now()
	if _, err := cl.Do(Cmd{Path: "/system/identity/print", Timeout: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 150*time.Millisecond {
		t.Errorf("a command took %v behind a slow stream consumer: the reader was stalled", took)
	}
}
