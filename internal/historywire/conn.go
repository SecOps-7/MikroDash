package historywire

import (
	"mikrodash/internal/history"
)

// The connectivity half of the history wire: now one method, and it WRITES.
//
// ── THE DEBOUNCE MOVED OUT, AND THE MOVE IS THE POINT ──────────────────────
//
// This file used to own the whole thing: a `history.Connectivity` per router,
// the threshold, `Connected`, `Disconnected`, `Tick` and `TickAll`. That was
// right while recording an outage was the only consumer. It stopped being right
// the moment the fleet's Online/Offline badge and the Router Offline / Online
// alert started asking the same question, because `-history` defaults to FALSE
// and every entry point here returned early on a disabled wire. The debounce
// would have been dead on any install that had not opted into recording.
//
// So `internal/connstate` owns the machine, and this owns only the decision it
// was always the right place for: WHETHER TO WRITE.
//
// ── CONNECTIVITY IS NOT GATED ON REPORTING, SINCE 2026-10-01 ───────────────
//
// It used to be. A router with reporting off kept its live Online/Offline
// status and its alerts, and nothing about its reachability was written down -
// so the Devices page's connectivity strip had nothing to draw for it, and on
// the dev install that was three routers of four. Measured: zero rows in seven
// days for every reporting-off router.
//
// Reporting still governs TRAFFIC and PING history, which are minute series and
// the reason the toggle exists. Connectivity is not a series. It is one row per
// state CHANGE, so a router that never drops writes one row per process start
// and nothing else, and the cost of recording it for every router is the cost
// of recording its outages - which is the thing an operator looks at a strip to
// see. So the only gate left is the one that means "this install records
// nothing at all": `-history`.
func (w *Wire) RecordConn(rows []history.Row) {
	if w == nil || !w.enabled || len(rows) == 0 {
		return
	}
	w.persist(rows)
}
