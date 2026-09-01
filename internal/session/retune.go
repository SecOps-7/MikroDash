package session

import (
	"mikrodash/internal/collection"
	"mikrodash/internal/store"
)

// Applying a settings save to the collectors that are already running.
//
// `collection.PollRetunes` decides WHICH collectors change and to WHAT; this is
// the half that reaches them. The two are separate because the decision is pure
// and gated against the live route's own table, and this is nothing but a
// dispatch.

// pollTargets maps the live route's collector names to this session's fields.
//
// ── IT IS BUILT PER CALL, AND THAT IS NOT WASTE ─────────────────────────────
//
// A collector is nil until `Acquire` has connected once, and several are built
// only on page focus or reconnect. A map built at construction would capture
// nils and silently drop the re-tune for exactly the collectors a busy session
// has and an idle one does not.
//
// The names are the LIVE ones (`conns`, `ifStatus`, `dhcpNetworks`), not this
// package's field names. `collect`'s own ledger asserts every name in the lifted
// `pollMap` has a `SetPollMs`; this asserts the session can find it.
func (s *Session) pollTargets() map[string]interface{ SetPollMs(int) } {
	out := map[string]interface{ SetPollMs(int) }{}
	add := func(name string, c interface{ SetPollMs(int) }) {
		// A TYPED NIL IS NOT NIL. `s.dns` is a `*collect.DNS`, and putting one
		// that happens to be nil into an interface gives a non-nil interface
		// holding a nil pointer — which would pass a `!= nil` test here and
		// panic on the call. Each caller below passes the concrete pointer and
		// The nil check happens there, where the type is still concrete.
		out[name] = c
	}
	if s.bandwidth != nil {
		add("bandwidth", s.bandwidth)
	}
	if s.bridges != nil {
		add("bridges", s.bridges)
	}
	if s.capsman != nil {
		add("capsman", s.capsman)
	}
	if s.conns != nil {
		add("conns", s.conns)
	}
	if s.dhcpNetworks != nil {
		add("dhcpNetworks", s.dhcpNetworks)
	}
	if s.dns != nil {
		add("dns", s.dns)
	}
	if s.firewall != nil {
		add("firewall", s.firewall)
	}
	if s.ifStatus != nil {
		add("ifStatus", s.ifStatus)
	}
	if s.packages != nil {
		add("packages", s.packages)
	}
	if s.ping != nil {
		add("ping", s.ping)
	}
	if s.ppp != nil {
		add("ppp", s.ppp)
	}
	if s.queues != nil {
		add("queues", s.queues)
	}
	if s.rosUsers != nil {
		add("rosusers", s.rosUsers)
	}
	if s.routing != nil {
		add("routing", s.routing)
	}
	if s.system != nil {
		add("system", s.system)
	}
	if s.talkers != nil {
		add("talkers", s.talkers)
	}
	if s.topology != nil {
		add("topology", s.topology)
	}
	if s.vlans != nil {
		add("vlans", s.vlans)
	}
	if s.vpn != nil {
		add("vpn", s.vpn)
	}
	if s.wan != nil {
		add("wan", s.wan)
	}
	if s.wifi != nil {
		add("wifi", s.wifi)
	}
	if s.wireless != nil {
		add("wireless", s.wireless)
	}
	return out
}

// ApplyPollRetunes applies one settings save to this session's collectors.
//
// ── THE OVERRIDES ARE THIS ROUTER'S, WHICH IS THE WHOLE POINT ───────────────
//
// `PollRetunes` refuses to apply a key this router has pinned (#105): the value
// is still SAVED to the file, it is just not applied here. So the overrides have
// to come from the session, not from the caller — a server-wide save has one set
// of updates and as many override sets as it has live routers, and passing nil
// would silently un-pin every one of them.
//
// A collector this session has not built is skipped rather than treated as an
// error: several exist only after a page focus, and a save must not depend on
// which pages happen to be open.
func (s *Session) ApplyPollRetunes(updates, saved store.Settings) []string {
	targets := s.pollTargets()
	applied := []string{}
	for _, r := range collection.PollRetunes(updates, saved, s.eff.Overrides) {
		c, ok := targets[r.Collector]
		if !ok {
			continue
		}
		// KeepCurrent means the stored value was not a finite number, so the
		// collector's existing period stands. Calling SetPollMs with a zero here
		// would set it to the floor instead of leaving it alone.
		if r.KeepCurrent {
			continue
		}
		c.SetPollMs(r.PollMs)
		applied = append(applied, r.Collector)
	}
	return applied
}

// ApplyPollRetunes fans one save out to every live session.
//
// Each resolves against its OWN overrides, so a router that pinned an interval
// keeps it while the rest of the fleet moves.
func (m *Manager) ApplyPollRetunes(updates, saved store.Settings) map[string][]string {
	m.mu.Lock()
	all := make([]*Session, 0, len(m.live))
	for _, s := range m.live {
		all = append(all, s)
	}
	m.mu.Unlock()

	out := map[string][]string{}
	for _, s := range all {
		if applied := s.ApplyPollRetunes(updates, saved); len(applied) > 0 {
			out[s.RouterID] = applied
		}
	}
	return out
}
