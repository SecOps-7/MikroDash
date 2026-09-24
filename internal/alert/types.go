package alert

// The catalogue of everything a notification channel can subscribe to.
//
// ── WHY THIS FILE EXISTS ────────────────────────────────────────────────────
//
// Until notification channels, nothing needed a LIST of alert types: each rule
// wrote its own display string into `Fired.AlertType` and `StoredType` derived
// the stored key at the moment it was recorded. That is fine for a producer and
// useless for a consumer — the channel modal has to render every event with a
// toggle beside it, and a modal cannot read Go string literals.
//
// So the list is written down once, here, and `internal/verify` checks it
// against the literals in the evaluator's source IN BOTH DIRECTIONS: a type a
// rule raises and this list omits is a toggle an operator never gets, and an
// entry here that no rule raises is a toggle that silently does nothing. Both
// are invisible without the ledger, because each half agrees with itself.
//
// `alertLabels` in format.go is NOT this. It is a three-entry override map for
// the two RouterOS-update spellings and "connectivity", consulted by `LabelFor`
// when a stored key reads badly; it has nothing to say about the other nine.
//
// ── THE KEY IS THE DOWN TYPE'S STORED FORM ─────────────────────────────────
//
// `StoredType("Ping Loss")` is `ping_loss`, which is also the `ResolveType` the
// "up" side carries, which is also what `alert_events.alert_type` holds and what
// `cooldownKey` builds on. One identity, already used by four things, so the
// catalogue adopts it rather than minting a parallel vocabulary.
type Type struct {
	// Key is the stored form of Down, and the value a channel subscribes to.
	Key string
	// Down and Up are the display strings the rules emit. Up is empty for an
	// event that has no recovery side.
	Down string
	Up   string
	// Gate is the install-wide settings key that decides whether this event is
	// RAISED at all. A channel can only narrow what the gate already allowed —
	// see the header of `(*Evaluator).emit` for why the gate stays where it is.
	Gate string
	// Label and Desc are what the channel modal shows.
	Label string
	Desc  string
	// Backup marks the two events that are not alerts: they come from
	// `(*Server).dispatchBackup`, not from the evaluator, and so are checked
	// against a different source by the ledger.
	Backup bool
}

// types is the catalogue. Ordered for display rather than alphabetically: the
// events an operator is most likely to want are first.
var types = []Type{
	{
		Key: "interface_down", Down: "Interface Down", Up: "Interface Up",
		Gate: "notifIfaceUpDown", Label: "Interface up/down",
		Desc: "A monitored interface lost or regained its link",
	},
	{
		Key: "ping_loss", Down: "Ping Loss", Up: "Ping Restored",
		Gate: "notifPing", Label: "Ping loss",
		Desc: "Packet loss to the router's ping target crossed the threshold",
	},
	{
		Key: "high_cpu", Down: "High CPU", Up: "CPU Normal",
		Gate: "notifCpu", Label: "High CPU",
		Desc: "CPU load crossed the threshold",
	},
	{
		Key: "vpn_disconnected", Down: "VPN Disconnected", Up: "VPN Connected",
		Gate: "notifVpn", Label: "VPN peer",
		Desc: "A VPN peer disconnected or reconnected",
	},
	{
		Key: "host_down", Down: "Host Down", Up: "Host Up",
		Gate: "notifNetwatch", Label: "NetWatch host",
		Desc: "A NetWatch host stopped or started responding",
	},
	{
		Key: "router_offline", Down: "Router Offline", Up: "Router Online",
		Gate: "notifRouterStatus", Label: "Router reachability",
		Desc: "MikroDash lost or regained its connection to the router",
	},
	{
		Key: "routeros_update", Down: "RouterOS Update", Up: "RouterOS Updated",
		Gate: "notifRouterUpdate", Label: "RouterOS update",
		Desc: "A RouterOS upgrade became available, or was applied",
	},
	{
		Key: "bgp_peer_down", Down: "BGP Peer Down", Up: "BGP Peer Up",
		Gate: "notifBgp", Label: "BGP peer",
		Desc: "A BGP session went down or came back up",
	},
	{
		Key: "bgp_prefix_change", Down: "BGP Prefix Change", Up: "BGP Prefixes Settled",
		Gate: "notifBgp", Label: "BGP prefix change",
		Desc: "A BGP peer's advertised prefix count moved sharply",
	},
	{
		Key: "bgp_session_flapping", Down: "BGP Session Flapping", Up: "BGP Session Stable",
		Gate: "notifBgp", Label: "BGP session flapping",
		Desc: "A BGP session went up and down repeatedly",
	},
	{
		Key: "bgp_hold_timer_warning", Down: "BGP Hold Timer Warning", Up: "BGP Hold Timer OK",
		Gate: "notifBgp", Label: "BGP hold timer",
		Desc: "A BGP session's hold timer ran close to expiry",
	},

	// ── NOT ALERTS ─────────────────────────────────────────────────────────
	//
	// These two reach a channel through the same dispatcher but are raised by
	// the backup runner, never by the evaluator, and they record no
	// `alert_events` row. Their gates are the settings keys that have existed
	// since the Node app and, until channels, had no Go reader at all.
	//
	// They carry no Down/Up: those hold the display strings a RULE emits, and
	// these have none. Their key is `backup_` plus the runner's kind, which is
	// what the ledger checks them against.
	{
		Key: "backup_drift", Gate: "notifBackupDrift",
		Label: "Backup drift", Backup: true,
		Desc: "A scheduled backup found the configuration changed since the last one",
	},
	{
		Key: "backup_fail", Gate: "notifBackupFail",
		Label: "Backup failed", Backup: true,
		Desc: "A scheduled backup did not complete",
	},
}

// Types returns the catalogue. A copy, because the channel API hands it to a
// handler that is free to sort it.
func Types() []Type {
	out := make([]Type, len(types))
	copy(out, types)
	return out
}

// TypeByKey finds one entry. The second return is false for an unknown key,
// which is how a channel's stored event list is validated: a key that no longer
// exists is dropped rather than silently subscribing to nothing.
func TypeByKey(key string) (Type, bool) {
	for _, t := range types {
		if t.Key == key {
			return t, true
		}
	}
	return Type{}, false
}

// DefaultEvents is what a NEW channel subscribes to: everything the install
// currently raises.
//
// Not simply "everything", because a channel created while NetWatch alerts are
// switched off install-wide would show every NetWatch event ticked and still
// deliver nothing — the gate above it is closed. Reading the gates here makes
// the modal's initial state match what the channel would actually receive.
func DefaultEvents(gate func(key string) bool) []string {
	out := make([]string, 0, len(types))
	for _, t := range types {
		if gate(t.Gate) {
			out = append(out, t.Key)
		}
	}
	return out
}
