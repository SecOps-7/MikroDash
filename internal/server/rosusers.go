package server

// The Router Users write paths — a port of the rosuser/rosgroup/rossession
// socket actions in src/index.js.
//
// These are NOT resource-registry entries. The registry handles resources whose
// writes are uniform; these six actions each carry their own rules, and the one
// thing they share is the lockout guard. `portedGuards` is therefore not the
// hook here — internal/guard.CheckUser/CheckGroup/CheckSession are called
// directly, and this file is their first real caller.
//
// ── EVERY WRITE RE-READS FIRST ───────────────────────────────────────────────
//
// The guard is judged on rows read from the router IN THE SAME TICK as the
// write, never on the page's copy and never on the collector's last payload. A
// page can be stale and a request can be crafted, and the thing being prevented
// is unrecoverable from inside the app: break the login and the fix is WinBox,
// in person.
//
// ── THE PASSWORD IS NEVER RECORDED ───────────────────────────────────────────
//
// A password is accepted on create and on an explicit reset, sent to the router,
// and never mentioned again. The audit row carries a `passwordSet` FLAG rather
// than a value — and rather than a diff, because /user/print never returns a
// password, so `before` cannot know whether one was set. Recording
// «unset» → «changed» would be a claim the trail cannot support.
//
// The flag is computed once, before the write, and the audit call reads it
// instead of the plaintext: there is no expression in the audit path that could
// serialise a password even by accident. audit.go would mask a field named
// `password`, but this does not rely on that.

import (
	"encoding/json"
	"strings"
	"unicode/utf16"

	"mikrodash/internal/audit"
	"mikrodash/internal/collect"
	"mikrodash/internal/guard"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
)

type ruRequest struct {
	ID           string `json:"id"`
	ExpectedName string `json:"expectedName"`
	Name         string `json:"name"`
	Group        string `json:"group"`
	Address      string `json:"address"`
	Comment      string `json:"comment"`
	Disabled     bool   `json:"disabled"`
	// Password is read here and leaves this struct only as an argument to the
	// router. Nothing logs, diffs or echoes it.
	Password string   `json:"password"`
	Policy   []string `json:"policy"`
}

func (cn *conn) ruErr(code string, extra map[string]any) {
	m := map[string]any{"code": code}
	for k, v := range extra {
		m[k] = v
	}
	cn.srv.hub.Send(cn.c, "rosusers:error", m)
}

// ruReady is the precondition the six handlers share: a router, a session, and
// page-write permission. Kept as one call so they cannot drift apart on it.
func (cn *conn) ruReady() bool {
	if cn.routerID == "" || cn.rsession == nil {
		cn.ruErr("unavailable", nil)
		return false
	}
	return true
}

func (cn *conn) ruMayWrite() bool { return cn.canPage("users", "write") }

// ruPageDenied records the page-level refusal and answers. The guard's own
// refusals are recorded at their call sites, because only there is the row —
// and therefore the target id — known.
func (cn *conn) ruPageDenied(action, targetType, name string) {
	cn.recorder().Denied(audit.Event{
		Action: action, TargetType: targetType, RouterID: cn.routerID,
		TargetName: name,
	})
	cn.ruErr("denied", nil)
}

// ruGuardDenied records a guard refusal and answers with its code.
func (cn *conn) ruGuardDenied(action, targetType, id, name string, v guard.Refusal) {
	cn.recorder().Denied(audit.Event{
		Action: action, TargetType: targetType, RouterID: cn.routerID,
		TargetID: id, TargetName: name, Note: v.Code,
	})
	detail := v.Detail
	if detail == "" {
		detail = name
	}
	cn.ruErr(v.Code, map[string]any{"name": detail})
}

// ruState is one fresh read of everything a decision needs.
type ruState struct {
	users  []routeros.Reply
	groups []routeros.Reply
	active []routeros.Reply
	self   guard.Self
}

// ruRead re-reads the three tables and resolves self from them.
//
// NO PROPLIST. The guard compares names and groups, and a narrow read that
// happened to omit `group` would resolve self to nothing and then refuse
// everything — failing closed, but for the wrong reason and invisibly.
func (cn *conn) ruRead() (ruState, error) {
	users, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/print"})
	if err != nil {
		return ruState{}, err
	}
	groups, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/group/print"})
	if err != nil {
		return ruState{}, err
	}
	active, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/active/print"})
	if err != nil {
		return ruState{}, err
	}
	return ruState{
		users: users, groups: groups, active: active,
		self: guard.ResolveSelf(users, active, []string{cn.rsession.Username()}),
	}, nil
}

