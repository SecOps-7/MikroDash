package server

import (
	"log"

	"mikrodash/internal/historywire"
)

// The history recorder's construction — the other half of cutover step 0,
// built to the same standard as `buildBackupScheduler`.
//
// ── OFF UNLESS SWITCHED ON, AND THE REASON IS ARITHMETIC ───────────────────
//
// Two processes bucketing the same per-second samples into one SQLite file
// write TWO rows per minute per interface, and Reports averages BY MINUTE. The
// result is not a broken chart — it is a plausible chart with wrong numbers,
// which nobody would think to check.
//
// So `-history` defaults false and this returns a DISABLED wire rather than
// nil. That is deliberate: a disabled wire is a real object whose methods are
// reached and do nothing, so the call sites in `session.go` are exercised on
// every run instead of only after the flag flips. A nil would have made the
// window the first time that code ever executed.
func (s *Server) buildHistoryWire(enabled bool) *historywire.Wire {
	if s.auditDB == nil {
		if enabled {
			log.Printf("[history] -history was passed but there is no database; " +
				"nothing will be recorded")
		}
		return nil
	}
	if !enabled {
		log.Printf("[history] recording off; traffic, ping and connectivity history " +
			"will not be written (pass -history to enable)")
		return historywire.New(false, s.auditDB)
	}
	log.Printf("[history] recording on — traffic, ping and connectivity history is being written")
	return historywire.New(true, s.auditDB)
}

// buildCoverage is the monitoring-coverage writer, under the same switch and
// over the same database as the history wire: "monitored" on the Devices page's
// connectivity strip means "recorded", so an install that records nothing has no
// coverage either and its strips are honestly grey. Nil without a database,
// and nil is inert.
func (s *Server) buildCoverage(enabled bool) *historywire.Coverage {
	if s.auditDB == nil {
		return nil
	}
	return historywire.NewCoverage(enabled, s.auditDB)
}

// coveredRouters is every router OBSERVED right now: held by a session whose
// state the debounce has judged. A session that exists but whose first dial has
// not been judged is not yet observing anything, and a disabled router has no
// session at all - both are exactly the time the strip must draw grey.
func (s *Server) coveredRouters() map[string]bool {
	out := map[string]bool{}
	if s.sessions == nil || s.connTrack == nil {
		return out
	}
	for id := range s.sessions.Live() {
		if _, known := s.connTrack.Online(id); known {
			out[id] = true
		}
	}
	return out
}
