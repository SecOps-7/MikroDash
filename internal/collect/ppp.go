package collect

// PPP collector — the port of src/collectors/ppp.js (issue #32, "Live PPPoE
// Metrics").
//
//	/ppp/active                     the sessions
//	/ppp/profile                    the profiles they were assigned
//	/interface/pppoe-server/server  the PPPoE servers that accept them
//
// /ppp/secret IS NEVER READ. It stores account passwords in clear text, and a
// page listing who is connected has no need of them. The Node original records
// the same decision in two collectors and a test enforces it across both.
//
// ── RATES ARE DERIVED, AND null IS NOT ZERO ──────────────────────────────────
//
// RouterOS reports cumulative bytes only, so per-user bandwidth — the actual ask
// in #32 — comes from differencing two readings. The FIRST sample of a session
// therefore has no rate at all, and that is reported as null rather than 0:
// there is no measurement window yet, and 0 would claim an idle session that may
// be saturating the line.
//
// Two further rules, both carried over intact:
//
//   - the rate is clamped at 0, because a session that reconnects restarts its
//     counters and a negative rate is worse than a missed sample;
//   - the baseline timestamp advances ONLY when the bytes actually moved, so the
//     window always spans a real interval even when polls land between counter
//     updates. Bytes unchanged for longer than the idle threshold then read as
//     idle rather than as "still at the last rate".
//
// ── NOT VERIFIED AGAINST HARDWARE ────────────────────────────────────────────
//
// The Node header says so and it is still true here: the fleet runs no PPP, so
// /ppp/active returns the empty-menu junk row and every session-shaped field
// comes from the RouterOS field reference and the fixture. The EMPTY state is
// the only part real hardware has exercised — on either side.

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

var (
	pppActiveCmd = routeros.Cmd{Path: "/ppp/active/print", Args: []string{
		"=.proplist=.id,name,service,caller-id,address,uptime,encoding,session-id," +
			"limit-bytes-in,limit-bytes-out,bytes-in,bytes-out"}}
	pppProfileCmd = routeros.Cmd{Path: "/ppp/profile/print", Args: []string{
		"=.proplist=.id,name,local-address,remote-address,rate-limit,only-one,use-encryption"}}
	pppServerCmd = routeros.Cmd{Path: "/interface/pppoe-server/server/print", Args: []string{
		"=.proplist=.id,service-name,interface,disabled,max-sessions,authentication"}}
)

// Config is re-read every N ticks; sessions are read every tick.
const pppConfigEvery = 12

// Bytes unchanged for longer than this means idle, not "still at the last rate".
const pppIdleAfterSec = 10.0

// PPPSession is one row of /ppp/active as the page renders it.
type PPPSession struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Service   string   `json:"service"`
	Address   string   `json:"address"`
	CallerID  string   `json:"callerId"`
	Uptime    string   `json:"uptime"`
	Encoding  string   `json:"encoding"`
	SessionID string   `json:"sessionId"`
	LimitIn   *int     `json:"limitIn"`
	LimitOut  *int     `json:"limitOut"`
	RX        int      `json:"rx"`
	TX        int      `json:"tx"`
	RXRate    *float64 `json:"rxRate"`
	TXRate    *float64 `json:"txRate"`
}

type PPPProfile struct {
	Name          string `json:"name"`
	LocalAddress  string `json:"localAddress"`
	RemoteAddress string `json:"remoteAddress"`
	RateLimit     string `json:"rateLimit"`
	OnlyOne       string `json:"onlyOne"`
	Encryption    string `json:"encryption"`
}

type PPPServer struct {
	ServiceName string `json:"serviceName"`
	Interface   string `json:"interface"`
	MaxSessions string `json:"maxSessions"`
	Auth        string `json:"auth"`
	Disabled    bool   `json:"disabled"`
}

type PPPPayload struct {
	TS          int64          `json:"ts"`
	PollMs      int            `json:"pollMs"`
	Sessions    []PPPSession   `json:"sessions"`
	Profiles    []PPPProfile   `json:"profiles"`
	Servers     []PPPServer    `json:"servers"`
	ByService   map[string]int `json:"byService"`
	TotalRXRate *float64       `json:"totalRxRate"`
	TotalTXRate *float64       `json:"totalTxRate"`
	// So the page can say "this router has no PPP service" rather than showing
	// an empty table, which reads as a failure.
	Available bool `json:"available"`
}

