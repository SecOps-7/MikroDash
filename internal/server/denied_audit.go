package server

// Every refused write is recorded in the audit trail (the operator's ruling of
// 2026-11-20, docs/DECISIONS.md).
//
// ── WHY A MIDDLEWARE ────────────────────────────────────────────────────────
//
// About twenty socket handlers record their own refusals, and so do some HTTP
// handlers. Most HTTP write routes answered 401 or 403 before anything was
// recorded: a refused `DELETE /api/routers/<id>` left no row. Adding a call to
// each of them would be many places to forget one; this records the refusal
// for every POST, PUT, PATCH and DELETE that ends in 401 or 403, in one place.
//
// ── NEVER TWICE ─────────────────────────────────────────────────────────────
//
// A handler that records its own refusal through `httpRecorder` marks the
// request (audit.Recorder.OnDenied), and the middleware then writes nothing.
// The routes in deniedAuditExempt keep their own records under their own
// rules. Every row goes through the server's DeniedLimiter, so a client
// cannot flood the trail.

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"

	"mikrodash/internal/audit"
	"mikrodash/internal/ztp"
)

// deniedAuditExempt: write routes whose refusals are recorded elsewhere, by
// pattern, with the reason. A test fails if an entry names no registered
// route, so the list cannot outlive the routes it excuses.
var deniedAuditExempt = map[string]string{
	"POST /api/auth/login": "a failed sign-in is recorded by the login route " +
		"(auth.login, outcome denied) under its own rate limit",
	"POST " + ztp.LANPath: "an enrolment refusal is recorded by the enrolment " +
		"route itself (ztp.enrol.denied), with the device it names",
}

type deniedMarkKey struct{}

type deniedMark struct{ done atomic.Bool }

// deniedMarkOf returns the callback a recorder runs once it has handled a
// refusal for this request, or nil outside the middleware.
func deniedMarkOf(r *http.Request) func() {
	if r == nil {
		return nil
	}
	m, _ := r.Context().Value(deniedMarkKey{}).(*deniedMark)
	if m == nil {
		return nil
	}
	return func() { m.done.Store(true) }
}

func isWriteMethod(m string) bool {
	return m == http.MethodPost || m == http.MethodPut || m == http.MethodPatch || m == http.MethodDelete
}

// deniedAudit records a refused write that its handler did not record.
func (s *Server) deniedAudit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isWriteMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		mark := &deniedMark{}
		r = r.WithContext(context.WithValue(r.Context(), deniedMarkKey{}, mark))
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status != http.StatusUnauthorized && sw.status != http.StatusForbidden {
			return
		}
		if mark.done.Load() {
			return
		}
		if _, exempt := deniedAuditExempt[r.Pattern]; exempt {
			return
		}
		s.deniedRecorder(r).Denied(audit.Event{
			Action: r.Pattern, TargetType: "route", TargetID: r.URL.Path,
			Note: "HTTP " + strconv.Itoa(sw.status),
		})
	})
}

// deniedRecorder is the recorder for a refused request: the signed-in user if
// there is one, otherwise "anonymous" at the client's address.
func (s *Server) deniedRecorder(r *http.Request) *audit.Recorder {
	if s.auth != nil {
		if sess, err := s.auth.Validate(r.Header.Get("Cookie")); err == nil && sess != nil {
			return s.httpRecorder(r, sess)
		}
	}
	var sink audit.Sink
	if s.auditDB != nil {
		sink = auditSink{s.auditDB}
	}
	actor := audit.Actor{Name: "anonymous", IP: audit.NormalizeIP(clientIPOf(r))}
	return audit.New(sink, actor, nowMillis).WithDeniedLimit(s.deniedLimit)
}

// statusWriter remembers the status a handler sent.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}
