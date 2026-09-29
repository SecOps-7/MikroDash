// Package credprof applies a credential profile to one router: it creates,
// updates and removes the RouterOS account a profile describes.
//
// ── WHAT THIS IS NOT ────────────────────────────────────────────────────────
//
// It never touches the account MikroDash signs in with. That is not a
// convention here, it is enforced per router by `internal/guard`, which is
// consulted before every command and fails closed when it cannot work out which
// account is ours. A profile that named MikroDash's own account or group is
// refused twice: once when it is saved, and once per device by the guard.
//
// ── WHY THIS IS NOT A FLEET ENDPOINT ────────────────────────────────────────
//
// `internal/server/dnsfleet_api.go` is the only other fleet write outside
// Config Management, and its header says exactly why it is allowed to exist:
//
//	"THAT IS ONLY SAFE BECAUSE dnsStatic DECLARES NO GUARD. A guarded resource
//	would lose its guard down this path."
//
// `resource.RosUser` DOES declare `selfAccount`. So this cannot be modelled on
// that endpoint, and the guard is run here, per router, against three tables
// read in the same tick as the write.
//
// ── WHY AN Execer AND NOT A SESSION ─────────────────────────────────────────
//
// Every step is a command in and rows out, so the whole sequence — including
// each refusal, which is the part worth testing — runs against a fake with no
// router, no connection and no server. The caller holds the router's write
// queue; this package holds no locks and no state.
package credprof

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mikrodash/internal/guard"
	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
)

// Execer runs one RouterOS command. `internal/session`'s Exec satisfies it.
type Execer interface {
	Exec(routeros.Cmd) ([]routeros.Reply, error)
}

// State is where one profile stands on one router.
//
// ── refused AND conflict ARE TERMINAL ───────────────────────────────────────
//
// Both mean a human has to decide something: the guard will refuse this profile
// on this device every time, or an account that is not ours is in the way.
// Retrying either writes an audit row every sweep, for ever, which is how a
// trail becomes something people learn to scroll past.
//
// `unknown` is NOT terminal, unlike `InterruptCfgRuns`' treatment of an
// interrupted deploy: applying a profile is idempotent, and a fresh read tells
// the truth about what is on the device.
const (
	StatePending     = "pending"
	StateApplying    = "applying"
	StateApplied     = "applied"
	StateRefused     = "refused"
	StateConflict    = "conflict"
	StateFailed      = "failed"
	StateUnreachable = "unreachable"
	StateRemoving    = "removing"
	StateOrphaned    = "orphaned"
	StateUnknown     = "unknown"
)

// Terminal reports whether a state should stop being retried.
func Terminal(state string) bool {
	return state == StateRefused || state == StateConflict
}

// PermBuiltin uses one of RouterOS's own groups; PermCustom makes one.
const (
	PermBuiltin = "builtin"
	PermCustom  = "custom"
)

// BuiltinGroups are the three RouterOS ships with.
var BuiltinGroups = []string{"read", "write", "full"}

// Spec is one profile as the applier needs it: the password already unsealed,
// and nothing about which routers it is linked to.
type Spec struct {
	ID       string
	Name     string
	Username string
	PermKind string
	// Builtin is read, write or full when PermKind is PermBuiltin.
	Builtin string
	// Group and Policies describe the group to make when PermKind is PermCustom.
	Group    string
	Policies []string
	Password string
	Revision int64
}

// Outcome is what one Apply or Remove did.
type Outcome struct {
	State string
	Code  string
	Err   error
}

func fail(state, code string, err error) Outcome {
	return Outcome{State: state, Code: code, Err: err}
}

// ── RESERVED NAMES ──────────────────────────────────────────────────────────
//
// The guard already refuses MikroDash's own account per router, and it is the
// authority: it reads the live connection's name and the group that connection
// actually landed in. This list is a SECOND, cruder refusal that runs when a
// profile is saved, before any router is contacted.
//
// It is not redundant. Without it, a profile named `mikrodash-ztp` is accepted,
// linked to forty devices, and then refused forty times — forty error rows
// describing one mistake, none of them at the moment it was made. With it the
// operator is told once, while they are still looking at the form.
//
// `mikrodash` is what the README tells operators to call the account on a
// hand-added router; `mikrodash-ztp` is what ZTP creates. Both are conventions
// rather than guarantees, which is exactly why the guard is still the authority.
var reservedNames = []string{"mikrodash", "mikrodash-ztp"}

