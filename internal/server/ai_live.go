package server

// Live tools: answers built from a collector's payload rather than a menu's rows.
//
// ── WHY THEY EXIST ──────────────────────────────────────────────────────────
//
// A resource tool reads configuration: the rows of a menu the registry
// declares. Some questions are about what the router is DOING (throughput,
// which uplink carries traffic, who is connected), and that lives in the
// collectors that feed the pages, not in any menu the registry can list. A live
// tool hands over the payload a page already draws. See `aitools.liveTools`.
//
// ── ONE PATH FOR EVERY LIVE TOOL ────────────────────────────────────────────
//
// Each tool supplies where its payload comes from and a pure `render` that
// shapes it for the model. The rest (whether to re-read, the refresh, the
// reading's age, the size cap and the untrusted wrapper) is `liveAnswer` and
// `capRows`, written once, so fifteen tools cannot drift into fifteen slightly
// different freshness rules or budgets.

import (
	"encoding/json"
	"fmt"
	"math"
	"mikrodash/internal/session"
	"net"
	"sort"
	"strings"
	"time"

	"mikrodash/internal/aicontext"
	"mikrodash/internal/aitools"
	"mikrodash/internal/collect"
	"mikrodash/internal/guard"
)

// aiLiveMaxAge is how old a FreshLive reading may be before a tool re-reads it.
//
// ── SECONDS, NOT THE STALENESS RULE ─────────────────────────────────────────
//
// The context summary calls a reading stale only after its interval plus a grace,
// which suits "is this still roughly true". A throughput question is "what is it
// doing NOW", and a rate from a minute ago answers a different one. With the
// page open the collector reads every second, so this almost never costs a read;
// with nothing open it costs one refresh per question.
const aiLiveMaxAge = 5 * time.Second

// liveToolReaders answers each live tool, keyed by the collector it names.
//
// A LEDGER IN BOTH DIRECTIONS (`TestEveryLiveToolHasAReader`): a live tool with
// no reader would be advertised and then answer "not available", and a reader no
// tool names is code nothing can reach.
var liveToolReaders = map[string]func(rs *session.Session, t aitools.Tool) string{
	"system":       liveSystemStatus,
	"ifStatus":     liveInterfaceTraffic,
	"wan":          liveWanStatus,
	"packages":     livePackages,
	"rosusers":     liveRouterUsers,
	"queues":       liveQueues,
	"logs":         liveLogs,
	"wireless":     liveWifiClients,
	"conns":        liveConnections,
	"bandwidth":    liveBandwidth,
	"topology":     liveTopology,
	"vpn":          liveWireguardStatus,
	"ppp":          livePPPSessions,
	"routing":      liveBGPSessions,
	"capsman":      liveCapsman,
	"dhcpNetworks": liveDHCPNetworks,
}

// runLiveTool reads the collector on the session the question was asked about:
// the exchange's snapshot, never the connection's live field (see conn).
func runLiveTool(rs *session.Session, t aitools.Tool) string {
	read, ok := liveToolReaders[t.Collector]
	if !ok {
		return "That tool is not available on this build."
	}
	if rs == nil {
		return "No device is selected, so nothing was read."
	}
	return read(rs, t)
}

// liveReadingDue decides whether a live tool must re-read its collector before
// answering. Pure, so the rule is tested without a router.
//
// ── NO READING, OR NO TIME ON IT, IS ALWAYS DUE ─────────────────────────────
//
// `aicontext.IsStale` calls a zero timestamp "unknown, not stale", which is the
// honest thing for a summary to say about a reading. A tool is asked to ANSWER,
// and a reading it cannot date is not one it should hand over as current.
func liveReadingDue(freshness string, present bool, now, ts int64, pollMs int) bool {
	if !present || ts <= 0 {
		return true
	}
	if freshness == aitools.FreshMetadata {
		return aicontext.IsStale(now, ts, pollMs)
	}
	// FreshLive, and anything undeclared: the stricter bound.
	// TestEveryLiveToolDeclaresItsFreshness keeps "undeclared" from happening.
	return now-ts > aiLiveMaxAge.Milliseconds()
}

// liveSource is what `liveAnswer` needs from one collector.
type liveSource[P any] struct {
	last    func() *P
	stamp   func(*P) (ts int64, pollMs int)
	refresh func()
	// menu is the RouterOS menu the reading comes from, named in the result so
	// the model's vocabulary matches the operator's documentation.
	menu string
	// absent is what the model is told when there is no reading at all.
	absent string
}

// liveAnswer applies the freshness rule, renders the payload, and wraps it.
func liveAnswer[P any](t aitools.Tool, src liveSource[P], render func(*P) any) string {
	p := src.last()
	var ts int64
	var pollMs int
	if p != nil {
		ts, pollMs = src.stamp(p)
	}
	if liveReadingDue(t.Freshness, p != nil, time.Now().UnixMilli(), ts, pollMs) {
		src.refresh()
		if p = src.last(); p != nil {
			ts, _ = src.stamp(p)
		}
	}
	if p == nil {
		return src.absent
	}
	body, err := json.Marshal(struct {
		Tool       string  `json:"tool"`
		Menu       string  `json:"menu"`
		AgeSeconds float64 `json:"readingAgeSeconds"`
		Data       any     `json:"data"`
	}{Tool: t.Name, Menu: src.menu, AgeSeconds: ageSeconds(ts, time.Now()), Data: render(p)})
	if err != nil {
		return "That data could not be encoded."
	}
	// UNTRUSTED like every tool result: names, comments and addresses are chosen
	// by whoever configured the router and the devices on it.
	return aicontext.Wrap(string(body))
}

// ageSeconds is a reading's age to a tenth of a second, or -1 when undated.
func ageSeconds(ts int64, now time.Time) float64 {
	if ts <= 0 {
		return -1
	}
	return math.Round(now.Sub(time.UnixMilli(ts)).Seconds()*10) / 10
}

// capRows keeps rows within the budget a resource tool uses, row by row, so a
// router with hundreds of VLAN interfaces or uplinks cannot fill the model's
// context. A row that will not encode is skipped rather than failing the answer.
func capRows[T any](rows []T) (kept []T, truncated bool) {
	kept = make([]T, 0, min(len(rows), aiToolMaxRows))
	used := 0
	for _, r := range rows {
		if len(kept) >= aiToolMaxRows {
			return kept, true
		}
		b, err := json.Marshal(r)
		if err != nil {
			continue
		}
		if used+len(b) > aiToolMaxBytes {
			return kept, true
		}
		used += len(b)
		kept = append(kept, r)
	}
	return kept, false
}

// ── list_interface_traffic ──────────────────────────────────────────────────

func liveInterfaceTraffic(rs *session.Session, t aitools.Tool) string {
	col := rs.IfStatus()
	return liveAnswer(t, liveSource[collect.IfStatusPayload]{
		last:    col.Last,
		stamp:   func(p *collect.IfStatusPayload) (int64, int) { return p.TS, 0 },
		refresh: col.RefreshNow,
		menu:    "/interface/monitor-traffic",
		absent:  "The interface readings are not available from this router yet.",
	}, renderInterfaceTraffic)
}

type liveIfRow struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Comment     string   `json:"comment,omitempty"`
	Running     bool     `json:"running"`
	Disabled    bool     `json:"disabled"`
	RxMbps      float64  `json:"rxMbps"`
	TxMbps      float64  `json:"txMbps"`
	IPs         []string `json:"addresses,omitempty"`
	RxBytes     *float64 `json:"rxBytes,omitempty"`
	TxBytes     *float64 `json:"txBytes,omitempty"`
	ErrorsDelta *float64 `json:"errorsSinceLastReading,omitempty"`
	DropsDelta  *float64 `json:"dropsSinceLastReading,omitempty"`
}

func renderInterfaceTraffic(p *collect.IfStatusPayload) any {
	rows := make([]liveIfRow, 0, len(p.Interfaces))
	for _, i := range p.Interfaces {
		rows = append(rows, liveIfRow{Name: i.Name, Type: i.Type, Comment: i.Comment,
			Running: i.Running, Disabled: i.Disabled, RxMbps: i.RxMbps, TxMbps: i.TxMbps,
			IPs: i.IPs, RxBytes: i.RxBytes, TxBytes: i.TxBytes,
			ErrorsDelta: i.ErrorsDelta, DropsDelta: i.DropsDelta})
	}
	kept, truncated := capRows(rows)
	return struct {
		Interfaces []liveIfRow `json:"interfaces"`
		Total      int         `json:"totalInterfaces"`
		Truncated  bool        `json:"truncated"`
	}{kept, len(p.Interfaces), truncated}
}

