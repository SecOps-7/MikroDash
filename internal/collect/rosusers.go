package collect

// Router users collector — the port of src/collectors/rosusers.js.
//
//	/user           who may log into the router
//	/user/group     what each group may do
//	/user/active    who is logged in right now
//	/user/settings  the router's own password policy, which the create form needs
//
// RouterOS `/user`, not MikroDash accounts. The two are unrelated.
//
// ── THIS COLLECTOR ONLY READS ────────────────────────────────────────────────
//
// Every write — add, edit, remove, ending a session — lives in the socket
// actions, gated on router:write and on the page. A collector runs unattended on
// a timer for every connected router, so a write reachable from here would be a
// write nobody asked for.
//
// `/user/print` DOES NOT RETURN PASSWORDS, verified against a live router, so
// the read path carries no secret and needs no redaction. Nothing here should
// ever be changed in a way that makes that untrue.
//
// ── THE PROTECTED MARKS ARE A CONVENIENCE, NOT THE GUARD ─────────────────────
//
// The payload carries a `self` block naming the accounts and groups that must
// not be touched, and marks the matching rows `protected`. That is for the page.
// The GUARD is server-side in the action handlers, which re-read from the router
// in the same tick as the write, because a page can be stale or crafted.
//
// `ResolveSelf` is imported from internal/guard rather than reimplemented here,
// for the reason the original gives: the page's marks and the handlers' refusals
// must never be able to disagree about what "ours" means, and two copies of that
// rule is how they would.

import (
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/guard"
	"mikrodash/internal/routeros"
)

var (
	rosUserCmd = routeros.Cmd{Path: "/user/print", Args: []string{
		"=.proplist=.id,name,group,address,comment,disabled,expired,last-logged-in," +
			"inactivity-timeout,inactivity-policy"}}
	rosGroupCmd = routeros.Cmd{Path: "/user/group/print",
		Args: []string{"=.proplist=.id,name,policy,skin,comment"}}
	rosActiveCmd = routeros.Cmd{Path: "/user/active/print",
		Args: []string{"=.proplist=.id,when,name,address,via,group,radius"}}
	rosSettingsCmd = routeros.Cmd{Path: "/user/settings/print"}
)

// A user list changes when somebody edits it, not on a tick. Actions call
// RefreshNow, so a slow cadence costs nothing in responsiveness.
const rosConfigEvery = 6

// Policies is the full RouterOS policy vocabulary, in the order WinBox shows it.
//
// Exported because the group editor renders exactly this list: a policy the UI
// does not know about is one an operator cannot see they are removing.
var Policies = []string{
	"local", "telnet", "ssh", "ftp", "reboot", "read", "write", "policy", "test",
	"winbox", "password", "web", "sniff", "sensitive", "api", "romon", "rest-api",
}

// ParsePolicy splits a stored policy string into what is granted and what is
// denied.
//
// RouterOS answers with every policy listed and the negated ones prefixed `!`,
// so the granted set is what survives filtering. BOTH HALVES ARE RETURNED,
// because "this group does not mention rest-api at all" and "it denies it" are
// different facts — and they differ on an older RouterOS that lacks a policy
// this build knows about.
func ParsePolicy(raw string) (granted, denied []string) {
	granted, denied = []string{}, []string{}
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "!") {
			denied = append(denied, p[1:])
		} else {
			granted = append(granted, p)
		}
	}
	return granted, denied
}