// ErrReservedName is a profile naming an account MikroDash uses itself.
var ErrReservedName = errors.New("that username is one MikroDash signs in with")

// CheckSpec validates a profile at save time, before any router is involved.
func CheckSpec(s Spec) error {
	name := strings.ToLower(strings.TrimSpace(s.Username))
	if name == "" {
		return errors.New("a profile needs a RouterOS username")
	}
	for _, r := range reservedNames {
		if name == r {
			return fmt.Errorf("%w: %s", ErrReservedName, s.Username)
		}
	}
	switch s.PermKind {
	case PermBuiltin:
		if !contains(BuiltinGroups, s.Builtin) {
			return fmt.Errorf("%q is not a RouterOS built-in group", s.Builtin)
		}
	case PermCustom:
		g := strings.ToLower(strings.TrimSpace(s.Group))
		if g == "" {
			return errors.New("a custom permission set needs a group name")
		}
		// A custom group must not be named after a built-in one: RouterOS would
		// refuse the add, and that refusal would arrive per device rather than
		// here.
		if contains(BuiltinGroups, g) {
			return fmt.Errorf("%q is a RouterOS built-in group; choose another name", s.Group)
		}
		for _, p := range s.Policies {
			if !contains(resource.UserPolicies, p) {
				return fmt.Errorf("%q is not a RouterOS policy", p)
			}
		}
	default:
		return fmt.Errorf("unknown permission kind %q", s.PermKind)
	}
	if s.Password == "" {
		// The same rule, and the same reason, as resource.RequiredOnCreate: a
		// profile with no password provisions accounts anyone can sign in to.
		return errors.New("a profile needs a password")
	}
	return nil
}

// ── THE OWNERSHIP MARKER ────────────────────────────────────────────────────
//
// Every account and group this package creates carries one in its `comment`:
//
//	MikroDash credential profile: NOC Read-only [mdp:7f3a…]
//
// It is a FACT ON THE DEVICE, and that is the point. Ownership cannot be
// inferred from `cred_profile_links` alone: a router restored from a backup, or
// a MikroDash database rebuilt from nothing, leaves rows and accounts that no
// longer agree. The marker survives both, because it travels with the account.
//
// The ID is what is matched, not the name: renaming a profile must not orphan
// every account it has already placed.
const markerPrefix = "MikroDash credential profile:"

func marker(s Spec) string {
	return fmt.Sprintf("%s %s [mdp:%s]", markerPrefix, s.Name, s.ID)
}

// ownedBy reports whether a row's comment carries this profile's marker.
func ownedBy(comment, profileID string) bool {
	return strings.Contains(comment, "[mdp:"+profileID+"]")
}

// ownedByAnyProfile reports whether a row is some profile's, which is a
// different answer from "is it ours" and deserves a different message.
func ownedByAnyProfile(comment string) bool {
	return strings.Contains(comment, markerPrefix)
}

