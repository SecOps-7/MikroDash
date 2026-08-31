package server

// `backups:restore` — the write that replaces a router's entire configuration
// and reboots it.
//
// ── THE SHAPE OF IT ─────────────────────────────────────────────────────────
//
// Every other backup action reads or writes files on OUR disk. This one hands
// the router a URL and tells it to fetch and load, so the router reaches back to
// us — which is why `/api/backups/{id}/raw` exists and why a capability token
// stands in for the session that cannot be presented.
//
// ── A SERIAL MISMATCH IS A REFUSAL; A VERSION MISMATCH IS A QUESTION ────────
//
// Restoring one device's configuration onto another is not a thing an operator
// can mean, so a serial that does not match the row is denied and audited. A
// RouterOS version difference is a different matter — it is usually fine, and
// the operator is the one who knows — so it is asked once and accepted with
// `acceptVersion`. Getting these the same way round either blocks routine work
// or silently allows the unrecoverable one.
//
// ── THE AUDIT IS WRITTEN BEFORE THE CALL ────────────────────────────────────
//
// The load reboots the router, and the reboot takes the connection — and with it
// any answer. An audit row written afterwards would be missing for exactly the
// restores that worked.
//
// ── THE LOAD IS NOT EXPECTED TO ANSWER ──────────────────────────────────────
//
// `/system/backup/load` reboots, so its call does not return. A rejection there
// is NORMAL and must not be reported as a failed restore — the live comment says
// the same, and reporting it would tell an operator their restore failed while
// the router was busy applying it.

import (
	"encoding/json"
	"strconv"
	"strings"

	"mikrodash/internal/audit"
	"mikrodash/internal/guard"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
)

// restoreDst is the filename the router saves the fetched backup under. Fixed,
// because it is overwritten every restore and never read by anything else.
const restoreDst = "mikrodash-restore.backup"

type restoreReq struct {
	ID            int64  `json:"id"`
	Confirm       string `json:"confirm"`
	AcceptVersion bool   `json:"acceptVersion"`
}

// backupsRestore decodes the request and serialises the work.
//
// SERIALISED through `InWriteQueue`, the same chain every other router write
// goes through: a restore holds an API channel while the router fetches, and the
// documented limit on this hardware is concurrent channels.
func (cn *conn) backupsRestore(raw json.RawMessage) {
	var req restoreReq
	if len(raw) > 0 {
		// A malformed body is not a reason to answer something other than the
		// zero request: the checks below refuse it on the confirmation anyway,
		// and inventing a third failure mode would be a divergence.
		_ = json.Unmarshal(raw, &req)
	}
	if cn.rsession == nil {
		cn.bkErr("denied", nil)
		return
	}
	if err := cn.rsession.InWriteQueue(func() error {
		cn.restoreLocked(req)
		return nil
	}); err != nil {
		cn.bkErr("failed", map[string]any{"message": safe.Message(err.Error())})
	}
}

func (cn *conn) restoreLocked(req restoreReq) {
	if cn.routerID == "" || cn.rsession == nil || !cn.bkMayWrite() {
		cn.recorder().Denied(audit.Event{
			Action: "backup.restore", TargetType: "backup", RouterID: cn.routerID,
		})
		cn.bkErr("denied", nil)
		return
	}
	if cn.srv.auditDB == nil {
		cn.bkErr("unavailable", nil)
		return
	}
	row, err := cn.srv.auditDB.GetBackup(req.ID)
	if err != nil || row == nil || row.RouterID != cn.routerID ||
		row.Stem == nil || *row.Stem == "" || row.PrunedAt != nil {
		cn.bkErr("not-found", nil)
		return
	}

	// TYPED CONFIRMATION, compared to the label the operator can SEE. Not the id,
	// which is not on screen, and not a yes/no — the point is that the words are
	// hard to type by accident.
	if strings.TrimSpace(req.Confirm) != strings.TrimSpace(cn.rsession.Label) {
		cn.bkErr("confirm-mismatch", nil)
		return
	}
	if !cn.rsession.Connected() {
		cn.bkErr("unavailable", nil)
		return
	}

	// ── IDENTITY, READ FRESH FROM THE DEVICE ────────────────────────────────
	//
	// Not from the session's cached system payload: a restore is the one action
	// where "which device is this, right now" has to be answered by the device.
	serialNow, versionNow, err := cn.readIdentity()
	if err != nil {
		cn.bkErr("failed", map[string]any{"message": safe.Message(err.Error())})
		return
	}

	if row.Serial != nil && *row.Serial != "" && serialNow != "" && *row.Serial != serialNow {
		cn.recorder().Denied(audit.Event{
			Action: "backup.restore", TargetType: "backup", Scope: "router",
			RouterID: cn.routerID, TargetID: strconv64(row.ID), Note: "serial-mismatch",
		})
		cn.bkErr("serial-mismatch", map[string]any{"was": *row.Serial, "now": serialNow})
		return
	}
	if row.OSVersion != nil && *row.OSVersion != "" && versionNow != "" &&
		*row.OSVersion != versionNow && !req.AcceptVersion {
		cn.bkErr("version-mismatch", map[string]any{"was": *row.OSVersion, "now": versionNow})
		return
	}

	base, ok := cn.restoreBase()
	if !ok {
		cn.bkErr("no-route-back", nil)
		return
	}

	// AUDITED BEFORE THE CALL — see the header.
	fromVersion := ""
	if row.OSVersion != nil {
		fromVersion = *row.OSVersion
	}
	serial := ""
	if row.Serial != nil {
		serial = *row.Serial
	}
	cn.recorder().Record(audit.Event{
		Action: "backup.restore", TargetType: "backup", Scope: "router",
		RouterID: cn.routerID, TargetID: strconv64(row.ID), TargetName: *row.Stem,
		Note: "stem " + *row.Stem + "; serial " + serial + "; " + fromVersion + " -> " + versionNow +
			acceptedNote(req.AcceptVersion),
	})

	token, err := cn.srv.restoreTokens.Mint(row.ID, cn.routerID, cn.rsession.Host())
	if err != nil {
		cn.bkErr("failed", map[string]any{"message": safe.Message(err.Error())})
		return
	}

	cn.srv.hub.Send(cn.c, "backups:restoring",
		map[string]any{"routerId": cn.routerID, "id": row.ID})

	url := base + backupRawURL(row.ID, token)
	if _, err := cn.rsession.Exec(routeros.Cmd{
		Path: "/tool/fetch",
		Args: []string{"=url=" + url, "=dst-path=" + restoreDst},
	}); err != nil {
		cn.bkErr("failed", map[string]any{"message": safe.Message(err.Error())})
		return
	}

	// THE LOAD REBOOTS, so this is not expected to answer. The error is dropped
	// deliberately: reporting it would tell an operator their restore failed
	// while the router was busy applying it.
	//
	// THE PASSWORD FALLBACK NEEDS ITS PARENTHESES. The live code carries a
	// comment about exactly this: written as `'=password=' + a || ''`, the `+`
	// binds tighter, the left side always starts with `=password=` and is never
	// falsy, so the fallback is dead and a router with no backup block sends the
	// literal eight characters `undefined`. RouterOS accepts that and fails to
	// decrypt, and the operator sees a failure that points at their stored
	// credential rather than at a missing block. Go has no such trap, and the
	// behaviour it produces — an EMPTY password when there is no block — is what
	// is reproduced here.
	pw := cn.backupRecordFor(cn.routerID).password
	_, _ = cn.rsession.Exec(routeros.Cmd{
		Path: "/system/backup/load",
		Args: []string{"=name=" + restoreDst, "=password=" + pw},
	})

	cn.srv.hub.Send(cn.c, "backups:restored",
		map[string]any{"routerId": cn.routerID, "id": row.ID})
}

