package rbac

// `syncUserGrants` — WHICH GRANTS a user gets, as a plan rather than as writes.
//
// ── IT DECIDES WHETHER A PERSON CAN USE THE APPLICATION AT ALL ──────────────
//
// The live route that mints the first administrator calls it, and says why:
// "Without this the very first administrator of a fresh install holds no grants,
// and every guard refuses them — locked out of their own instance the moment
// setup completes." The other direction is worse and quieter: an over-broad
// grant is a person seeing routers nobody gave them.
//
// ── THE INVERSION ───────────────────────────────────────────────────────────
//
// `allowedRouterIds: []` means UNRESTRICTED, not "no routers". An empty list
// produces ONE GLOBAL grant; a non-empty one produces a router-scoped grant per
// id. Read quickly, the original says the opposite of what it does, and a port
// that "fixed" it would lock out every unrestricted user on the first run after
// cutover.
//
// Several inputs collapse into that empty case and they do not look alike: an
// empty list, a NON-ARRAY (null, a string), and a list whose entries are all
// falsy — `.filter(Boolean)` runs before the length test, so `[""]` is
// unrestricted. `AllowedRouterIDs` below is `any` for exactly that reason: the
// COERCION is the rule, and a `[]string` field would have decided it before this
// function ran.
//
// ── AND IT IS A REPLACE ─────────────────────────────────────────────────────
//
// The delete is unconditional and comes BEFORE the branch, so a user whose every
// id is unknown ends with no grants at all. That is the one way this function
// locks somebody out, it is a different outcome from the empty case, and the
// plan carries the delete as a step rather than leaving it implied.
//
// ── NO CALLER YET, ON PURPOSE ───────────────────────────────────────────────
//
// `POST /api/users/setup` is the caller and is the remaining half of the same
// queue item. This repo already holds pure decisions ahead of their callers —
// `internal/routers/pool.go` and `internal/history.Bucketer` — because the
// DECISION is what needs pinning against the live implementation while the
// wiring is what needs a running server. `db.UpsertGrant` and
// `db.DeleteGrantsForPrincipal` are the effects, already exist, and likewise
// have no caller outside `internal/db`.

import (
	"fmt"
	"strconv"
)

// UserForGrants is the little of a user record this decision reads. It reads no
// credential, and the corpus generator refuses to write a password-shaped key
// for that reason.
type UserForGrants struct {
	ID       string
	Username string
	// Role is the raw stored value, not a validated one. Deciding it here is the
	// point: see grantRole.
	Role string
	// AllowedRouterIDs is `any` because the coercion is the rule. See the header.
	AllowedRouterIDs any
}

// GrantStep is one operation in the plan, in order.
type GrantStep struct {
	// Op is "delete" or "upsert".
	Op string
	// Role, ScopeType and ScopeID are set on an upsert only. ScopeID is empty for
	// a global grant.
	Role      string
	ScopeType string
	ScopeID   string
}

// GrantPlan is what a caller should do, and what it should say while doing it.
type GrantPlan struct {
	Steps []GrantStep
	// Warnings are the dropped-unknown-router lines, worded as the live side
	// words them. Carried rather than logged here so the decision stays pure and
	// a test can see them — a dropped router is the difference between a person
	// having access to a device and not.
	Warnings []string
	// Made is the live function's return value: the number of grants written.
	// ZERO IS MEANINGFUL and is not an error — it is the lockout.
	Made int
}