// Apply makes the profile true on one router, or says why it did not.
//
// ── THE ORDER IS THE SAFETY PROPERTY ────────────────────────────────────────
//
// Read, resolve, guard, THEN write. Nothing is sent to the router before the
// guard has answered, so a refusal costs zero commands and can never be a
// partial change. `TestTheGuardRunsBeforeAnyWrite` holds the order against the
// source, because a reordering is a one-line edit that no behavioural test
// notices on a router where the guard happens to say yes.
func Apply(ex Execer, s Spec, selfNames []string) Outcome {
	users, groups, active, out := readAll(ex)
	if out != nil {
		return *out
	}

	self := guard.ResolveSelf(users, active, selfNames)
	if !self.Resolved {
		// FAIL CLOSED. Not being able to tell which account is ours is exactly
		// when a write is most likely to be the one that cuts us off.
		return fail(StateRefused, "self-unresolved", errors.New(
			"MikroDash cannot identify its own account on this router, so no user write is safe"))
	}
	if err := CheckSpec(s); err != nil {
		return fail(StateRefused, "invalid-profile", err)
	}

	group := s.Builtin
	if s.PermKind == PermCustom {
		group = s.Group
		if o := ensureGroup(ex, s, groups, self); o.State != StateApplied {
			return o
		}
	}

	// THE GUARD, on the values this write actually carries. A `full` profile
	// dies here on a ZTP device, where MikroDash itself sits in `full`:
	// protected-group-value, and nothing was sent.
	existing := rowByName(users, s.Username)
	act := guard.UserAction{
		Verb:     "add",
		Values:   map[string]string{"name": s.Username, "group": group},
		ValueSet: map[string]bool{"name": true, "group": true},
	}
	if existing != nil {
		act.Verb = "set"
		act.Target = routeros.Reply{"name": existing["name"], "group": existing["group"]}
	}
	if r := guard.CheckUser(self, act); !r.OK {
		return fail(StateRefused, r.Code, fmt.Errorf(
			"the lockout guard refused this write: %s %s", r.Code, r.Detail))
	}

	// ── AN ACCOUNT WE DO NOT OWN IS A CONFLICT, NEVER AN ADOPTION ────────────
	//
	// Taking over a row that is already there would reset a stranger's password
	// on every linked router, silently, as a side effect of pressing Link. An
	// operator who wants that asks for it by name, through Adopt, one device at
	// a time, looking at the row.
	if existing != nil && !ownedBy(existing["comment"], s.ID) {
		detail := "an account called " + s.Username + " is already on this router"
		if ownedByAnyProfile(existing["comment"]) {
			detail += ", placed by another credential profile"
		}
		return fail(StateConflict, "conflict", errors.New(detail))
	}

	vals := map[string]string{
		"name":     s.Username,
		"group":    group,
		"password": s.Password,
		"comment":  marker(s),
	}
	if existing != nil {
		// The password is ALWAYS re-sent, which is the only way a device whose
		// password drifted can converge: RouterOS never reads one back, so there
		// is nothing to compare and no way to tell that it needs changing.
		if err := run(ex, "/user/set",
			append([]string{"=.id=" + existing[".id"]}, words(vals)...)); err != nil {
			return writeFailure(err)
		}
	} else if err := run(ex, "/user/add", words(vals)); err != nil {
		return writeFailure(err)
	}

	// CONFIRMED, as every write in this app is (#97): the row is read back.
	// "The command returned !done" and "the account is there" are different
	// claims, and only the second is what `applied` is supposed to mean.
	after, err := rows(ex, "/user/print")
	if err != nil {
		return fail(StateUnknown, "unknown", err)
	}
	made := rowByName(after, s.Username)
	if made == nil {
		return fail(StateUnknown, "unknown", errors.New(
			"the write was accepted but the account is not on the router"))
	}
	if !strings.EqualFold(strings.TrimSpace(made["group"]), strings.TrimSpace(group)) {
		return fail(StateUnknown, "unknown", fmt.Errorf(
			"the account is in group %q, not %q", made["group"], group))
	}
	return Outcome{State: StateApplied}
}

// ensureGroup creates or updates a custom profile's group. A builtin profile
// never reaches it.
func ensureGroup(ex Execer, s Spec, groups []routeros.Reply, self guard.Self) Outcome {
	existing := rowByName(groups, s.Group)

	act := guard.UserAction{
		Verb:     "add",
		Values:   map[string]string{"name": s.Group},
		ValueSet: map[string]bool{"name": true},
	}
	if existing != nil {
		act.Verb = "set"
		act.Target = routeros.Reply{"name": existing["name"]}
	}
	if r := guard.CheckGroup(self, act); !r.OK {
		return fail(StateRefused, r.Code, fmt.Errorf(
			"the lockout guard refused this group write: %s %s", r.Code, r.Detail))
	}
	if existing != nil && !ownedBy(existing["comment"], s.ID) {
		return fail(StateConflict, "conflict", errors.New(
			"a group called "+s.Group+" is already on this router"))
	}

	// THROUGH THE RESOURCE, not by hand. `RosGroup` declares `NegateUnset` on
	// `policy`, so the write names all seventeen policies with the unchosen ones
	// negated — and RouterOS removes a policy ONLY when it is named with `!`.
	// Sending the positive list would make every permission edit one-way: a
	// profile could gain `write` and never lose it again.
	v, errs := resource.RosGroup.Validate(map[string]string{
		"name":    s.Group,
		"policy":  strings.Join(s.Policies, ","),
		"comment": marker(s),
	}, existing != nil)
	if len(errs) > 0 {
		return fail(StateRefused, "invalid-profile",
			fmt.Errorf("%s: %s", errs[0].Field, errs[0].Message))
	}
	args := resource.RosGroup.BuildArgs(v)
	if existing != nil {
		if err := run(ex, "/user/group/set",
			append([]string{"=.id=" + existing[".id"]}, args...)); err != nil {
			return writeFailure(err)
		}
	} else if err := run(ex, "/user/group/add", args); err != nil {
		return writeFailure(err)
	}
	return Outcome{State: StateApplied}
}

