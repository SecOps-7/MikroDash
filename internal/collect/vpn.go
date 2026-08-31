package collect

// VPN collector — the port of src/collectors/vpn.js.
//
//	/interface/wireguard/peers   the WireGuard peers
//	/ppp/active                  L2TP, PPTP, SSTP and OpenVPN sessions
//	/ip/ipsec/active-peers       the IPsec peers
//	/ip/ipsec/installed-sa       and the ciphers they negotiated
//
// ── THE PRE-SHARED KEY IS NOT IN THE PAYLOAD, AND MUST NEVER BE ──────────────
//
// `public-key` is here because a public key is public by construction, and the
// write path round-trips it to prove a row is still the one the operator was
// looking at. `preshared-key` is the secret half: the form's `secret` type never
// reads one back and the audit trail masks it on name alone. Putting it in this
// payload would send it to every viewer of the VPN page.
//
// /ppp/secret is not read at all, for the same reason it is not read in ppp.go —
// it stores account passwords in clear text, and the active session list already
// carries everything worth showing.
//
// ── HANDSHAKE AGE IS THE LIVENESS SIGNAL ─────────────────────────────────────
//
// WireGuard re-keys roughly every two minutes while a peer is actually passing
// traffic, so the AGE of the last handshake is the only real evidence a tunnel
// is up. An earlier rule asked "has this peer ever handshaken", which counted a
// peer that vanished days ago as connected and contradicted the page, which was
// already grading the same value by age and drawing it red. The thresholds here
// match that badge: under three minutes is active, older is stale, never is
// never.
//
// ── RATES ARE ZERO HERE, NOT NULL ────────────────────────────────────────────
//
// Unlike ppp.go, a first sample reports 0 rather than null. That is the original
// behaviour and it is reproduced rather than harmonised: the two collectors were
// written at different times and the VPN page has always rendered a number.
// Changing it would be a visible difference for no defect.

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

var (
	vpnPeersCmd = routeros.Cmd{Path: "/interface/wireguard/peers/print", Args: []string{"=detail="}}
	vpnPppCmd   = routeros.Cmd{Path: "/ppp/active/print"}
	vpnSaCmd    = routeros.Cmd{Path: "/ip/ipsec/installed-sa/print"}
	vpnPeerCmd  = routeros.Cmd{Path: "/ip/ipsec/active-peers/print"}
)

// A peer whose counters have not moved for longer than this reads as idle.
const vpnIdleAfterSec = 10.0

// Under this many seconds since the last handshake, a peer is active.
const vpnActiveWithinSec = 180

