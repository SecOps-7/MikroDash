package loginprof

import (
	"strings"
	"testing"

	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
	"mikrodash/internal/store"
	"mikrodash/internal/ztp"
)

type fake struct {
	users, groups, active []routeros.Reply
	sent                  []string
}

func (f *fake) Exec(c routeros.Cmd) ([]routeros.Reply, error) {
	switch c.Path {
	case "/user/print":
		return f.users, nil
	case "/user/group/print":
		return f.groups, nil
	case "/user/active/print":
		return f.active, nil
	}
	f.sent = append(f.sent, c.Path+" "+strings.Join(c.Args, " "))
	if c.Path == "/user/add" {
		f.users = append(f.users, routeros.Reply{".id": "*99", "name": "MikroDash"})
	}
	return nil, nil
}

func base(live string) *fake {
	return &fake{
		users:  []routeros.Reply{{".id": "*1", "name": live, "group": "full"}},
		groups: []routeros.Reply{{".id": "*g1", "name": "full"}},
		active: []routeros.Reply{{"name": live, "via": "api"}},
	}
}

// A ROUTER WITHOUT THE ACCOUNT gets the group (every policy) and the user, and
// Undo takes both off again.
func TestPutCreatesAndUndoRemoves(t *testing.T) {
	f := base("admin")
	c, err := Put(f, "Fleet", "a-long-fleet-password", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !c.GroupAdded || !c.UserAdded || c.UserID != "*99" || c.WasSelf {
		t.Errorf("change = %+v", c)
	}
	all := strings.Join(f.sent, "\n")
	if !strings.Contains(all, "/user/group/add") || !strings.Contains(all, "policy=local,telnet") {
		t.Errorf("the group was not created with every policy:\n%s", all)
	}
	if !strings.Contains(all, "/user/add =name=MikroDash =group=MikroDash =password=a-long-fleet-password") {
		t.Errorf("the account was not created in the group:\n%s", all)
	}
	f.groups = append(f.groups, routeros.Reply{".id": "*g9", "name": "MikroDash"})
	f.sent = nil
	if err := Undo(f, c, "unused"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.sent, "\n"); !strings.Contains(got, "/user/remove =.id=*99") ||
		!strings.Contains(got, "/user/group/remove =.id=*g9") {
		t.Errorf("Undo left something behind:\n%s", got)
	}
}

// OUR OWN ACCOUNT: Undo puts the OLD PASSWORD back, or MikroDash is locked out
// at its next connect.
func TestUndoRestoresOurOwnPassword(t *testing.T) {
	f := base("MikroDash")
	f.groups = append(f.groups, routeros.Reply{".id": "*g9", "name": "MikroDash"})
	c, err := Put(f, "Fleet", "a-long-fleet-password", "MikroDash")
	if err != nil {
		t.Fatal(err)
	}
	if c.GroupAdded || c.UserAdded || !c.WasSelf || c.PrevGroup != "full" {
		t.Errorf("change = %+v", c)
	}
	f.sent = nil
	if err := Undo(f, c, "the-old-password"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.sent, "\n"); got != "/user/set =.id=*1 =group=full =password=the-old-password" {
		t.Errorf("Undo sent %q, want the old group and password back on our account", got)
	}
}

// `mikrodash` IS SOMEBODY ELSE: RouterOS names are case-sensitive.
func TestALowerCaseMikrodashIsNotOurAccount(t *testing.T) {
	f := base("mikrodash")
	c, err := Put(f, "Fleet", "a-long-fleet-password", "mikrodash")
	if err != nil {
		t.Fatal(err)
	}
	if !c.UserAdded {
		t.Error("an existing `mikrodash` was taken for `MikroDash`")
	}
	for _, s := range f.sent {
		if strings.Contains(s, "=.id=*1") {
			t.Errorf("the lower-case account was written: %s", s)
		}
	}
}

// NOT KNOWING WHICH ACCOUNT IS OURS IS A REFUSAL, before any write.
func TestPutRefusesWhenSelfIsUnknown(t *testing.T) {
	f := base("admin")
	f.users, f.active = nil, nil
	if _, err := Put(f, "Fleet", "a-long-fleet-password", "admin"); err == nil {
		t.Fatal("Put wrote without knowing which account is ours")
	}
	if len(f.sent) != 0 {
		t.Errorf("writes before the refusal: %v", f.sent)
	}
}

// ONE ACCOUNT ACROSS THE FLEET: zero-touch provisioning creates the same user,
// in the same group, with the same policies, as a login profile does - the
// operator's decision. Three constants in three packages, held equal here.
func TestZTPAndLoginProfilesNameOneAccount(t *testing.T) {
	if ztp.UserName != store.LoginUserName || ztp.GroupName != store.LoginGroupName {
		t.Errorf("ZTP creates %s/%s, a login profile %s/%s", ztp.UserName, ztp.GroupName,
			store.LoginUserName, store.LoginGroupName)
	}
	if strings.Join(ztp.GroupPolicies, ",") != strings.Join(resource.UserPolicies, ",") {
		t.Errorf("ZTP's group policies %v differ from the vocabulary %v", ztp.GroupPolicies, resource.UserPolicies)
	}
}
