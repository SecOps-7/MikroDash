package server

import (
	"errors"
	"time"
)

// routerWritesPerMinute bounds how fast one user may change one router (#97).
//
// ── SERIALISING IS NOT LIMITING ─────────────────────────────────────────────
//
// `Session.InWriteQueue` is a mutex: it stops two read-check-write sequences
// interleaving, which is correctness. It does nothing about how many run one
// after another, so a single client could change a router as fast as the router
// answered. This is the second control, and the two answer different questions.
//
// Thirty is well above a person working through edits, reorders and undo, and
// low enough that a script cannot hammer a router.
const routerWritesPerMinute = 30

var errWriteRateLimited = errors.New("too many changes to this router in the last minute; wait a moment and try again")

// writeLimitKey is one user on one router. Per user rather than per socket, so a
// second tab is not a second allowance; per router, so a busy router does not
// throttle work on another.
func writeLimitKey(username, routerID string) string {
	return username + "|" + routerID
}

// inWriteQueue is every user-triggered router write: the rate limit, then the
// router's write queue. A refused write never takes the queue.
//
// A Server built without a limiter (a test that does not concern writes) is not
// limited; `New` always builds one.
func (cn *conn) inWriteQueue(fn func() error) error {
	if l := cn.srv.writeLimit; l != nil {
		name := ""
		if cn.sess != nil {
			name = cn.sess.Username
		}
		if ok, _, _ := l.take(writeLimitKey(name, cn.routerID)); !ok {
			return errWriteRateLimited
		}
	}
	return cn.rsession.InWriteQueue(fn)
}

func newWriteLimiter() *rateLimiter {
	return newRateLimiter(routerWritesPerMinute, time.Minute)
}