// ruRow ADDRESSES a row by id and IDENTIFIES it by name.
//
// A `.id` survives a rename, so on its own it says which slot but not which
// row. If the name no longer matches, the operator is looking at something
// else and the write is refused as stale.
func ruRow(rows []routeros.Reply, id, expectedName string) routeros.Reply {
	for _, r := range rows {
		if r[".id"] != id {
			continue
		}
		if expectedName != "" && r["name"] != expectedName {
			return nil
		}
		return r
	}
	return nil
}

// pwLen counts UTF-16 code units, which is what JavaScript's String#length
// returns and therefore what the live app compares against the router's
// minimum. Bytes would reject a short password made of non-ASCII characters
// that Node accepted, and runes would accept one Node rejected.
func pwLen(s string) int { return len(utf16.Encode([]rune(s))) }

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// ── users ───────────────────────────────────────────────────────────────────

func (cn *conn) ruUserSave(raw json.RawMessage) {
	var req ruRequest
	if json.Unmarshal(raw, &req) != nil {
		cn.ruErr("bad-request", nil)
		return
	}
	if !cn.ruReady() {
		return
	}
	editing := req.ID != ""
	action := "rosuser.create"
	if editing {
		action = "rosuser.update"
	}
	if !cn.ruMayWrite() {
		cn.ruPageDenied(action, "rosuser", req.Name)
		return
	}

	name := strings.TrimSpace(req.Name)
	group := strings.TrimSpace(req.Group)
	if name == "" || group == "" {
		cn.ruErr("bad-request", nil)
		return
	}

	err := cn.rsession.InWriteQueue(func() error {
		st, err := cn.ruRead()
		if err != nil {
			return err
		}
		known := false
		for _, g := range st.groups {
			if g["name"] == group {
				known = true
				break
			}
		}
		if !known {
			cn.ruErr("no-such-group", map[string]any{"name": group})
			return nil
		}

		var target routeros.Reply
		if editing {
			if target = ruRow(st.users, req.ID, req.ExpectedName); target == nil {
				cn.ruErr("stale-row", map[string]any{"name": name})
				return nil
			}
		}

		verb := "add"
		if editing {
			verb = "set"
		}
		var guardTarget routeros.Reply
		if target != nil {
			guardTarget = routeros.Reply{"name": target["name"], "group": target["group"]}
		}
		if v := guard.CheckUser(st.self, guard.UserAction{
			Verb: verb, Target: guardTarget,
			Values:   map[string]string{"name": name, "group": group},
			ValueSet: map[string]bool{"name": true, "group": true},
		}); !v.OK {
			cn.ruGuardDenied(action, "rosuser", "", name, v)
			return nil
		}

		// The router enforces its own minimum, but answers with a bare failure.
		// Checking here turns that into a sentence the operator can act on; the
		// router stays the authority either way.
		pw := req.Password
		passwordSet := pw != ""
		minLen := 0
		if last := cn.rsession.RosUsers().Last(); last != nil {
			minLen = last.PasswordPolicy.MinLength
		}
		if pw != "" && minLen > 0 && pwLen(pw) < minLen {
			cn.ruErr("weak-password", map[string]any{"minLength": minLen})
			return nil
		}
		if !editing && pw == "" && minLen > 0 {
			cn.ruErr("weak-password", map[string]any{"minLength": minLen})
			return nil
		}

		args := []string{
			"=name=" + name, "=group=" + group,
			"=address=" + strings.TrimSpace(req.Address),
			"=comment=" + strings.TrimSpace(req.Comment),
			"=disabled=" + yesNo(req.Disabled),
		}
		if pw != "" {
			args = append(args, "=password="+pw)
		}

		before := map[string]any{}
		if target != nil {
			before = map[string]any{
				"name": target["name"], "group": target["group"],
				"address": target["address"], "comment": target["comment"],
				"disabled": target["disabled"] == "true",
			}
		}
		after := map[string]any{
			"name": name, "group": group,
			"address":  strings.TrimSpace(req.Address),
			"comment":  strings.TrimSpace(req.Comment),
			"disabled": req.Disabled,
		}

		path, cmdArgs := "/user/add", args
		if editing {
			path = "/user/set"
			cmdArgs = append([]string{"=.id=" + req.ID}, args...)
		}
		if _, err := cn.rsession.Exec(routeros.Cmd{Path: path, Args: cmdArgs}); err != nil {
			return err
		}

		var extra []audit.KV
		if passwordSet {
			extra = []audit.KV{{Key: "passwordSet", Value: true}}
		}
		targetID := ""
		if editing {
			targetID = req.ID
		}
		cn.recorder().Record(audit.Event{
			Action: action, TargetType: "rosuser", TargetID: targetID, TargetName: name,
			RouterID: cn.routerID, Before: before, After: after, Extra: extra,
		})
		if cn.rsession.CollectorEnabled("rosusers") {
			cn.rsession.RosUsers().RefreshNow()
		}
		outcome := "create"
		if editing {
			outcome = "update"
		}
		cn.srv.hub.Send(cn.c, "rosusers:ok", map[string]any{"action": outcome, "name": name})
		return nil
	})
	if err != nil {
		cn.ruErr(writeFailCode(err), map[string]any{"name": name, "message": safe.Message(err.Error())})
	}
}