// Remove takes the profile's account off one router.
//
// ── A FAILED REMOVAL IS NOT A FINISHED ONE ──────────────────────────────────
//
// Every failure here returns a state the caller must KEEP the link row for.
// Deleting it is how MikroDash forgets an account it created, which is the
// worst thing this feature can do: the login stays on the device at whatever
// privilege the profile carried, and nothing is left that knows it is there.
func Remove(ex Execer, s Spec, selfNames []string) Outcome {
	users, groups, active, out := readAll(ex)
	if out != nil {
		return *out
	}
	self := guard.ResolveSelf(users, active, selfNames)
	if !self.Resolved {
		return fail(StateOrphaned, "self-unresolved", errors.New(
			"MikroDash cannot identify its own account on this router, so no user write is safe"))
	}

	existing := rowByName(users, s.Username)
	switch {
	case existing == nil:
		// Already gone. Not a failure: somebody removing the account by hand is
		// an ordinary thing, and the desired state is reached either way.
	case !ownedBy(existing["comment"], s.ID):
		// It is not ours any more — adopted by another profile, or recreated by
		// hand. Removing it would be deleting somebody else's account.
		return fail(StateOrphaned, "not-ours", errors.New(
			"the account on this router no longer carries this profile's marker; it was left alone"))
	default:
		// ── THE LAST FULL USER ────────────────────────────────────────────────
		//
		// MikroTik: "There always should be at least one user with full access
		// rights. If the user with full access rights is the only one, it cannot
		// be removed." Sending the command anyway gets a router-denied with no
		// explanation of which rule was hit; refusing here says so.
		//
		// Only `full` is checked. A custom group holding `policy` is the same
		// hazard and is NOT decidable from here without re-implementing
		// RouterOS's own rule, so the router's error stays the backstop for it.
		if last, why := lastFullUser(users, existing); last {
			return fail(StateOrphaned, "last-full-user", errors.New(why))
		}
		if r := guard.CheckUser(self, guard.UserAction{
			Verb:   "remove",
			Target: routeros.Reply{"name": existing["name"], "group": existing["group"]},
		}); !r.OK {
			return fail(StateOrphaned, r.Code, fmt.Errorf(
				"the lockout guard refused this removal: %s %s", r.Code, r.Detail))
		}
		if err := run(ex, "/user/remove", []string{"=.id=" + existing[".id"]}); err != nil {
			return Outcome{State: StateOrphaned, Code: "router-denied", Err: err}
		}
		after, err := rows(ex, "/user/print")
		if err != nil {
			return fail(StateUnknown, "unknown", err)
		}
		if rowByName(after, s.Username) != nil {
			return fail(StateUnknown, "unknown", errors.New(
				"the remove was accepted but the account is still on the router"))
		}
	}

	// ── GROUP CLEANUP IS BEST-EFFORT, AND DELIBERATELY SO ────────────────────
	//
	// An empty group left behind is untidy. An account left behind is a
	// credential nobody knows about. Only the second is worth failing a removal
	// over, so a group that will not go is not reported as a failure.
	if s.PermKind == PermCustom {
		if g := rowByName(groups, s.Group); g != nil && ownedBy(g["comment"], s.ID) {
			if after, err := rows(ex, "/user/print"); err == nil && !groupInUse(after, s.Group) {
				_ = run(ex, "/user/group/remove", []string{"=.id=" + g[".id"]})
			}
		}
	}
	return Outcome{State: StateApplied}
}

