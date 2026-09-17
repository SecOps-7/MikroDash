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
	"math"
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
var liveToolReaders = map[string]func(cn *conn, t aitools.Tool) string{
	"ifStatus": (*conn).liveInterfaceTraffic,
	"wan":      (*conn).liveWanStatus,
	"packages": (*conn).livePackages,
	"rosusers": (*conn).liveRouterUsers,
	"queues":   (*conn).liveQueues,
}

func (cn *conn) runLiveTool(t aitools.Tool) string {
	read, ok := liveToolReaders[t.Collector]
	if !ok {
		return "That tool is not available on this build."
	}
	if cn.rsession == nil {
		return "No device is selected, so nothing was read."
	}
	return read(cn, t)
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

func (cn *conn) liveInterfaceTraffic(t aitools.Tool) string {
	col := cn.rsession.IfStatus()
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

func (cn *conn) liveWanStatus(t aitools.Tool) string {
	col := cn.rsession.Wan()
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

func (cn *conn) livePackages(t aitools.Tool) string {
	col := cn.rsession.Packages()
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

func (cn *conn) liveRouterUsers(t aitools.Tool) string {
	col := cn.rsession.RosUsers()
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
func (cn *conn) liveQueues(t aitools.Tool) string {
	col := cn.rsession.Queues()
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
