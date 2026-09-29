package credprof

import (
	"errors"
	"os"
	"strings"
	"testing"

	"mikrodash/internal/routeros"
)

// ── THE FAKE ROUTER ─────────────────────────────────────────────────────────
//
// It records every command in order, which is what makes "nothing was sent"
// checkable. A refusal that still wrote to the router is the failure this whole
// package is arranged to prevent, and the only way to see it is to count.
type fakeRouter struct {
	users  []routeros.Reply
	groups []routeros.Reply
	active []routeros.Reply
	sent   []routeros.Cmd
	// readErr fails one read path, to exercise unreachable.
	readErr map[string]error
	// writeErr fails any write.
	writeErr error
	// onWrite, if set, mutates the tables so a read-back can see the write.
	onWrite func(f *fakeRouter, c routeros.Cmd)
}

func (f *fakeRouter) Exec(c routeros.Cmd) ([]routeros.Reply, error) {
	if err := f.readErr[c.Path]; err != nil {
		return nil, err
	}
	switch c.Path {
	case "/user/print":
		return f.users, nil
	case "/user/group/print":
		return f.groups, nil
	case "/user/active/print":
		return f.active, nil
	}
	f.sent = append(f.sent, c)
	if f.writeErr != nil {
		return nil, f.writeErr
	}
	if f.onWrite != nil {
		f.onWrite(f, c)
	}
	return nil, nil
}

// writes is every command that was not a read.
func (f *fakeRouter) writes() []string {
	out := make([]string, 0, len(f.sent))
	for _, c := range f.sent {
		out = append(out, c.Path+" "+strings.Join(c.Args, " "))
	}
	return out
}

// applying is a fake that lets an add succeed: the row appears, so the
// read-back confirming it finds what it is looking for.
func applying(group string) func(*fakeRouter, routeros.Cmd) {
	return func(f *fakeRouter, c routeros.Cmd) {
		if c.Path != "/user/add" {
			return
		}
		row := routeros.Reply{".id": "*99", "group": group}
		for _, a := range c.Args {
			if k, v, ok := splitArg(a); ok {
				row[k] = v
			}
		}
		f.users = append(f.users, row)
	}
}

func splitArg(a string) (string, string, bool) {
	a = strings.TrimPrefix(a, "=")
	i := strings.Index(a, "=")
	if i < 0 {
		return "", "", false
	}
	return a[:i], a[i+1:], true
}

// mdSelf is a router where MikroDash is an ordinary user in a custom group,
// which is the README's arrangement on a hand-added router.
func mdSelf() *fakeRouter {
	return &fakeRouter{
		users: []routeros.Reply{
			{".id": "*1", "name": "admin", "group": "full"},
			{".id": "*2", "name": "mikrodash", "group": "mikrodash"},
		},
		groups: []routeros.Reply{
			{".id": "*g1", "name": "mikrodash", "policy": "read,api,test"},
		},
		active: []routeros.Reply{{"name": "mikrodash", "group": "mikrodash"}},
	}
}

func readSpec() Spec {
	return Spec{ID: "cp1", Name: "NOC Read-only", Username: "noc",
		PermKind: PermBuiltin, Builtin: "read", Password: "a-real-password", Revision: 1}
}

// TestApplyCreatesTheAccountWithItsOwnershipMarker: the ordinary case, and the
// control every refusal test below needs — without it they would all pass for a
// package that refuses everything.
func TestApplyCreatesTheAccountWithItsOwnershipMarker(t *testing.T) {
	f := mdSelf()
	f.onWrite = applying("read")

	if o := Apply(f, readSpec(), []string{"mikrodash"}); o.State != StateApplied {
		t.Fatalf("a plain read-only profile did not apply: %s %s %v", o.State, o.Code, o.Err)
	}
	w := f.writes()
	if len(w) != 1 || !strings.HasPrefix(w[0], "/user/add ") {
		t.Fatalf("expected one /user/add, got %v", w)
	}
	for _, want := range []string{"=name=noc", "=group=read", "=password=a-real-password",
		"[mdp:cp1]"} {
		if !strings.Contains(w[0], want) {
			t.Errorf("the add does not carry %q: %s", want, w[0])
		}
	}
}