// ── list_wan_status ─────────────────────────────────────────────────────────

func liveWanStatus(rs *session.Session, t aitools.Tool) string {
	col := rs.Wan()
	return liveAnswer(t, liveSource[collect.WANPayload]{
		last:    col.Last,
		stamp:   func(p *collect.WANPayload) (int64, int) { return p.TS, p.PollMs },
		refresh: col.RefreshNow,
		menu:    "/interface/detect-internet/state",
		absent:  "The WAN readings are not available from this router yet.",
	}, renderWanStatus)
}

type liveWanDhcp struct {
	Status       string `json:"status"`
	Server       string `json:"server,omitempty"`
	ExpiresAfter string `json:"expiresAfter,omitempty"`
	Invalid      bool   `json:"invalid,omitempty"`
}

type liveWanRow struct {
	Name            string       `json:"name"`
	Type            string       `json:"type"`
	IsTunnel        bool         `json:"isTunnel"`
	State           string       `json:"state,omitempty"`
	Manual          bool         `json:"declaredManually,omitempty"`
	Since           string       `json:"stateSince,omitempty"`
	Running         *bool        `json:"running,omitempty"`
	Address         string       `json:"address,omitempty"`
	IsPublic        *bool        `json:"addressIsPublic,omitempty"`
	Gateway         string       `json:"gateway,omitempty"`
	RouteDistance   string       `json:"defaultRouteDistance,omitempty"`
	RouteActive     bool         `json:"carriesDefaultRoute"`
	HasDefaultRoute bool         `json:"hasDefaultRoute"`
	RxMbps          *float64     `json:"rxMbps,omitempty"`
	TxMbps          *float64     `json:"txMbps,omitempty"`
	Dhcp            *liveWanDhcp `json:"dhcpLease,omitempty"`
}

// renderWanStatus shapes the WAN payload for the model.
//
// ── THE EMPTY CASES SAY WHY ─────────────────────────────────────────────────
//
// No uplinks usually means internet detection is switched off, not that the
// router is offline, and a denied read means the API user lacks the policy. The
// page says which; so does this, or the model reports "no internet" for a router
// that is merely not asked.
func renderWanStatus(p *collect.WANPayload) any {
	rows := make([]liveWanRow, 0, len(p.Wans))
	for _, w := range p.Wans {
		r := liveWanRow{Name: w.Name, Type: w.Type, IsTunnel: w.IsTunnel, State: w.State,
			Manual: w.Manual, Since: w.Since, Running: w.Running, Address: w.Address,
			IsPublic: w.IsPublic, Gateway: w.Gateway, RouteDistance: w.RouteDistance,
			RouteActive: w.RouteActive, HasDefaultRoute: w.HasDefaultRoute,
			RxMbps: w.RxMbps, TxMbps: w.TxMbps}
		if w.Dhcp != nil {
			r.Dhcp = &liveWanDhcp{Status: w.Dhcp.Status, Server: w.Dhcp.Server,
				ExpiresAfter: w.Dhcp.ExpiresAfter, Invalid: w.Dhcp.Invalid}
		}
		rows = append(rows, r)
	}
	kept, truncated := capRows(rows)
	note := ""
	switch {
	case p.Denied:
		note = "The router refused to report WAN state to MikroDash's API user; this is a permission on the router, not an outage."
	case len(p.Wans) == 0 && !p.DetectionEnabled:
		note = "No uplinks are listed because RouterOS internet detection is switched off; this does not mean the router is offline."
	}
	return struct {
		ActiveDefaultWan string       `json:"activeDefaultUplink,omitempty"`
		PublicIP         string       `json:"publicAddress,omitempty"`
		DetectionEnabled bool         `json:"internetDetectionEnabled"`
		UplinkSource     string       `json:"uplinksChosenBy,omitempty"`
		Note             string       `json:"note,omitempty"`
		Uplinks          []liveWanRow `json:"uplinks"`
		Total            int          `json:"totalUplinks"`
		Truncated        bool         `json:"truncated"`
	}{p.ActiveDefaultWan, p.PublicIP, p.DetectionEnabled, p.UplinkSource, note,
		kept, len(p.Wans), truncated}
}

// ── list_packages ───────────────────────────────────────────────────────────

// liveSystemStatus is the router's health from the system collector, the
// reading the dashboard's gauges draw. Before (MikroMCP's check_router_health
// and get_system_status), it reached the model only as a line of every
// prompt's context, as old as that prompt; this is re-read when it is more
// than a few seconds old.
func liveSystemStatus(rs *session.Session, t aitools.Tool) string {
	col := rs.System()
	return liveAnswer(t, liveSource[collect.SystemPayload]{
		last:    col.Last,
		stamp:   func(p *collect.SystemPayload) (int64, int) { return p.TS, p.PollMs },
		refresh: col.RefreshNow,
		menu:    "/system/resource",
		absent:  "The router's health readings are not available yet.",
	}, renderSystemStatus)
}

type liveSystem struct {
	CPULoadPct      int      `json:"cpuLoadPct"`
	CPUCount        int      `json:"cpuCount,omitempty"`
	CPUMHz          int      `json:"cpuMHz,omitempty"`
	MemoryUsedPct   int      `json:"memoryUsedPct"`
	MemoryUsedMB    int      `json:"memoryUsedMB"`
	MemoryTotalMB   int      `json:"memoryTotalMB"`
	StorageUsedPct  int      `json:"storageUsedPct"`
	StorageFreeMB   int      `json:"storageFreeMB"`
	StorageTotalMB  int      `json:"storageTotalMB"`
	TemperatureC    *float64 `json:"temperatureC,omitempty"`
	Uptime          string   `json:"uptime"`
	Version         string   `json:"version"`
	LatestVersion   string   `json:"latestVersion,omitempty"`
	UpdateAvailable bool     `json:"updateAvailable"`
	UpdateChannel   string   `json:"updateChannel,omitempty"`
	Board           string   `json:"board,omitempty"`
}

// renderSystemStatus shapes the payload for the model. NO SERIAL AND NO
// LICENCE: they identify the device and answer nothing about its health.
func renderSystemStatus(p *collect.SystemPayload) any {
	mb := func(b int) int { return b / (1 << 20) }
	return liveSystem{
		CPULoadPct: p.CPULoad, CPUCount: p.CPUCount, CPUMHz: p.CPUFreq,
		MemoryUsedPct: p.MemPct, MemoryUsedMB: mb(p.UsedMem), MemoryTotalMB: mb(p.TotalMem),
		StorageUsedPct: p.HddPct, StorageFreeMB: mb(p.FreeHdd), StorageTotalMB: mb(p.TotalHdd),
		TemperatureC: p.TempC, Uptime: p.UptimeRaw, Version: p.Version,
		LatestVersion: p.LatestVersion, UpdateAvailable: p.UpdateAvailable, UpdateChannel: p.UpdateChannel,
		Board: p.BoardName,
	}
}

func livePackages(rs *session.Session, t aitools.Tool) string {
	col := rs.Packages()
	return liveAnswer(t, liveSource[collect.PackagesPayload]{
		last:    col.Last,
		stamp:   func(p *collect.PackagesPayload) (int64, int) { return p.TS, p.PollMs },
		refresh: col.RefreshNow,
		menu:    "/system/package",
		absent:  "The package readings are not available from this router yet.",
	}, renderPackages)
}

type livePackageRow struct {
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	BuildTime string   `json:"buildTime,omitempty"`
	SizeMB    *float64 `json:"sizeMB,omitempty"`
	// Installed is false for an extra package MikroTik offers that is not on
	// the router, which the payload calls "on server".
	Installed       bool   `json:"installed"`
	Disabled        bool   `json:"disabled"`
	State           string `json:"state,omitempty"`
	ScheduledAction string `json:"scheduledForNextReboot,omitempty"`
}

type liveFirmware struct {
	IsRouterboard    bool   `json:"isRouterboard"`
	BoardName        string `json:"boardName,omitempty"`
	Model            string `json:"model,omitempty"`
	FirmwareType     string `json:"firmwareType,omitempty"`
	CurrentFirmware  string `json:"currentFirmware,omitempty"`
	UpgradeFirmware  string `json:"upgradeFirmware,omitempty"`
	MinimumFirmware  string `json:"minimumFirmware,omitempty"`
	UpgradeAvailable bool   `json:"upgradeAvailable"`
	AutoUpgrade      *bool  `json:"autoUpgrade,omitempty"`
}