// pppSample is the previous reading of one session's counters.
type pppSample struct {
	rx, tx int
	ts     time.Time
}

type PPP struct {
	ros    Reader
	emit   Emit
	poll   *pollLoop
	pollMs *pollInterval

	mu       sync.Mutex
	prev     map[string]pppSample
	sessions []PPPSession
	profiles []PPPProfile
	servers  []PPPServer
	ticks    int
	lastFP   string
	last     *PPPPayload
	// nil = unprobed, false = this router has no such menu, stop asking.
	activeAvail  *bool
	profileAvail *bool
	serverAvail  *bool
}

func NewPPP(ros Reader, emit Emit, pollMs int) *PPP {
	// The Node signature is clampPoll(raw, def, hi, lo) and the call is
	// (pollMs, 5000, 60000, 2000). Reordered for this side's (raw, def, lo, hi).
	ms := clampPoll(pollMs, 5000, 2000, 60000)
	p := &PPP{ros: ros, emit: emit, pollMs: newPollInterval(ms), prev: map[string]pppSample{}}
	p.poll = newPollLoop(func() { p.Tick() },
		func() time.Duration { return time.Duration(ms) * time.Millisecond })
	return p
}

// pppInt is the original's `_int`: parseInt on the string form, 0 when that is
// not finite. parseInt takes a LEADING number, so "100k" is 100 and "" is 0.
func pppInt(v string) int {
	s := strings.TrimSpace(v)
	end := 0
	if end < len(s) && (s[end] == '-' || s[end] == '+') {
		end++
	}
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0
	}
	return n
}

