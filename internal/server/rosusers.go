package server

// Ending an active RouterOS session: the one Router Users write that is not a
// row in the resource registry.
//
// Users and groups are the `rosUser` and `rosGroup` resources, written through
// the resource engine with the `selfAccount` guard. A session is different: it
// is not configuration, it has no form and no undo, and ending one is a verb on
// /user/active. So it stays a page action, still judged by the lockout guard,
// because MikroDash keeps several logins open per router and ending one of its
// own is pointless churn.
//
// ── THE ROWS ARE RE-READ FIRST ───────────────────────────────────────────────
//
// The guard is judged on /user and /user/active as read IN THE SAME TICK as the
// write, never on the page's copy: a page can be stale and a request crafted.

import (
	"encoding/json"

	"mikrodash/internal/audit"
	"mikrodash/internal/guard"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
)

type ruSessionRequest struct {
	ID           string `json:"id"`
	ExpectedName string `json:"expectedName"`
}

func (cn *conn) ruErr(code string, extra map[string]any) {
	m := map[string]any{"code": code}
	for k, v := range extra {
		m[k] = v
	}
	EvRosusersError.Send(cn.srv.hub, cn.c, m)
}

func (cn *conn) ruSessionRemove(raw json.RawMessage) {
	const action = "rossession.remove"
	var req ruSessionRequest
	if json.Unmarshal(raw, &req) != nil || req.ID == "" {
		cn.ruErr("bad-request", nil)
		return
	}
	if cn.routerID == "" || cn.rsession == nil {
		cn.ruErr("unavailable", nil)
		return
	}
	if !cn.canPage("users", "write") {
		cn.recorder().Denied(audit.Event{
			Action: action, TargetType: "rossession", RouterID: cn.routerID,
			TargetName: req.ExpectedName,
		})
		cn.ruErr("denied", nil)
		return
	}

	err := cn.inWriteQueue(func() error {
		// NO PROPLIST. The guard compares names and groups, and a narrow read
		// that happened to omit `group` would resolve self to nothing and refuse
		// everything: failing closed, but for the wrong reason and invisibly.
		users, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/print"})
		if err != nil {
			return err
		}
		active, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/active/print"})
		if err != nil {
			return err
		}
		self := guard.ResolveSelf(users, active, []string{cn.rsession.Username()})

		// ADDRESSED by id and IDENTIFIED by name: RouterOS reuses `*N` ids.
		var target routeros.Reply
		for _, r := range active {
			if r[".id"] == req.ID && (req.ExpectedName == "" || r["name"] == req.ExpectedName) {
				target = r
				break
			}
		}
		if target == nil {
			// And the page IS refreshed, because the message says it was.
			if cn.rsession.CollectorEnabled("rosusers") {
				cn.rsession.RosUsers().RefreshNow()
			}
			cn.ruErr("stale-row", nil)
			return nil
		}
		if v := guard.CheckSession(self, routeros.Reply{"name": target["name"]}); !v.OK {
			cn.recorder().Denied(audit.Event{
				Action: action, TargetType: "rossession", RouterID: cn.routerID,
				TargetID: req.ID, TargetName: target["name"], Note: v.Code,
			})
			detail := v.Detail
			if detail == "" {
				detail = target["name"]
			}
			cn.ruErr(v.Code, map[string]any{"name": detail})
			return nil
		}

		if _, err := cn.rsession.Exec(routeros.Cmd{
			Path: "/user/active/remove", Args: []string{"=.id=" + req.ID}}); err != nil {
			return err
		}
		cn.recorder().Record(audit.Event{
			Action: action, TargetType: "rossession", TargetID: req.ID,
			TargetName: target["name"], RouterID: cn.routerID,
			Note: "ended an active RouterOS session",
			Extra: []audit.KV{
				{Key: "via", Value: target["via"]},
				{Key: "from", Value: target["address"]},
			},
		})
		if cn.rsession.CollectorEnabled("rosusers") {
			cn.rsession.RosUsers().RefreshNow()
		}
		EvRosusersOk.Send(cn.srv.hub, cn.c, map[string]any{"action": "session-remove", "name": target["name"]})
		return nil
	})
	if err != nil {
		cn.ruErr(writeFailCode(err), map[string]any{"message": safe.Message(err.Error())})
	}
}