// TestApplyIsRefusedWhenMikroDashCannotIdentifyItself.
//
// ── FAIL CLOSED ─────────────────────────────────────────────────────────────
//
// `guard.ResolveSelf` answers Resolved=false when neither /user nor
// /user/active names an account we recognise — a RADIUS login, a renamed
// account, an API user that cannot see the table. That is precisely when a
// write is most likely to be the one that cuts MikroDash off, so it refuses,
// and refuses BEFORE anything is sent.
func TestApplyIsRefusedWhenMikroDashCannotIdentifyItself(t *testing.T) {
	f := mdSelf()
	f.active = nil
	f.users = []routeros.Reply{{".id": "*1", "name": "admin", "group": "full"}}

	o := Apply(f, readSpec(), []string{"mikrodash"})
	if o.State != StateRefused || o.Code != "self-unresolved" {
		t.Errorf("an unidentifiable MikroDash gave %s/%s, want refused/self-unresolved",
			o.State, o.Code)
	}
	if w := f.writes(); len(w) != 0 {
		t.Errorf("a refusal still wrote to the router: %v", w)
	}

	// ── AND THE MESSAGE, WHICH IS THE PART THAT IS NOT REDUNDANT ─────────────
	//
	// The mutation sweep found that deleting this check entirely still refuses,
	// because `guard.CheckUser` fails closed on the same condition further down.
	// That is defence in depth working, and it also means a test asserting only
	// the state and code cannot see the check at all - it would pass on a
	// version that had none.
	//
	// What the early check actually adds is a sentence naming MikroDash and this
	// router, instead of the guard's bare `self-unresolved` arriving from four
	// frames away. So that is what is asserted.
	if o.Err == nil || !strings.Contains(o.Err.Error(), "cannot identify its own account") {
		t.Errorf("the refusal does not explain itself in the operator's terms: %v", o.Err)
	}
}

// TestRefusalsAreTerminalAndFailuresAreNot.
//
// ── BOTH DIRECTIONS, BECAUSE ONE IS USELESS ─────────────────────────────────
//
// The reconciler reads this to decide whether to try again. Getting it wrong
// one way retries a guard refusal every sweep for ever, writing an audit row
// each time until the trail is something people scroll past; getting it wrong
// the other way strands a router that was merely switched off.
//
// Asserting only the negative half is how the first version of this passed
// while `Terminal` returned false for everything.
func TestRefusalsAreTerminalAndFailuresAreNot(t *testing.T) {
	for _, s := range []string{StateRefused, StateConflict} {
		if !Terminal(s) {
			t.Errorf("%s is retryable; a guard refusal does not become true by being "+
				"asked again, and each retry writes another audit row", s)
		}
	}
	for _, s := range []string{StateUnreachable, StateFailed, StateUnknown, StatePending,
		StateOrphaned, StateApplied} {
		if Terminal(s) {
			t.Errorf("%s is terminal; a router that was switched off would never converge", s)
		}
	}
}

// TestAFullProfileIsRefusedWhereMikroDashItselfIsFull.
//
// ── THE ZTP INTERACTION, MEASURED RATHER THAN ASSUMED ───────────────────────
//
// ZTP creates `mikrodash-ztp` with group=full (internal/ztp/script.go), so on
// every ZTP-onboarded device MikroDash's own group IS `full` — and the guard
// refuses moving any user into MikroDash's group, which is the
// privilege-escalation rule rather than the lockout one.
//
// The rule is therefore NOT "no full profiles". It is "a profile cannot use
// whichever group MikroDash occupies on THAT device", which varies per device.
// The control below is the same profile on a router where MikroDash sits in a
// custom group, where `full` is perfectly fine.
func TestAFullProfileIsRefusedWhereMikroDashItselfIsFull(t *testing.T) {
	ztp := &fakeRouter{
		users: []routeros.Reply{
			{".id": "*1", "name": "admin", "group": "full"},
			{".id": "*2", "name": "mikrodash-ztp", "group": "full"},
		},
		groups: []routeros.Reply{},
		active: []routeros.Reply{{"name": "mikrodash-ztp", "group": "full"}},
	}
	s := readSpec()
	s.Builtin = "full"

	o := Apply(ztp, s, []string{"mikrodash-ztp"})
	if o.State != StateRefused || o.Code != "protected-group-value" {
		t.Errorf("a full profile on a ZTP device gave %s/%s, want refused/protected-group-value",
			o.State, o.Code)
	}
	if w := ztp.writes(); len(w) != 0 {
		t.Errorf("the refusal still wrote to the router: %v", w)
	}

	// THE CONTROL: the same full profile on a README-configured router, where
	// MikroDash is in its own group, applies.
	ok := mdSelf()
	ok.onWrite = applying("full")
	if o := Apply(ok, s, []string{"mikrodash"}); o.State != StateApplied {
		t.Errorf("a full profile was refused where MikroDash is not full: %s %s %v",
			o.State, o.Code, o.Err)
	}
}

