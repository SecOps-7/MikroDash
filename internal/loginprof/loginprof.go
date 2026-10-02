// Package loginprof puts a login profile's account on one router: the group
// and user MikroDash itself signs in with, both called `mikrodash`
// (store.LoginUserName / store.LoginGroupName).
//
// ── THIS WRITES MIKRODASH'S OWN ACCOUNT, ON PURPOSE ────────────────────────
//
// Everything else that writes `/user` refuses to touch the account MikroDash
// signs in with (`internal/guard/selfguard.go`), because breaking that login is
// unrecoverable from inside the app. This package exists to CHANGE that login,
// so the guard's refusal cannot be its safety. Its safety is the sequence the
// server runs around it:
//
//  1. Put writes the account through the router's current, working login.
//  2. The server signs in AFRESH with the new credential and reads the router.
//  3. Only if that works does the router record switch to the profile.
//  4. If it does not, Undo puts back exactly what Put changed - the old
//     password on our own account, or removes what Put created - through the
//     same still-open connection, which RouterOS does not drop when the
//     password of its account changes.
//
// The guard is still consulted - `guard.ResolveSelf` - to know WHETHER the
// account being written is the one we are signed in as, because that decides
// what Undo must restore.
//
// Pure in the sense the guards are: commands in, rows out, through an Execer,
// so every path including Undo is tested against a scripted router.
package loginprof

import (
	"errors"
	"fmt"
	"strings"

	"mikrodash/internal/guard"
	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
	"mikrodash/internal/store"
)

// Execer runs one RouterOS command. `internal/session`'s Exec satisfies it.
type Execer interface {
	Exec(routeros.Cmd) ([]routeros.Reply, error)
}

// Comment marks the account and group as a login profile's, on the device.
func Comment(profileName string) string {
	return "MikroDash login profile: " + profileName
}

// Change is what Put did, which is what Undo needs.
type Change struct {
	GroupAdded bool
	UserAdded  bool
	// UserID is the account's `.id`.
	UserID string
	// PrevGroup is the account's group before Put moved it, when it existed.
	PrevGroup string
	// WasSelf: the account written is the one the connection is signed in as.
	// Its old password is then known (it is the router's current credential),
	// and Undo must put it back or MikroDash is locked out at the next connect.
	WasSelf bool
}

// Put makes the group and user exist, with the profile's password.
//
// A GROUP THAT IS ALREADY THERE IS LEFT AS IT IS. Creating one grants every
// RouterOS policy - the operator's choice, so the account is the same on every
// device whether it was added by hand or by zero-touch provisioning, which
// creates the same group. An existing one belongs to whoever made it, and if it
// lacks something MikroDash needs, the sign-in check after Put fails and Undo
// runs.
func Put(ex Execer, profileName, password, liveUser string) (Change, error) {
	var c Change
	users, err := ex.Exec(routeros.Cmd{Path: "/user/print"})
	if err != nil {
		return c, fmt.Errorf("reading /user: %w", err)
	}
	groups, err := ex.Exec(routeros.Cmd{Path: "/user/group/print"})
	if err != nil {
		return c, fmt.Errorf("reading /user/group: %w", err)
	}
	active, err := ex.Exec(routeros.Cmd{Path: "/user/active/print"})
	if err != nil {
		return c, fmt.Errorf("reading /user/active: %w", err)
	}
	self := guard.ResolveSelf(users, active, []string{liveUser})
	if !self.Resolved {
		// FAIL CLOSED, as every other /user writer does: if we cannot tell which
		// account is ours we cannot tell what Undo would have to restore.
		return c, errors.New("MikroDash cannot identify its own account on this router")
	}

	if exact(groups, store.LoginGroupName) == nil {
		v, errs := resource.RosGroup.Validate(map[string]string{
			"name":    store.LoginGroupName,
			"policy":  strings.Join(resource.UserPolicies, ","),
			"comment": Comment(profileName),
		}, false)
		if len(errs) > 0 {
			return c, fmt.Errorf("%s: %s", errs[0].Field, errs[0].Message)
		}
		if _, err := ex.Exec(routeros.Cmd{Path: "/user/group/add", Args: resource.RosGroup.BuildArgs(v)}); err != nil {
			return c, fmt.Errorf("creating group %s: %w", store.LoginGroupName, err)
		}
		c.GroupAdded = true
	}

	vals := []string{"=group=" + store.LoginGroupName, "=password=" + password,
		"=comment=" + Comment(profileName), "=disabled=no"}
	if u := exact(users, store.LoginUserName); u != nil {
		c.UserID, c.PrevGroup = u[".id"], u["group"]
		c.WasSelf = liveUser == store.LoginUserName
		if _, err := ex.Exec(routeros.Cmd{Path: "/user/set",
			Args: append([]string{"=.id=" + u[".id"]}, vals...)}); err != nil {
			return c, fmt.Errorf("updating user %s: %w", store.LoginUserName, err)
		}
	} else {
		if _, err := ex.Exec(routeros.Cmd{Path: "/user/add",
			Args: append([]string{"=name=" + store.LoginUserName}, vals...)}); err != nil {
			return c, fmt.Errorf("creating user %s: %w", store.LoginUserName, err)
		}
		c.UserAdded = true
		after, err := ex.Exec(routeros.Cmd{Path: "/user/print"})
		if err != nil {
			return c, fmt.Errorf("reading /user back: %w", err)
		}
		u := exact(after, store.LoginUserName)
		if u == nil {
			return c, errors.New("the account was accepted but is not on the router")
		}
		c.UserID = u[".id"]
	}
	return c, nil
}