// PlanUserGrants is `syncUserGrants` with the writes lifted out.
//
// liveRouterIDs is `new Set(Routers.loadAll().map(r => r.id))`. It is passed in
// rather than read here so the decision has no I/O, which is what lets
// The syncgrants corpus drive both sides from one corpus.
func PlanUserGrants(u UserForGrants, liveRouterIDs map[string]bool) GrantPlan {
	// A refusal that does NOTHING, delete included. A port that deleted first and
	// then checked the id would wipe a real user's grants on a malformed call.
	if u.ID == "" {
		return GrantPlan{}
	}

	plan := GrantPlan{Steps: []GrantStep{{Op: "delete"}}}
	role := grantRole(u.Role)
	ids := truthyIDs(u.AllowedRouterIDs)

	if len(ids) == 0 {
		plan.Steps = append(plan.Steps, GrantStep{Op: "upsert", Role: role, ScopeType: "global"})
		plan.Made = 1
		return plan
	}
	for _, rid := range ids {
		if !liveRouterIDs[rid] {
			// The live wording, reproduced: it names the user and the id, and an
			// operator wondering why somebody cannot see a router goes looking
			// for exactly this line.
			plan.Warnings = append(plan.Warnings,
				fmt.Sprintf("[rbac] dropping %s's access to unknown router %s", u.Username, rid))
			continue
		}
		// NOT DE-DUPLICATED. The live loop upserts once per entry and the upsert
		// absorbs a repeat; `Made` counts entries rather than distinct routers,
		// and the corpus carries a duplicate so a port that tidied this fails.
		plan.Steps = append(plan.Steps, GrantStep{Op: "upsert", Role: role, ScopeType: "router", ScopeID: rid})
		plan.Made++
	}
	return plan
}

// grantRole is the three-way ladder, and the default is the point.
//
//	user.role === 'admin'      ? 'admin'
//	: user.role === 'operator' ? 'operator'
//	: 'viewer'
//
// ANYTHING UNRECOGNISED BECOMES viewer — not admin, and not an error. The match
// is exact, so "Admin" is a viewer. Least privilege is the right way to be wrong
// about a role, and the live migration beside it says so in as many words about
// the same mapping.
func grantRole(role string) string {
	switch role {
	case "admin":
		return "admin"
	case "operator":
		return "operator"
	default:
		return "viewer"
	}
}

// truthyIDs is `(Array.isArray(x) ? x : []).filter(Boolean).map(String)`.
//
// ── THE ORDER OF THOSE THREE IS THE WHOLE COERCION ──────────────────────────
//
// A non-array becomes empty, which means UNRESTRICTED — so a caller sending the
// string "r1" grants everything rather than one router. Then `filter(Boolean)`
// drops "", 0, null and false BEFORE the length test, so a list of only falsy
// entries is also unrestricted. Only then is each survivor stringified, which is
// what makes a numeric id match a stored string one.
//
// Decoded from JSON a list arrives as `[]any` holding strings and float64s, so
// both are handled. Anything else is DROPPED rather than formatted, because Go's
// `%v` for a map is not JavaScript's `String({})` and neither would match a
// router id — a value that cannot name a router is treated as one that does not.
func truthyIDs(v any) []string {
	switch list := v.(type) {
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := truthyID(item); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		// A list from Go code rather than from JSON, accepted so a caller that
		// already has one need not wrap it. Same falsy rule: "" is dropped.
		out := make([]string, 0, len(list))
		for _, s := range list {
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		// NOT AN ARRAY, so unrestricted. nil, a string and a number all land here,
		// and a string landing here is the surprising one — see the header.
		return nil
	}
}

// truthyID is `Boolean(x) && String(x)` for one JSON value.
func truthyID(item any) (string, bool) {
	switch x := item.(type) {
	case string:
		return x, x != ""
	case float64:
		// 0 is falsy and drops. Every other number is stringified the way
		// JavaScript does it, so 1 is "1" and not "1.000000" — which is what lets
		// a numeric id match a router stored under a string.
		return strconv.FormatFloat(x, 'f', -1, 64), x != 0
	case bool:
		// `true` survives `filter(Boolean)` and becomes "true", which matches no
		// router id — so it reaches the caller as a dropped-router warning rather
		// than being silently ignored here. `false` is falsy and drops.
		return "true", x
	default:
		// nil included: `null` is falsy.
		return "", false
	}
}
