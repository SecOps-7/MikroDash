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
	"sort"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/guard"
	"mikrodash/internal/resource"
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
// The list itself is resource.UserPolicies, which the group resource writes.
var Policies = resource.UserPolicies

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
	tableCore[RosUsersPayload]
	emit      Emit
	usernames []string

	// settings is the slow lane, carried between readings.
	settings routeros.Reply
	denied   bool
	// nil = unprobed, false = this router has no such menu or refuses it.
	userAvail     *bool
	groupAvail    *bool
	activeAvail   *bool
	settingsAvail *bool
}

func NewRosUsers(ros Reader, emit Emit, usernames []string, pollMs int) *RosUsers {
	r := &RosUsers{emit: emit, usernames: usernames}
	// Node's clampPoll is (raw, def, hi, lo) and the call is
	// (pollMs, 30000, 300000, 5000). Reordered for this side's (raw, def, lo, hi).
	// Subscribed to the USER list; groups and sessions are read every reading,
	// and the settings row on the slow lane.
	r.setup(r, ros, pollMs, tableSpec{
		cmd: rosUserCmd, poll: [3]int{30000, 5000, 300000}, slowEvery: rosConfigEvery,
	})
	return r
}

// read runs one menu, latching an absent or refused one off. A refusal is
// remembered, because the page shows a different banner for it: the fixes differ.
func (r *RosUsers) read(cmd routeros.Cmd, flag **bool) []routeros.Reply {
	rows, refused := readOptional(r.ros.Do, cmd, flag)
	if refused {
		r.denied = true
	}
	return rows
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

// derive is the user list with its groups, sessions and settings. A user menu
// this router does not have, or refuses, is sent once as unavailable and not
// asked again on this connection.
func (r *RosUsers) derive(rows []routeros.Reply, err error, slow bool) (*RosUsersPayload, string) {
	if err != nil && !menuGone(err) {
		return nil, ""
	}
	if latchMenu(&r.userAvail, err) {
		r.denied = true
	}
	if err != nil {
		r.retire()
		rows = nil
	}
	if slow {
		r.settings = nil
		if srows := r.read(rosSettingsCmd, &r.settingsAvail); len(srows) > 0 {
			r.settings = srows[0]
		}
	}
	return r.build(rows)
}

func (r *RosUsers) send(p RosUsersPayload) { EvRosusersUpdate.Emit(r.emit, rosUsersRooms.Join(), p) }

// reset drops every latch and the refusal, which a permission change on the
// router clears only for a new connection.
func (r *RosUsers) reset() {
	r.denied = false
	r.userAvail, r.groupAvail, r.activeAvail, r.settingsAvail = nil, nil, nil, nil
}

// applyLocked builds and emits. The caller holds the lock.
func (r *RosUsers) build(userRows []routeros.Reply) (*RosUsersPayload, string) {
	groupRows := r.read(rosGroupCmd, &r.groupAvail)
	activeRows := r.read(rosActiveCmd, &r.activeAvail)

	self, users, groups, sessions, policy :=
		BuildUsersView(userRows, groupRows, activeRows, r.settings, r.usernames)

	payload := &RosUsersPayload{
		TS: time.Now().UnixMilli(), PollMs: r.pollMs.ms(),
		Users: users, Groups: groups, Sessions: sessions, Self: self,
		PasswordPolicy: policy, Policies: Policies,
		Available: MenuAvailable(r.userAvail),
		Denied:    r.denied,
	}
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
	return payload, fp.String()
}