// Tunnel is one WireGuard peer as the page renders it.
type Tunnel struct {
	ID        string `json:"id"`
	PublicKey string `json:"publicKey"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	State     string `json:"state"`
	// Comment is TRIMMED here (vpn.js:137), unlike netwatch's, which is not.
	// Reported as the router has it, with no attempt to suppress a comment that
	// happens to equal the peer's name: peerName already falls back to the
	// comment for a peer with no name, so suppressing it would blank the field
	// for exactly the peers it identifies. Feeds {{comment}}.
	Comment string `json:"comment"`
	// LastHandshake is named for what it is. WireGuard is stateless — there is
	// no session and therefore no uptime — and calling this `uptime` made it
	// read as one.
	LastHandshake string  `json:"lastHandshake"`
	Keepalive     string  `json:"keepalive"`
	Endpoint      string  `json:"endpoint"`
	AllowedIP     string  `json:"allowedIp"`
	Interface     string  `json:"interface"`
	RX            int     `json:"rx"`
	TX            int     `json:"tx"`
	RXRate        float64 `json:"rxRate"`
	TXRate        float64 `json:"txRate"`
}

// PppTunnel is a PPP-based session — L2TP, PPTP, SSTP, OpenVPN or PPPoE.
type PppTunnel struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Service  string `json:"service"`
	Address  string `json:"address"`
	CallerID string `json:"callerId"`
	// A REAL session uptime, which is the thing WireGuard has no concept of.
	Uptime string `json:"uptime"`
	RX     int    `json:"rx"`
	TX     int    `json:"tx"`
}

// IpsecTunnel is an active IPsec peer joined to the SA that carries its ciphers.
type IpsecTunnel struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	State  string `json:"state"`
	Uptime string `json:"uptime"`
	Side   string `json:"side"`
	Enc    string `json:"enc"`
	Auth   string `json:"auth"`
}

type VPNPayload struct {
	TS      int64         `json:"ts"`
	Tunnels []Tunnel      `json:"tunnels"`
	Ppp     []PppTunnel   `json:"ppp"`
	Ipsec   []IpsecTunnel `json:"ipsec"`
	PollMs  int           `json:"pollMs"`
}

type vpnSample struct {
	rx, tx int
	ts     time.Time
}

type VPN struct {
	ros    Reader
	emit   Emit
	poll   *pollLoop
	pollMs *pollInterval

	mu sync.Mutex
	// order is the peer keys in the order the router first mentioned them, and
	// peers is the row behind each — the JavaScript Map this payload's array
	// order depends on. See dhcpleases.go for the same trap at length.
	order  []string
	peers  map[string]routeros.Reply
	prev   map[string]vpnSample
	ppp    []PppTunnel
	ipsec  []IpsecTunnel
	last   *VPNPayload
	lastFP string
	// nil = unprobed, false = this router has no such subsystem, stop asking.
	pppAvail   *bool
	ipsecAvail *bool
}

func NewVPN(ros Reader, emit Emit, pollMs int) *VPN {
	ms := clampPoll(pollMs, 10000, 500, 30000)
	v := &VPN{
		ros: ros, emit: emit,
		peers: map[string]routeros.Reply{}, prev: map[string]vpnSample{},
		ppp: []PppTunnel{}, ipsec: []IpsecTunnel{},
	}
	v.pollMs = newPollInterval(ms)
	v.poll = newPollLoop(func() { v.RefreshNow() }, v.pollMs.duration)
	return v
}

var vpnDur = regexp.MustCompile(`(\d+)([wdhms])`)

// HandshakeAgeSec parses a RouterOS duration — "2m30s", "1h5m20s", "3d4h" — into
// seconds. An absent or "never" handshake is infinitely old.
//
// The original runs one regex per unit and takes the FIRST match of each, so a
// repeated unit contributes once. Reproduced rather than summed, because a
// malformed string should degrade the same way on both sides.
func HandshakeAgeSec(s string) float64 {
	if s == "" || s == "never" {
		return math.Inf(1)
	}
	total := 0
	seen := map[string]bool{}
	for _, m := range vpnDur.FindAllStringSubmatch(s, -1) {
		unit := m[2]
		if seen[unit] {
			continue
		}
		seen[unit] = true
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		switch unit {
		case "w":
			total += n * 604800
		case "d":
			total += n * 86400
		case "h":
			total += n * 3600
		case "m":
			total += n * 60
		case "s":
			total += n
		}
	}
	return float64(total)
}

// PeerState grades a peer by the age of its last handshake.
func PeerState(lastHandshake string) string {
	if lastHandshake == "" || lastHandshake == "never" {
		return "never"
	}
	if HandshakeAgeSec(lastHandshake) < vpnActiveWithinSec {
		return "active"
	}
	return "stale"
}

// peerName is the first of name, comment or allowed-address that has content,
// falling back to a truncated public key and then to "?".
func peerName(p routeros.Reply) string {
	if s := strings.TrimSpace(p["name"]); s != "" {
		return s
	}
	if s := strings.TrimSpace(p["comment"]); s != "" {
		return s
	}
	if s := strings.TrimSpace(p["allowed-address"]); s != "" {
		return s
	}
	if k := p["public-key"]; k != "" {
		// UTF-16 code units, as JavaScript's slice counts them. A public key is
		// base64 and therefore ASCII, but the rule is the rule.
		return sliceUTF16(k, 16) + "…"
	}
	return "?"
}

// vpnInt is `parseInt(v || '0', 10)` — leading digits, or zero.
func vpnInt(v string) int { return pppInt(v) }

// sliceUTF16 is JavaScript's `s.slice(0, n)`, which counts UTF-16 CODE UNITS
// rather than runes or bytes.
//
// Local rather than shared: internal/audit has its own, unexported and with a
// different signature, and importing audit from a collector would be the wrong
// direction entirely. For the one caller here — a base64 public key, therefore
// ASCII — every definition agrees; the JavaScript rule is followed anyway so
// that agreement is a fact rather than a coincidence.
func sliceUTF16(s string, n int) string {
	units := 0
	for i, r := range s {
		if units >= n {
			return s[:i]
		}
		if r > 0xFFFF {
			units += 2 // a surrogate pair
		} else {
			units++
		}
	}
	return s
}

// buildTunnels turns the peer rows into the payload's array. Caller holds the
// lock.
func (v *VPN) buildTunnels() []Tunnel {
	now := time.Now()
	out := make([]Tunnel, 0, len(v.order))
	live := map[string]bool{}

	for _, key := range v.order {
		p, ok := v.peers[key]
		if !ok {
			continue
		}
		lh := p["last-handshake"]
		name := peerName(p)

		rx := p["rx"]
		if rx == "" {
			rx = p["rx-bytes"]
		}
		tx := p["tx"]
		if tx == "" {
			tx = p["tx-bytes"]
		}
		rxBytes, txBytes := vpnInt(rx), vpnInt(tx)
		live[key] = true

		rxRate, txRate := 0.0, 0.0
		if pr, seen := v.prev[key]; seen && now.After(pr.ts) {
			dtSec := now.Sub(pr.ts).Seconds()
			rxRate = max(0, float64(rxBytes-pr.rx)/dtSec)
			txRate = max(0, float64(txBytes-pr.tx)/dtSec)
			if rxBytes == pr.rx && txBytes == pr.tx && dtSec > vpnIdleAfterSec {
				rxRate, txRate = 0, 0
			}
		}
		// Only advance the timestamp when the bytes actually moved, so the
		// window always spans a real interval even when the counter stream
		// fires between counter updates.
		if pr, seen := v.prev[key]; !seen || rxBytes != pr.rx || txBytes != pr.tx {
			v.prev[key] = vpnSample{rx: rxBytes, tx: txBytes, ts: now}
		}

		endpoint := p["endpoint-address"]
		if endpoint == "" {
			endpoint = p["current-endpoint-address"]
		}

		out = append(out, Tunnel{
			ID: p[".id"], PublicKey: p["public-key"],
			Type: "WireGuard", Name: name, State: PeerState(lh),
			Comment:       strings.TrimSpace(p["comment"]),
			LastHandshake: lh,
			Keepalive:     p["persistent-keepalive"],
			Endpoint:      endpoint,
			AllowedIP:     p["allowed-address"],
			Interface:     p["interface"],
			RX:            rxBytes, TX: txBytes, RXRate: rxRate, TXRate: txRate,
		})
	}
	for k := range v.prev {
		if !live[k] {
			delete(v.prev, k)
		}
	}
	return out
}

// RefreshNow re-reads the peers and emits. This is what a write calls, and what
// the fixture replay drives.
func (v *VPN) RefreshNow() {
	if !v.ros.Connected() {
		return
	}
	rows, err := v.ros.Do(vpnPeersCmd)
	if err != nil {
		return
	}
	v.mu.Lock()
	clear(v.peers)
	v.order = v.order[:0]
	for _, p := range rows {
		key := p["public-key"]
		if key == "" {
			key = peerName(p)
		}
		if _, seen := v.peers[key]; !seen {
			v.order = append(v.order, key)
		}
		v.peers[key] = p
	}
	v.mu.Unlock()
	v.build()
}

// build assembles and emits, suppressing an unchanged payload.
func (v *VPN) build() {
	v.mu.Lock()
	tunnels := v.buildTunnels()
	payload := &VPNPayload{
		TS: time.Now().UnixMilli(), Tunnels: tunnels,
		Ppp: v.ppp, Ipsec: v.ipsec,
		// Zero in the original too — this collector is stream-driven and the
		// page does not use the field.
		PollMs: 0,
	}
	v.last = payload

	// The fingerprint covers structural state, cumulative bytes and rates
	// rounded to two places, so a transition to or from zero throughput reaches
	// the browser while an identical idle tick does not. `lastHandshake` is
	// EXCLUDED: it changes every few minutes with no traffic at all, and
	// including it would defeat the suppression entirely.
	var fp strings.Builder
	for _, t := range tunnels {
		fp.WriteString(t.Name + "|" + t.State + "|" + strconv.Itoa(t.RX) + "|" + strconv.Itoa(t.TX) +
			"|" + strconv.FormatFloat(t.RXRate, 'f', 2, 64) +
			"|" + strconv.FormatFloat(t.TXRate, 'f', 2, 64) + ";")
	}
	fp.WriteString("|")
	for _, s := range v.ppp {
		fp.WriteString(s.Name + "|" + s.Service + "|" + s.Address + "|" +
			strconv.Itoa(s.RX) + "|" + strconv.Itoa(s.TX) + ";")
	}
	fp.WriteString("|")
	for _, s := range v.ipsec {
		fp.WriteString(s.Name + "|" + s.State + "|" + s.Enc + "|" + s.Auth + ";")
	}
	changed := fp.String() != v.lastFP
	v.lastFP = fp.String()
	v.mu.Unlock()

	if !changed {
		return
	}
	// ONE EMIT TO THE UNION, not one per room. `session.go`'s emit closure:
	// "A sub naming SEVERAL rooms, comma separated, delivers ONE copy to the
	// union — socket.io's `.to(a).to(b)` behaves the same way, and looping
	// Broadcast would send that viewer the frame twice." This was two calls,
	// so a viewer in both rooms received it twice.
	v.emit("page-vpn,dash-card-vpn", "vpn:update", payload)
}

// ParsePppSessions is the PPP half. Exported for the same reason ppp.go's is:
// it is pure, and it is the part worth testing without a router.
func ParsePppSessions(rows []routeros.Reply) []PppTunnel {
	out := make([]PppTunnel, 0, len(rows))
	for _, r := range rows {
		if r["name"] == "" {
			continue
		}
		out = append(out, PppTunnel{
			Type: "PPP", Name: r["name"],
			Service:  strings.ToUpper(r["service"]),
			Address:  r["address"],
			CallerID: r["caller-id"],
			Uptime:   r["uptime"],
			RX:       vpnInt(r["bytes-in"]), TX: vpnInt(r["bytes-out"]),
		})
	}
	return out
}

// ParseIpsecPeers joins active peers to installed SAs on the peer address, so
// each row can state what it actually negotiated: the peers carry the session
// and the SAs carry the ciphers.
func ParseIpsecPeers(peers, sas []routeros.Reply) []IpsecTunnel {
	byAddr := map[string]routeros.Reply{}
	for _, sa := range sas {
		addr := strings.SplitN(sa["dst-address"], "/", 2)[0]
		if addr == "" {
			continue
		}
		if _, seen := byAddr[addr]; !seen {
			byAddr[addr] = sa
		}
	}
	out := make([]IpsecTunnel, 0, len(peers))
	for _, p := range peers {
		if p["remote-address"] == "" && p["id"] == "" {
			continue
		}
		addr := strings.SplitN(p["remote-address"], "/", 2)[0]
		sa := byAddr[addr]
		name := addr
		if name == "" {
			name = "(peer)"
		}
		out = append(out, IpsecTunnel{
			Type: "IPsec", Name: name,
			State: p["state"], Uptime: p["uptime"], Side: p["side"],
			Enc: sa["enc-algorithm"], Auth: sa["auth-algorithm"],
		})
	}
	return out
}

// loadOther reads the non-WireGuard tables.
//
// Polled rather than streamed, because none of these paths supports /listen, and
// LATCHED OFF after the first "no such command" so a router without the
// subsystem is not re-probed for ever.
func (v *VPN) loadOther() {
	read := func(cmd routeros.Cmd, flag **bool) []routeros.Reply {
		if *flag != nil && !**flag {
			return nil
		}
		rows, err := v.ros.Do(cmd)
		if err != nil {
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "no such") || strings.Contains(msg, "unknown command") {
				no := false
				*flag = &no
			}
			return nil
		}
		out := make([]routeros.Reply, 0, len(rows))
		for _, r := range rows {
			if len(r) > 0 {
				out = append(out, r)
			}
		}
		return out
	}
	ppp := ParsePppSessions(read(vpnPppCmd, &v.pppAvail))
	peers := read(vpnPeerCmd, &v.ipsecAvail)
	sas := read(vpnSaCmd, &v.ipsecAvail)

	v.mu.Lock()
	v.ppp, v.ipsec = ppp, ParseIpsecPeers(peers, sas)
	v.mu.Unlock()
}

func (v *VPN) Last() *VPNPayload {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.last
}

func (v *VPN) Tick() {
	v.loadOther()
	v.RefreshNow()
}

func (v *VPN) Start() { v.Tick(); v.poll.start() }

func (v *VPN) Reconnected() {
	v.poll.stop()
	v.mu.Lock()
	clear(v.prev)
	v.lastFP = ""
	v.pppAvail, v.ipsecAvail = nil, nil
	v.mu.Unlock()
	v.Tick()
	v.poll.start()
}

func (v *VPN) Suspend() { v.poll.stop() }
func (v *VPN) Resume()  { v.poll.start() }
func (v *VPN) Stop()    { v.poll.stop() }

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (v *VPN) SetPollMs(ms int) {
	v.pollMs.set(ms)
	v.poll.retime()
}