// ── groups ──────────────────────────────────────────────────────────────────

func (cn *conn) ruGroupSave(raw json.RawMessage) {
	var req ruRequest
	if json.Unmarshal(raw, &req) != nil {
		cn.ruErr("bad-request", nil)
		return
	}
	if !cn.ruReady() {
		return
	}
	editing := req.ID != ""
	action := "rosgroup.create"
	if editing {
		action = "rosgroup.update"
	}
	if !cn.ruMayWrite() {
		cn.ruPageDenied(action, "rosgroup", req.Name)
		return
	}

	name := strings.TrimSpace(req.Name)
	// nil, not empty: the browser sends the GRANTED list and an empty one is a
	// group with no policies, which is a legal thing to ask for. A missing
	// field is not.
	if name == "" || req.Policy == nil {
		cn.ruErr("bad-request", nil)
		return
	}
	// BuildPolicy filters to the vocabulary the UI actually showed rather than
	// passing strings through. RouterOS normalises whatever it is sent — send
	// `read,api` and it stores all seventeen with the rest negated — so the
	// granted list is the whole input.
	policy := collect.BuildPolicy(req.Policy)

	err := cn.rsession.InWriteQueue(func() error {
		st, err := cn.ruRead()
		if err != nil {
			return err
		}
		var target routeros.Reply
		if editing {
			if target = ruRow(st.groups, req.ID, req.ExpectedName); target == nil {
				cn.ruErr("stale-row", map[string]any{"name": name})
				return nil
			}
		}

		verb := "add"
		if editing {
			verb = "set"
		}
		var guardTarget routeros.Reply
		if target != nil {
			guardTarget = routeros.Reply{"name": target["name"]}
		}
		if v := guard.CheckGroup(st.self, guard.UserAction{
			Verb: verb, Target: guardTarget,
			Values:   map[string]string{"name": name},
			ValueSet: map[string]bool{"name": true},
		}); !v.OK {
			cn.ruGuardDenied(action, "rosgroup", "", name, v)
			return nil
		}

		args := []string{"=name=" + name, "=policy=" + policy,
			"=comment=" + strings.TrimSpace(req.Comment)}
		path, cmdArgs := "/user/group/add", args
		if editing {
			path = "/user/group/set"
			cmdArgs = append([]string{"=.id=" + req.ID}, args...)
		}
		if _, err := cn.rsession.Exec(routeros.Cmd{Path: path, Args: cmdArgs}); err != nil {
			return err
		}

		before := map[string]any{}
		if target != nil {
			before = map[string]any{"name": target["name"], "policy": target["policy"]}
		}
		targetID := ""
		if editing {
			targetID = req.ID
		}
		cn.recorder().Record(audit.Event{
			Action: action, TargetType: "rosgroup", TargetID: targetID, TargetName: name,
			RouterID: cn.routerID, Before: before,
			After: map[string]any{"name": name, "policy": policy},
		})
		if cn.rsession.CollectorEnabled("rosusers") {
			cn.rsession.RosUsers().RefreshNow()
		}
		outcome := "group-create"
		if editing {
			outcome = "group-update"
		}
		cn.srv.hub.Send(cn.c, "rosusers:ok", map[string]any{"action": outcome, "name": name})
		return nil
	})
	if err != nil {
		cn.ruErr(writeFailCode(err), map[string]any{"name": name, "message": safe.Message(err.Error())})
	}
}

// ── the three deletes ───────────────────────────────────────────────────────

// removeSpec is the shape a delete takes. The three differ only in which table
// they address, which guard call judges them, and what the audit row carries —
// so the SEQUENCE lives in one place, where the re-read and the staleness check
// cannot be skipped by accident in one copy of three.
type removeSpec struct {
	action, targetType, menu, okAction, note string
	rows                                     func(ruState) []routeros.Reply
	check                                    func(ruState, routeros.Reply) guard.Refusal
	extra                                    func(routeros.Reply) []audit.KV
	// mapErr turns a router refusal this action can predict into its own code.
	mapErr func(error) string
}