// renderPackages shapes the package payload for the model.
//
// ── NO SERIAL, NO ROW IDS ───────────────────────────────────────────────────
//
// The serial identifies the physical device and answers nothing about software;
// a hosted endpoint would receive it on every such question. Package ids exist
// so an action can target a row, and actions do not go through this tool.
func renderPackages(p *collect.PackagesPayload) any {
	rows := make([]livePackageRow, 0, len(p.Packages))
	for _, pk := range p.Packages {
		r := livePackageRow{Name: pk.Name, Version: pk.Version, BuildTime: pk.BuildTime,
			Installed: !pk.OnServer, Disabled: pk.Disabled, State: pk.State,
			ScheduledAction: pk.ScheduledAction}
		if pk.Size != nil {
			mb := math.Round(*pk.Size/1048576*10) / 10
			r.SizeMB = &mb
		}
		rows = append(rows, r)
	}
	kept, truncated := capRows(rows)
	note := ""
	if !p.Available {
		note = "The package list could not be read from this router, so the list below may be empty for that reason rather than because nothing is installed."
	}
	fw := p.Firmware
	return struct {
		Note          string                `json:"note,omitempty"`
		Update        collect.Update        `json:"routerosUpdate"`
		Firmware      liveFirmware          `json:"routerboardFirmware"`
		PendingReboot bool                  `json:"changesPendingReboot"`
		Counts        collect.PackageCounts `json:"counts"`
		Packages      []livePackageRow      `json:"packages"`
		Truncated     bool                  `json:"truncated"`
	}{
		Note: note, Update: p.Update,
		Firmware: liveFirmware{IsRouterboard: fw.IsRouterboard, BoardName: fw.BoardName,
			Model: fw.Model, FirmwareType: fw.FirmwareType, CurrentFirmware: fw.CurrentFirmware,
			UpgradeFirmware: fw.UpgradeFirmware, MinimumFirmware: fw.MinimumFirmware,
			UpgradeAvailable: fw.UpgradeAvailable, AutoUpgrade: fw.AutoUpgrade},
		PendingReboot: p.PendingReboot, Counts: p.Counts, Packages: kept, Truncated: truncated,
	}
}

// ── list_router_users ───────────────────────────────────────────────────────

func liveRouterUsers(rs *session.Session, t aitools.Tool) string {
	col := rs.RosUsers()
	return liveAnswer(t, liveSource[collect.RosUsersPayload]{
		last:    col.Last,
		stamp:   func(p *collect.RosUsersPayload) (int64, int) { return p.TS, p.PollMs },
		refresh: col.RefreshNow,
		menu:    "/user",
		absent:  "The router's user readings are not available yet.",
	}, renderRouterUsers)
}

type liveRosUser struct {
	Name              string `json:"name"`
	Group             string `json:"group"`
	Address           string `json:"allowedAddress,omitempty"`
	Comment           string `json:"comment,omitempty"`
	Disabled          bool   `json:"disabled"`
	Expired           bool   `json:"expired,omitempty"`
	LastLogin         string `json:"lastLogin,omitempty"`
	InactivityTimeout string `json:"inactivityTimeout,omitempty"`
	UsedByMikroDash   bool   `json:"usedByMikroDash,omitempty"`
}

type liveRosGroup struct {
	Name            string   `json:"name"`
	Granted         []string `json:"grantedPolicies"`
	Denied          []string `json:"deniedPolicies"`
	Comment         string   `json:"comment,omitempty"`
	Members         int      `json:"members"`
	UsedByMikroDash bool     `json:"usedByMikroDash,omitempty"`
}

type liveRosSession struct {
	Name            string `json:"user"`
	Address         string `json:"address,omitempty"`
	Via             string `json:"via,omitempty"`
	When            string `json:"since,omitempty"`
	UsedByMikroDash bool   `json:"usedByMikroDash,omitempty"`
}

// renderRouterUsers shapes the RouterOS accounts for the model.
//
// ── `protected` IS NAMED FOR WHAT IT MEANS ──────────────────────────────────
//
// The payload's flag marks the account and group MikroDash signs in with, which
// the Users page refuses to edit because changing them can lock the dashboard
// out of the router for good. `usedByMikroDash` says that in words a model will
// act on; `protected` alone reads as a RouterOS attribute it could reason around.
func renderRouterUsers(p *collect.RosUsersPayload) any {
	users := make([]liveRosUser, 0, len(p.Users))
	for _, u := range p.Users {
		users = append(users, liveRosUser{Name: u.Name, Group: u.Group, Address: u.Address,
			Comment: u.Comment, Disabled: u.Disabled, Expired: u.Expired, LastLogin: u.LastLogin,
			InactivityTimeout: u.InactivityTimeout, UsedByMikroDash: u.Protected})
	}
	groups := make([]liveRosGroup, 0, len(p.Groups))
	for _, g := range p.Groups {
		groups = append(groups, liveRosGroup{Name: g.Name, Granted: nonNil(g.Granted),
			Denied: nonNil(g.Denied), Comment: g.Comment, Members: g.Members, UsedByMikroDash: g.Protected})
	}
	sessions := make([]liveRosSession, 0, len(p.Sessions))
	for _, a := range p.Sessions {
		sessions = append(sessions, liveRosSession{Name: a.Name, Address: a.Address, Via: a.Via,
			When: a.When, UsedByMikroDash: a.Protected})
	}
	keptU, tU := capRows(users)
	keptG, tG := capRows(groups)
	keptS, tS := capRows(sessions)
	note := ""
	switch {
	case p.Denied:
		note = "The router refused to list its users to MikroDash's API user; this is a permission on the router."
	case !p.Available:
		note = "The router's user list could not be read, so empty lists below do not mean there are no users."
	}
	return struct {
		Note           string                    `json:"note,omitempty"`
		Users          []liveRosUser             `json:"users"`
		Groups         []liveRosGroup            `json:"groups"`
		Sessions       []liveRosSession          `json:"activeSessions"`
		PasswordPolicy collect.RosPasswordPolicy `json:"passwordPolicy"`
		Truncated      bool                      `json:"truncated"`
	}{note, keptU, keptG, keptS, p.PasswordPolicy, tU || tG || tS}
}

// nonNil is a slice that marshals as [] rather than null, for a list the model
// should read as "none" rather than "unknown".
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ── list_queues ─────────────────────────────────────────────────────────────

// liveQueues forgets the rate baselines before a refresh it has to force.
//
// ── A SUSPENDED COLLECTOR KEEPS AN OLD BASELINE ─────────────────────────────
//
// Queue rates are byte-counter deltas. `Suspend` does not clear the previous
// sample, so a queue collector idle for an hour would measure its next rate over
// the whole hour and report a long average as "now". Forgetting first makes the
// refreshed reading use RouterOS's own current `rate`, labelled `router`, which
// is an honest first sample. With the page open no refresh is forced and the
// measured deltas pass through untouched.
func liveQueues(rs *session.Session, t aitools.Tool) string {
	col := rs.Queues()
	return liveAnswer(t, liveSource[collect.QueuesPayload]{
		last:  col.Last,
		stamp: func(p *collect.QueuesPayload) (int64, int) { return p.TS, p.PollMs },
		refresh: func() {
			col.ForgetRates()
			col.RefreshNow()
		},
		menu:   "/queue/simple",
		absent: "The queue readings are not available from this router yet.",
	}, renderQueues)
}

type liveSimpleQueue struct {
	Order       int              `json:"order"`
	Name        string           `json:"name"`
	Target      string           `json:"target,omitempty"`
	Parent      string           `json:"parent,omitempty"`
	PacketMarks string           `json:"packetMarks,omitempty"`
	Priority    string           `json:"priority,omitempty"`
	QueueType   string           `json:"queueType,omitempty"`
	LimitAt     guard.Pair       `json:"limitAtBps"`
	MaxLimit    guard.Pair       `json:"maxLimitBps"`
	BurstLimit  guard.Pair       `json:"burstLimitBps"`
	RateBps     collect.RatePair `json:"rateBps"`
	RateSource  string           `json:"rateSource,omitempty"`
	Dropped     collect.IntPair  `json:"droppedPackets"`
	Disabled    bool             `json:"disabled"`
	Invalid     bool             `json:"invalid,omitempty"`
	Dynamic     bool             `json:"dynamic,omitempty"`
	Comment     string           `json:"comment,omitempty"`
}

