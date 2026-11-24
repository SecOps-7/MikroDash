package server

// list_app_usage: the AI agent's view of the Apps Usage page (the operator,
// 2026-11-24).
//
// ── WHY IT NEEDS THE SERVER ────────────────────────────────────────────────
//
// Every other live tool reads one collector on the router's session
// (liveToolReaders). This one also reads the stored per-app history, which
// lives in the database, so its reader takes the server as well
// (liveServerReaders). One reader, one map entry; the dispatcher checks this
// map first (ai_tools.go), after the page permission like every tool.
//
// ── WHAT IT GIVES, AND WHAT IT KEEPS BACK ──────────────────────────────────
//
// The last hour from the apps collector, as the page draws it: each app's
// bytes and share, its busiest devices, and the unnamed total. Then the stored
// totals over 24 hours, 7 days and 30 days for a router with Reporting on. The
// busiest apps only, so a long tail cannot fill the model's context. No
// destinations and no DNS names: the Unnamed detail is closer to a browsing
// history than to a usage summary, and the page keeps those to itself.

import (
	"encoding/json"
	"time"

	"mikrodash/internal/aicontext"
	"mikrodash/internal/aitools"
	"mikrodash/internal/apps"
	"mikrodash/internal/collect"
	"mikrodash/internal/session"
)

// liveServerReaders are the live tools whose reader also needs the server.
// TestEveryLiveToolHasAReader holds them to the catalogue with liveToolReaders.
var liveServerReaders = map[string]func(s *Server, rs *session.Session, t aitools.Tool) string{
	"apps": liveAppUsage,
}

// How much of each period the answer lists.
const (
	aiAppsListed  = 15
	aiAppsDevices = 3
)

type aiAppDevice struct {
	Name  string `json:"name,omitempty"`
	IP    string `json:"ip"`
	Bytes int64  `json:"bytes"`
}

type aiApp struct {
	Name     string        `json:"name"`
	Category string        `json:"category"`
	RxBytes  int64         `json:"downloadBytes"`
	TxBytes  int64         `json:"uploadBytes"`
	Total    int64         `json:"totalBytes"`
	Share    float64       `json:"sharePct"`
	Devices  int           `json:"devices,omitempty"`
	Top      []aiAppDevice `json:"busiestDevices,omitempty"`
}

type aiAppPeriod struct {
	Period       string  `json:"period"`
	From         int64   `json:"fromMs"`
	To           int64   `json:"toMs"`
	TotalBytes   int64   `json:"totalBytes"`
	UnnamedBytes int64   `json:"unnamedBytes"`
	Apps         []aiApp `json:"apps"`
	More         int     `json:"moreApps,omitempty"`
	Note         string  `json:"note,omitempty"`
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }

// aiLastHour is the collector's window as the answer lists it. Pure.
func aiLastHour(p *collect.AppUsagePayload) aiAppPeriod {
	out := aiAppPeriod{Period: "last hour", From: p.From, To: p.To, TotalBytes: p.RxBytes + p.TxBytes,
		UnnamedBytes: p.UnnamedBytes, Apps: []aiApp{}}
	for i, a := range p.Apps {
		if i >= aiAppsListed {
			out.More = len(p.Apps) - aiAppsListed
			break
		}
		app := aiApp{Name: a.Name, Category: a.Category, RxBytes: a.RxBytes, TxBytes: a.TxBytes,
			Total: a.TotalBytes, Share: round1(a.Share), Devices: a.Devices}
		for j, d := range p.Devices[a.ID] {
			if j >= aiAppsDevices {
				break
			}
			app.Top = append(app.Top, aiAppDevice{Name: d.Name, IP: d.IP, Bytes: d.RxBytes + d.TxBytes})
		}
		out.Apps = append(out.Apps, app)
	}
	return out
}

// aiStored is one stored period as the answer lists it. Pure.
func aiStored(label string, h AppHistory) aiAppPeriod {
	total := h.UnnamedRxBytes + h.UnnamedTxBytes
	for _, a := range h.Apps {
		total += a.RxBytes + a.TxBytes
	}
	out := aiAppPeriod{Period: label, From: h.From, To: h.To, TotalBytes: total,
		UnnamedBytes: h.UnnamedRxBytes + h.UnnamedTxBytes, Apps: []aiApp{}}
	for i, a := range h.Apps {
		if i >= aiAppsListed {
			out.More = len(h.Apps) - aiAppsListed
			break
		}
		app := aiApp{Name: a.Name, Category: a.Category, RxBytes: a.RxBytes, TxBytes: a.TxBytes,
			Total: a.RxBytes + a.TxBytes}
		if total > 0 {
			app.Share = round1(100 * float64(app.Total) / float64(total))
		}
		out.Apps = append(out.Apps, app)
	}
	return out
}

func liveAppUsage(s *Server, rs *session.Session, t aitools.Tool) string {
	var periods []aiAppPeriod
	var hourTS int64
	if col := rs.Apps(); col != nil {
		if p := col.LiveFor(""); p != nil {
			periods = append(periods, aiLastHour(p))
			hourTS = p.TS
		}
	}
	if len(periods) == 0 {
		periods = append(periods, aiAppPeriod{Period: "last hour", Apps: []aiApp{},
			Note: "Not measured: app usage is measured while the Apps Usage page is open or the router's Reporting is on."})
	}

	recording := s.appUsage.recording(rs.RouterID)
	if s.auditDB != nil {
		loc, err := time.LoadLocation(s.displayTZ())
		if err != nil {
			loc = time.UTC
		}
		now := time.Now()
		for _, pr := range [][2]string{{"day", "last 24 hours"}, {"week", "last 7 days"}, {"month", "last 30 days"}} {
			starts := appHistoryBuckets(pr[0], now, loc)
			rows, err := s.auditDB.AppUsage(rs.RouterID, starts[0], now.UnixMilli())
			if err != nil {
				continue
			}
			p := aiStored(pr[1], buildAppHistory(rows, starts, now.UnixMilli(), apps.Current()))
			if !recording {
				p.Note = "This router's Reporting is off, so its app usage is not stored; any totals are from before it was turned off."
			}
			periods = append(periods, p)
		}
	}

	body, err := json.Marshal(struct {
		Tool       string        `json:"tool"`
		Menu       string        `json:"menu"`
		AgeSeconds float64       `json:"readingAgeSeconds"`
		Note       string        `json:"note"`
		Periods    []aiAppPeriod `json:"periods"`
	}{
		Tool: t.Name, Menu: "/ip/firewall/connection", AgeSeconds: ageSeconds(hourTS, time.Now()),
		Note: "Bytes, not rates. Apps are named from the router's DNS cache, then well-known ports, then " +
			"the network owner; a device using its own encrypted DNS is often unnamed.",
		Periods: periods,
	})
	if err != nil {
		return "That data could not be encoded."
	}
	// UNTRUSTED like every tool result: device names come from DHCP leases.
	return aicontext.Wrap(string(body))
}