// lastFullUser reports whether removing `target` would take the router's only
// enabled full-access account.
func lastFullUser(users []routeros.Reply, target routeros.Reply) (bool, string) {
	if !strings.EqualFold(strings.TrimSpace(target["group"]), "full") {
		return false, ""
	}
	for _, u := range users {
		if u[".id"] == target[".id"] || u["disabled"] == "true" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(u["group"]), "full") {
			return false, ""
		}
	}
	return true, "this is the router's only full-access account, and RouterOS does not allow " +
		"the last one to be removed"
}

// groupInUse reports whether any user still names the group.
func groupInUse(users []routeros.Reply, group string) bool {
	for _, u := range users {
		if strings.EqualFold(strings.TrimSpace(u["group"]), strings.TrimSpace(group)) {
			return true
		}
	}
	return false
}

// readAll takes the three tables the guard needs, in one tick.
//
// ── FRESH, AND NOT FROM A COLLECTOR ─────────────────────────────────────────
//
// `selfAccountVerdict`'s rule, for its reason: the guard's answer is only as
// current as the rows it saw, and a cached `/user/active` can name a session
// that has since moved. A table that cannot be READ is a failure here rather
// than an empty list, because an empty `/user/print` makes every account look
// absent and every conflict disappear.
func readAll(ex Execer) (users, groups, active []routeros.Reply, bad *Outcome) {
	var err error
	if users, err = rows(ex, "/user/print"); err != nil {
		o := fail(StateUnreachable, "read-failed", fmt.Errorf("reading /user: %w", err))
		return nil, nil, nil, &o
	}
	if groups, err = rows(ex, "/user/group/print"); err != nil {
		o := fail(StateUnreachable, "read-failed", fmt.Errorf("reading /user/group: %w", err))
		return nil, nil, nil, &o
	}
	if active, err = rows(ex, "/user/active/print"); err != nil {
		o := fail(StateUnreachable, "read-failed", fmt.Errorf("reading /user/active: %w", err))
		return nil, nil, nil, &o
	}
	return users, groups, active, nil
}

func rows(ex Execer, path string) ([]routeros.Reply, error) {
	return ex.Exec(routeros.Cmd{Path: path})
}

func run(ex Execer, path string, args []string) error {
	_, err := ex.Exec(routeros.Cmd{Path: path, Args: args})
	return err
}

// writeFailure separates a router saying no from the connection going away: the
// first stands until something changes, the second is worth retrying soon.
func writeFailure(err error) Outcome {
	var trap *routeros.Trap
	if errors.As(err, &trap) {
		return Outcome{State: StateFailed, Code: "router-denied", Err: err}
	}
	return Outcome{State: StateUnreachable, Code: "unreachable", Err: err}
}

// rowByName finds a row by its `name`, case-insensitively.
//
// RouterOS names are case-SENSITIVE, so this over-matches — which is the
// direction to err in: treating `NOC` and `noc` as one account produces a
// conflict refusal, while treating them as two produces a second account.
func rowByName(rs []routeros.Reply, name string) routeros.Reply {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, r := range rs {
		if strings.ToLower(strings.TrimSpace(r["name"])) == want {
			return r
		}
	}
	return nil
}

// words turns a value map into `=key=value` arguments, in a fixed order so a
// command is reproducible and a test can read it.
func words(vals map[string]string) []string {
	out := make([]string, 0, len(vals))
	for _, k := range []string{"name", "group", "password", "comment", "address"} {
		if v, ok := vals[k]; ok {
			out = append(out, "="+k+"="+v)
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// ParsePolicies reads the stored policy_json column.
func ParsePolicies(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// MarshalPolicies writes the policy_json column. NEVER "null": the column is
// NOT NULL, and a nil slice marshals to null, which is the same shape
// TestNoPayloadSendsANullArray refuses on the wire.
func MarshalPolicies(p []string) string {
	if p == nil {
		p = []string{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "[]"
	}
	return string(b)
}