type liveTreeQueue struct {
	Order               int        `json:"order"`
	Name                string     `json:"name"`
	Parent              string     `json:"parent,omitempty"`
	PacketMark          string     `json:"packetMark,omitempty"`
	Priority            string     `json:"priority,omitempty"`
	QueueType           string     `json:"queueType,omitempty"`
	LimitAt             guard.Rate `json:"limitAtBps"`
	MaxLimit            guard.Rate `json:"maxLimitBps"`
	BurstLimit          guard.Rate `json:"burstLimitBps"`
	RateBps             *float64   `json:"rateBps"`
	RateSource          string     `json:"rateSource,omitempty"`
	Dropped             *int       `json:"droppedPackets"`
	Disabled            bool       `json:"disabled"`
	Invalid             bool       `json:"invalid,omitempty"`
	Comment             string     `json:"comment,omitempty"`
	FasttrackBypassable bool       `json:"bypassedByFasttrack,omitempty"`
}

// rateSourceText says where a rate came from in words: a measured delta, or
// RouterOS's own average on a first reading.
func rateSourceText(s *string) string {
	if s == nil {
		return ""
	}
	switch *s {
	case "delta":
		return "measured"
	case "router":
		return "router average, first reading"
	}
	return *s
}

func renderQueues(p *collect.QueuesPayload) any {
	simple := make([]liveSimpleQueue, 0, len(p.Simple))
	for _, q := range p.Simple {
		simple = append(simple, liveSimpleQueue{Order: q.Order, Name: q.Name, Target: q.Target,
			Parent: q.Parent, PacketMarks: q.PacketMarks, Priority: q.Priority, QueueType: q.QueueType,
			LimitAt: q.LimitAt, MaxLimit: q.MaxLimit, BurstLimit: q.BurstLimit, RateBps: q.RateBps,
			RateSource: rateSourceText(q.RateSource), Dropped: q.Dropped, Disabled: q.Disabled,
			Invalid: q.Invalid, Dynamic: q.Dynamic, Comment: q.Comment})
	}
	tree := make([]liveTreeQueue, 0, len(p.Tree))
	for _, q := range p.Tree {
		tree = append(tree, liveTreeQueue{Order: q.Order, Name: q.Name, Parent: q.Parent,
			PacketMark: q.PacketMark, Priority: q.Priority, QueueType: q.QueueType,
			LimitAt: q.LimitAt, MaxLimit: q.MaxLimit, BurstLimit: q.BurstLimit, RateBps: q.RateBps,
			RateSource: rateSourceText(q.RateSource), Dropped: q.Dropped, Disabled: q.Disabled,
			Invalid: q.Invalid, Comment: q.Comment, FasttrackBypassable: q.FasttrackBypassable})
	}
	keptS, tS := capRows(simple)
	keptT, tT := capRows(tree)
	note := ""
	switch {
	case p.Denied:
		note = "The router refused to list its queues to MikroDash's API user; this is a permission on the router."
	case !p.Available:
		note = "The queue tables could not be read, so empty lists below do not mean there are no queues."
	}
	return struct {
		Note      string            `json:"note,omitempty"`
		Fasttrack collect.Fasttrack `json:"fasttrack"`
		Stats     string            `json:"statisticsReported,omitempty"`
		Simple    []liveSimpleQueue `json:"simpleQueues"`
		Tree      []liveTreeQueue   `json:"queueTree"`
		Truncated bool              `json:"truncated"`
	}{note, p.Fasttrack, p.Stats, keptS, keptT, tS || tT}
}

// ── list_logs ───────────────────────────────────────────────────────────────

// logsReading is the ring plus how current it is. The logs collector has no
// payload with a timestamp: it is a stream into a ring, and "fresh" means "the
// channel is open", which `ReadingTS` reports.
type logsReading struct {
	entries []collect.LogEntry
	ts      int64
}

func liveLogs(rs *session.Session, t aitools.Tool) string {
	col := rs.Logs()
	return liveAnswer(t, liveSource[logsReading]{
		last: func() *logsReading {
			h := col.Last()
			if h == nil {
				return nil
			}
			return &logsReading{entries: h, ts: col.ReadingTS()}
		},
		stamp:   func(r *logsReading) (int64, int) { return r.ts, 0 },
		refresh: col.Reload,
		menu:    "/log",
		absent:  "No log lines are available from this router.",
	}, renderLogs)
}

type liveLogLine struct {
	Time     string `json:"time"`
	Severity string `json:"severity,omitempty"`
	Topics   string `json:"topics,omitempty"`
	Message  string `json:"message"`
}

// renderLogs puts the NEWEST line first, then caps.
//
// The ring is oldest first, as the router prints it. Capping that order would
// keep the oldest lines and drop the recent ones, which are the ones a question
// about "what just happened" is asking for.
func renderLogs(r *logsReading) any {
	lines := make([]liveLogLine, 0, len(r.entries))
	for i := len(r.entries) - 1; i >= 0; i-- {
		e := r.entries[i]
		lines = append(lines, liveLogLine{Time: e.Time, Severity: e.Severity, Topics: e.Topics, Message: e.Message})
	}
	kept, truncated := capRows(lines)
	return struct {
		Lines     []liveLogLine `json:"linesNewestFirst"`
		Total     int           `json:"linesHeld"`
		Truncated bool          `json:"olderLinesOmitted"`
	}{kept, len(r.entries), truncated}
}

// ── list_wifi_clients ───────────────────────────────────────────────────────

// liveWifiClients re-derives the client list when it is out of date with the
// tables it is JOINED against, whether or not the list itself is due.
//
// ── A REGISTRATION ROW CARRIES A MAC AND NOTHING ELSE ───────────────────────
//
// A client's name comes from the DHCP lease table and its address from ARP, both
// separate collectors. The tool reported 0 of 32 clients named and none with an
// address on the hAP ax3, while the Wifi Clients page received 23 named and 32
// with addresses.
//
// ── MEASURED BEFORE IT WAS FIXED, AFTER TWO WRONG GUESSES ───────────────────
//
// Loading the joins inside the list's refresh changed nothing, and nor did
// loading them whenever they were absent. Logging the state at call time showed
// why: the lease table (46) and ARP (47) WERE loaded. The list had been derived
// before they were, just after the session started, and was still inside its
// freshness bound, so nothing re-derived it. The staleness is of the JOIN, not
// of the list and not of the sources.
//
// So the list is re-derived when a join could now fill it: a source that has
// never loaded is loaded first; the lease table being newer than the list, or
// ARP knowing an address for a client the list shows without one, re-derives it.
// Both checks read memory, so a list that is already joined costs nothing.
func liveWifiClients(rs *session.Session, t aitools.Tool) string {
	col := rs.Wireless()
	leases, arp := rs.DHCPLeases(), rs.ARP()
	rederive := false
	if leases != nil && leases.Last() == nil {
		leases.RefreshNow()
		rederive = true
	}
	if arp != nil && arp.Last() == nil {
		arp.RefreshNow()
		rederive = true
	}
	if w := col.Last(); w != nil && !rederive {
		rederive = wifiJoinOutOfDate(w, leases, arp)
	}
	if rederive {
		col.RefreshNow()
	}
	return liveAnswer(t, liveSource[collect.WirelessPayload]{
		last:    col.Last,
		stamp:   func(p *collect.WirelessPayload) (int64, int) { return p.TS, p.PollMs },
		refresh: col.RefreshNow,
		menu:    "/interface/wifi/registration-table",
		absent:  "The wireless client readings are not available from this router yet.",
	}, renderWifiClients)
}

// wifiJoinOutOfDate reports whether a client list was derived before the tables
// it joins against could fill it. Pure over its inputs, so it is tested without a
// router.
func wifiJoinOutOfDate(w *collect.WirelessPayload, leases interface {
	Last() *collect.LeasesPayload
}, arp interface{ IPForMAC(string) string }) bool {
	if leases != nil {
		if l := leases.Last(); l != nil && l.TS > w.TS {
			return true
		}
	}
	if arp != nil {
		for _, c := range w.Clients {
			if c.IP == "" && arp.IPForMAC(c.MAC) != "" {
				return true
			}
		}
	}
	return false
}