// Undo reverses Put. `oldPassword` is the router's current credential, used
// when the account written was our own.
func Undo(ex Execer, c Change, oldPassword string) error {
	var errs []string
	switch {
	case c.UserAdded && c.UserID != "":
		if _, err := ex.Exec(routeros.Cmd{Path: "/user/remove", Args: []string{"=.id=" + c.UserID}}); err != nil {
			errs = append(errs, "removing the new account: "+err.Error())
		}
	case c.UserID != "":
		args := []string{"=.id=" + c.UserID}
		if c.PrevGroup != "" {
			args = append(args, "=group="+c.PrevGroup)
		}
		if c.WasSelf {
			args = append(args, "=password="+oldPassword)
		}
		if len(args) > 1 {
			if _, err := ex.Exec(routeros.Cmd{Path: "/user/set", Args: args}); err != nil {
				errs = append(errs, "restoring the account: "+err.Error())
			}
		}
	}
	if c.GroupAdded {
		groups, err := ex.Exec(routeros.Cmd{Path: "/user/group/print"})
		if err == nil {
			if g := exact(groups, store.LoginGroupName); g != nil {
				if _, err := ex.Exec(routeros.Cmd{Path: "/user/group/remove", Args: []string{"=.id=" + g[".id"]}}); err != nil {
					errs = append(errs, "removing the new group: "+err.Error())
				}
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// SetPassword changes the account's password, for a profile password change
// on a router already signed in as it. Undo is the same call with the old one.
func SetPassword(ex Execer, password string) error {
	users, err := ex.Exec(routeros.Cmd{Path: "/user/print"})
	if err != nil {
		return fmt.Errorf("reading /user: %w", err)
	}
	u := exact(users, store.LoginUserName)
	if u == nil {
		return fmt.Errorf("there is no %s account on this router", store.LoginUserName)
	}
	_, err = ex.Exec(routeros.Cmd{Path: "/user/set", Args: []string{"=.id=" + u[".id"], "=password=" + password}})
	return err
}

// exact finds a row by name, CASE-SENSITIVELY: RouterOS user and group names
// are (measured, see store.LoginUserName), so `mikrodash` is somebody else.
func exact(rows []routeros.Reply, name string) routeros.Reply {
	for _, r := range rows {
		if r["name"] == name {
			return r
		}
	}
	return nil
}
