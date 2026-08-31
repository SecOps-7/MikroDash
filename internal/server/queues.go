package server

// The Queues write paths — a port of the queue:* socket actions in src/index.js.
//
// Like Router Users, these are NOT resource-registry entries: the registry
// handles resources whose writes are uniform, and these five each carry their
// own rules. The three properties they share with the Router Users handlers are
// the same three:
//
//	a fresh read in the same tick as the write,
//	a round-tripped name so a `.id` addresses a row without identifying it,
//	and per-router serialisation.
//
// ── THE GUARD HERE WARNS; IT DOES NOT REFUSE ────────────────────────────────
//
// internal/guard/queueguard.go inverts selfguard in both directions: it warns
// rather than refuses, and it fails open rather than closed. Read its header
// before assuming the sibling's rules apply. A queue that throttles the
// dashboard is recoverable from the very row that created it; being locked out
// of /user is not.
//
// ── THE ACKNOWLEDGEMENT IS A PROMPT, NOT A REFUSAL ──────────────────────────
//
// A warned write with no ack returns `self-throttle` and writes NOTHING — and
// audits nothing, because a denial row would make the trail lie about what was
// attempted. The browser re-submits with the fingerprint the server issued.
//
// A MISMATCHED ack is reported separately as `stale-warning`, so an
// acknowledgement cannot be carried from a mild queue to a harsher one, or
// replayed against a different write. The fingerprint is recomputed from the
// fresh read every time, which is what makes that check mean anything.

import (
	"encoding/json"
	"strings"

	"mikrodash/internal/audit"
	"mikrodash/internal/guard"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
)

var queueMenus = map[string]string{"simple": "/queue/simple", "tree": "/queue/tree"}

type queueRequest struct {
	Menu         string `json:"menu"`
	ID           string `json:"id"`
	ExpectedName string `json:"expectedName"`
	Name         string `json:"name"`
	Target       string `json:"target"`
	Parent       string `json:"parent"`
	PacketMarks  string `json:"packetMarks"`
	PacketMark   string `json:"packetMark"`
	MaxLimit     string `json:"maxLimit"`
	LimitAt      string `json:"limitAt"`
	Priority     string `json:"priority"`
	Comment      string `json:"comment"`
	Disabled     bool   `json:"disabled"`
	Ack          string `json:"ack"`
	Direction    string `json:"direction"`
}

// menu normalises to one of the two known menus. Anything unrecognised is
// "simple", which is the original's `r.menu === 'tree' ? 'tree' : 'simple'`.
func (r queueRequest) menu() string {
	if r.Menu == "tree" {
		return "tree"
	}
	return "simple"
}

func (cn *conn) qErr(code string, extra map[string]any) {
	m := map[string]any{"code": code}
	for k, v := range extra {
		m[k] = v
	}
	cn.srv.hub.Send(cn.c, "queues:error", m)
}

func (cn *conn) qMayWrite() bool { return cn.canPage("queues", "write") }

func (cn *conn) qCaps() {
	if cn.routerID == "" || cn.rsession == nil {
		cn.qErr("unavailable", nil)
		return
	}
	if !cn.canPage("queues", "read") {
		cn.qErr("denied", nil)
		return
	}
	cn.srv.hub.Send(cn.c, "queues:caps", map[string]any{
		"permitted":  cn.qMayWrite(),
		"routerName": cn.rsession.Label,
	})
}

