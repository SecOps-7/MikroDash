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
