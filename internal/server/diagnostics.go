package server

// The API Diagnostics card's feed.
//
// ── IT COSTS THE ROUTER NOTHING, WHICH IS THE REQUIREMENT ──────────────────
//
// `session.Diagnostics` reads counters this process already keeps: the command
// rate, the cache's subscription set, the collector table and the hub's room
// occupancy. No RouterOS command is issued to build this payload, so watching
// the card cannot change what it is measuring.
//
// ── THE CARD NEVER WORKED IN THIS PORT ─────────────────────────────────────
//
// The renderer, the listener and the room have all existed since the port, and
// nothing ever emitted `diagnostics:update` — `internal/verify/event_test.go`
// recorded it as a deliberate gap: "there is no diagnostics collector... The card
// renders empty." That was honest and it was also the whole bug: a card that
// renders empty looks identical to a card with nothing to say.
//
// ── AND IT IS NOT A COLLECTOR ──────────────────────────────────────────────
//
// It reports on THIS PROCESS, not on the router, so it has no menu, no cadence
// of its own to negotiate and nothing to put in `internal/collect`. It is a view
// over state, driven by a ticker that runs only while somebody is looking at it.

import (
	"path/filepath"
	"strings"
	"time"

	"mikrodash/internal/session"
)

// ── WHO ASKED: THE CARD'S SOURCES ──────────────────────────────────────────
//
// The card splits the router's command rate by source. The collectors are named
// by the session; every other command reaches the router through
// `Session.Exec` or `Session.StreamUntilDone` from a file in this package, and
// the session reads the calling file off the stack and asks `commandSource` to
// name it (internal/session/sources.go). The OUTERMOST named file wins, so the
// AI agent's writes through the resource path, and its security_scan, are the
// agent's.
//
// A LEDGER, BOTH WAYS: `TestEveryRouterCallerHasASource` fails on a file here
// that sends a router command and is not named below, and on a name below whose
// file no longer sends one. A new feature therefore cannot land in "Other"
// silently.

// aiPrefix names every file of the AI agent, which reaches the router from
// several of them and through other features' code.
const aiPrefix = "ai_"

// commandSources names each file that sends a router command.
var commandSources = map[string]string{
	"secscan.go":          "Security Scan",
	"apps.go":             "Apps",
	"tools.go":            "Tools",
	"wifiscan.go":         "WiFi scan",
	"wireguard.go":        "Pages",
	"resource.go":         "Pages",
	"area_group.go":       "Pages",
	"history.go":          "Pages",
	"rosusers.go":         "Pages",
	"wan.go":              "Pages",
	"routers_identity.go": "Pages",
	"dnsfleet_api.go":     "Pages",
	"topofleet_api.go":    "Pages",
	"packages.go":         "Packages",
	"files.go":            "Files",
	"clock.go":            "Clock",
	"backups.go":          "Backups",
	"backups_restore.go":  "Backups",
	"backup_scheduler.go": "Backups",
	"cfgrouter.go":        "Config Management",
	"cfgjob.go":           "Config Management",
	"cfghistory.go":       "Config Management",
}

// commandSource names the feature a source file belongs to, or "" for a file
// that is not one of this package's.
func commandSource(file string) string {
	if !strings.Contains(filepath.ToSlash(file), "/internal/server/") {
		return ""
	}
	base := filepath.Base(file)
	if strings.HasPrefix(base, aiPrefix) && !strings.HasSuffix(base, "_test.go") {
		return "AI agent"
	}
	return commandSources[base]
}

func init() { session.SetSourceNamer(commandSource) }

// diagRefresh is how often the card is repainted. Two seconds, matching the
// Devices page: the numbers are per-minute rates, so a faster tick would show
// the same value repeatedly and a slower one would feel dead.
const diagRefresh = 2 * time.Second

// diagFocus starts this viewer's diagnostics feed.
//
// Called from `dashCardFocus` for the one card that needs a push nothing else
// produces. Every other card is fed by a collector that is already running.
func (cn *conn) diagFocus() {
	cn.sendDiagnostics()

	cn.diagMu.Lock()
	defer cn.diagMu.Unlock()
	if cn.diagTick != nil {
		// Already ticking. A browser can send `dashcard:focus` twice for a card
		// it already has, and the live grid does exactly that on a layout
		// restore.
		return
	}
	t := time.NewTicker(diagRefresh)
	stop := make(chan struct{})
	cn.diagTick, cn.diagStop = t, stop
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				// On the connection's loop, which owns its router state.
				cn.post(cn.sendDiagnostics)
			}
		}
	}()
}

// diagBlur stops it. Called when the card is removed and when the socket goes:
// a ticker left running holds this connection alive and repaints a card nobody
// has.
func (cn *conn) diagBlur() {
	cn.diagMu.Lock()
	t, stop := cn.diagTick, cn.diagStop
	cn.diagTick, cn.diagStop = nil, nil
	cn.diagMu.Unlock()
	if t != nil {
		t.Stop()
	}
	if stop != nil {
		close(stop)
	}
}

// sendDiagnostics builds and sends one payload to THIS socket.
//
// To this socket rather than to the card's room, because the payload describes
// the router this connection has selected: two viewers on two routers share the
// room and must not share the answer.
func (cn *conn) sendDiagnostics() {
	rs := cn.rsession
	if rs == nil || cn.routerID == "" {
		return
	}
	EvDiagnosticsUpdate.Send(cn.srv.hub, cn.c, rs.Diagnostics(time.Now().UnixMilli()))
}
