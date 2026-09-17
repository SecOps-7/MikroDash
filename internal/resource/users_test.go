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