// BuildPolicy is the inverse: the string to SEND, with every ungranted policy
// explicitly negated.
//
// THE NEGATIONS ARE LOAD-BEARING, and only on `set`. Verified against a live
// router: `/user/group/set =policy=read` against a group holding `read,test,api`
// changes NOTHING — a positive-only list is purely additive, and RouterOS
// removes a policy only when it is named with a `!`.
//
//	set =policy=read                      -> read,test,api   (silently unchanged)
//	set =policy=!local,...,read,...,!api  -> read
//
// `add` is the misleading case: there RouterOS fills the negations in itself, so
// a positive-only list works and the create path looks fine while every EDIT
// quietly fails to remove anything. One form is correct for both, so this always
// emits the full seventeen.
//
// A policy outside the vocabulary is dropped rather than relayed: the editor
// renders exactly Policies, so anything else is a newer RouterOS or a crafted
// request.
func BuildPolicy(granted []string) string {
	set := map[string]bool{}
	for _, p := range granted {
		if containsStr(Policies, p) {
			set[p] = true
		}
	}
	out := make([]string, 0, len(Policies))
	for _, p := range Policies {
		if set[p] {
			out = append(out, p)
		} else {
			out = append(out, "!"+p)
		}
	}
	return strings.Join(out, ",")
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

type RosUser struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Group    string `json:"group"`
	Address  string `json:"address"`
	Comment  string `json:"comment"`
	Disabled bool   `json:"disabled"`
	Expired  bool   `json:"expired"`
	// LastLogin is a RouterOS date string, passed through verbatim and never
	// parsed — the page renders it as the router wrote it.
	LastLogin         string `json:"lastLogin"`
	InactivityTimeout string `json:"inactivityTimeout"`
	InactivityPolicy  string `json:"inactivityPolicy"`
	Protected         bool   `json:"protected"`
}

type RosGroup struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Granted   []string `json:"granted"`
	Denied    []string `json:"denied"`
	Skin      string   `json:"skin"`
	Comment   string   `json:"comment"`
	Protected bool     `json:"protected"`
	Members   int      `json:"members"`
}

type RosSession struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Via     string `json:"via"`
	Group   string `json:"group"`
	// When is a RouterOS date string, sorted as a STRING — which works because
	// the format is lexicographically ordered, and is what the original does.
	When      string `json:"when"`
	Radius    bool   `json:"radius"`
	Protected bool   `json:"protected"`
}

// RosSelf is the `self` block as it goes on the wire.
//
// SEPARATE FROM guard.Self, for one field: `source` is null when nothing
// resolved, and a Go string would marshal to "" instead. The golden records
// null and the page distinguishes them, so the wire type carries a pointer.
type RosSelf struct {
	Names    []string `json:"names"`
	Groups   []string `json:"groups"`
	Resolved bool     `json:"resolved"`
	Source   *string  `json:"source"`
}

type RosPasswordPolicy struct {
	MinLength     int `json:"minLength"`
	MinCategories int `json:"minCategories"`
}

type RosUsersPayload struct {
	TS             int64             `json:"ts"`
	PollMs         int               `json:"pollMs"`
	Users          []RosUser         `json:"users"`
	Groups         []RosGroup        `json:"groups"`
	Sessions       []RosSession      `json:"sessions"`
	Self           RosSelf           `json:"self"`
	PasswordPolicy RosPasswordPolicy `json:"passwordPolicy"`
	Policies       []string          `json:"policies"`
	// Available is false when the API user cannot read /user at all, so the page
	// can say that rather than showing an empty list as if there were no users.
	Available bool `json:"available"`
	// Denied separates "read succeeded, nothing there" from "the router refused".
	// The page shows two different banners, because the fixes differ.
	Denied bool `json:"denied"`
}

type RosUsers struct {
	ros       Reader
	emit      Emit
	poll      *pollLoop
	pollMs    *pollInterval
	usernames []string

	mu       sync.Mutex
	settings routeros.Reply
	ticks    int
	lastFP   string
	last     *RosUsersPayload
	denied   bool
	// nil = unprobed, false = this router has no such menu or refuses it.
	userAvail     *bool
	groupAvail    *bool
	activeAvail   *bool
	settingsAvail *bool
}

func NewRosUsers(ros Reader, emit Emit, usernames []string, pollMs int) *RosUsers {
	// Node's clampPoll is (raw, def, hi, lo) and the call is
	// (pollMs, 30000, 300000, 5000). Reordered for this side's (raw, def, lo, hi).
	ms := clampPoll(pollMs, 30000, 5000, 300000)
	r := &RosUsers{ros: ros, emit: emit, pollMs: newPollInterval(ms), usernames: usernames}
	r.poll = newPollLoop(func() { r.Tick() },
		func() time.Duration { return time.Duration(ms) * time.Millisecond })
	return r
}

