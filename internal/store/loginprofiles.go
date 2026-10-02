package store

// Login profiles: one username and password that many routers sign in with.
//
// ── WHY HERE, AND NOT BESIDE THE CREDENTIAL PROFILES IN THE DATABASE ────────
//
// The credential profiles (#143, `internal/db/credprof.go`) are accounts
// MikroDash PUTS ON routers for people. A login profile is how MikroDash
// REACHES a router, which is `routers.json`'s business: it has to be readable
// wherever a router record is - the server, `cmd/conformance` and `cmd/compat`
// all open /data through this package and nothing else - and it must travel
// with routers.json when /data is copied. So it is a sibling file, sealed with
// the same envelope as each router's own password.
//
// ── RESOLVED AT READ, IN ONE PLACE ─────────────────────────────────────────
//
// A router record holds `loginProfileId` and nothing else about the profile.
// `Routers()` fills Username and Password from the profile, so the fifty-odd
// readers of a router's credential need no change and cannot disagree about
// which one a router uses.
//
// ── THE FILE IS A BARE ARRAY, NEW AND ABSENT ON EVERY EXISTING INSTALL ──────
//
// Absent is "no profiles", which is every install before this file existed.

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const loginProfilesFile = "login-profiles.json"

// LoginUserName and LoginGroupName are the RouterOS account and group a login
// profile signs in as - FIXED, by the operator's decision, so every device in
// the fleet carries the same account whether it was added by hand or by
// zero-touch provisioning (`internal/ztp` uses the same two names).
//
// LOWER CASE, and so the SAME account the README has operators create by hand
// (operator's decision, 2026-10-02, replacing `MikroDash` the same day). On a
// router already set up that way, linking adopts that account and its group.
// RouterOS user names are case-sensitive - measured on RouterOS 7.24 (CHR):
// `ZzCaseProbe` and `zzcaseprobe` were two accounts - so the case matters.
const (
	LoginUserName  = "mikrodash"
	LoginGroupName = "mikrodash"
)