// TestAProfileCannotBeNamedAfterMikroDashsOwnAccount: the guard's value-side
// rule, which is privilege escalation rather than lockout. Both names are
// checked, because ZTP and a hand-added router use different ones.
func TestAProfileCannotBeNamedAfterMikroDashsOwnAccount(t *testing.T) {
	for _, name := range []string{"mikrodash", "mikrodash-ztp", "MikroDash"} {
		s := readSpec()
		s.Username = name
		if err := CheckSpec(s); err == nil {
			t.Errorf("a profile called %q was accepted at save time", name)
		}
		// And the router-side guard refuses it too, so the save-time list being
		// wrong or incomplete is not the only thing standing between an operator
		// and a broken login.
		f := mdSelf()
		if o := Apply(f, s, []string{"mikrodash"}); o.State != StateRefused {
			t.Errorf("a profile called %q was applied to a router: %s", name, o.State)
		}
		if w := f.writes(); len(w) != 0 {
			t.Errorf("a profile called %q wrote to the router: %v", name, w)
		}
	}
	// THE CONTROL: an ordinary name passes both.
	if err := CheckSpec(readSpec()); err != nil {
		t.Errorf("an ordinary profile was refused at save time: %v", err)
	}
}

// TestAnAccountWeDoNotOwnIsAConflictAndIsNeverAdopted.
//
// Silently taking over an existing account would reset a stranger's password on
// every linked router as a side effect of pressing Link. So an account with no
// marker, or another profile's, stops the apply with nothing written.
func TestAnAccountWeDoNotOwnIsAConflictAndIsNeverAdopted(t *testing.T) {
	for _, tc := range []struct{ what, comment string }{
		{"an account somebody made by hand", "the night shift"},
		{"another profile's account", markerPrefix + " Other [mdp:cp2]"},
	} {
		f := mdSelf()
		f.users = append(f.users,
			routeros.Reply{".id": "*7", "name": "noc", "group": "full", "comment": tc.comment})

		o := Apply(f, readSpec(), []string{"mikrodash"})
		if o.State != StateConflict {
			t.Errorf("%s gave %s/%s, want conflict", tc.what, o.State, o.Code)
		}
		if w := f.writes(); len(w) != 0 {
			t.Errorf("%s was written over: %v", tc.what, w)
		}
	}

	// THE CONTROL: OUR account is updated rather than refused, and the password
	// is re-sent - the only way a device whose password drifted converges, since
	// RouterOS never reads one back to compare.
	f := mdSelf()
	f.users = append(f.users, routeros.Reply{".id": "*7", "name": "noc", "group": "read",
		"comment": markerPrefix + " NOC Read-only [mdp:cp1]"})
	if o := Apply(f, readSpec(), []string{"mikrodash"}); o.State != StateApplied {
		t.Fatalf("our own account was not updated: %s %s %v", o.State, o.Code, o.Err)
	}
	w := f.writes()
	if len(w) != 1 || !strings.HasPrefix(w[0], "/user/set ") {
		t.Fatalf("expected one /user/set, got %v", w)
	}
	if !strings.Contains(w[0], "=password=a-real-password") {
		t.Errorf("the update did not re-send the password: %s", w[0])
	}
}

// TestACustomGroupNamesEveryPolicyNegated.
//
// RouterOS removes a group policy ONLY when it is named with `!`: `set
// policy=read` against a group holding read,test,api changes nothing. Going
// through `resource.RosGroup`, which declares NegateUnset, is what gets that
// right. Writing the positive list by hand would make every permission edit
// one-way - a profile could gain `write` and never lose it again.
func TestACustomGroupNamesEveryPolicyNegated(t *testing.T) {
	f := mdSelf()
	f.onWrite = applying("noc-group")
	s := readSpec()
	s.PermKind, s.Group, s.Policies = PermCustom, "noc-group", []string{"read", "api", "winbox"}

	if o := Apply(f, s, []string{"mikrodash"}); o.State != StateApplied {
		t.Fatalf("a custom profile did not apply: %s %s %v", o.State, o.Code, o.Err)
	}
	w := f.writes()
	if len(w) != 2 || !strings.HasPrefix(w[0], "/user/group/add ") {
		t.Fatalf("expected a group add then a user add, got %v", w)
	}
	// Granted positively, everything else negated. `sniff` and `password` are
	// the ones worth naming: a group that quietly kept them would be a privilege
	// the operator did not choose.
	for _, want := range []string{"read", "api", "winbox", "!sniff", "!password", "!policy"} {
		if !strings.Contains(w[0], want) {
			t.Errorf("the group write does not name %q: %s", want, w[0])
		}
	}
	if !strings.Contains(w[0], "[mdp:cp1]") {
		t.Errorf("the group carries no ownership marker: %s", w[0])
	}
}