// read fetches one menu, latching the flag off when the answer will not change.
//
// A PERMISSION REFUSAL IS THE COMMON CASE HERE, not an edge one: the documented
// monitoring group denies `policy`, and RouterOS gates /user behind it. So a
// refusal latches too, and sets `denied` so the page can say which of the two
// empty states this is.
func (r *RosUsers) read(cmd routeros.Cmd, flag **bool) []routeros.Reply {
	if *flag != nil && !**flag {
		return nil
	}
	rows, err := r.ros.Do(cmd)
	if err != nil {
		msg := strings.ToLower(err.Error())
		switch {
		case strings.Contains(msg, "no such"), strings.Contains(msg, "unknown command"):
			no := false
			*flag = &no
		case strings.Contains(msg, "not enough permission"),
			strings.Contains(msg, "permission denied"),
			strings.Contains(msg, "no permissions"):
			no := false
			*flag = &no
			r.denied = true
		default:
			log.Printf("[rosusers] %s: %v", cmd.Path, err)
		}
		return nil
	}
	yes := true
	*flag = &yes
	out := make([]routeros.Reply, 0, len(rows))
	for _, row := range rows {
		if len(row) > 0 {
			out = append(out, row)
		}
	}
	return out
}

// numOrZero is `Number(x || 0) || 0` — anything unparseable becomes zero.
func numOrZero(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0
	}
	return n
}

// BuildUsersView joins the four reads into one view. Pure, and exported for the
// same reason the guard is: it is the whole of the interesting logic.
func BuildUsersView(userRows, groupRows, activeRows []routeros.Reply,
	settings routeros.Reply, usernames []string) (RosSelf, []RosUser, []RosGroup, []RosSession, RosPasswordPolicy) {

	self := guard.ResolveSelf(userRows, activeRows, usernames)

	users := make([]RosUser, 0, len(userRows))
	for _, row := range userRows {
		if row["name"] == "" {
			continue // also drops the empty-menu junk row
		}
		users = append(users, RosUser{
			ID: row[".id"], Name: row["name"], Group: row["group"],
			Address: row["address"], Comment: row["comment"],
			Disabled: boolOf(row["disabled"]), Expired: boolOf(row["expired"]),
			LastLogin:         row["last-logged-in"],
			InactivityTimeout: row["inactivity-timeout"],
			InactivityPolicy:  row["inactivity-policy"],
			Protected:         self.IsSelfUser(row["name"]),
		})
	}
	sort.SliceStable(users, func(i, j int) bool { return Collate(users[i].Name, users[j].Name) < 0 })

	groups := make([]RosGroup, 0, len(groupRows))
	for _, row := range groupRows {
		if row["name"] == "" {
			continue
		}
		granted, denied := ParsePolicy(row["policy"])
		members := 0
		for _, u := range users {
			if strings.EqualFold(strings.TrimSpace(u.Group), strings.TrimSpace(row["name"])) {
				members++
			}
		}
		groups = append(groups, RosGroup{
			ID: row[".id"], Name: row["name"],
			Granted: granted, Denied: denied,
			Skin: row["skin"], Comment: row["comment"],
			// The group the connecting account belongs to is protected too:
			// dropping `api` or `read` from it disconnects MikroDash just as
			// surely as deleting the account.
			Protected: self.IsSelfGroup(row["name"]),
			Members:   members,
		})
	}
	sort.SliceStable(groups, func(i, j int) bool { return Collate(groups[i].Name, groups[j].Name) < 0 })

	sessions := make([]RosSession, 0, len(activeRows))
	for _, row := range activeRows {
		if row["name"] == "" {
			continue
		}
		sessions = append(sessions, RosSession{
			ID: row[".id"], Name: row["name"], Address: row["address"],
			Via: row["via"], Group: row["group"], When: row["when"],
			Radius: boolOf(row["radius"]),
			// MikroDash keeps several logins per router open at once. All of
			// them are ours, and ending one buys nothing: it would reconnect.
			Protected: self.IsSelfUser(row["name"]),
		})
	}
	// DESCENDING by `when`, so the most recent login is first.
	sort.SliceStable(sessions, func(i, j int) bool {
		return Collate(sessions[j].When, sessions[i].When) < 0
	})

	var source *string
	if self.Source != "" {
		s := self.Source
		source = &s
	}
	wireSelf := RosSelf{
		Names: self.Names, Groups: self.Groups, Resolved: self.Resolved, Source: source,
	}
	policy := RosPasswordPolicy{
		MinLength:     numOrZero(settings["minimum-password-length"]),
		MinCategories: numOrZero(settings["minimum-categories"]),
	}
	return wireSelf, users, groups, sessions, policy
}