type liveWifiClient struct {
	Name     string `json:"name,omitempty"`
	Comment  string `json:"dhcpComment,omitempty"`
	MAC      string `json:"mac"`
	IP       string `json:"ip,omitempty"`
	SSID     string `json:"ssid,omitempty"`
	Iface    string `json:"interface,omitempty"`
	Band     string `json:"band,omitempty"`
	Standard string `json:"standard,omitempty"`
	Signal   int    `json:"signalDbm"`
	TxRate   string `json:"txRate,omitempty"`
	RxRate   string `json:"rxRate,omitempty"`
	Uptime   string `json:"connectedFor,omitempty"`
	CAPsMAN  bool   `json:"viaCapsman,omitempty"`
}

// renderWifiClients shapes the registration table for the model, strongest
// signal first so a cap keeps the clients most likely to be asked about by name
// and drops the fringe of a busy site.
func renderWifiClients(p *collect.WirelessPayload) any {
	rows := make([]liveWifiClient, 0, len(p.Clients))
	for _, c := range p.Clients {
		rows = append(rows, liveWifiClient{Name: c.Name, Comment: c.Comment, MAC: c.MAC, IP: c.IP,
			SSID: c.SSID, Iface: c.Iface, Band: c.Band, Standard: c.Standard, Signal: c.Signal,
			TxRate: c.TxRate, RxRate: c.RxRate, Uptime: c.Uptime, CAPsMAN: c.Source != ""})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Signal > rows[j].Signal })
	kept, truncated := capRows(rows)
	note := ""
	switch {
	case p.Mode == "none":
		note = "This router has no wireless stack (neither /interface/wifi nor /interface/wireless), so it has no wireless clients of its own."
	case len(p.Clients) == 0 && p.SSIDsManagedElsewhere > 0:
		note = "This router's radios take their SSIDs from a CAPsMAN manager; its clients may be listed on that manager instead."
	}
	ssids := p.SSIDs
	if ssids == nil {
		ssids = []collect.WirelessSSID{}
	}
	return struct {
		Note      string                 `json:"note,omitempty"`
		Stack     string                 `json:"wirelessStack,omitempty"`
		SSIDs     []collect.WirelessSSID `json:"ssids"`
		Clients   []liveWifiClient       `json:"clientsStrongestFirst"`
		Total     int                    `json:"totalClients"`
		Truncated bool                   `json:"truncated"`
	}{note, p.Mode, ssids, kept, len(p.Clients), truncated}
}

// ── list_connections ────────────────────────────────────────────────────────

// liveConnections joins like list_wifi_clients, with one more table.
//
// A source's name and MAC come from the DHCP leases and ARP, and whether an
// address is LOCAL at all comes from the DHCP networks' LAN ranges. With those
// unloaded every source would read as remote and nameless. Learned the hard way
// on list_wifi_clients, and applied here from the start: load a join that has
// never loaded, and re-derive the summary when a join is newer than it or ARP can
// now name a source it lists without a MAC.
func liveConnections(rs *session.Session, t aitools.Tool) string {
	col := rs.Conns()
	leases, arp, nets := rs.DHCPLeases(), rs.ARP(), rs.DHCPNetworks()
	rederive := false
	if leases != nil && leases.Last() == nil {
		leases.RefreshNow()
		rederive = true
	}
	if arp != nil && arp.Last() == nil {
		arp.RefreshNow()
		rederive = true
	}
	if nets != nil && nets.Last() == nil {
		nets.RefreshNow()
		rederive = true
	}
	if p := col.Last(); p != nil && !rederive {
		var netTS int64
		if nets != nil {
			if n := nets.Last(); n != nil {
				netTS = n.TS
			}
		}
		rederive = connsJoinOutOfDate(p, leases, arp, netTS)
	}
	if rederive {
		col.Tick()
	}
	return liveAnswer(t, liveSource[collect.ConnsPayload]{
		last:    col.Last,
		stamp:   func(p *collect.ConnsPayload) (int64, int) { return p.TS, p.PollMs },
		refresh: col.Tick, // Tick reads the router directly, not through the cache
		menu:    "/ip/firewall/connection",
		absent:  "The connection readings are not available from this router yet.",
	}, renderConnections)
}

// connsJoinOutOfDate reports whether a connection summary was built before the
// tables it joins against could fill it.
func connsJoinOutOfDate(p *collect.ConnsPayload, leases interface {
	Last() *collect.LeasesPayload
}, arp interface{ MACForIP(string) (string, string) }, networksTS int64) bool {
	if networksTS > p.TS {
		return true
	}
	if leases != nil {
		if l := leases.Last(); l != nil && l.TS > p.TS {
			return true
		}
	}
	if arp != nil {
		for _, s := range p.TopSources {
			if s.MAC == "" {
				if mac, _ := arp.MACForIP(s.IP); mac != "" {
					return true
				}
			}
		}
	}
	return false
}

type liveConnDest struct {
	Destination string `json:"destination"`
	Count       int    `json:"connections"`
	Country     string `json:"country,omitempty"`
	City        string `json:"city,omitempty"`
	Org         string `json:"organisation,omitempty"`
	Category    string `json:"category,omitempty"`
}

type liveConnCountry struct {
	Country string   `json:"country"`
	City    string   `json:"city,omitempty"`
	Count   int      `json:"connections"`
	TopOrgs []string `json:"topOrganisations"`
}

func strOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// renderConnections is the SUMMARY only. The four per-country and per-source
// indexes the page builds for drill-down are left out: they are large, they are
// built only while the page is open, and a question the summary cannot answer is
// one for the Connections page rather than for a context window.
func renderConnections(p *collect.ConnsPayload) any {
	dests := make([]liveConnDest, 0, len(p.TopDestinations))
	for _, d := range p.TopDestinations {
		dests = append(dests, liveConnDest{Destination: d.Key, Count: d.Count, Country: d.Country,
			City: d.City, Org: strOf(d.Org), Category: strOf(d.Cat)})
	}
	countries := make([]liveConnCountry, 0, len(p.TopCountries))
	for _, c := range p.TopCountries {
		orgs := []string{}
		for i, o := range c.Orgs {
			if i == 3 {
				break
			}
			orgs = append(orgs, fmt.Sprintf("%s (%d)", o.Org, o.Count))
		}
		countries = append(countries, liveConnCountry{Country: c.CC, City: c.City, Count: c.Count, TopOrgs: orgs})
	}
	sources, tS := capRows(p.TopSources)
	destKept, tD := capRows(dests)
	note := ""
	if p.ProcessingCapped {
		note = fmt.Sprintf("Only %d of %d connections were aggregated, so the top lists are a sample.", p.Processed, p.Total)
	}
	ports := p.TopPorts
	if ports == nil {
		ports = []collect.ConnPort{}
	}
	return struct {
		Note         string                  `json:"note,omitempty"`
		Total        int                     `json:"totalConnections"`
		Protocols    collect.ConnProtoCounts `json:"protocols"`
		Sources      []collect.ConnSource    `json:"topSources"`
		Destinations []liveConnDest          `json:"topDestinations"`
		Countries    []liveConnCountry       `json:"topCountries"`
		Ports        []collect.ConnPort      `json:"topPorts"`
		Truncated    bool                    `json:"truncated"`
	}{note, p.Total, p.ProtoCounts, sources, destKept, countries, ports, tS || tD}
}

// ── list_bandwidth ──────────────────────────────────────────────────────────

// bandwidthSampleGap is the window a forced bandwidth reading measures over.
//
// ── TWO READINGS, BECAUSE A RATE IS A DIFFERENCE ────────────────────────────
//
// Per-connection rates are byte-counter deltas against the previous reading.
// With the Bandwidth page closed the previous reading is old or absent: a
// connection that existed then is averaged over the whole gap, and a new one has
// nothing to difference against and reads 0. Either way the answer to "what is
// using bandwidth now" would be wrong in the most misleading direction, idle. Two
// readings two seconds apart give every connection present in both a real
// two-second rate. It costs one extra connection-table read, only when the page
// is not already measuring.
const bandwidthSampleGap = 2 * time.Second