// LoginProfile is one stored login.
type LoginProfile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Username is always LoginUserName. Stored so the file reads plainly, and
	// IGNORED on read: a hand-edited file cannot make a profile sign in as
	// somebody else.
	Username string `json:"username"`
	// Encrypted is what the file holds, under the same `password` key and the
	// same envelope as a router's own credential.
	Encrypted string `json:"password"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	// Password is the DECRYPTED value, never serialised.
	Password string `json:"-"`
}

// resolvedLogin is a profile as `Routers()` applies it.
type resolvedLogin struct {
	Name, Username, Password string
	err                      error
}

// ErrLoginProfileInUse refuses deleting a profile that routers still sign in
// with: deleting it would leave each of them with no credential at all.
var ErrLoginProfileInUse = errors.New("routers still sign in with this login profile")

// ErrLoginProfileInvalid wraps a validation refusal, so the API can answer 400.
var ErrLoginProfileInvalid = errors.New("invalid login profile")

func (s *Store) loginProfilesPath() string { return filepath.Join(s.Dir, loginProfilesFile) }

// readLoginProfiles reads the file without decrypting.
func (s *Store) readLoginProfiles() ([]LoginProfile, error) {
	b, missing, err := readIfPresent(s.loginProfilesPath())
	if err != nil {
		return nil, err
	}
	if missing {
		return []LoginProfile{}, nil
	}
	var out []LoginProfile
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("store: %s: %w", loginProfilesFile, err)
	}
	if out == nil {
		out = []LoginProfile{}
	}
	return out, nil
}

// LoginProfiles reads every profile and decrypts its password. A password that
// fails to decrypt is reported and left empty, as `Routers()` does for a
// router's own: one bad record must not hide the rest.
func (s *Store) LoginProfiles() ([]LoginProfile, []error) {
	out, err := s.readLoginProfiles()
	if err != nil {
		return nil, []error{err}
	}
	var problems []error
	for i := range out {
		plain, err := s.Decrypt(out[i].Encrypted)
		if err != nil {
			problems = append(problems, fmt.Errorf("login profile %s: %w", out[i].Name, err))
			continue
		}
		out[i].Password = plain
		out[i].Username = LoginUserName
	}
	return out, problems
}

// loginProfileMap is what `Routers()` resolves against.
func (s *Store) loginProfileMap() (map[string]resolvedLogin, error) {
	list, err := s.readLoginProfiles()
	if err != nil {
		return nil, err
	}
	m := make(map[string]resolvedLogin, len(list))
	for _, p := range list {
		plain, derr := s.Decrypt(p.Encrypted)
		m[p.ID] = resolvedLogin{Name: p.Name, Username: LoginUserName, Password: plain, err: derr}
	}
	return m, nil
}

func (s *Store) writeLoginProfiles(list []LoginProfile) error {
	b, err := encodeDataFile(list)
	if err != nil {
		return err
	}
	return writeAtomic(s.loginProfilesPath(), b)
}

// validLoginName checks a profile name, and that it is unique.
func validLoginName(list []LoginProfile, selfID, name string) error {
	if name == "" {
		return fmt.Errorf("%w: a name is required", ErrLoginProfileInvalid)
	}
	if len(name) > 64 {
		return fmt.Errorf("%w: the name is too long", ErrLoginProfileInvalid)
	}
	for _, p := range list {
		if p.ID != selfID && strings.EqualFold(p.Name, name) {
			return fmt.Errorf("%w: a profile called %q already exists", ErrLoginProfileInvalid, p.Name)
		}
	}
	return nil
}

// validLoginPassword is the floor a fleet-wide password must clear. It signs
// in as an account in a group holding every policy, on every linked device.
func validLoginPassword(pw string) error {
	if len(pw) < 12 {
		return fmt.Errorf("%w: the password needs at least 12 characters", ErrLoginProfileInvalid)
	}
	if strings.ContainsAny(pw, "\r\n\x00") {
		return fmt.Errorf("%w: the password may not contain a line break", ErrLoginProfileInvalid)
	}
	return nil
}

// ValidLoginPassword is validLoginPassword for the API, which checks a new
// password before pushing it to any router.
func ValidLoginPassword(pw string) error { return validLoginPassword(pw) }

// AddLoginProfile creates a profile. Nothing is written to any router: that
// happens when a device is linked.
func (s *Store) AddLoginProfile(name, password string) (LoginProfile, error) {
	name = strings.TrimSpace(name)
	list, err := s.readLoginProfiles()
	if err != nil {
		return LoginProfile{}, err
	}
	if err := validLoginName(list, "", name); err != nil {
		return LoginProfile{}, err
	}
	if err := validLoginPassword(password); err != nil {
		return LoginProfile{}, err
	}
	sealed, err := s.Encrypt(password)
	if err != nil {
		return LoginProfile{}, err
	}
	id, err := newUUID()
	if err != nil {
		return LoginProfile{}, err
	}
	now := time.Now().UnixMilli()
	p := LoginProfile{ID: id, Name: name, Username: LoginUserName, Encrypted: sealed,
		CreatedAt: now, UpdatedAt: now}
	if err := s.writeLoginProfiles(append(list, p)); err != nil {
		return LoginProfile{}, err
	}
	p.Password = password
	return p, nil
}

// LoginProfile reads one profile, decrypted.
func (s *Store) LoginProfile(id string) (LoginProfile, error) {
	list, _ := s.LoginProfiles()
	for _, p := range list {
		if p.ID == id {
			if p.Password == "" {
				return p, fmt.Errorf("store: login profile %q cannot be opened", p.Name)
			}
			return p, nil
		}
	}
	return LoginProfile{}, fmt.Errorf("store: no login profile %s", id)
}

// RenameLoginProfile changes a profile's name and nothing else.
func (s *Store) RenameLoginProfile(id, name string) error {
	name = strings.TrimSpace(name)
	list, err := s.readLoginProfiles()
	if err != nil {
		return err
	}
	if err := validLoginName(list, id, name); err != nil {
		return err
	}
	for i := range list {
		if list[i].ID == id {
			list[i].Name = name
			list[i].UpdatedAt = time.Now().UnixMilli()
			return s.writeLoginProfiles(list)
		}
	}
	return fmt.Errorf("store: no login profile %s", id)
}

// SetLoginProfilePassword stores a new password.
//
// ── CALLED ONLY AFTER EVERY LINKED ROUTER HOLDS IT ─────────────────────────
//
// The routers using this profile read their password from here, so changing
// it first would have every one of them sign in with a password the router
// does not yet have. The server pushes the new password to each router,
// confirms each by signing in with it, and only then calls this.
func (s *Store) SetLoginProfilePassword(id, password string) error {
	if err := validLoginPassword(password); err != nil {
		return err
	}
	list, err := s.readLoginProfiles()
	if err != nil {
		return err
	}
	for i := range list {
		if list[i].ID == id {
			sealed, err := s.Encrypt(password)
			if err != nil {
				return err
			}
			list[i].Encrypted = sealed
			list[i].UpdatedAt = time.Now().UnixMilli()
			return s.writeLoginProfiles(list)
		}
	}
	return fmt.Errorf("store: no login profile %s", id)
}

// LoginProfileUsers is the ids of the routers that sign in with a profile,
// read from the raw file so a router record this binary cannot decode fully
// still counts.
func (s *Store) LoginProfileUsers(id string) ([]string, error) {
	b, missing, err := readIfPresent(filepath.Join(s.Dir, "routers.json"))
	if err != nil || missing {
		return []string{}, err
	}
	var recs []struct {
		ID             string `json:"id"`
		LoginProfileID string `json:"loginProfileId"`
	}
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, fmt.Errorf("store: routers.json: %w", err)
	}
	out := []string{}
	for _, r := range recs {
		if r.LoginProfileID == id {
			out = append(out, r.ID)
		}
	}
	return out, nil
}

// DeleteLoginProfile removes a profile nobody signs in with.
func (s *Store) DeleteLoginProfile(id string) error {
	users, err := s.LoginProfileUsers(id)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return fmt.Errorf("%w (%d)", ErrLoginProfileInUse, len(users))
	}
	list, err := s.readLoginProfiles()
	if err != nil {
		return err
	}
	keep := list[:0]
	found := false
	for _, p := range list {
		if p.ID == id {
			found = true
			continue
		}
		keep = append(keep, p)
	}
	if !found {
		return fmt.Errorf("store: no login profile %s", id)
	}
	return s.writeLoginProfiles(keep)
}

// UseLoginProfile points a router at a profile and CLEARS its own credential:
// one source, and no stored password left behind that nothing uses. An empty
// profileID is refused here; switching back to an own login is
// `UseOwnLogin`, which needs the credential to switch to.
func (s *Store) UseLoginProfile(routerID, profileID string) error {
	list, err := s.readLoginProfiles()
	if err != nil {
		return err
	}
	found := false
	for _, p := range list {
		if p.ID == profileID {
			found = true
		}
	}
	if profileID == "" || !found {
		return fmt.Errorf("%w: no such login profile", ErrLoginProfileInvalid)
	}
	return s.UpdateRouter(routerID, map[string]any{
		"loginProfileId": profileID, "username": "", "password": "",
	})
}

// UseOwnLogin switches a router back to its own username and password.
func (s *Store) UseOwnLogin(routerID, username, password string) error {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return fmt.Errorf("%w: a username and password are required to sign in without a profile",
			ErrLoginProfileInvalid)
	}
	sealed, err := s.Encrypt(password)
	if err != nil {
		return err
	}
	return s.UpdateRouter(routerID, map[string]any{
		"loginProfileId": nil, "username": username, "password": sealed,
	})
}
