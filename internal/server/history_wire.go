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
