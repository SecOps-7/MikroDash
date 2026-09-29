package resource

import (
	"strings"
	"testing"
)

// TestGroupPolicyWritesEveryPolicyNegated. RouterOS removes a group policy only
// when it is named with `!`: `set policy=read` against a group holding
// read,test,api changes nothing (measured on RouterOS 7.24). So a write names all
// seventeen, and the value the engine keeps, diffs and replays on undo is the
// positive list.
func TestGroupPolicyWritesEveryPolicyNegated(t *testing.T) {
	v, errs := RosGroup.Validate(map[string]string{"name": "ops", "policy": "api, read,read,test"}, true)
	if len(errs) != 0 {
		t.Fatalf("a valid group was refused: %v", errs)
	}
	if got := v.Values["policy"]; got != "read,test,api" {
		t.Errorf("cleaned policy is %q, want the chosen set in vocabulary order", got)
	}
	var policyArg string
	for _, a := range RosGroup.BuildArgs(v) {
		if strings.HasPrefix(a, "=policy=") {
			policyArg = strings.TrimPrefix(a, "=policy=")
		}
	}
	words := strings.Split(policyArg, ",")
	if len(words) != len(UserPolicies) {
		t.Fatalf("the write names %d policies, want all %d: %s", len(words), len(UserPolicies), policyArg)
	}
	for i, p := range UserPolicies {
		want := "!" + p
		if p == "read" || p == "test" || p == "api" {
			want = p
		}
		if words[i] != want {
			t.Errorf("policy %d is %q, want %q", i, words[i], want)
		}
	}

	// Nothing chosen is a real request, a group with no permissions, and it
	// must negate everything rather than send nothing.
	v, errs = RosGroup.Validate(map[string]string{"name": "none", "policy": ""}, true)
	if len(errs) != 0 {
		t.Fatalf("an empty permission set was refused: %v", errs)
	}
	found := false
	for _, a := range RosGroup.BuildArgs(v) {
		if a == "=policy="+negateUnset(UserPolicies, "") {
			found = true
		}
	}
	if !found {
		t.Errorf("an empty set did not write every policy negated: %v", RosGroup.BuildArgs(v))
	}

	if _, errs := RosGroup.Validate(map[string]string{"name": "x", "policy": "read,superuser"}, false); len(errs) == 0 {
		t.Error("a policy outside the vocabulary was accepted")
	}
}

// TestGroupPolicyReadsBackAsTheGrantedSet: the router answers with every policy,
// the denied ones negated, and the form and the diff want what is granted.
func TestGroupPolicyReadsBackAsTheGrantedSet(t *testing.T) {
	row := map[string]string{"name": "ops",
		"policy": "!local,!telnet,ssh,!ftp,!reboot,read,!write,!policy,test,!winbox,!password,!web,!sniff,!sensitive,api,!romon,!rest-api"}
	got := RosGroup.RowValues(row)["policy"]
	if got != "ssh,read,test,api" {
		t.Errorf("policy read back as %q", got)
	}
	// And it round-trips: what is read back validates to itself, which is what
	// undo replays.
	v, errs := RosGroup.Validate(map[string]string{"name": "ops", "policy": got.(string)}, true)
	if len(errs) != 0 || v.Values["policy"] != got {
		t.Errorf("the read-back value does not validate to itself: %v %v", v.Values["policy"], errs)
	}
}

// TestRouterUserPasswordIsNeverReadOrPreviewed: a blank password leaves the
// stored one alone, and a set one is masked in the preview.
func TestRouterUserPasswordIsNeverReadOrPreviewed(t *testing.T) {
	if _, ok := RosUser.RowValues(map[string]string{"name": "a", "password": "hunter2"})["password"]; ok {
		t.Error("a password was read back into the form values")
	}
	v, _ := RosUser.Validate(map[string]string{"name": "a", "group": "read", "password": ""}, true)
	for _, a := range RosUser.BuildArgs(v) {
		if strings.HasPrefix(a, "=password=") {
			t.Errorf("a blank password was written: %s", a)
		}
	}
	v, _ = RosUser.Validate(map[string]string{"name": "a", "group": "read", "password": "s3cret-value"}, false)
	if cmd := RosUser.PreviewCommand(v, ""); strings.Contains(cmd, "s3cret-value") {
		t.Errorf("the preview shows the password: %s", cmd)
	}
}

