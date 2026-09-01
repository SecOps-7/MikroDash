package server

import (
	"net/http"
	"time"
)

// `GET /healthz` — the port of `src/health.js` plus its route.
//
// ── IT WAS MISSING, AND TWO THINGS DEPENDED ON IT ─────────────────────────
//
//	the container   `docker-compose.yml` runs
//	                `wget -qO- http://127.0.0.1:3081/healthz` as its HEALTHCHECK.
//	                A port that 404s here comes up permanently unhealthy.
//	the app itself  `web/src/pages/settings.ts` and `web/src/account.ts` both
//	                fetch it for the version string. Those were failing silently
//	                — each guards on `d.version`, so a 404 shows a blank instead
//	                of an error.
//
// Found on 2026-08-29 by listing live's modules and asking which have no port
// equivalent. The endpoint audit could not have found it twice over: it
// only looked at `/api` paths, and its wildcard matcher treated Go's `{$}`
// end-of-path anchor as a segment wildcard, so `/{$}` "served" every
// single-segment path. Both are fixed.
//
// ── WHAT MAKES IT OK ──────────────────────────────────────────────────────
//
// `computeHealthStatus` is `startupReady && rosConnected && nothing stale`. The
// port has no per-collector freshness ledger, so the third clause has no
// equivalent yet and is NOT faked: reporting healthy on two of three checks is
// honest; inventing a `stale` array that is always empty would look like the
// third check passing.
func (s *Server) registerHealth(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.healthz)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	connected, activeID := s.activeRouterHealth()
	// STARTING is not FAILING. The live route distinguishes them so an
	// orchestrator does not kill a container that is still dialling: a 503
	// during the grace window is expected, and the body says which it is.
	starting := time.Since(s.startedAt) < healthStartupGrace && !connected
	ok := connected

	code := http.StatusOK
	if !ok {
		code = http.StatusServiceUnavailable
	}

	// ── AN UNAUTHENTICATED CALLER GETS THE STATUS AND NOTHING ELSE ────────
	//
	// The live comment: "version, router ids and collector detail would
	// otherwise be free fingerprinting for anyone who can reach the port." The
	// Docker healthcheck needs only the code and these two flags.
	if _, err := s.auth.Validate(r.Header.Get("Cookie")); err != nil {
		w.WriteHeader(code)
		writeJSON(w, map[string]any{"ok": ok, "starting": starting})
		return
	}

	w.WriteHeader(code)
	writeJSON(w, map[string]any{
		"ok": ok, "starting": starting,
		"routerConnected": connected,
		"activeRouterId":  activeID,
		"startupReady":    !starting,
		"uptime":          time.Since(s.startedAt).Seconds(),
		"now":             time.Now().UnixMilli(),
		"version":         AppVersion,
		// NO `checks` MAP, and that omission IS deliberate: `computeHealthStatus`
		// builds it from a per-collector freshness ledger this port does not
		// have. Reporting healthy on two of three checks is honest; an always-
		// empty `stale` array would look like the third one passing.
	})
}

// AppVersion is what this build reports as the application version.
//
// ── SET TO MATCH THE LIVE APP, ON THE OPERATOR'S INSTRUCTION (2026-08-29) ──
//
// Reported as `version` on /healthz; `web/src/pages/settings.ts` and
// `web/src/account.ts` render `'v' + d.version`, so this is the bare number with
// no `v`.
//
// It is a CONSTANT rather than read from a file. The Node app read it from
// package.json, which no longer exists, and inventing a file to hold one number
// would add a build input for nothing. CLAUDE.md's rule still applies: a bump
// happens only when the operator says package it up, and one bump covers the
// whole session.
//
// 0.8.0 was the cutover release — the first on Go and TypeScript — but it never
// produced an image: its build failed on the newly restored 32-bit ARM target.
// 0.8.1 is that same cutover with the 32-bit fix, and was the first published
// Go image. 0.7.40 was the last on Node.
//
// 0.8.10 SORTS AFTER 0.8.2, and the jump is deliberate rather than a typo: these
// are numbers, not decimals, so ten follows two. Docker tags are strings and
// sort lexically, which is a good reason to be sure the next one is 0.8.11.
//
// ONE DEFINITION. Anything else needing the app version reads this.
const AppVersion = "0.8.10"

// healthStartupGrace matches the live `STARTUP_GRACE_MS`: a container that has
// not finished its first dial is starting, not broken.
const healthStartupGrace = 90 * time.Second

// activeRouterHealth reports whether the router this install is pointed at is
// reachable, and which one that is.
//
// It asks the INTERACTIVE session first and the always-on pool second, because
// those are the two things that hold a connection — and after `internal/alertpool`
// landed, a router nobody is watching is genuinely connected rather than merely
// unknown. Before that this would have read "down" for the whole fleet whenever
// nobody had a browser open, which is exactly the wrong answer for a healthcheck.
func (s *Server) activeRouterHealth() (bool, string) {
	activeID := ""
	if cfg, err := s.mergedSettings(); err == nil {
		activeID, _ = cfg["activeRouterId"].(string)
	}
	if activeID == "" {
		return false, ""
	}
	if s.sessions != nil {
		if live := s.sessions.Live(); live[activeID] != nil {
			return live[activeID].Connected(), activeID
		}
	}
	if s.alertPool != nil {
		if up, known := s.alertPool.Status()[activeID]; known {
			return up, activeID
		}
	}
	return false, activeID
}