// TestAnUnreadableTableIsUnreachableNotEmpty.
//
// ── AN EMPTY RESULT IS NOT A NEGATIVE RESULT ────────────────────────────────
//
// If a failed `/user/print` were treated as "no users", every account would
// look absent, every conflict would disappear, and the applier would add a
// second `noc` to a router that already has one. So a read that fails is a
// failure, not an empty list. It is also RETRYABLE, unlike a refusal: the
// router being unreachable says nothing about whether the profile is sound.
func TestAnUnreadableTableIsUnreachableNotEmpty(t *testing.T) {
	for _, path := range []string{"/user/print", "/user/group/print", "/user/active/print"} {
		f := mdSelf()
		f.readErr = map[string]error{path: errors.New("connection lost")}
		o := Apply(f, readSpec(), []string{"mikrodash"})
		if o.State != StateUnreachable {
			t.Errorf("a failed %s gave %s/%s, want unreachable", path, o.State, o.Code)
		}
		if Terminal(o.State) {
			t.Errorf("a failed %s was treated as terminal; the router being down says "+
				"nothing about whether the profile is sound", path)
		}
		if w := f.writes(); len(w) != 0 {
			t.Errorf("a failed %s still led to a write: %v", path, w)
		}
	}
}

// TestAWriteThatDidNotLandIsUnknownNotApplied.
//
// "The command returned !done" and "the account is on the router" are different
// claims (#97). Only the second is what `applied` is supposed to mean, and the
// difference is invisible without the read-back.
func TestAWriteThatDidNotLandIsUnknownNotApplied(t *testing.T) {
	f := mdSelf() // no onWrite: the add is accepted and the row never appears
	o := Apply(f, readSpec(), []string{"mikrodash"})
	if o.State != StateUnknown {
		t.Errorf("an add whose row never appeared gave %s/%s, want unknown", o.State, o.Code)
	}
	// THE MESSAGE, not just the state. The mutation sweep found that deleting
	// the absent-row check still returns unknown, because the group comparison
	// below it reads "" out of a nil row and disagrees with the wanted group. So
	// a test reading only the state passes on a version with no absent-row check
	// and a message about the wrong group, which is not what happened.
	if o.Err == nil || !strings.Contains(o.Err.Error(), "not on the router") {
		t.Errorf("an absent account is reported as %v, which does not say it is absent", o.Err)
	}

	// And a row that lands in the WRONG GROUP is equally not applied: the
	// account exists, so a test asking only "is it there" would pass while the
	// operator got a privilege level nobody chose. The group is forced AFTER the
	// command's own arguments, or the fake would simply obey the write and this
	// would be testing nothing.
	g := mdSelf()
	g.onWrite = func(f *fakeRouter, c routeros.Cmd) {
		applying("read")(f, c)
		if c.Path == "/user/add" {
			f.users[len(f.users)-1]["group"] = "full"
		}
	}
	if o := Apply(g, readSpec(), []string{"mikrodash"}); o.State != StateUnknown {
		t.Errorf("an account that landed in the wrong group gave %s, want unknown", o.State)
	}
}

// TestRemovingTheLastFullUserIsRefusedBeforeItIsAttempted.
//
// MikroTik: "There always should be at least one user with full access rights.
// If the user with full access rights is the only one, it cannot be removed."
// The router would refuse it anyway; refusing here is what makes the reason
// legible instead of a bare router-denied.
func TestRemovingTheLastFullUserIsRefusedBeforeItIsAttempted(t *testing.T) {
	s := readSpec()
	s.Builtin = "full"
	mine := routeros.Reply{".id": "*7", "name": "noc", "group": "full",
		"comment": markerPrefix + " NOC Read-only [mdp:cp1]"}

	// MikroDash is in a custom group, so OUR profile's account is the router's
	// only full-access one.
	f := mdSelf()
	f.users = append(f.users[1:], mine) // drop `admin`
	o := Remove(f, s, []string{"mikrodash"})
	if o.State != StateOrphaned || o.Code != "last-full-user" {
		t.Errorf("removing the last full user gave %s/%s, want orphaned/last-full-user",
			o.State, o.Code)
	}
	if w := f.writes(); len(w) != 0 {
		t.Errorf("a doomed remove was still sent: %v", w)
	}

	// THE CONTROL: with another full user present it goes.
	g := mdSelf()
	g.users = append(g.users, mine)
	g.onWrite = func(f *fakeRouter, c routeros.Cmd) {
		if c.Path == "/user/remove" {
			f.users = f.users[:len(f.users)-1]
		}
	}
	if o := Remove(g, s, []string{"mikrodash"}); o.State != StateApplied {
		t.Errorf("a removable full user was refused: %s/%s %v", o.State, o.Code, o.Err)
	}
}