// TestClearingAFieldSendsWhatTheMenuMeansByNothing.
//
// Measured on the CHR through the generated IP Pools page: clearing Next Pool
// sent `=next-pool=` and the router refused it — "ambiguous value of next-pool,
// more than one possible value matches input" — because that menu's word for no
// next pool is `none`. An empty value is right for a comment and wrong for a
// field carrying a sentinel, so the field declares which it is.
func TestClearingAFieldSendsWhatTheMenuMeansByNothing(t *testing.T) {
	v, errs := IPPool.Validate(map[string]string{
		"name": "dhcp", "ranges": "10.0.0.10-10.0.0.20"}, true)
	if len(errs) != 0 {
		t.Fatalf("a valid pool was refused: %v", errs)
	}
	var next, comment string
	for _, a := range IPPool.BuildArgs(v) {
		if strings.HasPrefix(a, "=next-pool=") {
			next = a
		}
		if strings.HasPrefix(a, "=comment=") {
			comment = a
		}
	}
	if next != "=next-pool=none" {
		t.Errorf("clearing next-pool sends %q, want =next-pool=none", next)
	}
	// THE CONTROL: a field with no sentinel still clears to empty, which is what
	// every other clearable field in the registry relies on.
	if comment != "=comment=" {
		t.Errorf("clearing the comment sends %q, want =comment=", comment)
	}
	// And a CREATE sends neither: an omitted property keeps RouterOS's default.
	create, _ := IPPool.Validate(map[string]string{
		"name": "dhcp", "ranges": "10.0.0.10-10.0.0.20"}, false)
	for _, a := range IPPool.BuildArgs(create) {
		if strings.HasPrefix(a, "=next-pool=") || strings.HasPrefix(a, "=comment=") {
			t.Errorf("a create sends %q for a field nobody filled in", a)
		}
	}
}

// TestCreatingARouterUserDemandsAPassword.
//
// ── THE BUG, AND WHY NOTHING CAUGHT IT ──────────────────────────────────────
//
// BuildArgs drops a blank TypeSecret unconditionally, which is right on a `set`
// and wrong on an `add`: MikroTik documents that a /user added without the
// property "is left blank (hit Enter when logging in)". So Add with an empty
// password box created a RouterOS account anyone could sign in to. The write
// succeeded, the row appeared, and every test here passed — the suite only ever
// asked what a blank password does on an EDIT, where the answer is correct.
//
// `Required` could not express it: Validate applies that in both directions, so
// it would have refused every edit that did not retype the password. The split
// is RequiredOnCreate.
func TestCreatingARouterUserDemandsAPassword(t *testing.T) {
	blank := map[string]string{"name": "noc", "group": "read", "password": ""}

	v, errs := RosUser.Validate(blank, false)
	if len(errs) == 0 {
		t.Errorf("a create with no password was accepted, and would have sent %v", RosUser.BuildArgs(v))
	}
	named := false
	for _, e := range errs {
		if e.Field == "password" {
			named = true
		}
	}
	if !named {
		t.Errorf("the refusal does not name the password field: %v", errs)
	}

	// ── THE CONTROL, in both directions ──────────────────────────────────────
	//
	// Without these the test passes for a rule that refuses everything.

	// An EDIT still takes a blank, which is the whole reason Required is wrong
	// here: it is how an operator changes a comment without retyping a password.
	if _, errs := RosUser.Validate(blank, true); len(errs) != 0 {
		t.Errorf("an edit with a blank password was refused: %v", errs)
	}
	// And a create WITH one goes through, and writes it.
	filled, errs := RosUser.Validate(map[string]string{
		"name": "noc", "group": "read", "password": "a-real-password"}, false)
	if len(errs) != 0 {
		t.Fatalf("a create with a password was refused: %v", errs)
	}
	wrote := false
	for _, a := range RosUser.BuildArgs(filled) {
		if a == "=password=a-real-password" {
			wrote = true
		}
	}
	if !wrote {
		t.Errorf("the password was not written: %v", RosUser.BuildArgs(filled))
	}
}

// TestRequiredOnCreateIsOnlyForFieldsThatAreNeverReadBack.
//
// A field the router DOES report can use plain `Required`, because the form is
// populated from the row and the operator is not being asked to retype
// anything. RequiredOnCreate exists for the fields RowValues drops — a secret,
// or a WriteOnly field — where a blank edit is an omission rather than a value.
// Declaring it on a readable field would be a rule with no mechanism behind it.
func TestRequiredOnCreateIsOnlyForFieldsThatAreNeverReadBack(t *testing.T) {
	seen := 0
	for _, r := range All() {
		for _, f := range r.Fields {
			if !f.RequiredOnCreate {
				continue
			}
			seen++
			if !f.Unread() {
				t.Errorf("%s.%s is RequiredOnCreate but is read back from the router; use Required",
					r.Key, f.Name)
			}
			if f.Required {
				t.Errorf("%s.%s declares both Required and RequiredOnCreate", r.Key, f.Name)
			}
		}
	}
	// THE LEDGER'S OTHER DIRECTION: a mechanism with no instances is one nobody
	// re-measures. If the last RequiredOnCreate is removed, this says so rather
	// than passing over an empty registry.
	if seen == 0 {
		t.Error("no field declares RequiredOnCreate; delete the flag or restore the declaration")
	}
}

// TestDeletingARouterUserOffersNoUndo.
//
// The undo stack holds RowValues(row), which drops every Unread field — so a
// deleted user's password was never stored, because the router never sent it.
// Replaying the delete as an `add` would submit the row without one, which is
// the passwordless create RequiredOnCreate refuses. The button would have said
// "undo" and then failed at the router with a field error about a box the
// operator never filled in.
func TestDeletingARouterUserOffersNoUndo(t *testing.T) {
	if RosUser.UndoesRemoval() {
		t.Error("deleting a router user offers an undo that cannot restore its password")
	}
	// THE CONTROL: the sibling menu on the same page, with no secret, still
	// undoes. Without this the test passes for a rule that refuses everything.
	if !RosGroup.UndoesRemoval() {
		t.Error("deleting a user group no longer offers an undo")
	}
}
