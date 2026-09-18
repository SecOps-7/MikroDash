package routeros

// AN `add` NAMES THE ROW IT MADE, AND THE CALLER CAN HAVE IT.
//
// RouterOS answers `/…/add` with `!done =ret=*ID`. The adapter dropped the done
// sentence, so a write could only find its new row by reading the WHOLE menu
// before and after and diffing the ids — 37,111 rows, 6 s each way, for one
// address-list entry on the operator's router (2026-09-18). Cmd.Ret carries it.

import (
	"net"
	"testing"

	"github.com/go-routeros/routeros/v3/proto"
)

// retRouter logs a client in, then answers every command `!done =ret=*A1`.
func retRouter(t *testing.T) int {
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
		if _, err := r.ReadSentence(); err != nil { // the /login
			return
		}
		w.BeginSentence()
		w.WriteWord("!done")
		_ = w.EndSentence()
		for {
			sen, err := r.ReadSentence()
			if err != nil {
				return
			}
			w.BeginSentence()
			w.WriteWord("!done")
			w.WriteWord("=ret=*A1")
			if sen.Tag != "" {
				w.WriteWord(".tag=" + sen.Tag)
			}
			_ = w.EndSentence()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func TestAnAddsRetReachesTheCaller(t *testing.T) {
	cl := dialFake(t, retRouter(t))
	var ret string
	rows, err := cl.Do(Cmd{Path: "/ip/firewall/address-list/add", Args: []string{"=list=x", "=address=198.51.100.7"}, Ret: &ret})
	if err != nil {
		t.Fatal(err)
	}
	if ret != "*A1" {
		t.Errorf("ret = %q, want *A1 from the !done sentence", ret)
	}
	if len(rows) != 0 {
		t.Errorf("the done sentence was also returned as %d row(s)", len(rows))
	}
	// And a caller that does not ask for it is unaffected.
	if _, err := cl.Do(Cmd{Path: "/ip/firewall/address-list/add"}); err != nil {
		t.Fatal(err)
	}
}