// read fetches one menu, latching the flag off when the router says the menu
// does not exist.
//
// LATCHED OFF DELIBERATELY: a router without PPP should be asked once, not every
// five seconds for ever. Any other error leaves the flag alone, because a
// timeout is not evidence that the menu is absent.
func (p *PPP) read(cmd routeros.Cmd, flag **bool) []routeros.Reply {
	if *flag != nil && !**flag {
		return nil
	}
	rows, err := p.ros.Do(cmd)
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "no such") || strings.Contains(msg, "unknown command") {
			no := false
			*flag = &no
		}
		return nil
	}
	yes := true
	*flag = &yes
	out := make([]routeros.Reply, 0, len(rows))
	for _, r := range rows {
		if len(r) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// ParsePPPSessions turns /ppp/active rows into sessions with derived rates.
//
// Exported because it is the whole of the interesting arithmetic and deserves
// testing without a router — the same split the Node file makes by hanging
// parsePppSessions off the class.
func ParsePPPSessions(rows []routeros.Reply, prev map[string]pppSample, now time.Time) []PPPSession {
	out := make([]PPPSession, 0, len(rows))
	live := map[string]bool{}

	for _, r := range rows {
		// Drops the {undefined:''} row RouterOS returns for an empty menu.
		if r["name"] == "" {
			continue
		}
		rx := pppInt(r["bytes-in"])
		tx := pppInt(r["bytes-out"])
		key := r[".id"]
		if key == "" {
			key = r["name"] + "/" + r["service"]
		}
		live[key] = true

		var rxRate, txRate *float64
		pr, seen := prev[key]
		if seen && now.After(pr.ts) {
			dtSec := now.Sub(pr.ts).Seconds()
			rr := max(0, float64(rx-pr.rx)/dtSec)
			tr := max(0, float64(tx-pr.tx)/dtSec)
			if rx == pr.rx && tx == pr.tx && dtSec > pppIdleAfterSec {
				rr, tr = 0, 0
			}
			rxRate, txRate = &rr, &tr
		}
		// Only advance the timestamp when the bytes actually moved, so the
		// window always spans a real interval.
		if !seen || rx != pr.rx || tx != pr.tx {
			prev[key] = pppSample{rx: rx, tx: tx, ts: now}
		}

		var limitIn, limitOut *int
		if r["limit-bytes-in"] != "" {
			n := pppInt(r["limit-bytes-in"])
			limitIn = &n
		}
		if r["limit-bytes-out"] != "" {
			n := pppInt(r["limit-bytes-out"])
			limitOut = &n
		}

		out = append(out, PPPSession{
			ID: r[".id"], Name: r["name"],
			Service:   strings.ToUpper(r["service"]),
			Address:   r["address"],
			CallerID:  r["caller-id"],
			Uptime:    r["uptime"],
			Encoding:  r["encoding"],
			SessionID: r["session-id"],
			LimitIn:   limitIn, LimitOut: limitOut,
			RX: rx, TX: tx, RXRate: rxRate, TXRate: txRate,
		})
	}
	for k := range prev {
		if !live[k] {
			delete(prev, k)
		}
	}
	// localeCompare, not a byte sort — the same ordering every other table here
	// uses for a name column.
	sort.SliceStable(out, func(i, j int) bool { return Collate(out[i].Name, out[j].Name) < 0 })
	return out
}

func (p *PPP) loadConfig() {
	profiles := make([]PPPProfile, 0)
	for _, r := range p.read(pppProfileCmd, &p.profileAvail) {
		if r["name"] == "" {
			continue
		}
		profiles = append(profiles, PPPProfile{
			Name: r["name"], LocalAddress: r["local-address"],
			RemoteAddress: r["remote-address"], RateLimit: r["rate-limit"],
			OnlyOne: r["only-one"], Encryption: r["use-encryption"],
		})
	}
	servers := make([]PPPServer, 0)
	for _, r := range p.read(pppServerCmd, &p.serverAvail) {
		if r["interface"] == "" && r["service-name"] == "" {
			continue
		}
		servers = append(servers, PPPServer{
			ServiceName: r["service-name"], Interface: r["interface"],
			MaxSessions: r["max-sessions"], Auth: r["authentication"],
			Disabled: boolOf(r["disabled"]),
		})
	}
	p.profiles, p.servers = profiles, servers
}

func (p *PPP) Tick() {
	if !p.ros.Connected() {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ticks%pppConfigEvery == 0 {
		p.loadConfig()
	}
	p.ticks++
	rows := p.read(pppActiveCmd, &p.activeAvail)
	p.sessions = ParsePPPSessions(rows, p.prev, time.Now())

	byService := map[string]int{}
	for _, s := range p.sessions {
		k := s.Service
		if k == "" {
			k = "OTHER"
		}
		byService[k]++
	}
	// Totals over the sessions that HAVE a rate. All-null means null, not zero:
	// the distinction between "nothing is flowing" and "we cannot say yet" is
	// the whole reason the per-session rates are nullable.
	var totalRX, totalTX *float64
	known := 0
	var sumRX, sumTX float64
	for _, s := range p.sessions {
		if s.RXRate != nil {
			known++
			sumRX += *s.RXRate
			sumTX += *s.TXRate
		}
	}
	if known > 0 {
		totalRX, totalTX = &sumRX, &sumTX
	}

	payload := &PPPPayload{
		TS: time.Now().UnixMilli(), PollMs: p.pollMs.ms(),
		Sessions: p.sessions, Profiles: p.profiles, Servers: p.servers,
		ByService: byService, TotalRXRate: totalRX, TotalTXRate: totalTX,
		Available: p.activeAvail == nil || *p.activeAvail,
	}
	p.last = payload

	var fp strings.Builder
	for _, s := range p.sessions {
		fp.WriteString(s.ID + "|" + s.Name + "|" + s.Service + "|" + s.Address + "|" +
			strconv.Itoa(s.RX) + "|" + strconv.Itoa(s.TX) + ";")
	}
	fp.WriteString("|" + strconv.Itoa(len(p.profiles)) + "|" + strconv.Itoa(len(p.servers)) +
		"|" + strconv.FormatBool(payload.Available))
	if fp.String() == p.lastFP {
		return
	}
	p.lastFP = fp.String()
	p.emit("page-ppp", "ppp:update", payload)
}

func (p *PPP) Last() *PPPPayload {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

func (p *PPP) Start() { p.Tick(); p.poll.start() }

// Reconnected clears the rate baseline with everything else: a reconnect may be
// a different router, and session byte counters restart in any case.
func (p *PPP) Reconnected() {
	p.poll.stop()
	p.mu.Lock()
	clear(p.prev)
	p.lastFP = ""
	p.ticks = 0
	p.activeAvail, p.profileAvail, p.serverAvail = nil, nil, nil
	p.mu.Unlock()
	p.Tick()
	p.poll.start()
}

func (p *PPP) Suspend() { p.poll.stop() }
func (p *PPP) Resume()  { p.poll.start() }
func (p *PPP) Stop() {
	p.poll.stop()
	p.mu.Lock()
	p.lastFP = ""
	p.mu.Unlock()
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (p *PPP) SetPollMs(ms int) {
	p.pollMs.set(ms)
	p.poll.retime()
}