func (r *RosUsers) Tick() {
	if !r.ros.Connected() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.ticks%rosConfigEvery == 0 {
		rows := r.read(rosSettingsCmd, &r.settingsAvail)
		if len(rows) > 0 {
			r.settings = rows[0]
		} else {
			r.settings = nil
		}
	}
	r.ticks++

	userRows := r.read(rosUserCmd, &r.userAvail)
	groupRows := r.read(rosGroupCmd, &r.groupAvail)
	activeRows := r.read(rosActiveCmd, &r.activeAvail)

	self, users, groups, sessions, policy :=
		BuildUsersView(userRows, groupRows, activeRows, r.settings, r.usernames)

	payload := &RosUsersPayload{
		TS: time.Now().UnixMilli(), PollMs: r.pollMs.ms(),
		Users: users, Groups: groups, Sessions: sessions, Self: self,
		PasswordPolicy: policy, Policies: Policies,
		Available: r.userAvail == nil || *r.userAvail,
		Denied:    r.denied,
	}
	r.last = payload

	var fp strings.Builder
	for _, u := range users {
		fp.WriteString(u.Name + "|" + u.Group + "|" + strconv.FormatBool(u.Disabled) + "|" +
			u.Address + "|" + u.Comment + "|" + u.LastLogin + ";")
	}
	fp.WriteString("#")
	for _, g := range groups {
		fp.WriteString(g.Name + "|" + strings.Join(g.Granted, "|") + "|" + strconv.Itoa(g.Members) + ";")
	}
	fp.WriteString("#")
	for _, s := range sessions {
		fp.WriteString(s.Name + "|" + s.Address + "|" + s.Via + "|" + s.When + ";")
	}
	if fp.String() == r.lastFP {
		return
	}
	r.lastFP = fp.String()
	r.emit("page-users", "rosusers:update", payload)
}

func (r *RosUsers) Last() *RosUsersPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// RefreshNow is what an action calls, so the page shows what the router did. The
// tick counter resets so the settings row is re-read too — a password policy can
// change under an edit.
func (r *RosUsers) RefreshNow() {
	r.mu.Lock()
	r.ticks = 0
	r.mu.Unlock()
	r.Tick()
}

func (r *RosUsers) Start() { r.Tick(); r.poll.start() }

func (r *RosUsers) Reconnected() {
	r.poll.stop()
	r.mu.Lock()
	r.lastFP = ""
	r.ticks = 0
	r.denied = false
	r.userAvail, r.groupAvail, r.activeAvail, r.settingsAvail = nil, nil, nil, nil
	r.mu.Unlock()
	r.Tick()
	r.poll.start()
}

func (r *RosUsers) Suspend() { r.poll.stop() }
func (r *RosUsers) Resume()  { r.poll.start() }

func (r *RosUsers) Stop() {
	r.poll.stop()
	r.mu.Lock()
	r.lastFP = ""
	r.mu.Unlock()
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (r *RosUsers) SetPollMs(ms int) {
	r.pollMs.set(ms)
	r.poll.retime()
}
