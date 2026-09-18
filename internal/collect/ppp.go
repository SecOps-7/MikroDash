package collect

// PPP collector — the port of src/collectors/ppp.js (issue #32, "Live PPPoE
// Metrics").
//
//	/ppp/active                     the sessions
//	/ppp/profile                    the profiles they were assigned
//	/interface/pppoe-server/server  the PPPoE servers that accept them
//	/ppp/secret                     the accounts themselves, MINUS THE PASSWORD
//
// ── THE PASSWORD IS NEVER READ. THE REST OF THE MENU NOW IS ─────────────────
//
// This file used to say "/ppp/secret IS NEVER READ", and the reason recorded
// with it (CHANGELOG, issue #64) was two claims joined by "and":
//
//	it holds credentials, and the active list already carries everything
//	worth showing.
//
// Only the first survives. The second was true of a MONITORING page and is
// false of a MANAGEMENT one — managing subscribers is precisely the thing the
// active list cannot do (issue #125).
//
// So the rule is NARROWED, not reversed. `pppSecretCmd` names every property
// this page needs and does NOT name `password`, and that absence is the whole
// security property: a password cannot reach a browser through a payload it was
// never read into. Writes go the other way entirely, through
// `internal/resource`'s `TypeSecret`, which is write-only by construction.
//
// `TestNoProplistNamesACredential` enforces this now. It is worth knowing that
// the sentence this replaces claimed "a test enforces it across both" — that
// test was the Node original's and went at cutover, so the rule spent the whole
// port with nothing holding it up.
//
// ── WHAT THIS DOES NOT CLAIM ────────────────────────────────────────────────
//
// The property is "no password reaches a BROWSER", not "no password is ever
// read". The write path is a separate road: `internal/server`'s `readMenu`
// prints the whole menu with NO proplist — deliberately, because `ReadOnlyWhen`
// needs properties no page asks for — so a save, a delete or an enable does pull
// cleartext passwords into server memory for the length of that call. They stop
// there: `RowValues` drops every secret-typed field, `PreviewCommand` masks it,
// and the audit trail masks it by type. Narrowing that read too is a change to
// shared machinery and belongs in its own piece of work; what must not happen is
// this comment being read as covering it.
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
	// EVERY PROPERTY THE PAGE NEEDS, AND NOT `password`.
	//
	// The proplist is the enforcement point, not a convention: an explicit list
	// is the difference between "we chose not to show it" and "we never asked
	// for it". `/ppp/secret` also carries `remote-ipv6-prefix`, which nothing
	// renders yet and which is therefore left out rather than carried unused.
	pppSecretCmd = routeros.Cmd{Path: "/ppp/secret/print", Args: []string{
		"=.proplist=.id,name,service,profile,local-address,remote-address,caller-id," +
			"routes,limit-bytes-in,limit-bytes-out,comment,disabled"}}
)

// Config is re-read every N ticks; sessions are read every tick.
const pppConfigEvery = 12

// pppHeartbeat bounds how long the dirty check may stay silent.
//
// ── A CARD WITH NOTHING TO SAY WAS BEING CALLED STALE ──────────────────────
//
// The emit is gated on a fingerprint, so an unchanged tick sends nothing. That
// is right for bandwidth and wrong for LIVENESS, because the browser measures
// staleness as "how long since a payload arrived" and cannot tell a collector
// that is quiet from one that has stopped.
//
// The two contracts only conflict where the data can be genuinely static, and
// PPP is exactly that: a router with no sessions and no configuration changes
// produces an identical fingerprint for ever. So the first frame arrived, the
// timer ran, and 25 seconds later the Active Sessions card wore a "stale" badge
// while the collector was polling perfectly happily. Reported from the live
// install, on the routers that run no PPP at all.
//
// FIFTEEN SECONDS IS NOT ARBITRARY. `web/src/stale.ts` retunes each card's
// threshold to `pollMs + STALE_GRACE`, and STALE_GRACE is 20 s, so the smallest
// threshold this collector can face is its 1 s poll floor plus 20 s. A heartbeat
// below that is safe at every interval `clampPoll` allows, and 15 s clears the
// tightest case with room to spare. A slower poll simply emits on every tick,
// which is what it did before the dirty check existed.
//
// This does NOT make the fingerprint pointless: between heartbeats an unchanged
// tick still sends nothing, which on a 1 s poll is fourteen frames saved in fifteen.
const pppHeartbeat = 15 * time.Second

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

