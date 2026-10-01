package session

import (
	"mikrodash/internal/collect"
)

// What the Devices page reads from a session it is not otherwise driving.
//
// ── THERE USED TO BE A WHOLE SNAPSHOT TYPE HERE ─────────────────────────────
//
// `Snapshot` and `Manager.Snapshots` carried a session's reading to the Devices
// page's BACKGROUND half, which merged it into the overview pool's summaries for
// routers the pool held. The pool was deleted on 2026-10-01 and every enabled
// router is held WARM now, so the page reads every session directly through
// `Manager.Live` and the merge had nothing left to merge. What the snapshot did
// that the plain read does not was one thing - fall back to the primed reading
// when the session's own collector has none - and that is this method.

// SystemOrPrimed is the session's system reading: its own collector's when it
// has one, otherwise the one-shot reading `PrimeStats` took on the open socket.
//
// THE PRIME IS READ HERE OR NOWHERE. A warm session's system collector holds
// nothing until the `devices` hold starts it and it ticks, and the prime is the
// only thing standing between that gap and a card with a green badge over blank
// gauges. Nil when neither exists.
func (s *Session) SystemOrPrimed() *collect.SystemPayload {
	if p := s.systemReading(); p != nil {
		return p
	}
	return s.primedSystem()
}

// Status is connected-or-not per router, for /healthz and the Devices page.
func (m *Manager) Status() map[string]bool {
	live := m.Live()
	out := make(map[string]bool, len(live))
	for id, s := range live {
		out[id] = s.Connected()
	}
	return out
}