// readIdentity asks the device what it is, right now.
//
// The version is the FIRST token of what RouterOS reports: `7.24 (stable)` is
// the same build as `7.24`, and comparing the whole string would make every
// stable release read as a mismatch.
func (cn *conn) readIdentity() (serial, version string, err error) {
	rb, err := cn.rsession.Exec(routeros.Cmd{
		Path: "/system/routerboard/print",
		Args: []string{"=.proplist=serial-number"},
	})
	if err != nil {
		return "", "", err
	}
	if len(rb) > 0 {
		serial = rb[0]["serial-number"]
	}
	res, err := cn.rsession.Exec(routeros.Cmd{
		Path: "/system/resource/print",
		Args: []string{"=.proplist=version"},
	})
	if err != nil {
		return "", "", err
	}
	if len(res) > 0 {
		version = strings.Split(res[0]["version"], " ")[0]
	}
	return serial, version, nil
}

// restoreBase is the address the ROUTER can reach us at.
//
// ── `backupBaseUrl` MEANS WHAT IT SAYS NOW ──────────────────────────────────
//
// It is an operator-set address for "where this install is reachable", and
// before cutover it named NODE's port while the file had to come from Go's —
// which is the whole reason this was blocked. With Go owning the route there is
// one server, so the setting names it and is used as written.
//
// ── AND WHEN IT IS UNSET, THE ROUTER TELLS US ───────────────────────────────
//
// `/user/active/print` shows the address our own session is coming FROM, which
// is the one address known to be reachable from that router — better than
// anything this process could infer about its own interfaces, especially behind
// NAT. `guard.SelfAddresses` is the same decision `selfPath` makes.
func (cn *conn) restoreBase() (string, bool) {
	if s, err := cn.srv.store.Settings(); err == nil {
		if raw, ok := s["backupBaseUrl"].(string); ok {
			if base := strings.TrimRight(strings.TrimSpace(raw), "/"); base != "" {
				return base, true
			}
		}
	}
	active, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/active/print"})
	if err != nil {
		return "", false
	}
	addrs, ok := guard.SelfAddresses(active, []string{cn.rsession.Username()})
	if !ok || len(addrs) == 0 {
		return "", false
	}
	return "http://" + addrs[0] + portSuffix(cn.srv.listenAddr), true
}

// portSuffix turns a listen address into the `:port` a URL needs. `:3082` and
// `0.0.0.0:3082` both yield `:3082`.
func portSuffix(listen string) string {
	if i := strings.LastIndex(listen, ":"); i >= 0 {
		return listen[i:]
	}
	if listen == "" {
		return ""
	}
	return ":" + listen
}

func backupRawURL(id int64, token string) string {
	return "/api/backups/" + strconv64(id) + "/raw?t=" + token
}

func acceptedNote(accepted bool) string {
	if accepted {
		return "; version mismatch accepted"
	}
	return ""
}

// strconv64 renders a row id for an audit field and a URL path.
func strconv64(n int64) string { return strconv.FormatInt(n, 10) }
