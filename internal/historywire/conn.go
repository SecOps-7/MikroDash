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
// was always the right place for: WHETHER TO WRITE. A router with reporting off
// keeps its live Online/Offline status and its alerts; nothing about it is
// written down.
//
// ── CHECKED AT WRITE TIME, WHICH IS STRICTLY BETTER THAN BEFORE ────────────
//
// The old `apply` skipped the state machine outright when reporting was off,
// and `TickAll` repeated the check so that a router switched off mid-debounce
// did not have that one outage written when the timer expired. One check here
// covers both cases, because this is the only place a row becomes a row.
func (w *Wire) RecordConn(rows []history.Row) {
	if w == nil || !w.enabled || len(rows) == 0 {
		return
	}
	keep := rows[:0:0]
	for _, r := range rows {
		if w.Reporting(r.RouterID) {
			keep = append(keep, r)
		}
	}
	w.persist(keep)
}