func liveBandwidth(rs *session.Session, t aitools.Tool) string {
	col := rs.Bandwidth()
	leases, arp, nets := rs.DHCPLeases(), rs.ARP(), rs.DHCPNetworks()
	loaded := false
	if leases != nil && leases.Last() == nil {
		leases.RefreshNow()
		loaded = true
	}
	if arp != nil && arp.Last() == nil {
		arp.RefreshNow()
		loaded = true
	}
	if nets != nil && nets.Last() == nil {
		nets.RefreshNow()
		loaded = true
	}
	sample := func() {
		col.Tick()
		time.Sleep(bandwidthSampleGap)
		col.Tick()
	}
	// FRESH, SO THE PAGE IS MEASURING: one tick against its recent previous reading
	// re-derives the names, with no second sample. Done when a join was just
	// loaded or is newer than the reading. A DUE reading needs no such step: the
	// two-sample refresh below derives against the joins as they now are.
	if p := col.Last(); p != nil && !liveReadingDue(t.Freshness, true, time.Now().UnixMilli(), p.TS, p.PollMs) {
		var netTS int64
		if nets != nil {
			if n := nets.Last(); n != nil {
				netTS = n.TS
			}
		}
		if loaded || bandwidthJoinOutOfDate(p, leases, arp, netTS) {
			col.Tick()
		}
	}
	return liveAnswer(t, liveSource[collect.BandwidthPayload]{
		last:    col.Last,
		stamp:   func(p *collect.BandwidthPayload) (int64, int) { return p.TS, p.PollMs },
		refresh: sample,
		menu:    "/ip/firewall/connection",
		absent:  "The bandwidth readings are not available from this router yet.",
	}, renderBandwidth)
}

// bandwidthJoinOutOfDate is connsJoinOutOfDate over the bandwidth rows.
func bandwidthJoinOutOfDate(p *collect.BandwidthPayload, leases interface {
	Last() *collect.LeasesPayload
}, arp interface{ MACForIP(string) (string, string) }, networksTS int64) bool {
	if networksTS > p.TS {
		return true
	}
	if leases != nil {
		if l := leases.Last(); l != nil && l.TS > p.TS {
			return true
		}
	}
	if arp != nil {
		for _, d := range p.Devices {
			if d.IsLan && d.MAC == "" {
				if mac, _ := arp.MACForIP(d.SrcIP); mac != "" {
					return true
				}
			}
		}
	}
	return false
}

type liveBandwidthRow struct {
	Name      string  `json:"device,omitempty"`
	SrcIP     string  `json:"localIp"`
	MAC       string  `json:"mac,omitempty"`
	DstIP     string  `json:"remoteIp"`
	Country   string  `json:"country,omitempty"`
	Org       string  `json:"organisation,omitempty"`
	Proto     string  `json:"protocol,omitempty"`
	Iface     string  `json:"interface,omitempty"`
	RxMbps    float64 `json:"downloadMbps"`
	TxMbps    float64 `json:"uploadMbps"`
	TotalMbps float64 `json:"totalMbps"`
}

// renderBandwidth lists the connections actually moving data, busiest first.
func renderBandwidth(p *collect.BandwidthPayload) any {
	rows := make([]liveBandwidthRow, 0, len(p.Devices))
	idle := 0
	var total float64
	for _, d := range p.Devices {
		total += d.TotalMbps
		if d.TotalMbps <= 0 {
			idle++
			continue
		}
		rows = append(rows, liveBandwidthRow{Name: d.Name, SrcIP: d.SrcIP, MAC: d.MAC, DstIP: d.DstIP,
			Country: d.Country, Org: strOf(d.Org), Proto: d.Proto, Iface: d.Iface,
			RxMbps: d.RxMbps, TxMbps: d.TxMbps, TotalMbps: d.TotalMbps})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].TotalMbps > rows[j].TotalMbps })
	kept, truncated := capRows(rows)
	return struct {
		TotalMbps float64            `json:"totalMbpsAcrossConnections"`
		Active    []liveBandwidthRow `json:"activeConnectionsBusiestFirst"`
		Idle      int                `json:"idleConnections"`
		Truncated bool               `json:"truncated"`
	}{math.Round(total*1000) / 1000, kept, idle, truncated}
}

// ── list_topology ───────────────────────────────────────────────────────────

func liveTopology(rs *session.Session, t aitools.Tool) string {
	col := rs.Topology()
	return liveAnswer(t, liveSource[collect.TopologyPayload]{
		last:    col.Last,
		stamp:   func(p *collect.TopologyPayload) (int64, int) { return p.TS, p.PollMs },
		refresh: col.Tick, // Tick reads /ip/neighbor directly
		menu:    "/ip/neighbor",
		absent:  "The topology readings are not available from this router yet.",
	}, renderTopology)
}

type liveTopoDevice struct {
	Role        string   `json:"role"` // this router | neighbour
	Name        string   `json:"name"`
	Identity    string   `json:"identity,omitempty"`
	IP          string   `json:"ip,omitempty"`
	MAC         string   `json:"mac,omitempty"`
	Type        string   `json:"type,omitempty"`
	Platform    string   `json:"platform,omitempty"`
	Board       string   `json:"board,omitempty"`
	Version     string   `json:"version,omitempty"`
	SeenOn      []string `json:"seenOnLocalPorts,omitempty"`
	RemoteIface string   `json:"theirPort,omitempty"`
	RTT         *float64 `json:"pingMs,omitempty"`
	Loss        *float64 `json:"pingLossPct,omitempty"`
	Status      string   `json:"status,omitempty"`
	Gone        bool     `json:"gone,omitempty"`
	Clients     int      `json:"clients"`
}

type liveTopoLink struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Iface       string `json:"localPort,omitempty"`
	RemoteIface string `json:"remotePort,omitempty"`
	Inferred    bool   `json:"inferred,omitempty"`
	Pinned      bool   `json:"pinnedByOperator,omitempty"`
	Gone        bool   `json:"gone,omitempty"`
}

// renderTopology keeps the infrastructure and counts the clients.
//
// ── CLIENTS ARE COUNTED, NOT LISTED ─────────────────────────────────────────
//
// The node list mixes the router, its neighbours and every client below them.
// Clients are already answered in more detail by list_wifi_clients and
// list_connections, and listing them here would spend the budget on the part of
// the graph this tool is not for. Links are kept only between infrastructure
// nodes, named rather than keyed, so the model can say "sw1 port 3".
func renderTopology(p *collect.TopologyPayload) any {
	devices := []liveTopoDevice{}
	names := map[string]string{}
	for _, n := range p.Nodes {
		switch v := n.(type) {
		case *collect.TopoCore:
			names[v.Key] = firstNonEmpty(v.Identity, v.Name)
			devices = append(devices, liveTopoDevice{Role: "this router", Name: v.Name, Identity: v.Identity,
				IP: v.IP, MAC: v.MAC, Type: v.Type, Platform: v.Platform, Board: v.Board, Version: v.Version,
				Status: v.Status, Clients: v.ClientCount})
		case collect.TopoCore:
			names[v.Key] = firstNonEmpty(v.Identity, v.Name)
			devices = append(devices, liveTopoDevice{Role: "this router", Name: v.Name, Identity: v.Identity,
				IP: v.IP, MAC: v.MAC, Type: v.Type, Platform: v.Platform, Board: v.Board, Version: v.Version,
				Status: v.Status, Clients: v.ClientCount})
		case *collect.TopoNeighbor:
			names[v.Key] = firstNonEmpty(v.Identity, v.Name)
			devices = append(devices, topoNeighbourRow(*v))
		case collect.TopoNeighbor:
			names[v.Key] = firstNonEmpty(v.Identity, v.Name)
			devices = append(devices, topoNeighbourRow(v))
		}
	}
	links := []liveTopoLink{}
	for _, e := range p.Edges {
		from, okF := names[e.From]
		to, okT := names[e.To]
		if e.Client || !okF || !okT {
			continue
		}
		links = append(links, liveTopoLink{From: from, To: to, Iface: firstNonEmpty(e.ViaPort, e.Iface),
			RemoteIface: e.RemoteIface, Inferred: e.Inferred, Pinned: e.Pinned, Gone: e.Gone})
	}
	keptD, tD := capRows(devices)
	keptL, tL := capRows(links)
	note := ""
	switch {
	case p.PermissionDenied:
		note = "The router refused /ip/neighbor to MikroDash's API user, so no topology can be discovered; this is a permission on the router."
	case len(devices) <= 1:
		note = "No neighbouring devices were discovered. Neighbour discovery may be switched off or limited to some interfaces (see discovery below)."
	}
	return struct {
		Note      string                 `json:"note,omitempty"`
		Discovery *collect.TopoDiscovery `json:"discovery,omitempty"`
		Devices   []liveTopoDevice       `json:"devices"`
		Links     []liveTopoLink         `json:"links"`
		Clients   int                    `json:"totalClients"`
		Truncated bool                   `json:"truncated"`
	}{note, p.Discovery, keptD, keptL, p.ClientCount, tD || tL}
}

