package guard

// The lockout guard for the Router Users page — a port of
// src/routeros/selfGuard.js.
//
// MikroDash logs into every router it manages as an ordinary RouterOS user, and
// the Router Users page can edit RouterOS users. So that page can, in about ten
// different ways, cut the dashboard off from the device it is managing — and
// unlike every other write in this app, THE FAILURE IS UNRECOVERABLE FROM INSIDE
// IT. Once the login is broken, the fix is WinBox.
//
// What counts as fatal:
//
//	remove / disable the account      no login
//	rename it                         routers.json still holds the old name
//	change or expire its password     likewise, or the next login is forced to change
//	move it to another group          the new group may lack `api` or `read`
//	set an address restriction        may exclude MikroDash's source address
//	set an inactivity timeout         disconnects on a timer
//	edit / rename / remove ITS GROUP  dropping `api` or `read` disconnects it
//	end its /user/active session      pointless churn; it just reconnects
//
// ── TWO RULES, NOT ONE ───────────────────────────────────────────────────────
//
// The obvious rule protects the TARGET: our account and our group are never a
// valid thing to act on. The second protects the VALUE: no other user may be
// moved INTO our group, and no new user may be created with our name. That one
// is not lockout — it is silent privilege escalation through a screen that reads
// as low-stakes, since a viewer with page-write could otherwise mint themself an
// account in the group holding `policy`.
//
// ── DELIBERATELY BLUNT ───────────────────────────────────────────────────────
//
// Editing our group's policy is refused outright rather than "refused if it
// drops api or read". Deciding which policy edits are survivable means parsing
// RouterOS's negation syntax and reasoning about implicit defaults — a second
// guard, with its own bugs, protecting the first. Likewise an address
// restriction is refused without asking whether the CIDR would still admit us:
// MikroDash's source address as the ROUTER sees it is not knowable from here,
// what with NAT, multi-homing and the container bridge.
//
// ── FAIL CLOSED ──────────────────────────────────────────────────────────────
//
// If we cannot identify ourselves on this router at all, every write is refused.
// Allowing everything because we cannot tell what is ours would be exactly the
// accident this module exists to prevent. That is the opposite of selfpath.go in
// this same package, which fails OPEN — and the difference is blast radius: a
// missed interface warning is a warning nobody saw, while a missed user refusal
// is a site visit.
//
// RouterOS has its own backstops — a user cannot grant policies it does not hold,
// and the last full-access user cannot be removed. Those are defence in depth.
// Nothing here relies on them.

import (
	"strings"

	"mikrodash/internal/routeros"
)