// PPPSecret is one row of /ppp/secret — an ACCOUNT, not a session.
//
// THERE IS NO PASSWORD FIELD, AND THERE MUST NEVER BE ONE. See the header: the
// proplist does not ask for it, so there is nothing here to hold. The edit form
// writes one through `resource.TypeSecret`, which is write-only in the other
// direction.
//
// `Connected` is NOT read from the router. It is joined onto each account at
// emit time from the /ppp/active names — see Tick for why that is not done when
// the secrets are read.
type PPPSecret struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Service       string `json:"service"`
	Profile       string `json:"profile"`
	LocalAddress  string `json:"localAddress"`
	RemoteAddress string `json:"remoteAddress"`
	CallerID      string `json:"callerId"`
	Routes        string `json:"routes"`
	LimitIn       *int   `json:"limitIn"`
	LimitOut      *int   `json:"limitOut"`
	Comment       string `json:"comment"`
	Disabled      bool   `json:"disabled"`
	Connected     bool   `json:"connected"`
}

// ID IS CARRIED ON THE PROFILE AND NOT ON THE SERVER, which is the difference
// between the two tables rather than an oversight. `.id` was requested and
// dropped for both while they were read-only; the edit form addresses a row by
// it, so a profile now keeps it. A PPPoE server stays read-only — see the plan:
// changing one can cut the operator's own management path, which needs a guard
// nothing here provides — so carrying its id would be exactly the unused field
// the proplist comment above refuses to carry.
type PPPProfile struct {
	ID            string `json:"id"`
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
	Secrets     []PPPSecret    `json:"secrets"`
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
	tableCore[PPPPayload]
	emit Emit

	// prev is the last counter reading per session, which is what makes a rate.
	prev     map[string]pppSample
	secrets  []PPPSecret
	profiles []PPPProfile
	servers  []PPPServer
	// nil = unprobed, false = this router has no such menu, stop asking.
	activeAvail  *bool
	profileAvail *bool
	serverAvail  *bool
	secretAvail  *bool
}