func (cn *conn) ruRemove(raw json.RawMessage, spec removeSpec) {
	var req ruRequest
	if json.Unmarshal(raw, &req) != nil {
		cn.ruErr("bad-request", nil)
		return
	}
	if !cn.ruReady() {
		return
	}
	if !cn.ruMayWrite() {
		cn.ruPageDenied(spec.action, spec.targetType, req.ExpectedName)
		return
	}
	if req.ID == "" {
		cn.ruErr("bad-request", nil)
		return
	}

	err := cn.rsession.InWriteQueue(func() error {
		st, err := cn.ruRead()
		if err != nil {
			return err
		}
		target := ruRow(spec.rows(st), req.ID, req.ExpectedName)
		if target == nil {
			// AND THE PAGE IS ACTUALLY REFRESHED, because the client says it was:
			// "That row changed on the router — the page has been refreshed".
			// It was not. `ruRead` above re-read the router into `st`, but that is
			// this handler's private copy; nothing pushed it to the browser, so the
			// operator was told to look at fresh data they had not been sent, and
			// the next click failed the same way.
			if cn.rsession.CollectorEnabled("rosusers") {
				cn.rsession.RosUsers().RefreshNow()
			}
			cn.ruErr("stale-row", nil)
			return nil
		}
		if v := spec.check(st, target); !v.OK {
			cn.ruGuardDenied(spec.action, spec.targetType, req.ID, target["name"], v)
			return nil
		}

		if _, err := cn.rsession.Exec(routeros.Cmd{
			Path: spec.menu + "/remove", Args: []string{"=.id=" + req.ID}}); err != nil {
			return err
		}
		cn.recorder().Record(audit.Event{
			Action: spec.action, TargetType: spec.targetType, TargetID: req.ID,
			TargetName: target["name"], RouterID: cn.routerID,
			Note: spec.note, Extra: spec.extra(target),
		})
		if cn.rsession.CollectorEnabled("rosusers") {
			cn.rsession.RosUsers().RefreshNow()
		}
		cn.srv.hub.Send(cn.c, "rosusers:ok",
			map[string]any{"action": spec.okAction, "name": target["name"]})
		return nil
	})
	if err != nil {
		if spec.mapErr != nil {
			if code := spec.mapErr(err); code != "" {
				cn.ruErr(code, nil)
				return
			}
		}
		cn.ruErr(writeFailCode(err), map[string]any{"message": safe.Message(err.Error())})
	}
}

func (cn *conn) ruUserRemove(raw json.RawMessage) {
	cn.ruRemove(raw, removeSpec{
		action: "rosuser.delete", targetType: "rosuser", menu: "/user",
		okAction: "delete",
		rows:     func(st ruState) []routeros.Reply { return st.users },
		check: func(st ruState, target routeros.Reply) guard.Refusal {
			return guard.CheckUser(st.self, guard.UserAction{
				Verb:   "remove",
				Target: routeros.Reply{"name": target["name"], "group": target["group"]},
			})
		},
		extra: func(target routeros.Reply) []audit.KV {
			return []audit.KV{{Key: "group", Value: target["group"]}}
		},
	})
}

func (cn *conn) ruGroupRemove(raw json.RawMessage) {
	cn.ruRemove(raw, removeSpec{
		action: "rosgroup.delete", targetType: "rosgroup", menu: "/user/group",
		okAction: "group-delete",
		rows:     func(st ruState) []routeros.Reply { return st.groups },
		check: func(st ruState, target routeros.Reply) guard.Refusal {
			return guard.CheckGroup(st.self, guard.UserAction{
				Verb: "remove", Target: routeros.Reply{"name": target["name"]},
			})
		},
		extra: func(target routeros.Reply) []audit.KV {
			return []audit.KV{{Key: "policy", Value: target["policy"]}}
		},
		// NOT pre-checked against the member count. The router refuses with
		// "group has some users" and it is the authority — a count read a moment
		// earlier could be wrong in either direction.
		mapErr: func(err error) string {
			if strings.Contains(strings.ToLower(err.Error()), "has some users") {
				return "group-in-use"
			}
			return ""
		},
	})
}

func (cn *conn) ruSessionRemove(raw json.RawMessage) {
	cn.ruRemove(raw, removeSpec{
		action: "rossession.remove", targetType: "rossession", menu: "/user/active",
		okAction: "session-remove", note: "ended an active RouterOS session",
		rows: func(st ruState) []routeros.Reply { return st.active },
		check: func(st ruState, target routeros.Reply) guard.Refusal {
			return guard.CheckSession(st.self, routeros.Reply{"name": target["name"]})
		},
		extra: func(target routeros.Reply) []audit.KV {
			return []audit.KV{
				{Key: "via", Value: target["via"]},
				{Key: "from", Value: target["address"]},
			}
		},
	})
}