// qRead reads one queue menu, plus the active sessions the throttle warning
// needs.
//
// /user/active is read for its `address` column — the source address the ROUTER
// sees us from, which is what makes the warning possible at all. A router that
// denies it simply yields no addresses and the guard fails open, which is the
// COMMON case rather than an edge one: the documented monitoring group denies
// `policy`. The error is therefore swallowed deliberately.
func (cn *conn) qRead(menu string) ([]routeros.Reply, []string, error) {
	rows, err := cn.rsession.Exec(routeros.Cmd{Path: queueMenus[menu] + "/print"})
	if err != nil {
		return nil, nil, err
	}
	live := make([]routeros.Reply, 0, len(rows))
	for _, r := range rows {
		if r[".id"] != "" {
			live = append(live, r)
		}
	}
	var active []routeros.Reply
	if a, aerr := cn.rsession.Exec(routeros.Cmd{Path: "/user/active/print"}); aerr == nil {
		for _, r := range a {
			if r["name"] != "" {
				active = append(active, r)
			}
		}
	}
	self, _ := guard.SelfAddresses(active, []string{cn.rsession.Username()})
	return live, self, nil
}

// qRow addresses by id and identifies by name — a `.id` survives a rename, a
// name does not.
func qRow(rows []routeros.Reply, id, expectedName string) routeros.Reply {
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

// qDynamic reports whether a freshly-read row is router-managed.
//
// Checked on the READ row, never the browser's claim. Only simple queues can be
// dynamic — a tree has no such field, so this is always false there.
func qDynamic(r routeros.Reply) bool { return r["dynamic"] == "true" }

// qLimitsOk pre-checks max-limit against limit-at.
//
// RouterOS refuses with "download-max-limit less than download-limit". Checking
// here turns that into a sentence naming both fields; the router stays the
// authority either way, and the same code is mapped from its error below.
func qLimitsOk(maxLimit, limitAt string) bool {
	m, l := guard.ParsePair(maxLimit), guard.ParsePair(limitAt)
	bad := func(mx, lo guard.Rate) bool {
		return mx.Set && mx.Bps > 0 && lo.Set && lo.Bps > mx.Bps
	}
	return !(bad(m.Up, l.Up) || bad(m.Down, l.Down))
}

// qWarn runs the self-throttle guard and turns a warning into the prompt or the
// stale-ack refusal. It returns true when the caller must stop.
func (cn *conn) qWarn(self []string, values guard.SimpleQueueValues,
	before *guard.SimpleQueueValues, ack, name string) bool {

	v := guard.CheckSimpleQueue(self, values, before, guard.SelfThrottleFloorBps)
	if v.Level != "warn" {
		// A verdict of "none" with an ack present is harmless: the warning
		// simply no longer applies to what is being written.
		return false
	}
	if ack == "" {
		// Nothing written and nothing audited — this is a prompt, not a refusal.
		cn.qErr("self-throttle", map[string]any{
			"warning": v.Detail, "fingerprint": v.Fingerprint, "name": name})
		return true
	}
	if ack != v.Fingerprint {
		cn.qErr("stale-warning", map[string]any{
			"warning": v.Detail, "fingerprint": v.Fingerprint, "name": name})
		return true
	}
	return false
}

// qAckExtra records the acknowledgement, which is the interesting fact in the
// trail — not the queue.
func qAckExtra(menu, ack string) []audit.KV {
	out := []audit.KV{{Key: "menu", Value: menu}}
	if ack != "" {
		out = append(out, audit.KV{Key: "selfThrottleAcknowledged", Value: true})
	}
	return out
}

// qWriteFail maps the one router refusal these paths can predict.
func qWriteFail(err error) string {
	if strings.Contains(strings.ToLower(err.Error()), "less than") {
		return "limit-above-max"
	}
	return writeFailCode(err)
}

func (cn *conn) qSave(raw json.RawMessage) {
	var req queueRequest
	if json.Unmarshal(raw, &req) != nil {
		cn.qErr("bad-request", nil)
		return
	}
	if cn.routerID == "" || cn.rsession == nil {
		cn.qErr("unavailable", nil)
		return
	}
	menu := req.menu()
	editing := req.ID != ""
	action := "queue.create"
	if editing {
		action = "queue.update"
	}
	if !cn.qMayWrite() {
		cn.recorder().Denied(audit.Event{
			Action: action, TargetType: "queue", RouterID: cn.routerID,
			TargetName: req.Name, Extra: []audit.KV{{Key: "menu", Value: menu}},
		})
		cn.qErr("denied", nil)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		cn.qErr("bad-request", nil)
		return
	}
	if menu == "simple" && !editing && req.Target == "" {
		cn.qErr("bad-request", nil)
		return
	}
	if !qLimitsOk(req.MaxLimit, req.LimitAt) {
		cn.qErr("limit-above-max", nil)
		return
	}

	err := cn.rsession.InWriteQueue(func() error {
		rows, self, err := cn.qRead(menu)
		if err != nil {
			return err
		}
		var target routeros.Reply
		if editing {
			if target = qRow(rows, req.ID, req.ExpectedName); target == nil {
				cn.qErr("stale-row", map[string]any{"name": name})
				return nil
			}
			if qDynamic(target) {
				cn.recorder().Denied(audit.Event{
					Action: action, TargetType: "queue", RouterID: cn.routerID,
					TargetID: req.ID, TargetName: name, Note: "dynamic-row",
				})
				cn.qErr("dynamic-row", map[string]any{"name": name})
				return nil
			}
		}

		// Only simple queues carry a target, so only they can be aimed at us.
		if menu == "simple" {
			var before *guard.SimpleQueueValues
			if target != nil {
				before = &guard.SimpleQueueValues{
					Target:   target["target"],
					MaxLimit: guard.ParsePair(target["max-limit"]),
					Disabled: target["disabled"] == "true",
				}
			}
			values := guard.SimpleQueueValues{
				Target:   req.Target,
				MaxLimit: guard.ParsePair(req.MaxLimit),
				Disabled: req.Disabled,
			}
			if cn.qWarn(self, values, before, req.Ack, name) {
				return nil
			}
		}

		args := []string{"=name=" + name,
			"=comment=" + strings.TrimSpace(req.Comment),
			"=disabled=" + yesNo(req.Disabled)}
		// An EMPTY value is omitted rather than sent blank, matching the
		// original's `put`. Sending `=max-limit=` would clear a field the form
		// never showed.
		put := func(k, v string) {
			if s := strings.TrimSpace(v); s != "" {
				args = append(args, "="+k+"="+s)
			}
		}
		put("max-limit", req.MaxLimit)
		put("limit-at", req.LimitAt)
		put("priority", req.Priority)
		if menu == "simple" {
			put("target", req.Target)
			put("packet-marks", req.PacketMarks)
		} else {
			put("parent", req.Parent)
			put("packet-mark", req.PacketMark)
		}

		path, cmdArgs := queueMenus[menu]+"/add", args
		if editing {
			path = queueMenus[menu] + "/set"
			cmdArgs = append([]string{"=.id=" + req.ID}, args...)
		}
		if _, werr := cn.rsession.Exec(routeros.Cmd{Path: path, Args: cmdArgs}); werr != nil {
			return werr
		}

		before := map[string]any{}
		if target != nil {
			before = map[string]any{
				"name": target["name"], "target": target["target"], "parent": target["parent"],
				"maxLimit": target["max-limit"], "limitAt": target["limit-at"],
				"disabled": target["disabled"] == "true",
			}
		}
		targetID := ""
		if editing {
			targetID = req.ID
		}
		cn.recorder().Record(audit.Event{
			Action: action, TargetType: "queue", TargetID: targetID, TargetName: name,
			RouterID: cn.routerID, Before: before,
			After: map[string]any{
				"name": name, "target": req.Target, "parent": req.Parent,
				"maxLimit": req.MaxLimit, "limitAt": req.LimitAt, "disabled": req.Disabled,
			},
			Extra: qAckExtra(menu, req.Ack),
		})
		// A set can zero a counter, and the next window would otherwise be
		// measured against a baseline the router no longer agrees with.
		cn.rsession.Queues().ForgetRates()
		if cn.rsession.CollectorEnabled("queues") {
			cn.rsession.Queues().RefreshNow()
		}
		outcome := "create"
		if editing {
			outcome = "update"
		}
		cn.srv.hub.Send(cn.c, "queues:ok",
			map[string]any{"action": outcome, "name": name, "menu": menu})
		return nil
	})
	if err != nil {
		cn.qErr(qWriteFail(err), map[string]any{"name": name, "message": safe.Message(err.Error())})
	}
}

func (cn *conn) qRemove(raw json.RawMessage) {
	var req queueRequest
	if json.Unmarshal(raw, &req) != nil {
		cn.qErr("bad-request", nil)
		return
	}
	if cn.routerID == "" || cn.rsession == nil {
		cn.qErr("unavailable", nil)
		return
	}
	menu := req.menu()
	if !cn.qMayWrite() {
		cn.recorder().Denied(audit.Event{
			Action: "queue.delete", TargetType: "queue", RouterID: cn.routerID,
			TargetName: req.ExpectedName, Extra: []audit.KV{{Key: "menu", Value: menu}},
		})
		cn.qErr("denied", nil)
		return
	}
	if req.ID == "" {
		cn.qErr("bad-request", nil)
		return
	}

	err := cn.rsession.InWriteQueue(func() error {
		rows, _, err := cn.qRead(menu)
		if err != nil {
			return err
		}
		target := qRow(rows, req.ID, req.ExpectedName)
		if target == nil {
			cn.qErr("stale-row", nil)
			return nil
		}
		if qDynamic(target) {
			cn.recorder().Denied(audit.Event{
				Action: "queue.delete", TargetType: "queue", RouterID: cn.routerID,
				TargetID: req.ID, TargetName: target["name"], Note: "dynamic-row",
			})
			cn.qErr("dynamic-row", map[string]any{"name": target["name"]})
			return nil
		}

		if _, werr := cn.rsession.Exec(routeros.Cmd{
			Path: queueMenus[menu] + "/remove", Args: []string{"=.id=" + req.ID}}); werr != nil {
			return werr
		}
		where := target["target"]
		if where == "" {
			where = target["parent"]
		}
		cn.recorder().Record(audit.Event{
			Action: "queue.delete", TargetType: "queue", TargetID: req.ID,
			TargetName: target["name"], RouterID: cn.routerID,
			Extra: []audit.KV{{Key: "menu", Value: menu}, {Key: "target", Value: where},
				{Key: "maxLimit", Value: target["max-limit"]}},
		})
		cn.rsession.Queues().ForgetRates()
		if cn.rsession.CollectorEnabled("queues") {
			cn.rsession.Queues().RefreshNow()
		}
		cn.srv.hub.Send(cn.c, "queues:ok",
			map[string]any{"action": "delete", "name": target["name"], "menu": menu})
		return nil
	})
	if err != nil {
		cn.qErr(writeFailCode(err), map[string]any{"message": safe.Message(err.Error())})
	}
}

func (cn *conn) qToggle(raw json.RawMessage) {
	var req queueRequest
	if json.Unmarshal(raw, &req) != nil {
		cn.qErr("bad-request", nil)
		return
	}
	if cn.routerID == "" || cn.rsession == nil {
		cn.qErr("unavailable", nil)
		return
	}
	menu := req.menu()
	if !cn.qMayWrite() {
		cn.recorder().Denied(audit.Event{
			Action: "queue.toggle", TargetType: "queue", RouterID: cn.routerID,
			TargetName: req.ExpectedName, Extra: []audit.KV{{Key: "menu", Value: menu}},
		})
		cn.qErr("denied", nil)
		return
	}
	if req.ID == "" {
		cn.qErr("bad-request", nil)
		return
	}

	err := cn.rsession.InWriteQueue(func() error {
		rows, self, err := cn.qRead(menu)
		if err != nil {
			return err
		}
		target := qRow(rows, req.ID, req.ExpectedName)
		if target == nil {
			cn.qErr("stale-row", nil)
			return nil
		}
		if qDynamic(target) {
			cn.recorder().Denied(audit.Event{
				Action: "queue.toggle", TargetType: "queue", RouterID: cn.routerID,
				TargetID: req.ID, TargetName: target["name"], Note: "dynamic-row",
			})
			cn.qErr("dynamic-row", map[string]any{"name": target["name"]})
			return nil
		}

		wasDisabled := target["disabled"] == "true"
		// ENABLING is the moment a throttle takes effect, and the easy one to
		// miss: the values were checked when the queue was created, but it may
		// have sat disabled ever since. `before` is nil here on purpose — there
		// is no "was it worse before" question when the queue was not in force.
		if menu == "simple" && wasDisabled {
			values := guard.SimpleQueueValues{
				Target:   target["target"],
				MaxLimit: guard.ParsePair(target["max-limit"]),
				Disabled: false,
			}
			v := guard.CheckSimpleQueue(self, values, nil, guard.SelfThrottleFloorBps)
			// One check, not qWarn's two: the original does not distinguish a
			// missing ack from a stale one here, so neither does this.
			if v.Level == "warn" && req.Ack != v.Fingerprint {
				cn.qErr("self-throttle", map[string]any{
					"warning": v.Detail, "fingerprint": v.Fingerprint, "name": target["name"]})
				return nil
			}
		}

		if _, werr := cn.rsession.Exec(routeros.Cmd{
			Path: queueMenus[menu] + "/set",
			Args: []string{"=.id=" + req.ID, "=disabled=" + yesNo(!wasDisabled)}}); werr != nil {
			return werr
		}
		cn.recorder().Record(audit.Event{
			Action: "queue.toggle", TargetType: "queue", TargetID: req.ID,
			TargetName: target["name"], RouterID: cn.routerID,
			Before: map[string]any{"disabled": wasDisabled},
			After:  map[string]any{"disabled": !wasDisabled},
			Extra:  qAckExtra(menu, req.Ack),
		})
		if cn.rsession.CollectorEnabled("queues") {
			cn.rsession.Queues().RefreshNow()
		}
		outcome := "disable"
		if wasDisabled {
			outcome = "enable"
		}
		cn.srv.hub.Send(cn.c, "queues:ok",
			map[string]any{"action": outcome, "name": target["name"], "menu": menu})
		return nil
	})
	if err != nil {
		cn.qErr(writeFailCode(err), map[string]any{"message": safe.Message(err.Error())})
	}
}

func (cn *conn) qResetCounters(raw json.RawMessage) {
	var req queueRequest
	if json.Unmarshal(raw, &req) != nil {
		cn.qErr("bad-request", nil)
		return
	}
	if cn.routerID == "" || cn.rsession == nil {
		cn.qErr("unavailable", nil)
		return
	}
	menu := req.menu()
	if !cn.qMayWrite() {
		cn.recorder().Denied(audit.Event{
			Action: "queue.reset", TargetType: "queue", RouterID: cn.routerID,
			TargetName: req.ExpectedName, Extra: []audit.KV{{Key: "menu", Value: menu}},
		})
		cn.qErr("denied", nil)
		return
	}
	if req.ID == "" {
		cn.qErr("bad-request", nil)
		return
	}

	err := cn.rsession.InWriteQueue(func() error {
		rows, _, err := cn.qRead(menu)
		if err != nil {
			return err
		}
		target := qRow(rows, req.ID, req.ExpectedName)
		if target == nil {
			cn.qErr("stale-row", nil)
			return nil
		}
		if _, werr := cn.rsession.Exec(routeros.Cmd{
			Path: queueMenus[menu] + "/reset-counters",
			Args: []string{"=.id=" + req.ID}}); werr != nil {
			return werr
		}
		cn.recorder().Record(audit.Event{
			Action: "queue.reset", TargetType: "queue", TargetID: req.ID,
			TargetName: target["name"], RouterID: cn.routerID,
			Note: "zeroed the queue statistics", Extra: []audit.KV{{Key: "menu", Value: menu}},
		})
		// MANDATORY here, not merely tidy: the counter just went to zero and the
		// next delta would be measured against the pre-reset baseline.
		cn.rsession.Queues().ForgetRates()
		if cn.rsession.CollectorEnabled("queues") {
			cn.rsession.Queues().RefreshNow()
		}
		cn.srv.hub.Send(cn.c, "queues:ok",
			map[string]any{"action": "reset", "name": target["name"], "menu": menu})
		return nil
	})
	if err != nil {
		cn.qErr(writeFailCode(err), map[string]any{"message": safe.Message(err.Error())})
	}
}

// qMove reorders a simple queue.
//
// Only simple queues have meaningful order — each packet walks the list until
// one matches, so position changes behaviour. Trees are unordered and offer no
// move.
func (cn *conn) qMove(raw json.RawMessage) {
	var req queueRequest
	if json.Unmarshal(raw, &req) != nil {
		cn.qErr("bad-request", nil)
		return
	}
	if cn.routerID == "" || cn.rsession == nil {
		cn.qErr("unavailable", nil)
		return
	}
	if !cn.qMayWrite() {
		cn.recorder().Denied(audit.Event{
			Action: "queue.move", TargetType: "queue", RouterID: cn.routerID,
			TargetName: req.ExpectedName,
		})
		cn.qErr("denied", nil)
		return
	}
	if req.ID == "" || (req.Direction != "up" && req.Direction != "down") {
		cn.qErr("bad-request", nil)
		return
	}

	err := cn.rsession.InWriteQueue(func() error {
		rows, _, err := cn.qRead("simple")
		if err != nil {
			return err
		}
		idx := -1
		for i, r := range rows {
			if r[".id"] == req.ID {
				idx = i
				break
			}
		}
		if idx < 0 {
			cn.qErr("stale-row", nil)
			return nil
		}
		target := qRow(rows, req.ID, req.ExpectedName)
		if target == nil {
			cn.qErr("stale-row", nil)
			return nil
		}

		// RouterOS moves a row to sit BEFORE `destination`. Moving down
		// therefore means "before the row after the next one", and moving the
		// last row down or the first row up is a NO-OP rather than an error.
		destIdx := idx + 2
		if req.Direction == "up" {
			destIdx = idx - 1
		}
		if destIdx < 0 || (idx == len(rows)-1 && req.Direction == "down") {
			cn.srv.hub.Send(cn.c, "queues:ok",
				map[string]any{"action": "move", "name": target["name"], "menu": "simple"})
			return nil
		}
		args := []string{"=.id=" + req.ID}
		if destIdx < len(rows) {
			args = append(args, "=destination="+rows[destIdx][".id"])
		}
		if _, werr := cn.rsession.Exec(routeros.Cmd{Path: "/queue/simple/move", Args: args}); werr != nil {
			return werr
		}

		after := idx + 1
		if req.Direction == "up" {
			after = idx - 1
		}
		cn.recorder().Record(audit.Event{
			Action: "queue.move", TargetType: "queue", TargetID: req.ID,
			TargetName: target["name"], RouterID: cn.routerID,
			Before: map[string]any{"position": idx},
			After:  map[string]any{"position": after},
			Note:   "simple queue order decides which queue a packet matches first",
			Extra:  []audit.KV{{Key: "menu", Value: "simple"}},
		})
		if cn.rsession.CollectorEnabled("queues") {
			cn.rsession.Queues().RefreshNow()
		}
		cn.srv.hub.Send(cn.c, "queues:ok",
			map[string]any{"action": "move", "name": target["name"], "menu": "simple"})
		return nil
	})
	if err != nil {
		cn.qErr(writeFailCode(err), map[string]any{"message": safe.Message(err.Error())})
	}
}