func topoNeighbourRow(v collect.TopoNeighbor) liveTopoDevice {
	return liveTopoDevice{Role: "neighbour", Name: v.Name, Identity: v.Identity, IP: v.IP, MAC: v.MAC,
		Type: v.Type, Platform: v.Platform, Board: v.Board, Version: v.Version, SeenOn: v.Via,
		RemoteIface: v.RemoteIface, RTT: v.RTT, Loss: v.Loss, Status: v.Status, Gone: v.Gone,
		Clients: v.ClientCount}
}

// ── list_wireguard_status ───────────────────────────────────────────────────

// liveWireguardStatus samples twice when the VPN page is not measuring, for the
// reason list_bandwidth does: peer rates are byte-counter deltas against the
// previous reading, which after a suspend is old, and a first reading is 0.
func liveWireguardStatus(rs *session.Session, t aitools.Tool) string {
	col := rs.VPN()
	return liveAnswer(t, liveSource[collect.VPNPayload]{
		last:  col.Last,
		stamp: func(p *collect.VPNPayload) (int64, int) { return p.TS, p.PollMs },
		refresh: func() {
			col.RefreshNow()
			time.Sleep(bandwidthSampleGap)
			col.RefreshNow()
		},
		menu:   "/interface/wireguard/peers",
		absent: "The VPN readings are not available from this router yet.",
	}, renderWireguardStatus)
}

type liveWgPeer struct {
	Name          string  `json:"name,omitempty"`
	Comment       string  `json:"comment,omitempty"`
	KeyPrefix     string  `json:"publicKeyPrefix,omitempty"`
	Interface     string  `json:"interface,omitempty"`
	State         string  `json:"state,omitempty"`
	LastHandshake string  `json:"lastHandshake,omitempty"`
	Endpoint      string  `json:"currentEndpoint,omitempty"`
	AllowedIP     string  `json:"allowedAddresses,omitempty"`
	Keepalive     string  `json:"persistentKeepalive,omitempty"`
	RxMbps        float64 `json:"downloadMbps"`
	TxMbps        float64 `json:"uploadMbps"`
}

// renderWireguardStatus shapes the VPN payload's WireGuard and IPsec halves.
//
// ── A KEY PREFIX, NOT THE KEY ───────────────────────────────────────────────
//
// A WireGuard public key is not a secret, but it is a stable identifier of the
// far end, and 44 characters of it per peer spend the budget on nothing a person
// reads. Eight characters tell two peers apart and match what an operator sees
// in Winbox's truncated column. Row ids are not passed.
func renderWireguardStatus(p *collect.VPNPayload) any {
	peers := make([]liveWgPeer, 0, len(p.Tunnels))
	active := 0
	for _, tn := range p.Tunnels {
		key := tn.PublicKey
		if len(key) > 8 {
			key = key[:8]
		}
		if tn.State == "active" {
			active++
		}
		peers = append(peers, liveWgPeer{Name: tn.Name, Comment: tn.Comment, KeyPrefix: key,
			Interface: tn.Interface, State: tn.State, LastHandshake: tn.LastHandshake,
			Endpoint: tn.Endpoint, AllowedIP: tn.AllowedIP, Keepalive: tn.Keepalive,
			RxMbps: math.Round(tn.RXRate*8/1e6*1000) / 1000, TxMbps: math.Round(tn.TXRate*8/1e6*1000) / 1000})
	}
	keptW, tW := capRows(peers)
	ipsec := p.Ipsec
	if ipsec == nil {
		ipsec = []collect.IpsecTunnel{}
	}
	keptI, tI := capRows(ipsec)
	return struct {
		ActiveWireguard int                   `json:"activeWireguardPeers"`
		TotalWireguard  int                   `json:"totalWireguardPeers"`
		Wireguard       []liveWgPeer          `json:"wireguardPeers"`
		Ipsec           []collect.IpsecTunnel `json:"activeIpsecPeers"`
		Truncated       bool                  `json:"truncated"`
	}{active, len(p.Tunnels), keptW, keptI, tW || tI}
}

// ── list_ppp_sessions ───────────────────────────────────────────────────────

// livePPPSessions carries no rates. The collector derives them from /ppp/active's
// bytes-in and bytes-out, which RouterOS 7.24 does not return (measured on the
// hAP AC2 with a live L2TP session: the counters are on the dynamic
// <service-user> interface instead), so every rate it holds is 0 or null. A 0
// handed to the model reads as "idle"; the answer says where the traffic is.
func livePPPSessions(rs *session.Session, t aitools.Tool) string {
	col := rs.PPP()
	return liveAnswer(t, liveSource[collect.PPPPayload]{
		last:    col.Last,
		stamp:   func(p *collect.PPPPayload) (int64, int) { return p.TS, p.PollMs },
		refresh: col.RefreshNow,
		menu:    "/ppp/active",
		absent:  "The PPP readings are not available from this router yet.",
	}, renderPPPSessions)
}

type livePPPSession struct {
	User      string `json:"user"`
	Service   string `json:"service,omitempty"`
	Address   string `json:"address,omitempty"`
	CallerID  string `json:"callerId,omitempty"`
	Uptime    string `json:"uptime,omitempty"`
	Encoding  string `json:"encoding,omitempty"`
	LimitIn   *int   `json:"limitInBps,omitempty"`
	LimitOut  *int   `json:"limitOutBps,omitempty"`
	Interface string `json:"interface"`
}

// pppTrafficNote tells the model where a session's traffic can be read.
const pppTrafficNote = "Per-session traffic is not in this reading. Each session has a dynamic interface, " +
	"named in 'interface'; list_interface_traffic reports its rates."

func renderPPPSessions(p *collect.PPPPayload) any {
	rows := make([]livePPPSession, 0, len(p.Sessions))
	for _, x := range p.Sessions {
		rows = append(rows, livePPPSession{User: x.Name, Service: x.Service, Address: x.Address,
			CallerID: x.CallerID, Uptime: x.Uptime, Encoding: x.Encoding, LimitIn: x.LimitIn,
			LimitOut: x.LimitOut, Interface: pppInterfaceName(x.Service, x.Name)})
	}
	kept, truncated := capRows(rows)
	by := p.ByService
	if by == nil {
		by = map[string]int{}
	}
	return struct {
		Sessions  []livePPPSession `json:"activeSessions"`
		ByService map[string]int   `json:"sessionsByService"`
		Traffic   string           `json:"traffic"`
		Truncated bool             `json:"truncated"`
	}{kept, by, pppTrafficNote, truncated}
}

// pppInterfaceName is the dynamic interface RouterOS creates for a server-side
// session: <l2tp-looptest> for user looptest on L2TP, as /interface printed it.
func pppInterfaceName(service, user string) string {
	if service == "" {
		return ""
	}
	return "<" + strings.ToLower(service) + "-" + user + ">"
}

// ── list_bgp_sessions ───────────────────────────────────────────────────────

// liveBGPSessions answers from the routing collector, whose fast lane is the BGP
// session menu. A forced refresh also re-reads the route tables (RefreshNow
// resets the slow-lane count), which costs more than the question needs but keeps
// one refresh path for the collector.
func liveBGPSessions(rs *session.Session, t aitools.Tool) string {
	col := rs.Routing()
	return liveAnswer(t, liveSource[collect.RoutingPayload]{
		last:    col.Last,
		stamp:   func(p *collect.RoutingPayload) (int64, int) { return p.TS, p.PollMs },
		refresh: col.RefreshNow,
		menu:    "/routing/bgp/session",
		absent:  "The routing readings are not available from this router yet.",
	}, renderBGPSessions)
}

type liveBGPPeer struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	RemoteAddr   string `json:"remoteAddress,omitempty"`
	RemoteAs     int64  `json:"remoteAs,omitempty"`
	PeerType     string `json:"peerType"`
	State        string `json:"state"`
	UptimeSec    int    `json:"uptimeSeconds"`
	Prefixes     int    `json:"prefixes"`
	MessagesSent int    `json:"messagesSent"`
	MessagesRecv int    `json:"messagesReceived"`
	LastError    string `json:"lastError,omitempty"`
	HoldTime     int    `json:"holdTimeSeconds,omitempty"`
	Keepalive    int    `json:"keepaliveSeconds,omitempty"`
	Flapping     bool   `json:"flapping"`
}

