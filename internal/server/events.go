package server

// The events the server sends, and the payload each one carries.
//
// ONE LIST, like internal/collect/events.go and for the same reason: cmd/tsgen
// reads it to write the browser's event map. Most of these are replies to a
// request — `packages:ok`, `res:error` — built as `map[string]any`, so their
// TypeScript types are hand-written in web/src/events-hand.ts, and tsc fails if
// that file and these declarations disagree about which events those are.
//
// Collector events the server replays on page open are declared in
// internal/collect, and router:status and collection:status in
// internal/session: each event is declared once, next to its payload type.

import (
	"mikrodash/internal/alert"
	"mikrodash/internal/backups"
	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/routers"
	"mikrodash/internal/session"
	"mikrodash/internal/store"
)

// Struct payloads, whose browser types are generated.
var (
	EvAlertAcked        = hub.Declare[alert.Row]("alert:acked")
	EvBackupsState      = hub.Declare[backups.StatePayload]("backups:state")
	EvDiagnosticsUpdate = hub.Declare[session.Diagnostics]("diagnostics:update")
	EvRoutersStats      = hub.Declare[[]routers.Row]("routers:stats")
	EvSitesUpdate       = hub.Declare[[]db.Site]("sites:update")
)

// Map payloads, whose browser types are hand-written in web/src/events-hand.ts.
var (
	EvAIChunk            = hub.Declare[map[string]any]("ai:chunk")
	EvAIError            = hub.Declare[map[string]any]("ai:error")
	EvAIHistory          = hub.Declare[map[string]any]("ai:history")
	EvAIOverview         = hub.Declare[map[string]any]("ai:overview")
	EvAIPropose          = hub.Declare[map[string]any]("ai:propose")
	EvAIReply            = hub.Declare[map[string]any]("ai:reply")
	EvAIWritten          = hub.Declare[map[string]any]("ai:written")
	EvAccessNone         = hub.Declare[map[string]any]("access:none")
	EvAccessRevoked      = hub.Declare[map[string]any]("access:revoked")
	EvAlertsClearedAll   = hub.Declare[map[string]any]("alerts:cleared-all")
	EvAlertsOpen         = hub.Declare[map[string]any]("alerts:open")
	EvBackupsDiff        = hub.Declare[map[string]any]("backups:diff")
	EvBackupsError       = hub.Declare[map[string]any]("backups:error")
	EvBackupsRan         = hub.Declare[map[string]any]("backups:ran")
	EvBackupsRestored    = hub.Declare[map[string]any]("backups:restored")
	EvBackupsRestoring   = hub.Declare[map[string]any]("backups:restoring")
	EvBackupsRunning     = hub.Declare[map[string]any]("backups:running")
	EvCollectionConfig   = hub.Declare[map[string]any]("collection:config")
	EvPackagesApplying   = hub.Declare[map[string]any]("packages:applying")
	EvPackagesCaps       = hub.Declare[map[string]any]("packages:caps")
	EvPackagesError      = hub.Declare[map[string]any]("packages:error")
	EvPackagesNotes      = hub.Declare[map[string]any]("packages:notes")
	EvPackagesOk         = hub.Declare[map[string]any]("packages:ok")
	EvPermsChanged       = hub.Declare[map[string]any]("perms:changed")
	EvPingHistory        = hub.Declare[map[string]any]("ping:history")
	EvQueuesCaps         = hub.Declare[map[string]any]("queues:caps")
	EvQueuesError        = hub.Declare[map[string]any]("queues:error")
	EvQueuesOk           = hub.Declare[map[string]any]("queues:ok")
	EvResError           = hub.Declare[map[string]any]("res:error")
	EvResHistory         = hub.Declare[map[string]any]("res:history")
	EvResNew             = hub.Declare[map[string]any]("res:new")
	EvResOk              = hub.Declare[map[string]any]("res:ok")
	EvResPreview         = hub.Declare[map[string]any]("res:preview")
	EvResRow             = hub.Declare[map[string]any]("res:row")
	EvResSchema          = hub.Declare[map[string]any]("res:schema")
	EvRosusersCaps       = hub.Declare[map[string]any]("rosusers:caps")
	EvRosusersError      = hub.Declare[map[string]any]("rosusers:error")
	EvRosusersOk         = hub.Declare[map[string]any]("rosusers:ok")
	EvRouterActive       = hub.Declare[map[string]any]("router:active")
	EvRouterDisabled     = hub.Declare[map[string]any]("router:disabled")
	EvRouterSwitched     = hub.Declare[map[string]any]("router:switched")
	EvRoutersUpdate      = hub.Declare[[]map[string]any]("routers:update")
	EvSessionExpired     = hub.Declare[map[string]any]("session:expired")
	EvSettingsPages      = hub.Declare[store.Settings]("settings:pages")
	EvSetupRequired      = hub.Declare[map[string]any]("setup:required")
	EvWanCaps            = hub.Declare[map[string]any]("wan:caps")
	EvWanError           = hub.Declare[map[string]any]("wan:error")
	EvWanOk              = hub.Declare[map[string]any]("wan:ok")
	EvWifiscanDone       = hub.Declare[map[string]any]("wifiscan:done")
	EvWifiscanError      = hub.Declare[map[string]any]("wifiscan:error")
	EvWifiscanInterfaces = hub.Declare[map[string]any]("wifiscan:interfaces")
	EvWifiscanRows       = hub.Declare[map[string]any]("wifiscan:rows")
	EvWifiscanState      = hub.Declare[map[string]any]("wifiscan:state")
)