// TestRemovingAnAccountThatIsNoLongerOursLeavesItAlone.
//
// The marker is a fact on the device, so a row whose marker has gone - adopted,
// recreated by hand, restored from a backup taken before the link - is somebody
// else's account now. Removing it is a deletion MikroDash was never asked for.
func TestRemovingAnAccountThatIsNoLongerOursLeavesItAlone(t *testing.T) {
	f := mdSelf()
	f.users = append(f.users, routeros.Reply{".id": "*7", "name": "noc", "group": "read",
		"comment": "recreated by the night shift"})

	o := Remove(f, readSpec(), []string{"mikrodash"})
	if o.State != StateOrphaned || o.Code != "not-ours" {
		t.Errorf("an unmarked account gave %s/%s, want orphaned/not-ours", o.State, o.Code)
	}
	if w := f.writes(); len(w) != 0 {
		t.Errorf("an account that is not ours was written to: %v", w)
	}

	// AND AN ACCOUNT THAT IS ALREADY GONE IS NOT A FAILURE. Somebody deleting it
	// by hand reaches the desired state; reporting that as an error would leave
	// a link stuck in `orphaned` for ever with nothing to fix.
	g := mdSelf()
	if o := Remove(g, readSpec(), []string{"mikrodash"}); o.State != StateApplied {
		t.Errorf("removing an already-absent account gave %s/%s", o.State, o.Code)
	}
}

// TestBothSelfNamesAreProtected.
//
// ── THE LATENT GAP THIS FEATURE HAD TO CLOSE ────────────────────────────────
//
// `guard.ResolveSelf` accepts SEVERAL usernames and its comment says why: "the
// live connection can be logged in as one name while routers.json holds
// another, indefinitely. Both are protected." Every existing caller passes one.
//
// A background applier is exactly where that drift bites, because it runs
// without anybody watching, so this passes the live name, the stored name and
// ztp.UserName - and here proves the SECOND name is doing something, by
// protecting an account the live session is not signed in as.
func TestBothSelfNamesAreProtected(t *testing.T) {
	f := mdSelf()
	// The live session is `mikrodash`; routers.json still holds `md-old`, which
	// is also on the router, in a group of its own.
	f.users = append(f.users, routeros.Reply{".id": "*3", "name": "md-old", "group": "md-old-grp"})

	s := readSpec()
	s.Username = "md-old"

	// With BOTH names, the stored one is protected.
	if o := Apply(f, s, []string{"mikrodash", "md-old"}); o.State != StateRefused {
		t.Errorf("a profile claiming the stored username was applied: %s/%s", o.State, o.Code)
	}
	if w := f.writes(); len(w) != 0 {
		t.Errorf("a protected name was written: %v", w)
	}

	// THE CONTROL, and the point: with only the live name passed the guard has
	// never heard of `md-old` and lets it through. That is the gap, demonstrated
	// - so a future edit narrowing the call back to one name fails here.
	g := mdSelf()
	g.users = append(g.users, routeros.Reply{".id": "*3", "name": "md-old", "group": "md-old-grp"})
	g.onWrite = applying("read")
	if o := Apply(g, s, []string{"mikrodash"}); o.State == StateRefused {
		t.Error("the one-name control also refused, so this test proves nothing about " +
			"the second name")
	}
}

