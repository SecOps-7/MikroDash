package routers

import "mikrodash/internal/collect"

// The device modal's live frame: one router's readings, built from whatever its
// session last collected.
//
// ── PURE, LIKE BuildStats ──────────────────────────────────────────────────
//
// The server hands in the payloads; this decides what the modal is told. A
// reading that has not arrived is null, never zero, for the reason the package
// header gives: "0% CPU" and "no reading" are different claims.

// LiveRingPoints is how much WAN history the first frame carries: five minutes
// at the stream's one-second cadence, the modal chart's whole window.
const LiveRingPoints = 300

// LiveInput is one router's sources for one frame.
type LiveInput struct {
	RouterID  string
	Connected bool
	System    *collect.SystemPayload
	// WanIf is the interface the session streams by default; Wan is its ring.
	WanIf  string
	Wan    []collect.TrafficPoint
	Ifaces *collect.IfStatusPayload
	Leases *collect.LeasesPayload
	// SinceTS is the newest WAN point the viewer already has; 0 for the first
	// frame, which then carries the last LiveRingPoints.
	SinceTS int64
}

// LivePort is one physical port, as the modal's port row draws it.
type LivePort struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Running  bool   `json:"running"`
	Disabled bool   `json:"disabled"`
}

// Live is the `device:live` body.
type Live struct {
	RouterID  string   `json:"routerId"`
	Connected bool     `json:"connected"`
	CPU       *int     `json:"cpu"`
	MemPct    *int     `json:"memPct"`
	HddPct    *int     `json:"hddPct"`
	TempC     *float64 `json:"tempC"`
	Uptime    *string  `json:"uptime"`
	WanIf     string   `json:"wanIf"`
	// Points are the WAN samples newer than the viewer's SinceTS. The FIRST
	// frame carries the ring's tail, so the chart opens full rather than drawing
	// one point on a five-minute axis.
	Points []collect.TrafficPoint `json:"points"`
	// Ports are the physical ports. PortsRead says whether the interface reading
	// has arrived, because Go never sends a null array and "not read yet" and
	// "this router has no physical ports" draw differently.
	Ports     []LivePort `json:"ports"`
	PortsRead bool       `json:"portsRead"`
	// Leases counts DHCP leases; null until that reading arrives.
	Leases *int `json:"leases"`
}

// physicalTypes is the Physical Ports card's filter, which the modal shares.
var physicalTypes = map[string]bool{"ether": true, "sfp": true, "sfp-sfpplus": true}

// BuildLive builds one frame.
func BuildLive(in LiveInput) Live {
	out := Live{RouterID: in.RouterID, Connected: in.Connected, WanIf: in.WanIf,
		Points: []collect.TrafficPoint{}, Ports: []LivePort{}}
	if p := in.System; p != nil {
		cpu, mem, hdd, up := p.CPULoad, p.MemPct, p.HddPct, p.UptimeRaw
		out.CPU, out.MemPct, out.HddPct, out.Uptime = &cpu, &mem, &hdd, &up
		out.TempC = p.TempC
	}
	pts := in.Wan
	if in.SinceTS == 0 {
		if len(pts) > LiveRingPoints {
			pts = pts[len(pts)-LiveRingPoints:]
		}
		out.Points = append(out.Points, pts...)
	} else {
		for _, pt := range pts {
			if pt.TS > in.SinceTS {
				out.Points = append(out.Points, pt)
			}
		}
	}
	if p := in.Ifaces; p != nil {
		out.PortsRead = true
		for _, i := range p.Interfaces {
			if physicalTypes[i.Type] {
				out.Ports = append(out.Ports, LivePort{Name: i.Name, Type: i.Type,
					Running: i.Running, Disabled: i.Disabled})
			}
		}
	}
	if p := in.Leases; p != nil {
		n := len(p.Leases)
		out.Leases = &n
	}
	return out
}