// selfKey normalises a RouterOS name for comparison: trimmed and lower-cased.
//
// RouterOS names are case-SENSITIVE, so this over-matches a hypothetical second
// account differing only in case. That is the direction to err in.
func selfKey(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

// Self is the set of accounts and groups belonging to MikroDash on one router.
type Self struct {
	Names  []string
	Groups []string
	// Resolved is false when no group could be determined. Every check then
	// refuses.
	Resolved bool
	// Source is "active" or "user" — which table the group came from. Recorded
	// because the two are not equally trustworthy; see ResolveSelf.
	Source string
}

// ResolveSelf works out which accounts and groups are ours.
//
// MULTIPLE NAMES ARE ACCEPTED, and that is not defensive coding. The live app's
// collection fingerprint does not cover credentials, so updating a router does
// not rebuild the session when the username changes: the live connection can be
// logged in as one name while routers.json holds another, indefinitely. Both are
// protected. Over-protecting an account that is no longer ours is a nuisance;
// under-protecting the live one costs somebody a site visit.
//
// THE GROUP COMES FROM /user/active FIRST. That row names the group the session
// which actually authenticated landed in — true even when the /user row is
// absent, as with RADIUS, or hidden from this API user. /user is only a fallback.
func ResolveSelf(userRows, activeRows []routeros.Reply, usernames []string) Self {
	names := make([]string, 0, len(usernames))
	seenName := map[string]bool{}
	for _, n := range usernames {
		k := selfKey(n)
		if k != "" && !seenName[k] {
			seenName[k] = true
			names = append(names, k)
		}
	}

	groups := []string{}
	seenGroup := map[string]bool{}
	source := ""

	add := func(g, from string) {
		k := selfKey(g)
		if k == "" || seenGroup[k] {
			return
		}
		seenGroup[k] = true
		groups = append(groups, k)
		source = from
	}
	for _, r := range activeRows {
		if r["name"] != "" && seenName[selfKey(r["name"])] && r["group"] != "" {
			add(r["group"], "active")
		}
	}
	if len(groups) == 0 {
		for _, r := range userRows {
			if r["name"] != "" && seenName[selfKey(r["name"])] && r["group"] != "" {
				add(r["group"], "user")
			}
		}
	}

	return Self{Names: names, Groups: groups, Resolved: len(groups) > 0, Source: source}
}

// Refusal is a guard answer. OK true means proceed; otherwise Code names the
// rule and Detail names the value that tripped it.
type Refusal struct {
	OK     bool   `json:"ok"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

var selfOK = Refusal{OK: true}

func refuse(code, detail string) Refusal { return Refusal{Code: code, Detail: detail} }

// IsSelfUser reports whether a name is one of ours.
func (s Self) IsSelfUser(name string) bool { return contains(s.Names, selfKey(name)) }

// IsSelfGroup reports whether a group is ours.
func (s Self) IsSelfGroup(group string) bool { return contains(s.Groups, selfKey(group)) }

// UserAction is one write against /user: add, set, remove, or any verb that
// amounts to set — enable, disable, reset the password.
//
// Target is the row AS FRESHLY READ FROM THE ROUTER, never the row the browser
// sent and never one from the collector's last payload.
type UserAction struct {
	Verb   string
	Target routeros.Reply
	Values map[string]string
	// ValueSet names which keys Values actually carries. An ABSENT field and a
	// field set to "" are different requests — `{name: ''}` is an attempt to
	// write an empty name, while no `name` key means the action does not touch
	// it — and a Go map alone cannot express that difference the way the
	// original's `!== undefined` does.
	ValueSet map[string]bool
}

func (a UserAction) has(k string) bool {
	if a.ValueSet != nil {
		return a.ValueSet[k]
	}
	_, ok := a.Values[k]
	return ok
}

// CheckUser judges a /user write.
func CheckUser(self Self, a UserAction) Refusal {
	if !self.Resolved {
		return refuse("self-unresolved", "")
	}
	// Target side. Absent on add, which the value side below covers.
	if a.Target != nil && self.IsSelfUser(a.Target["name"]) {
		return refuse("protected-account", a.Target["name"])
	}
	// Value side: creating or renaming INTO our name, or moving anybody into our
	// group. Refused for every user, including ones that are not ours — this is
	// the privilege-escalation rule, not the lockout rule.
	if a.has("name") && self.IsSelfUser(a.Values["name"]) {
		return refuse("protected-name-value", a.Values["name"])
	}
	if a.has("group") && self.IsSelfGroup(a.Values["group"]) {
		return refuse("protected-group-value", a.Values["group"])
	}
	if a.Verb == "remove" && a.Target == nil {
		return refuse("bad-request", "")
	}
	return selfOK
}

// CheckGroup judges a /user/group write — add, set including rename and policy
// edits, or remove.
func CheckGroup(self Self, a UserAction) Refusal {
	if !self.Resolved {
		return refuse("self-unresolved", "")
	}
	if a.Target != nil && self.IsSelfGroup(a.Target["name"]) {
		return refuse("protected-group", a.Target["name"])
	}
	// Renaming another group ONTO ours would make the protected name ambiguous on
	// the next read. RouterOS rejects the duplicate itself; the refusal should
	// still be ours, and legible.
	if a.has("name") && self.IsSelfGroup(a.Values["name"]) {
		return refuse("protected-group-value", a.Values["name"])
	}
	if a.Verb == "remove" && a.Target == nil {
		return refuse("bad-request", "")
	}
	return selfOK
}

// CheckSession judges ending a /user/active session.
//
// EVERY row whose name is ours is refused, whatever the `via`: MikroDash keeps
// several logins open per router — the dashboard session, plus one each for
// alerts and the routers overview — and they are all equally ours.
func CheckSession(self Self, target routeros.Reply) Refusal {
	if !self.Resolved {
		return refuse("self-unresolved", "")
	}
	if target == nil {
		return refuse("bad-request", "")
	}
	if self.IsSelfUser(target["name"]) {
		return refuse("protected-account", target["name"])
	}
	return selfOK
}
