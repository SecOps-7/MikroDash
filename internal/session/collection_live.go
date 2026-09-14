package session

import (
	"reflect"

	"mikrodash/internal/collection"
)

// ApplyCollection hands a router's saved collection config to its live session,
// without reconnecting: collectors switched off are suspended, collectors
// switched on are resumed through `ResumeCollector` (so demand and dormancy still
// decide whether they run now), and a changed poll interval is applied to the
// collector. It reports whether anything changed, so the caller re-announces only
// a real change. A router with no live session has nothing to update; the next
// `Acquire` resolves the saved config.
//
// Before this, `eff` was resolved once, and the device dialog's collector
// switches did nothing to a running session.
func (m *Manager) ApplyCollection(routerID string, next collection.Resolved) bool {
	m.mu.Lock()
	s, ok := m.live[routerID]
	m.mu.Unlock()
	if !ok {
		return false
	}
	return s.applyCollection(next)
}

func (s *Session) applyCollection(next collection.Resolved) bool {
	prev := s.conf()
	if reflect.DeepEqual(*prev, next) {
		return false
	}
	// STORED FIRST, so `ResumeCollector` below reads the new switches, and a page
	// focus arriving during the loop cannot resume a collector just turned off.
	s.eff.Store(&next)

	// EVERY SWITCHABLE COLLECTOR IS IN targetKeys, which is also the table
	// `SuspendCollector` and `ResumeCollector` act through.
	for _, key := range targetKeys {
		was, now := enabledIn(prev, key), enabledIn(&next, key)
		switch {
		case was && !now:
			s.SuspendCollector(key)
		case !was && now:
			s.ResumeCollector(key)
		}
	}

	targets := s.pollTargets()
	for key, ms := range next.Poll {
		if prev.Poll[key] == ms {
			continue
		}
		if c, ok := targets[key]; ok {
			c.SetPollMs(ms)
		}
	}
	return true
}

// enabledIn matches `CollectorEnabled`: an unknown key reads as enabled.
func enabledIn(r *collection.Resolved, key string) bool {
	v, ok := r.Enabled[key]
	return !ok || v
}
