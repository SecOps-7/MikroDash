package cfgdeploy

import (
	"fmt"
	"strconv"
	"time"

	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/routeros"
)

// Clone is one router's part of a binary clone: another router's encrypted
// backup, loaded whole.
type Clone struct {
	// Source is the model and version the backup was taken on.
	Source cfgtpl.Device
	// Expect is the target as it was planned.
	Expect Identity
	// URL mints the single-use address the router fetches the backup from
	// (Backups' restore capability token, bound to this router's addresses).
	URL func() (string, error)
	// Password decrypts the backup: the SOURCE router's backup password.
	Password string
}

// cloneBackWindow is how long a cloned router has to come back: the fetch is
// done, so this is a reboot and the load itself.
const cloneBackWindow = 6 * time.Minute

// RunClone replaces a router's whole configuration with another router's
// binary backup, and reboots it.
//
// ── WHAT COMES WITH A BINARY BACKUP ─────────────────────────────────────────
//
// Everything: the source's addresses, its users and passwords, and its MAC
// addresses. So the router may come back at the SOURCE's address, and
// MikroDash's login on it is whatever the source had. Fresh is the server's,
// and it tries both addresses; if neither answers, the outcome says why that
// may be, rather than only that it failed.
//
// ── "LOADED" IS PROVEN BY THE REBOOT ────────────────────────────────────────
//
// `/system/backup/load` never answers: it reboots. A router that still
// answers after the load without having rebooted refused the backup (a wrong
// password, say), and that is reported as nothing applied.
func RunClone(env Env, c Clone) (out Outcome) {
	env.fill()
	out = Outcome{State: StatePreflightFailed, Applied: "none", Findings: []cfgtpl.Finding{}}
	env.Step("sweep")
	env.sweep()

	id, err := ReadIdentity(env.Do)
	if err != nil {
		out.Code, out.Message = "unreachable", err.Error()
		return out
	}
	if !sameRouter(id, c.Expect) {
		out.Code = "identity-changed"
		out.Message = "the router is no longer the one the run was planned for"
		return out
	}
	if d := cfgtpl.CheckTarget(cfgtpl.KindFullBinary, c.Source,
		cfgtpl.Device{Board: id.Board, OSVersion: id.OSVersion}, ""); !d.OK {
		out.Code = d.Code
		out.Message = fmt.Sprintf("a binary backup loads only on the same model and RouterOS release: "+
			"it was taken on %s, this router is %s", d.Was, d.Now)
		return out
	}

	env.Step("backup")
	bid, err := env.Backup()
	if err != nil {
		out.Code, out.Message = "no-restore-point", "no restore point could be taken, so nothing was sent: "+err.Error()
		return out
	}
	out.BackupID = bid

	base, err := cfgtpl.NewBaseName()
	if err != nil {
		out.Code, out.Message = "fetch", err.Error()
		return out
	}
	file := base + ".backup"
	url, err := c.URL()
	if err != nil {
		out.Code, out.Message = "fetch", err.Error()
		return out
	}
	env.Step("fetch")
	if _, err := env.Do(routeros.Cmd{Path: "/tool/fetch", Timeout: 5 * time.Minute,
		Args: []string{"=url=" + url, "=dst-path=" + file}}); err != nil {
		out.Code, out.Message = "fetch", "the router could not fetch the backup: "+err.Error()
		return out
	}
	if rows, err := env.Do(routeros.Cmd{Path: "/file/print", Timeout: 15 * time.Second,
		Args: []string{"?name=" + file, "=.proplist=size"}}); err != nil || len(rows) == 0 {
		out.Code, out.Message = "fetch", "the fetched backup is not on the router"
		return out
	}

	// ── From here the router is being replaced ────────────────────────────
	env.Step("load")
	loadAt := env.Now()
	// Never answers when it works: it reboots. Bounded so the write queue is
	// not held for ever (the 2026-09-07 incident, see backups_restore.go).
	_, _ = env.Do(routeros.Cmd{Path: "/system/backup/load", Timeout: 30 * time.Second,
		Args: []string{"=name=" + file, "=password=" + c.Password}})

	env.Step("reboot")
	out.State, out.Applied = StateUnknown, "unknown"
	answeredUnbooted := false
	for deadline := loadAt.Add(cloneBackWindow); env.Now().Before(deadline); {
		env.Sleep(5 * time.Second)
		back, err := env.Fresh()
		if err != nil {
			continue
		}
		if back.Uptime > env.Now().Sub(loadAt)+5*time.Second {
			// Still up since before the load: it refused the backup.
			answeredUnbooted = true
			if env.Now().Sub(loadAt) > 90*time.Second {
				break
			}
			continue
		}
		if back.Board != c.Expect.Board {
			out.Code = "identity-changed"
			out.Message = "a different model answered after the load"
			return out
		}
		out.State, out.Applied, out.Code = StateApplied, "all", ""
		out.ReconnectMS = env.Now().Sub(loadAt).Milliseconds()
		out.Message = "the router rebooted into the source's configuration, its addresses, users and MAC " +
			"addresses included"
		return out
	}
	restore := "restore point #" + strconv.FormatInt(bid, 10) + " was taken before the clone."
	if answeredUnbooted {
		out.State, out.Applied, out.Code = StateFailed, "none", "not-loaded"
		out.Message = "the router kept running instead of loading the backup; the source's backup password " +
			"may not match. Nothing changed."
		return out
	}
	out.Code = "not-back"
	out.Message = "the router has not answered since the load. It now has the source's addresses and users, " +
		"so MikroDash may not be able to reach or log in to it as before. " + restore
	return out
}
