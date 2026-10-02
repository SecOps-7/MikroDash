package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loginStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	own, err := st.Encrypt("own-password-1")
	if err != nil {
		t.Fatal(err)
	}
	src := `[{"id":"a","label":"Alpha","host":"198.51.100.1","username":"admin","password":"` + own + `"},
	         {"id":"b","label":"Bravo","host":"198.51.100.2","username":"admin","password":"` + own + `"}]`
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return st
}

func routerByID(t *testing.T, st *Store, id string) Router {
	t.Helper()
	rs, _ := st.Routers()
	for _, r := range rs {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no router %s", id)
	return Router{}
}

// A LINKED ROUTER SIGNS IN WITH THE PROFILE, resolved where every reader reads,
// and its own credential is cleared so nothing stale is left at rest.
func TestARouterUsingALoginProfileSignsInWithIt(t *testing.T) {
	st := loginStore(t)
	p, err := st.AddLoginProfile("Fleet", "a-long-fleet-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UseLoginProfile("a", p.ID); err != nil {
		t.Fatal(err)
	}
	a := routerByID(t, st, "a")
	if a.Username != LoginUserName || a.Password != "a-long-fleet-password" || a.LoginProfileName != "Fleet" {
		t.Errorf("router a reads %q / %q via %q, want the profile's login", a.Username, a.Password, a.LoginProfileName)
	}
	if a.Encrypted != "" {
		t.Error("the router's own sealed password was left behind after switching to a profile")
	}
	// THE CONTROL: the router that was not linked keeps its own login.
	if b := routerByID(t, st, "b"); b.Username != "admin" || b.Password != "own-password-1" {
		t.Errorf("router b reads %q / %q, want its own login", b.Username, b.Password)
	}
	// A new password is what the router then reads.
	if err := st.SetLoginProfilePassword(p.ID, "another-long-password"); err != nil {
		t.Fatal(err)
	}
	if a := routerByID(t, st, "a"); a.Password != "another-long-password" {
		t.Errorf("after the profile's password changed router a reads %q", a.Password)
	}
}

// THE USERNAME IS FIXED: a hand-edited file cannot make a profile sign in as
// anybody else, and the password is never written in clear.
func TestALoginProfileAlwaysSignsInAsMikroDash(t *testing.T) {
	st := loginStore(t)
	p, _ := st.AddLoginProfile("Fleet", "a-long-fleet-password")
	_ = st.UseLoginProfile("a", p.ID)
	path := filepath.Join(st.Dir, loginProfilesFile)
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "a-long-fleet-password") {
		t.Fatal("the profile's password is in the file in clear")
	}
	_ = os.WriteFile(path, []byte(strings.Replace(string(b), `"username": "mikrodash"`, `"username": "admin"`, 1)), 0o600)
	if a := routerByID(t, st, "a"); a.Username != LoginUserName {
		t.Errorf("an edited file made the profile sign in as %q", a.Username)
	}
}

// A MISSING PROFILE LEAVES NO CREDENTIAL, and says so, rather than signing in
// with something nobody chose.
func TestAMissingLoginProfileLeavesNoCredential(t *testing.T) {
	st := loginStore(t)
	if err := st.UpdateRouter("a", map[string]any{"loginProfileId": "gone", "password": ""}); err != nil {
		t.Fatal(err)
	}
	rs, problems := st.Routers()
	if len(rs) != 2 {
		t.Fatalf("one dangling profile hid the fleet: %d routers", len(rs))
	}
	if a := routerByID(t, st, "a"); a.Password != "" || a.Username != "" {
		t.Errorf("a router whose profile is gone signs in as %q / %q", a.Username, a.Password)
	}
	if len(problems) == 0 || !strings.Contains(problems[0].Error(), "does not exist") {
		t.Errorf("the dangling profile was not reported: %v", problems)
	}
}

// IN USE CANNOT BE DELETED, and names and passwords are checked.
func TestLoginProfileRules(t *testing.T) {
	st := loginStore(t)
	p, _ := st.AddLoginProfile("Fleet", "a-long-fleet-password")
	_ = st.UseLoginProfile("a", p.ID)
	if err := st.DeleteLoginProfile(p.ID); !errors.Is(err, ErrLoginProfileInUse) {
		t.Errorf("deleting a profile in use = %v, want ErrLoginProfileInUse", err)
	}
	if _, err := st.AddLoginProfile("fleet", "a-long-fleet-password"); !errors.Is(err, ErrLoginProfileInvalid) {
		t.Errorf("a duplicate name (other case) = %v", err)
	}
	if _, err := st.AddLoginProfile("Short", "short"); !errors.Is(err, ErrLoginProfileInvalid) {
		t.Errorf("an 5-character password = %v", err)
	}
	if err := st.UseOwnLogin("a", "admin", ""); !errors.Is(err, ErrLoginProfileInvalid) {
		t.Errorf("switching to an own login with no password = %v", err)
	}
	if err := st.UseOwnLogin("a", "admin", "own-password-2"); err != nil {
		t.Fatal(err)
	}
	if a := routerByID(t, st, "a"); a.LoginProfileID != "" || a.Password != "own-password-2" {
		t.Errorf("after switching back router a has profile %q and password %q", a.LoginProfileID, a.Password)
	}
	// THE CONTROL: no longer in use, so it goes.
	if err := st.DeleteLoginProfile(p.ID); err != nil {
		t.Errorf("deleting an unused profile: %v", err)
	}
}