// TestTheGuardRunsBeforeAnyWrite reads the source.
//
// ── WHY A BEHAVIOURAL TEST CANNOT HOLD THIS ─────────────────────────────────
//
// Moving the guard call below the write is a one-line edit. Every test above
// still passes, because on a router where the guard says yes the outcome is
// identical - and on one where it says no, the damage is already done by the
// time the refusal is returned. The order IS the safety property, so the order
// is what is checked.
//
// `internal/server/dnsfleet_api.go` is the case this exists for: it records in
// prose that a guarded resource would lose its guard down that path, and prose
// fails nothing.
func TestTheGuardRunsBeforeAnyWrite(t *testing.T) {
	src, err := os.ReadFile("credprof.go")
	if err != nil {
		t.Fatal(err)
	}
	body := stripComments(string(src))

	for _, fn := range []string{"func Apply(", "func Remove(", "func ensureGroup("} {
		start := strings.Index(body, fn)
		if start < 0 {
			t.Fatalf("%s is gone from the source; this check can no longer see what it names", fn)
		}
		seg := body[start:]
		if end := strings.Index(seg[1:], "\nfunc "); end >= 0 {
			seg = seg[:end+1]
		}
		guardAt := indexOfAny(seg, "guard.CheckUser(", "guard.CheckGroup(")
		writeAt := indexOfAny(seg, `"/user/add"`, `"/user/set"`, `"/user/remove"`,
			`"/user/group/add"`, `"/user/group/set"`)
		if guardAt < 0 {
			t.Errorf("%s writes to /user but never calls a guard", fn)
			continue
		}
		if writeAt >= 0 && guardAt > writeAt {
			t.Errorf("%s writes to the router before asking the guard; a refusal would "+
				"then arrive after the damage", fn)
		}
	}

	// THE CONTROL: the scan can see a guard call and a write when they are
	// there. Without this the test passes just as well on a file it failed to
	// read, which is the shape of an anchor that silently stopped matching.
	if !strings.Contains(body, "guard.CheckUser(") || !strings.Contains(body, `"/user/add"`) {
		t.Fatal("the scan found neither a guard call nor a write; it is matching nothing")
	}
	// And it must not be reading its own prose - the comment above names both.
	if strings.Contains(body, "one-line edit") {
		t.Fatal("comments were not stripped, so this check can match its own explanation")
	}
}

func indexOfAny(s string, subs ...string) int {
	best := -1
	for _, sub := range subs {
		if i := strings.Index(s, sub); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

// stripComments removes // and /* */ comments, so a scan cannot match the prose
// explaining what it looks for. `isTestSource` exists in internal/verify for
// exactly this reason.
func stripComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "//"):
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return b.String()
			}
			i += j
		case strings.HasPrefix(s[i:], "/*"):
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return b.String()
			}
			i += j + 4
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// TestMarshalPoliciesNeverWritesNull: the column is NOT NULL, and a nil slice
// marshals to `null`. Same rule as TestNoPayloadSendsANullArray on the wire.
func TestMarshalPoliciesNeverWritesNull(t *testing.T) {
	if got := MarshalPolicies(nil); got != "[]" {
		t.Errorf("a nil policy set marshals to %q", got)
	}
	if got := MarshalPolicies([]string{}); got != "[]" {
		t.Errorf("an empty policy set marshals to %q", got)
	}
	if got := MarshalPolicies([]string{"read", "api"}); got != `["read","api"]` {
		t.Errorf("policies marshal to %q", got)
	}
	if got := ParsePolicies(`["read","api"]`); len(got) != 2 || got[0] != "read" {
		t.Errorf("policies parse to %v", got)
	}
}

// TestCheckSpecRefusesAPolicyRouterOSDoesNotHave: the enumerated-value rule
// CLAUDE.md records from dnsStatic, where a select offering six of nine record
// types rewrote an MX record as an A record on save. The vocabulary is
// `resource.UserPolicies`, which is also what the group form renders, so there
// is one list rather than two that can drift.
func TestCheckSpecRefusesAPolicyRouterOSDoesNotHave(t *testing.T) {
	s := readSpec()
	s.PermKind, s.Group, s.Policies = PermCustom, "noc-group", []string{"read", "superuser"}
	if err := CheckSpec(s); err == nil {
		t.Error("a policy outside RouterOS's vocabulary was accepted")
	}
	// THE CONTROL.
	s.Policies = []string{"read", "api"}
	if err := CheckSpec(s); err != nil {
		t.Errorf("a real policy set was refused: %v", err)
	}
	// A custom group must not shadow a built-in one; RouterOS refuses the add,
	// and that refusal would otherwise arrive per device rather than at save.
	s.Group = "write"
	if err := CheckSpec(s); err == nil {
		t.Error("a custom group named after a built-in one was accepted")
	}
	// And a profile with no password is refused, for RequiredOnCreate's reason.
	s.Group, s.Password = "noc-group", ""
	if err := CheckSpec(s); err == nil {
		t.Error("a profile with no password was accepted")
	}
}