func NewPPP(ros Reader, emit Emit, pollMs int) *PPP {
	p := &PPP{emit: emit, prev: map[string]pppSample{}}
	// Node's call was (pollMs, 5000, 60000, 2000) in (raw, def, hi, lo) order. The
	// floor is now the store's 1 s, so a saved interval is the one used
	// (2026-09-18; pollbounds_test.go).
	// Subscribed to /ppp/active, the live sessions; the config tables are the
	// slow lane.
	p.setup(p, ros, pollMs, tableSpec{
		cmd: pppActiveCmd, poll: [3]int{5000, 1000, 60000},
		slowEvery: pppConfigEvery, heartbeat: pppHeartbeat,
	})
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

// pppLimit is a byte cap as the page renders it: ABSENT is nil, and a present
// zero is a real zero.
//
// RouterOS uses 0 for "no limit", so the two cannot be collapsed — nil means the
// router did not report the property at all, and the page draws a dash for one
// and "0" for the other. Extracted from ParsePPPSessions when /ppp/secret grew
// the same pair of properties; the behaviour is unchanged in both callers.
func pppLimit(v string) *int {
	if v == "" {
		return nil
	}
	n := pppInt(v)
	return &n
}

// read runs one menu THROUGH THE CACHE: `vpn` reads /ppp/active too, and reads
// it with no proplist, so the union on that menu widens to the whole row. The
// other three menus here have this collector alone and are unaffected.
func (p *PPP) read(cmd routeros.Cmd, flag **bool) []routeros.Reply {
	rows, _ := readOptional(p.readShared, cmd, flag)
	return rows
}

// ParsePPPSessions turns /ppp/active rows into sessions with derived rates.
//
// Exported because it is the whole of the interesting arithmetic and deserves
// testing without a router — the same split the Node file makes by hanging
// parsePppSessions off the class.
// ── PHASE 4.1: IT RETURNS THE NEXT STATE RATHER THAN MUTATING THE LAST ─────
//
// This took `prev` and wrote into it: new samples in, dead keys deleted. That
// made every caller's map an output parameter, and it made this function -- and
// `BuildPPP` above it -- impure in a way that only showed up when a caller
// passed the zero value: a nil map panicked on the first write, which is what a
// test discovered the moment the builder became callable without a collector.
//
// The shape 4.1 asks for is `func(prior, in) (out, prior)`, which
// `BuildBandwidth(prev, in)` already has. This is that: `prev` is read only, and
// the next state is BUILT and returned.
//
// Dead keys need no delete loop as a result -- a session that has gone is simply
// not in the map that gets built, which is the same outcome expressed as a
// consequence rather than as a sweep.
func ParsePPPSessions(rows []routeros.Reply, prev map[string]pppSample, now time.Time) ([]PPPSession, map[string]pppSample) {
	out := make([]PPPSession, 0, len(rows))
	next := make(map[string]pppSample, len(rows))

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
		// window always spans a real interval. An unchanged session carries its
		// OLD sample forward rather than being re-stamped, which is the same
		// rule the in-place version expressed by not writing.
		if !seen || rx != pr.rx || tx != pr.tx {
			next[key] = pppSample{rx: rx, tx: tx, ts: now}
		} else {
			next[key] = pr
		}

		limitIn, limitOut := pppLimit(r["limit-bytes-in"]), pppLimit(r["limit-bytes-out"])

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
	// localeCompare, not a byte sort — the same ordering every other table here
	// uses for a name column.
	sort.SliceStable(out, func(i, j int) bool { return Collate(out[i].Name, out[j].Name) < 0 })
	return out, next
}

func (p *PPP) loadConfig() {
	profiles := make([]PPPProfile, 0)
	for _, r := range p.read(pppProfileCmd, &p.profileAvail) {
		if r["name"] == "" {
			continue
		}
		profiles = append(profiles, PPPProfile{
			ID: r[".id"], Name: r["name"], LocalAddress: r["local-address"],
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
	// SECRETS RIDE THE CONFIG CADENCE, not the tick. A subscriber list does not
	// change every five seconds, and CLAUDE.md's measure of efficiency is router
	// channels rather than CPU — so this is one command a minute, not twelve.
	// A write does not wait for it: `RefreshNow` re-reads immediately.
	secrets := make([]PPPSecret, 0)
	for _, r := range p.read(pppSecretCmd, &p.secretAvail) {
		if r["name"] == "" {
			continue
		}
		secrets = append(secrets, PPPSecret{
			ID: r[".id"], Name: r["name"], Service: r["service"],
			Profile: r["profile"], LocalAddress: r["local-address"],
			RemoteAddress: r["remote-address"], CallerID: r["caller-id"],
			Routes: r["routes"], Comment: r["comment"],
			LimitIn: pppLimit(r["limit-bytes-in"]), LimitOut: pppLimit(r["limit-bytes-out"]),
			Disabled: boolOf(r["disabled"]),
		})
	}
	// The same ordering the sessions table uses for a name column.
	sort.SliceStable(secrets, func(i, j int) bool {
		return Collate(secrets[i].Name, secrets[j].Name) < 0
	})
	p.profiles, p.servers, p.secrets = profiles, servers, secrets
}

// derive is the live sessions, with the config tables on the slow lane. An
// active-session menu this router does not have, or refuses, is sent once as
// unavailable and not asked again on this connection.
func (p *PPP) derive(rows []routeros.Reply, err error, slow bool) (*PPPPayload, string) {
	if err != nil && !menuGone(err) {
		return nil, ""
	}
	latchMenu(&p.activeAvail, err)
	if err != nil {
		p.retire()
		rows = nil
	}
	if slow {
		p.loadConfig()
	}
	return p.build(rows)
}

func (p *PPP) send(pl PPPPayload) { EvPppUpdate.Emit(p.emit, pppRooms.Join(), pl) }

// reset forgets the counter samples and every latch. A router that has just come
// back may be a different build.
func (p *PPP) reset() {
	clear(p.prev)
	p.activeAvail, p.profileAvail, p.serverAvail, p.secretAvail = nil, nil, nil, nil
}

// PPPInput is one tick's worth of the outside world, for BuildPPP.
//
// ── FOUR TABLES ON TWO CADENCES, WHICH IS WHY THIS IS A STRUCT ─────────────
//
// Sessions are read every tick; secrets, profiles and servers once every
// `pppConfigEvery`. So most ticks build a payload from three tables the tick did
// not fetch, and they arrive here as inputs rather than as receiver state the
// derivation reaches around for.
type PPPInput struct {
	Rows []routeros.Reply
	// Prev is the previous counter reading per session, which is what makes a
	// RATE possible: a rate is a difference, and a function of the current rows
	// alone has nothing to subtract from. Same shape as `BuildBandwidth(prev, in)`.
	Prev     map[string]pppSample
	Secrets  []PPPSecret
	Profiles []PPPProfile
	Servers  []PPPServer
	// Available is the active-menu presence latch; nil means not yet known,
	// which reads as available.
	Available *bool
	PollMs    int
	Now       time.Time
}

// BuildPPP is the PPP payload, pure.
//
// It returns the parsed sessions as well, because the collector keeps them for
// the fingerprint and parsing the same rows twice could diverge.
//
// ── `Connected` IS JOINED HERE, NOT WHERE THE SECRETS WERE READ ────────────
//
// The two halves move at different speeds: sessions every tick, secrets once a
// minute. Setting `Connected` when the secrets are read would freeze the pill for
// up to a minute -- an account that dialled in four seconds ago would read as
// offline, which is exactly the question the column exists to answer.
//
// A FRESH SLICE each tick, because the caller's `secrets` is a cached read:
// writing `Connected` into it would leave last tick's answer behind on the next.
func BuildPPP(in PPPInput) (*PPPPayload, []PPPSession, map[string]pppSample) {
	sessions, nextPrev := ParsePPPSessions(in.Rows, in.Prev, in.Now)

	byService := map[string]int{}
	for _, s := range sessions {
		k := s.Service
		if k == "" {
			k = "OTHER"
		}
		byService[k]++
	}

	// Totals over the sessions that HAVE a rate. ALL-NULL MEANS NULL, NOT ZERO:
	// the distinction between "nothing is flowing" and "we cannot say yet" is the
	// whole reason the per-session rates are nullable, and summing into a plain
	// float would collapse it on the first tick of every session.
	var totalRX, totalTX *float64
	known := 0
	var sumRX, sumTX float64
	for _, s := range sessions {
		if s.RXRate != nil {
			known++
			sumRX += *s.RXRate
			sumTX += *s.TXRate
		}
	}
	if known > 0 {
		totalRX, totalTX = &sumRX, &sumTX
	}

	active := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		active[s.Name] = true
	}
	secrets := make([]PPPSecret, len(in.Secrets))
	for i, s := range in.Secrets {
		s.Connected = active[s.Name]
		secrets[i] = s
	}

	return &PPPPayload{
		TS: in.Now.UnixMilli(), PollMs: in.PollMs,
		Sessions: sessions, Secrets: secrets, Profiles: in.Profiles, Servers: in.Servers,
		ByService: byService, TotalRXRate: totalRX, TotalTXRate: totalTX,
		Available: MenuAvailable(in.Available),
	}, sessions, nextPrev
}

// applyLocked builds and emits. The caller holds the lock.
func (p *PPP) build(rows []routeros.Reply) (*PPPPayload, string) {
	payload, sessions, nextPrev := BuildPPP(PPPInput{
		Rows: rows, Prev: p.prev,
		Secrets: p.secrets, Profiles: p.profiles, Servers: p.servers,
		Available: p.activeAvail, PollMs: p.pollMs.ms(), Now: time.Now(),
	})
	// THE CARRIED STATE COMES BACK rather than having been written through the
	// argument. See ParsePPPSessions.
	p.prev = nextPrev
	secrets := payload.Secrets

	var fp strings.Builder
	for _, s := range sessions {
		fp.WriteString(s.ID + "|" + s.Name + "|" + s.Service + "|" + s.Address + "|" +
			strconv.Itoa(s.RX) + "|" + strconv.Itoa(s.TX) + ";")
	}
	// ── THE CONFIG TABLES ARE FINGERPRINTED BY CONTENT, NOT BY COUNT ───────
	//
	// This counted profiles and servers, which was survivable while both were
	// read-only: nothing could change a row without adding or removing one. The
	// moment a profile can be EDITED that becomes a silent hole — change a
	// rate-limit, the count is identical, the fingerprint matches, and the frame
	// is suppressed. The operator saves and the table does not move.
	for _, s := range secrets {
		fp.WriteString(s.ID + "|" + s.Name + "|" + s.Service + "|" + s.Profile + "|" +
			s.LocalAddress + "|" + s.RemoteAddress + "|" + s.Comment + "|" +
			strconv.FormatBool(s.Disabled) + "|" + strconv.FormatBool(s.Connected) + ";")
	}
	for _, pr := range p.profiles {
		fp.WriteString(pr.ID + "|" + pr.Name + "|" + pr.LocalAddress + "|" +
			pr.RemoteAddress + "|" + pr.RateLimit + "|" + pr.OnlyOne + "|" +
			pr.Encryption + ";")
	}
	for _, sv := range p.servers {
		fp.WriteString(sv.ServiceName + "|" + sv.Interface + "|" +
			sv.MaxSessions + "|" + sv.Auth + "|" + strconv.FormatBool(sv.Disabled) + ";")
	}
	fp.WriteString("|" + strconv.FormatBool(payload.Available))
	return payload, fp.String()
}