// renderBGPSessions leaves out the prefix history (a sparkline's data) and the
// route table, which list_route carries.
func renderBGPSessions(p *collect.RoutingPayload) any {
	rows := make([]liveBGPPeer, 0, len(p.Peers))
	for _, x := range p.Peers {
		rows = append(rows, liveBGPPeer{Name: x.Name, Description: x.Description, RemoteAddr: x.RemoteAddr,
			RemoteAs: x.RemoteAs, PeerType: x.PeerType, State: x.State, UptimeSec: x.UptimeSec,
			Prefixes: x.Prefixes, MessagesSent: x.MessagesSent, MessagesRecv: x.MessagesRecv,
			LastError: x.LastError, HoldTime: x.HoldTime, Keepalive: x.Keepalive, Flapping: x.Flapping})
	}
	kept, truncated := capRows(rows)
	return struct {
		Sessions    []liveBGPPeer `json:"sessions"`
		Total       int           `json:"total"`
		Established int           `json:"established"`
		Down        int           `json:"down"`
		Truncated   bool          `json:"truncated"`
	}{kept, p.Summary.Total, p.Summary.Established, p.Summary.Down, truncated}
}

// ── list_capsman_caps ───────────────────────────────────────────────────────

// liveCapsman is metadata: which CAPs are attached changes on the scale of
// reboots, and a forced refresh re-reads the manager, CAP and profile menus as
// well as the registration table.
func liveCapsman(rs *session.Session, t aitools.Tool) string {
	col := rs.Capsman()
	return liveAnswer(t, liveSource[collect.CapsmanPayload]{
		last:    col.Last,
		stamp:   func(p *collect.CapsmanPayload) (int64, int) { return p.TS, p.PollMs },
		refresh: col.RefreshNow,
		menu:    "/interface/wifi/capsman",
		absent:  "The CAPsMAN readings are not available from this router yet.",
	}, renderCapsman)
}

type liveCapsRadio struct {
	Interface string `json:"interface"`
	RadioMac  string `json:"radioMac,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
}

type liveCap struct {
	Identity      string          `json:"identity"`
	Address       string          `json:"address,omitempty"`
	Board         string          `json:"board,omitempty"`
	Version       string          `json:"version,omitempty"`
	BaseMac       string          `json:"baseMac,omitempty"`
	State         string          `json:"state,omitempty"`
	ConnectedTime string          `json:"connectedTime,omitempty"`
	Uptime        string          `json:"uptime,omitempty"`
	Radios        []liveCapsRadio `json:"radios"`
	Clients       int             `json:"clients"`
	Legacy        bool            `json:"legacyCapsman,omitempty"`
}

func capsRadios(in []collect.CapsRadio) []liveCapsRadio {
	out := make([]liveCapsRadio, 0, len(in))
	for _, r := range in {
		out = append(out, liveCapsRadio{Interface: r.Interface, RadioMac: r.RadioMac, Disabled: r.Disabled})
	}
	return out
}

// renderCapsman leaves out the certificates, serials and per-CAP client rows
// (list_wifi_clients has the clients), and the profiles, which have their own
// tools.
func renderCapsman(p *collect.CapsmanPayload) any {
	caps := make([]liveCap, 0, len(p.Caps))
	for _, c := range p.Caps {
		caps = append(caps, liveCap{Identity: c.Identity, Address: c.Address, Board: c.BoardName,
			Version: c.Version, BaseMac: c.BaseMac, State: c.State, ConnectedTime: c.ConnectedTime,
			Uptime: c.Uptime, Radios: capsRadios(c.Radios), Clients: c.ClientCount, Legacy: c.Legacy})
	}
	kept, truncated := capRows(caps)
	type manager struct {
		Enabled       bool     `json:"enabled"`
		Interfaces    []string `json:"interfaces"`
		UpgradePolicy string   `json:"upgradePolicy,omitempty"`
	}
	type capMode struct {
		Enabled         bool   `json:"enabled"`
		ManagerAddress  string `json:"managerAddress,omitempty"`
		ManagerIdentity string `json:"managerIdentity,omitempty"`
	}
	ifaces := p.Manager.Interfaces
	if ifaces == nil {
		ifaces = []string{}
	}
	return struct {
		Available     bool               `json:"wifiCapsmanAvailable"`
		LegacyManager bool               `json:"legacyCapsmanEnabled"`
		Role          string             `json:"role"`
		Manager       manager            `json:"manager"`
		Cap           capMode            `json:"capMode"`
		Caps          []liveCap          `json:"caps"`
		LocalRadios   []liveCapsRadio    `json:"localRadios"`
		Totals        collect.CapsTotals `json:"totals"`
		Truncated     bool               `json:"truncated"`
	}{p.Available, p.LegacyManager, p.Role,
		manager{p.Manager.Enabled, ifaces, p.Manager.UpgradePolicy},
		capMode{p.Cap.Enabled, p.Cap.CurrentAddress, p.Cap.CurrentIdentity},
		kept, capsRadios(p.LocalRadios), p.Totals, truncated}
}

// ── list_dhcp_networks ──────────────────────────────────────────────────────

// liveDHCPNetworks re-derives when the lease counts disagree with the lease
// table as it stands. The counts are a join against the leases collector, so a
// payload derived before the leases loaded says 0 leased while it is still fresh
// (the list_wifi_clients trap). Checked in memory, so it costs no router read.
func liveDHCPNetworks(rs *session.Session, t aitools.Tool) string {
	col := rs.DHCPNetworks()
	leases := rs.DHCPLeases()
	if leases != nil && leases.Last() == nil {
		leases.RefreshNow()
	}
	if p := col.Last(); p != nil && leases != nil && dhcpJoinOutOfDate(p, leases.UsedLeaseIPs()) {
		col.RefreshNow()
	}
	return liveAnswer(t, liveSource[collect.LanPayload]{
		last:    col.Last,
		stamp:   func(p *collect.LanPayload) (int64, int) { return p.TS, p.PollMs },
		refresh: col.RefreshNow,
		menu:    "/ip/dhcp-server/network",
		absent:  "The DHCP network readings are not available from this router yet.",
	}, renderDHCPNetworks)
}

// dhcpJoinOutOfDate reports whether the used leases that fall inside the
// payload's networks number differently from the payload's total.
func dhcpJoinOutOfDate(p *collect.LanPayload, used []string) bool {
	nets := make([]*net.IPNet, 0, len(p.Networks))
	for _, n := range p.Networks {
		if _, cidr, err := net.ParseCIDR(strings.TrimSpace(n.CIDR)); err == nil {
			nets = append(nets, cidr)
		}
	}
	count := 0
	for _, ip := range used {
		addr := net.ParseIP(strings.TrimSpace(ip))
		if addr == nil {
			continue
		}
		for _, cidr := range nets {
			if cidr.Contains(addr) {
				count++
			}
		}
	}
	return count != p.TotalLeases
}

type liveDHCPNetwork struct {
	Subnet   string   `json:"subnet"`
	Gateway  string   `json:"gateway,omitempty"`
	DNS      string   `json:"dnsServers,omitempty"`
	Leased   int      `json:"leased"`
	PoolSize int      `json:"poolSize"`
	InUsePct *float64 `json:"inUsePercent"`
}

// usePercent is null for a network with no pool: a static-only subnet is not
// 0% used, it has nothing to run out of.
func usePercent(leased, pool int) *float64 {
	if pool <= 0 {
		return nil
	}
	v := math.Round(float64(leased)*1000/float64(pool)) / 10
	return &v
}

func renderDHCPNetworks(p *collect.LanPayload) any {
	rows := make([]liveDHCPNetwork, 0, len(p.Networks))
	for _, n := range p.Networks {
		rows = append(rows, liveDHCPNetwork{Subnet: n.CIDR, Gateway: n.Gateway, DNS: n.DNS,
			Leased: n.LeaseCount, PoolSize: n.PoolSize, InUsePct: usePercent(n.LeaseCount, n.PoolSize)})
	}
	kept, truncated := capRows(rows)
	internet := make([]string, 0, len(p.InternetIface))
	for _, i := range p.InternetIface {
		internet = append(internet, i.Name)
	}
	return struct {
		Networks      []liveDHCPNetwork `json:"networks"`
		TotalLeased   int               `json:"totalLeased"`
		TotalPoolSize int               `json:"totalPoolSize"`
		TotalInUsePct *float64          `json:"totalInUsePercent"`
		WanAddress    string            `json:"wanAddress,omitempty"`
		Internet      []string          `json:"internetInterfaces"`
		Truncated     bool              `json:"truncated"`
	}{kept, p.TotalLeases, p.TotalPoolSize, usePercent(p.TotalLeases, p.TotalPoolSize), p.WanIP, internet, truncated}
}
