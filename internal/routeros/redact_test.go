package routeros

// THE DEBUG TRACE NEVER CARRIES A CREDENTIAL.
//
// go-routeros logs every word it sends when a Debug handler is installed, which
// MikroDash does for the "Debug Logging (ROS_DEBUG)" setting. Every write that
// carried a secret (backup and user passwords, Wi-Fi passphrases, WireGuard keys)
// reached the container log in clear text. Code scanning alert #158; the fix is
// patch 2 in third_party/go-routeros/PATCHES.md.
//
// Both logging paths are driven: RunArgs (a one-shot command) and ListenArgs (a
// stream). The ordinary words must still be there, or a trace that logged
// nothing at all would pass.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	ros "github.com/go-routeros/routeros/v3"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestTracingNeverLogsACredential(t *testing.T) {
	port := slowRouter(t, 0)
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	c, err := ros.NewClient(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login("u", "p"); err != nil {
		t.Fatal(err)
	}
	var trace lockedBuffer
	c.SetLogHandler(slog.NewTextHandler(&trace, &slog.HandlerOptions{Level: slog.LevelDebug}))

	if _, err := c.RunArgs([]string{"/user/set", "=numbers=u1",
		"=password=hunter2-run", "=comment=keep-comment"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.ListenArgsContext(ctx, []string{"/interface/wireguard/peers/listen",
		"=passphrase=hunter2-listen", "=pre-shared-key=hunter2-psk",
		"=preshared-key=hunter2-wg", "=private-key=hunter2-private",
		"=backup-password=hunter2-backup", "=secret=hunter2-ppp",
		"=public-key=keep-public", "=minimum-password-length=8"}); err != nil {
		t.Fatal(err)
	}

	out := trace.String()
	for _, secret := range []string{"hunter2-run", "hunter2-listen", "hunter2-psk",
		"hunter2-wg", "hunter2-private", "hunter2-backup", "hunter2-ppp"} {
		if strings.Contains(out, secret) {
			t.Errorf("the debug trace carries the credential %q:\n%s", secret, out)
		}
	}
	for _, kept := range []string{"/user/set", "=password=***", "keep-comment",
		"/interface/wireguard/peers/listen", "=preshared-key=***", "keep-public",
		"=minimum-password-length=8"} {
		if !strings.Contains(out, kept) {
			t.Errorf("the debug trace lost %q, which is not a credential:\n%s", kept, out)
		}
	}
}
