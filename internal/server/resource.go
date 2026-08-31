package server

// The write path: res:save and res:remove.
//
// THE ORDER OF THE CHECKS IS THE SAFETY ARGUMENT, and it is preserved from
// src/index.js rather than rearranged for readability:
//
//	permission → validate → READ THE MENU FRESH → find the row → is it still
//	the row the operator saw → may this row be edited at all → write
//
// The row is ADDRESSED by the `.id` the browser sends and AUTHORISED by nothing
// the browser sends. Both the staleness check and the read-only check run
// against the row as the router has it right now, never against the browser's
// claim about it — which is why the read happens before them and not once at
// page load.
//
// TWO GAPS, BOTH CUTOVER BLOCKERS, BOTH RECORDED RATHER THAN QUIETLY ACCEPTED:
//
//  1. PERMISSION IS COARSER THAN NODE'S. Node requires page-write AND
//     `router:write` for this router. `/api/auth/status` exposes neither
//     per-router page access nor router:write — see the long note in auth.go —
//     so this checks page-write unioned across readable routers, intersected
//     with "may read this router". Exact wherever a principal's access does not
//     vary between routers; over-permissive where it does.
//
//  2. Writes and denials ARE recorded now, in the same audit_events table Node
//     writes to — see internal/db for why a second writer is safe here and why
//     this side never migrates. Secrets are masked by FIELD TYPE before anything
//     is diffed (auditValues, in audit.go): a resource field is named for the
//     form, so `wpa2PreSharedKey` matches no credential name pattern, and the
//     type declaration is what actually keeps a passphrase out of the trail.

import (
	"encoding/json"
	"log"
	"sort"
	"strconv"
	"strings"

	"mikrodash/internal/audit"
	"mikrodash/internal/guard"
	"mikrodash/internal/history"
	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
)

type resRequest struct {
	Resource         string `json:"resource"`
	ID               string `json:"id"`
	ExpectedIdentity string `json:"expectedIdentity"`
	// `any`, not `string`. The browser sends a checkbox as a JSON boolean and a
	// number field as a JSON number, and res:row hands back booleans for every
	// bool field — so a strict map[string]string fails to unmarshal the very
	// values this server just produced, and the whole request vanishes. The
	// Node side never had the problem because JavaScript coerces on the way in;
	// this reproduces that coercion explicitly in `strValues`.
	Values map[string]any `json:"values"`
	// Ack is the fingerprint of a warning the operator has seen and accepted.
	Ack string `json:"ack"`
	// Direction and Anchor are res:move's two spellings — an arrow says which
	// way, a drag says which row to land before. HasAnchor distinguishes "land
	// at the end" (an empty anchor, deliberately) from "no anchor sent", which
	// is what `hasOwnProperty(r, 'anchor')` does on the Node side.
	Direction string `json:"direction"`
	Anchor    string `json:"anchor"`
	HasAnchor bool   `json:"-"`
	// Action names a verb from the resource's registry entry. Looked up there,
	// never used as a command word directly.
	Action string `json:"action"`
}

func (cn *conn) resErr(res, code, name string, extra map[string]any) {
	m := map[string]any{"resource": res, "code": code}
	if name != "" {
		m["name"] = name
	}
	for k, v := range extra {
		m[k] = v
	}
	cn.srv.hub.Send(cn.c, "res:error", m)
}

// resolve turns a browser request into a resource this connection may write,
// or reports why not. A nil return means the caller must stop.
// strValues flattens what the browser sent to the strings the validator works
// in, matching JavaScript's String() for the shapes that actually arrive.
// A null becomes "", which validate() then treats as blank.
func (r *resRequest) strValues() map[string]string {
	out := make(map[string]string, len(r.Values))
	for k, v := range r.Values {
		switch t := v.(type) {
		case nil:
			out[k] = ""
		case string:
			out[k] = t
		case bool:
			out[k] = strconv.FormatBool(t)
		case float64:
			// JavaScript renders an integral float without a decimal point, and
			// a PVID must reach the router as "5" rather than "5.000000".
			out[k] = strconv.FormatFloat(t, 'f', -1, 64)
		default:
			b, _ := json.Marshal(t)
			out[k] = string(b)
		}
	}
	return out
}

// resolve is the shared gate: parse, look the resource up, require a router and
// the write permission.
//
// auditDenied is the CALLER's choice and not a property of the gate, because
// index.js draws the line there: res:save, res:remove and res:action record a
// denial, while res:new, res:row and res:preview refuse silently. Opening a form
// is not an attempt to write one, and a "create denied" row every time someone
// clicks Add on a page they can only read would be noise in the one table that
// cannot be pruned selectively.
func (cn *conn) resolve(raw json.RawMessage, auditDenied bool) (*resource.Resource, *resRequest) {
	var req resRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		// Never silent. A dropped request looks exactly like a Save button that
		// does nothing, which is the worst way for this to fail.
		log.Printf("[res] cannot parse a request: %v", err)
		cn.srv.hub.Send(cn.c, "res:error", map[string]any{"code": "bad-request"})
		return nil, nil
	}
	res := resource.ByKey(req.Resource)
	if res == nil {
		return nil, nil // an unknown key is a refusal, never a default
	}
	if cn.routerID == "" || cn.rsession == nil {
		cn.resErr(res.Key, "unavailable", "", nil)
		return nil, nil
	}
	if !cn.canPage(res.Page, "write") {
		// The action names the verb the user attempted, not the check that
		// refused it — a trail of "denied" rows says nothing about what was
		// being tried. Matches index.js, which derives it the same way before
		// the permission check rather than after.
		if auditDenied {
			what := "create"
			if req.ID != "" {
				what = "update"
			}
			cn.recorder().Denied(audit.Event{
				Action: res.Key + "." + what, TargetType: res.Key, RouterID: cn.routerID,
				TargetID: req.ID, TargetName: req.ExpectedIdentity,
			})
		}
		cn.resErr(res.Key, "denied", "", nil)
		return nil, nil
	}
	return res, &req
}

// readMenu reads every row, with NO proplist: readOnlyWhen needs fields no page
// asked for, and this runs once per write rather than once per tick.
func (cn *conn) readMenu(res *resource.Resource) ([]routeros.Reply, error) {
	rows, err := cn.rsession.Exec(routeros.Cmd{Path: res.Menu + "/print"})
	if err != nil {
		return nil, err
	}
	out := make([]routeros.Reply, 0, len(rows))
	for _, r := range rows {
		if r[".id"] != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

// find addresses by id and identifies by the resource's identity field. A row
// whose identity no longer matches is treated as gone, because it is no longer
// the row the operator was looking at.
func find(res *resource.Resource, rows []routeros.Reply, id, expected string) routeros.Reply {
	for _, r := range rows {
		if r[".id"] != id {
			continue
		}
		if expected != "" && res.IdentityOf(r) != expected {
			return nil
		}
		return r
	}
	return nil
}

func (cn *conn) resSave(raw json.RawMessage) {
	res, req := cn.resolve(raw, true)
	if res == nil {
		return
	}
	editing := req.ID != ""

	validated, errs := res.Validate(req.strValues(), editing)
	if len(errs) > 0 {
		cn.resErr(res.Key, "invalid", "", map[string]any{"errors": errs})
		return
	}
	// A COMPOSITE identity yields no name here, and that is the original's
	// behaviour rather than an omission: `validated.values[resource.identity]`
	// with an array subscript is a key miss in JavaScript, so a firewall rule
	// falls through to the identity the browser round-tripped. On a create there
	// is none, and the audit row carries no name — which is honest, because a
	// firewall rule does not have one.
	name := ""
	if len(res.Identity) == 1 {
		name = validated.Values[res.Identity[0]]
	}
	if name == "" {
		name = req.ExpectedIdentity
	}

	err := cn.rsession.InWriteQueue(func() error {
		rows, err := cn.readMenu(res)
		if err != nil {
			return err
		}
		// The ids present BEFORE the write, so a create can find the row it made.
		seenIDs := make(map[string]bool, len(rows))
		for _, r := range rows {
			seenIDs[r[".id"]] = true
		}
		var before routeros.Reply
		if editing {
			before = find(res, rows, req.ID, req.ExpectedIdentity)
			if before == nil {
				cn.resErr(res.Key, "stale-row", name, nil)
				return nil
			}
		}
		if before != nil && res.ReadOnlyWhen != nil && res.ReadOnlyWhen(before) {
			cn.recorder().Denied(audit.Event{
				Action: res.Key + ".update", TargetType: res.Key, RouterID: cn.routerID,
				TargetID: req.ID, TargetName: name, Note: "read-only-row",
			})
			cn.resErr(res.Key, res.ReadOnlyReason, name, nil)
			return nil
		}

		// The guard runs AFTER the fresh read and BEFORE the write, so the
		// verdict is about the row as it is now rather than as the browser
		// remembers it.
		what := "update"
		if !editing {
			what = "create"
		}
		verdict, gerr := cn.verdictFor(res, what, validated.Values, before)
		if gerr != nil {
			// No equivalent in Node, which has every guard. Recorded as a
			// denial rather than only logged, because "the port refused a write
			// it could not check" is exactly the kind of gap that must be
			// visible in the trail rather than in a container log nobody reads.
			cn.recorder().Denied(audit.Event{
				Action: res.Key + "." + what, TargetType: res.Key, RouterID: cn.routerID,
				TargetID: req.ID, TargetName: name, Note: "guard-not-ported: " + gerr.Error(),
			})
			cn.resErr(res.Key, "guard-not-ported", name,
				map[string]any{"message": safe.Message(gerr.Error())})
			return nil
		}
		if gate := ackGate(verdict, req.Ack); gate != nil {
			gate["resource"] = res.Key
			gate["name"] = name
			cn.srv.hub.Send(cn.c, "res:error", gate)
			return nil
		}

		args := res.BuildArgs(validated)
		verb := "/add"
		if editing {
			verb = "/set"
			args = append([]string{"=.id=" + req.ID}, args...)
		}
		if _, err := cn.rsession.Exec(routeros.Cmd{Path: res.Menu + verb, Args: args}); err != nil {
			return err
		}

		action := "create"
		if editing {
			action = "update"
		}

		// BOTH SIDES GO THROUGH auditValues, which masks by field type. On the
		// `before` side that is a no-op — RowValues already drops secrets, since
		// the router's stored value is never read back into a form — and it runs
		// anyway so the two sides are built the same way and cannot drift apart.
		//
		// A create has NO before, and `{}` rather than nil is the difference
		// between "every field appeared" and "nothing to compare": Diff only
		// walks keys present in `after`, so an empty before reports the whole
		// row as new, which is what a create is.
		//
		// The bool quirk is NORMALISED, in both places. RowValues gives a real
		// boolean and Validate gives the string "yes"/"no", so `false` against
		// `"no"` once read as a change and every save of every resource carrying
		// a checkbox recorded one nobody made. This port found it, reported it,
		// and the live app fixed it in `_resAuditValues`; `auditValues` here is
		// re-synced to that, and `TestUnchangedCheckboxIsNotAChange` pins it.
		// The id the row NOW has. A create does not know it — RouterOS assigns
		// one — so the table is diffed against itself rather than the new row
		// being assumed last. Only undo needs this, which is why nothing read it
		// before; the audit row addresses a create by its name.
		newID := req.ID
		if !editing {
			newID = ""
			if after, rerr := cn.readMenu(res); rerr == nil {
				for _, r := range after {
					if !seenIDs[r[".id"]] {
						newID = r[".id"]
						break
					}
				}
			}
		}
		if newID != "" {
			var beforeHist map[string]string
			if before != nil {
				beforeHist = histValues(res.RowValues(before))
			}
			cn.histPush(res.Key, history.Build(res.Key, res.Label, action,
				newID, name, beforeHist, validated.Values))
		}

		var beforeVals map[string]any
		if before != nil {
			beforeVals = auditValues(res, res.RowValues(before))
		} else {
			beforeVals = map[string]any{}
		}
		cn.recorder().Record(audit.Event{
			Action: res.Key + "." + action, TargetType: res.Key, RouterID: cn.routerID,
			TargetID: req.ID, TargetName: name,
			Before: beforeVals,
			After:  auditValues(res, stringValuesAsAny(validated.Values)),
			Extra:  ackExtra(req.Ack),
		})

		cn.refreshFor(res)
		cn.srv.hub.Send(cn.c, "res:ok", map[string]any{
			"resource": res.Key, "action": action, "name": name})
		return nil
	})
	if err != nil {
		cn.resErr(res.Key, writeFailCode(err), name, map[string]any{"message": safe.Message(err.Error())})
	}
}

func (cn *conn) resRemove(raw json.RawMessage) {
	res, req := cn.resolve(raw, true)
	if res == nil {
		return
	}
	if req.ID == "" {
		cn.resErr(res.Key, "invalid", "", nil)
		return
	}
	// Only until the row is read. Everything after `find` uses the identity of
	// the row the SERVER actually found — see below.
	name := req.ExpectedIdentity

	err := cn.rsession.InWriteQueue(func() error {
		rows, err := cn.readMenu(res)
		if err != nil {
			return err
		}
		row := find(res, rows, req.ID, req.ExpectedIdentity)
		if row == nil {
			cn.resErr(res.Key, "stale-row", name, nil)
			return nil
		}
		// ── THE AUDIT NAME COMES FROM THE ROW, NOT FROM THE REQUEST ────────
		//
		// This used `req.ExpectedIdentity` throughout, which is CLIENT-SUPPLIED
		// AND OPTIONAL. A `res:remove` that omits it — nothing requires it, and
		// `find` accepts an empty one — produced an audit row saying a dnsStatic
		// was deleted and not WHICH: `target_name` empty, on the one record that
		// exists to answer exactly that question. Measured against hAP AC2 on
		// 2026-08-29 by performing a real delete.
		//
		// The live app never had this: `const name = Resources.identityOf(
		// resource, before)` (`src/index.js` res:remove), computed from the row
		// it just read, and used for the success record and all three denials.
		//
		// It is also the right SOURCE and not merely a non-empty one. An audit
		// trail records what the server observed; taking the name from the
		// request records what the caller asserted. A mismatched identity is
		// already refused as `stale-row`, so this changes no verdict — it
		// changes what the record is a record OF.
		if n := res.IdentityOf(row); n != "" {
			name = n
		}
		if res.ReadOnlyWhen != nil && res.ReadOnlyWhen(row) {
			cn.recorder().Denied(audit.Event{
				Action: res.Key + ".delete", TargetType: res.Key, RouterID: cn.routerID,
				TargetID: req.ID, TargetName: name, Note: "read-only-row",
			})
			cn.resErr(res.Key, res.ReadOnlyReason, name, nil)
			return nil
		}
		// EDITABLE BUT NOT REMOVABLE — a wireless radio is hardware, and its row
		// exists whether or not anyone wants it to. ReadOnlyWhen cannot say this
		// because it would block the edit too. Checked on the freshly-read row,
		// for the same reason ReadOnlyWhen is.
		if res.RemovableWhen != nil && !res.RemovableWhen(row) {
			cn.recorder().Denied(audit.Event{
				Action: res.Key + ".delete", TargetType: res.Key, RouterID: cn.routerID,
				TargetID: req.ID, TargetName: name, Note: "not-removable",
			})
			cn.resErr(res.Key, "not-removable", name, nil)
			return nil
		}
		// A delete always counts for the guard: removing a port and disabling
		// it cut the same link.
		verdict, gerr := cn.verdictFor(res, "delete", nil, row)
		if gerr != nil {
			cn.recorder().Denied(audit.Event{
				Action: res.Key + ".delete", TargetType: res.Key, RouterID: cn.routerID,
				TargetID: req.ID, TargetName: name, Note: "guard-not-ported: " + gerr.Error(),
			})
			cn.resErr(res.Key, "guard-not-ported", name,
				map[string]any{"message": safe.Message(gerr.Error())})
			return nil
		}
		if gate := ackGate(verdict, req.Ack); gate != nil {
			gate["resource"] = res.Key
			gate["name"] = name
			cn.srv.hub.Send(cn.c, "res:error", gate)
			return nil
		}
		if _, err := cn.rsession.Exec(routeros.Cmd{
			Path: res.Menu + "/remove", Args: []string{"=.id=" + req.ID}}); err != nil {
			return err
		}
		// Recorded BEFORE the audit row and from the row as it was, because the
		// row is gone now and its values are the only way back.
		cn.histPush(res.Key, history.Build(res.Key, res.Label, "delete",
			req.ID, name, histValues(res.RowValues(row)), nil))

		// after is `{}` for a delete: Diff walks the keys of `after`, so an empty
		// one reports NOTHING changed, which is right — a delete is described by
		// the row that went away, and the row itself is the target, not a diff.
		// Matches index.js, which passes `after: {}` here for the same reason.
		cn.recorder().Record(audit.Event{
			Action: res.Key + ".delete", TargetType: res.Key, RouterID: cn.routerID,
			TargetID: req.ID, TargetName: name,
			Before: auditValues(res, res.RowValues(row)),
			After:  map[string]any{},
			Extra:  ackExtra(req.Ack),
		})

		cn.refreshFor(res)
		cn.srv.hub.Send(cn.c, "res:ok", map[string]any{
			"resource": res.Key, "action": "delete", "name": name})
		return nil
	})
	if err != nil {
		cn.resErr(res.Key, writeFailCode(err), name, map[string]any{"message": safe.Message(err.Error())})
	}
}

// ackExtra records that an operator confirmed a warned-about write, which is
// the one piece of context a row cannot be reconstructed without: the same edit
// with and without an acknowledgement are different acts.
// Ack is the warning FINGERPRINT the browser echoed back, not a flag, and the
// test is emptiness — exactly what `r.ack ? {...} : undefined` does on the Node
// side. Whether it MATCHED is ackGate's business, and by the time this runs it
// has already been checked.
func ackExtra(ack string) []audit.KV {
	if ack == "" {
		return nil
	}
	return []audit.KV{{Key: "selfCutoffAcknowledged", Value: true}}
}

// writeFailCode separates "the router refused" from "we could not reach it".
// The page says different things about them, and conflating the two makes a
// permissions problem look like an outage.
func writeFailCode(err error) string {
	m := strings.ToLower(err.Error())
	switch {
	case strings.Contains(m, "not connected"):
		return "unavailable"
	case strings.Contains(m, "not enough permissions"), strings.Contains(m, "permission denied"):
		return "router-denied"
	case strings.Contains(m, "no such item"):
		return "stale-row"
	default:
		return "write-failed"
	}
}

// resRow fills the edit form from a FRESH read of the router, not from the
// collector's payload.
//
// The distinction is not pedantry. A collector reads with a proplist narrow
// enough for the page it feeds — the DNS one asks for eight columns — so a form
// populated from it would silently blank every property the page does not
// display. On dnsStatic that is match-subdomain, cname, forward-to and text:
// saving would then clear whichever of them the row actually had.
//
// It also re-derives `readOnly` here rather than trusting the browser, so the
// form opens read-only because the ROUTER's row says so.
// resSchema hands the browser one resource's form definition, plus the three
// things about it that only the SOCKET can answer.
//
// The schema itself is registry data and was served over HTTP until now. That
// was wrong for one reason: `permitted` depends on the SELECTED ROUTER, and an
// HTTP request does not have one. The page draws its Add button from
// `permitted` rather than from the collector payload, because the payload is
// shared by every viewer of the router and so can never answer "may YOU write
// this". Until this moved, a read-only viewer saw Add buttons on every ported
// page and found out by clicking.
//
// Gated on READ, not write: a viewer who may see the page must get the schema,
// or the table cannot render at all. The write question is answered IN the
// reply instead of by refusing it.
func (cn *conn) resSchema(raw json.RawMessage) {
	var req resRequest
	if json.Unmarshal(raw, &req) != nil {
		cn.srv.hub.Send(cn.c, "res:error", map[string]any{"code": "bad-request"})
		return
	}
	res := resource.ByKey(req.Resource)
	if res == nil {
		return
	}
	if cn.routerID == "" || cn.rsession == nil {
		cn.resErr(res.Key, "unavailable", "", nil)
		return
	}
	if !cn.canPage(res.Page, "read") {
		cn.resErr(res.Key, "denied", "", nil)
		return
	}

	// One read, once per connect, for the one resource that asks for it.
	unsupported := false
	if res.RequiresMenu != "" {
		if _, err := cn.rsession.Exec(routeros.Cmd{
			Path: res.RequiresMenu + "/print",
			Args: []string{"=.proplist=.id"},
		}); err != nil {
			unsupported = true
		}
	}

	out := res.Describe()
	out["permitted"] = !unsupported && cn.canPage(res.Page, "write")
	out["unsupported"] = unsupported
	out["ordered"] = res.Ordered
	cn.srv.hub.Send(cn.c, "res:schema", out)
	// So the undo and redo buttons start out grey rather than absent.
	cn.histEmit(res.Key)
}

// resAction runs a named verb against one row.
//
// A NAMED VERB IS STILL A WRITE, and this path takes the same route as a save:
// a fresh read, a staleness check, the row's own opinion of whether the verb
// applies, the guard, an audit row and a refresh. index.js says why in the
// firewall's terms — enabling a rule has exactly the blast radius of creating
// it, and disabling the accept that lets us in is the other half of a lockout.
//
// The action is looked up in the REGISTRY, never taken from the browser: `verb`
// becomes a RouterOS command word, so accepting one from the wire would let a
// caller name any command under the resource's menu.
func (cn *conn) resAction(raw json.RawMessage) {
	res, req := cn.resolve(raw, true)
	if res == nil {
		return
	}
	def := res.ActionByKey(req.Action)
	if def == nil || req.ID == "" {
		cn.resErr(res.Key, "bad-request", "", nil)
		return
	}
	action := res.Key + "." + def.Key

	err := cn.rsession.InWriteQueue(func() error {
		rows, err := cn.readMenu(res)
		if err != nil {
			return err
		}
		row := find(res, rows, req.ID, req.ExpectedIdentity)
		if row == nil {
			cn.resErr(res.Key, "stale-row", "", nil)
			return nil
		}
		name := res.IdentityOf(row)

		// Judged on the ROW as the router has it, not on the browser's claim
		// that the button was showing.
		if def.When != nil && !def.When(row) {
			cn.recorder().Denied(audit.Event{
				Action: action, TargetType: res.Key, RouterID: cn.routerID,
				TargetID: req.ID, TargetName: name, Note: "not-applicable",
			})
			cn.resErr(res.Key, "not-applicable", name, nil)
			return nil
		}

		verdict, gerr := cn.verdictFor(res, def.Key, histValues(res.RowValues(row)), row)
		if gerr != nil {
			cn.recorder().Denied(audit.Event{
				Action: action, TargetType: res.Key, RouterID: cn.routerID,
				TargetID: req.ID, TargetName: name, Note: "guard-not-ported: " + gerr.Error(),
			})
			cn.resErr(res.Key, "guard-not-ported", name,
				map[string]any{"message": safe.Message(gerr.Error())})
			return nil
		}
		if gate := ackGate(verdict, req.Ack); gate != nil {
			gate["resource"] = res.Key
			gate["name"] = name
			cn.srv.hub.Send(cn.c, "res:error", gate)
			return nil
		}

		if _, err := cn.rsession.Exec(routeros.Cmd{
			Path: res.Menu + "/" + def.Verb, Args: []string{"=.id=" + req.ID}}); err != nil {
			return err
		}

		// enable and disable invert each other, so they are recorded. A verb
		// with no inverse — make-static — yields nothing, and history.Build says
		// so by returning nil.
		cn.histPush(res.Key, history.Build(res.Key, res.Label, def.Key, req.ID, name, nil, nil))

		cn.recorder().Record(audit.Event{
			Action: action, TargetType: res.Key, RouterID: cn.routerID,
			TargetID: req.ID, TargetName: name, Note: def.Note,
		})

		cn.refreshFor(res)
		cn.srv.hub.Send(cn.c, "res:ok", map[string]any{
			"resource": res.Key, "action": def.Key, "name": name})
		return nil
	})
	if err != nil {
		cn.resErr(res.Key, writeFailCode(err), "", map[string]any{"message": safe.Message(err.Error())})
	}
}

// resNew opens a blank Add form.
//
// Its ONLY job is the pickers. They are read when the form opens rather than
// shipped with the schema, because the schema is requested for every resource
// on connect and that would be a burst of router reads nobody asked for — and
// because a bridge added a minute ago should be in the list.
//
// It was missing from this port until the route declaration went in, and its
// absence was invisible: `resRow` carries options, so the EDIT form on a page
// with a picker was correct while the ADD form on the same page silently
// rendered a text box. Every gate this project has looks at a page or at a
// declaration; neither opens a blank form.
//
// Gated on write like resRow, for the same reason: opening the Add form is the
// first half of a create.
func (cn *conn) resNew(raw json.RawMessage) {
	res, _ := cn.resolve(raw, false)
	if res == nil {
		return
	}
	cn.srv.hub.Send(cn.c, "res:new", map[string]any{
		"resource": res.Key,
		"options":  cn.resOptions(res),
	})
}

// resPreview answers `res:preview` — the RouterOS command this form WOULD issue.
//
// ── IT VALIDATES FIRST, AND REFUSES RATHER THAN PREVIEWING A BAD FORM ───────
//
// The original returns `invalid` with the field errors instead of a command, and
// that is the useful behaviour: a preview built from unvalidated input would
// show a command the Save button will not send.
//
// ── GATED ON WRITE ─────────────────────────────────────────────────────────
//
// A preview names the exact command, the menu and every value — it is a
// description of a write, so it is a write-level question. `resolve(raw, false)`
// applies the same permission check the rest of this file does, and refuses
// silently for the same reason `res:new` and `res:row` do (see this file's
// header): a reader who cannot write has no form open to be told about.
//
// The secret masking is `PreviewCommand`'s, not this handler's — see
// internal/resource for why it keys on the FIELD and not the value.
func (cn *conn) resPreview(raw json.RawMessage) {
	res, req := cn.resolve(raw, false)
	if res == nil {
		return
	}
	validated, errs := res.Validate(req.strValues(), req.ID != "")
	if len(errs) > 0 {
		cn.resErr(res.Key, "invalid", "", map[string]any{"errors": errs})
		return
	}
	cn.srv.hub.Send(cn.c, "res:preview", map[string]any{
		"resource": res.Key,
		"command":  res.PreviewCommand(validated, req.ID),
	})
}

func (cn *conn) resRow(raw json.RawMessage) {
	res, req := cn.resolve(raw, false)
	if res == nil {
		return
	}
	if req.ID == "" {
		cn.resErr(res.Key, "bad-request", "", nil)
		return
	}
	rows, err := cn.readMenu(res)
	if err != nil {
		cn.resErr(res.Key, writeFailCode(err), "", map[string]any{"message": safe.Message(err.Error())})
		return
	}
	row := find(res, rows, req.ID, req.ExpectedIdentity)
	if row == nil {
		cn.resErr(res.Key, "stale-row", "", nil)
		return
	}
	cn.srv.hub.Send(cn.c, "res:row", map[string]any{
		"resource": res.Key,
		"id":       req.ID,
		"identity": res.IdentityOf(row),
		"readOnly": res.ReadOnlyWhen != nil && res.ReadOnlyWhen(row),
		"actions":  res.ActionsFor(row),
		"values":   res.RowValues(row),
		"options":  cn.resOptions(res),
	})
}

// resOptions builds the picker lists a form needs.
//
// Each menu is read ONCE per form open and shared between the fields that name
// it — /interface backs both the VLAN parent and the bridge port, and reading it
// twice would be silly.
//
// EVERY READ FAILS SOFT. A menu the API user cannot see, or that this RouterOS
// build does not have, yields no options and the field renders as the text box
// it always was. A picker is a convenience; it must never be the thing that
// stops a write.
func (cn *conn) resOptions(res *resource.Resource) map[string][]string {
	out := res.StaticOptions()
	if cn.rsession == nil {
		return out
	}
	menus := map[string][]routeros.Reply{}
	failed := map[string]bool{}
	for _, src := range res.OptionSources() {
		if _, seen := menus[src.Menu]; !seen && !failed[src.Menu] {
			rows, err := cn.rsession.Exec(routeros.Cmd{Path: src.Menu + "/print"})
			if err != nil {
				failed[src.Menu] = true
			} else {
				menus[src.Menu] = rows
			}
		}
		rows, ok := menus[src.Menu]
		if !ok {
			continue
		}
		var vals []string
		seenVal := map[string]bool{}
		for _, r := range rows {
			v := strings.TrimSpace(r[src.Value])
			if v == "" || seenVal[v] {
				continue
			}
			seenVal[v] = true
			vals = append(vals, v)
		}
		if len(vals) > 0 {
			// Plain lexicographic, matching JavaScript's bare `.sort()`. NOT
			// Collate: that reproduces localeCompare, which the live app uses
			// for TABLE ordering and deliberately not here.
			sort.Strings(vals)
			out[src.Field] = vals
		}
	}
	return out
}

// managementPath asks the router where it sees us from.
//
// BOTH READS FAIL SOFT. /user/active is denied to the read-only API user the
// README recommends — that is the COMMON case, not an edge one — and a menu the
// API user cannot see must cost the warning, never the write. An unresolved
// path means "no warning", which is not the same as "no risk", and the comment
// on guard.ManagementPath says so at more length.
//
// Read in the same tick as the write is checked, deliberately: a collector's
// copy of the address table can be minutes old, and this question is about
// right now.
func (cn *conn) managementPath() guard.ManagementPath {
	var active, addrs []routeros.Reply
	if rows, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/active/print"}); err == nil {
		active = rows
	}
	// No proplist: selfPath needs `actual-interface`, which no page asks for.
	// It differs from `interface` exactly where it matters — an address on a
	// bridge reports the physical port as the actual one.
	if rows, err := cn.rsession.Exec(routeros.Cmd{Path: "/ip/address/print"}); err == nil {
		addrs = rows
	}
	return guard.ResolveManagementInterfaces(active, addrs, []string{cn.rsession.Username()})
}

// ackGate turns a verdict into a refusal the page can act on, or nil to proceed.
//
// A warning is shown once and acknowledged by its FINGERPRINT, which is
// recomputed from a fresh read on the retry — so an acknowledgement cannot be
// carried from one row to another or replayed against a different write. An ack
// that no longer matches is `stale-warning`: the ground moved between the
// prompt and the answer, and the operator must look again.
func ackGate(v guard.Verdict, ack string) map[string]any {
	if !v.Warned() {
		return nil
	}
	detail := map[string]any{"warning": v.Detail, "fingerprint": v.Fingerprint}
	if ack == "" {
		detail["code"] = v.Code
		return detail
	}
	if ack != v.Fingerprint {
		detail["code"] = "stale-warning"
		return detail
	}
	return nil
}

// ported names the guards this server can actually evaluate. A resource
// declaring anything else cannot be written through here — see verdictFor.
var portedGuards = map[string]bool{
	"selfPath": true, "fwGuard": true, "wifiInherit": true, "capsmanPush": true,
}

// errUnportedGuard is returned when a resource declares a guard this server
// cannot evaluate.
type errUnportedGuard struct{ kind string }

func (e errUnportedGuard) Error() string { return "guard not ported: " + e.kind }

// verdictFor runs whichever guards the resource declares. The first warn wins,
// because a second dialog after the first is answered is how somebody learns to
// click both without reading either.
//
// AN UNPORTED GUARD REFUSES THE WRITE. It would be easy to log and proceed, and
// that is exactly wrong: guards are ported just-in-time with the page that needs
// them, so "declared but not ported" is a state this server will be in
// routinely, and the failure mode of proceeding is a write that silently skips
// the check the live app makes. Refusing turns that into a visible blocker —
// the same rule the fixture gate applies to collectors, applied to safety
// checks: a gap is reported, never quietly tolerated.
func (cn *conn) verdictFor(res *resource.Resource, action string, values, before map[string]string) (guard.Verdict, error) {
	for _, kind := range res.Guard {
		if !portedGuards[kind] {
			return guard.Verdict{}, errUnportedGuard{kind}
		}
	}
	// EACH GUARD DECIDES WHAT IT NEEDS. The interface-target shortcut below is
	// selfPath's alone: it asks which interface carries us, so an edit naming no
	// interface cannot concern it. fwGuard asks whether a RULE could match our
	// traffic, and a rule that names no interface is the loudest case there —
	// `chain=input action=drop` matches everything. Returning early on empty
	// targets for both would have silenced the guard on exactly the write it
	// exists for.
	for _, kind := range res.Guard {
		switch kind {
		case "selfPath":
			targets := res.GuardTargets(action, values, before)
			if len(targets) == 0 {
				continue
			}
			if v := guard.CheckInterfaceEdit(cn.managementPath(), targets, action); v.Warned() {
				return v, nil
			}
		case "fwGuard":
			if v := cn.fwVerdict(res, action, values, before); v.Warned() {
				return v, nil
			}
		case "wifiInherit":
			if v := cn.wifiVerdict(res, action, values, before); v.Warned() {
				return v, nil
			}
		case "capsmanPush":
			if v := cn.capsVerdict(res, action, values, before); v.Warned() {
				return v, nil
			}
		}
	}
	return guard.Verdict{Level: "none"}, nil
}

// fwVerdict asks the lockout guard about one firewall write.
//
// The management path is read FRESH here rather than taken from a collector: it
// is the same tick as the write, and /user/active is what says where the router
// sees us from. A router that denies it yields no addresses and the guard fails
// open, which is the common case rather than an edge one.
func (cn *conn) fwVerdict(res *resource.Resource, action string,
	values, before map[string]string) guard.Verdict {

	path := cn.managementPath()
	ctx := guard.FWContext{
		Resolved: path.Resolved, Addresses: path.Addresses, Interfaces: path.Interfaces,
		APIPort: cn.rsession.APIPort(),
	}
	// `before` arrives RAW — the row as the router returned it, keyed by
	// RouterOS property names — because that is what selfPath's GuardTargets
	// needs. fwGuard reads registry field names, so it is converted here rather
	// than at the call sites, which is also what the original does
	// (`Resources.rowValues(resource, before)` at its own fwGuard branch).
	var beforeRule *guard.FWRule
	if before != nil {
		b := fwRuleFrom(histValues(res.RowValues(before)))
		beforeRule = &b
	}
	return guard.CheckRule(ctx, res.Menu, fwRuleFrom(values), beforeRule, action)
}

// wifiVerdict asks the inherited-profile guard about one wireless write.
//
// NO /user/active READ HERE, unlike the other two guards: this one is answered
// entirely from the menu the write is already about. `siblings` is every row in
// it, read FRESH, so the share count comes from the same tick as the write
// rather than from the collector's last one — a profile can gain or lose a
// follower between ticks, and the count is the whole question.
func (cn *conn) wifiVerdict(res *resource.Resource, action string,
	values, before map[string]string) guard.Verdict {

	if before == nil {
		// A create overrides nothing: there is no existing row whose values came
		// from a profile. CheckInherit says the same, but reading the menu to
		// learn it would be a round trip per create.
		return guard.Verdict{Level: "none"}
	}
	rows, err := cn.readMenu(res)
	if err != nil {
		// FAIL OPEN, like the guard itself: a menu this write is about that
		// cannot be re-read costs the warning, never the write.
		return guard.Verdict{Level: "none"}
	}
	set := map[string]bool{}
	for k := range values {
		set[k] = true
	}
	return guard.CheckInherit(guard.WifiValues{Values: values, Set: set},
		routeros.Reply(before), rows, action)
}

// capsVerdict asks the fleet-push guard about one CAPsMAN profile write.
//
// TWO MENUS THIS WRITE IS NOT ABOUT are read here, in the same tick as the write
// is checked: which configurations name this profile, and which provisioning
// rules name those configurations. The collector's copy can be two minutes old,
// and a rule enabled since then is the difference between a silent save and a
// fleet-wide push.
//
// BOTH READS FAIL SOFT. A menu the API user cannot see costs the warning, never
// the write — the guard fails open by design, and refusing to write because a
// warning could not be computed would be the wrong trade for something advisory.
func (cn *conn) capsVerdict(res *resource.Resource, action string,
	values, before map[string]string) guard.Verdict {

	soft := func(path string) []routeros.Reply {
		rows, err := cn.rsession.Exec(routeros.Cmd{Path: path})
		if err != nil {
			return nil
		}
		return rows
	}
	// The CAP count is advisory: it only fills a number in the sentence.
	caps := -1
	if rows, err := cn.rsession.Exec(routeros.Cmd{
		Path: "/interface/wifi/capsman/remote-cap/print",
		Args: []string{"=.proplist=.id"}}); err == nil {
		caps = len(rows)
	}

	return guard.CheckPush(guard.CapsPushInput{
		ResourceKey: res.Key, Action: action, Values: values,
		Before:     routeros.Reply(before),
		ConfigRows: soft("/interface/wifi/configuration/print"),
		ProvRows:   soft("/interface/wifi/provisioning/print"),
		CapCount:   caps,
	})
}

// fwRuleFrom reads a rule in the registry's field names, which is the shape
// both `values` and `rowValues(before)` arrive in.
func fwRuleFrom(v map[string]string) guard.FWRule {
	return guard.FWRule{
		Chain: v["chain"], Action: v["action"],
		SrcAddress: v["srcAddress"], DstAddress: v["dstAddress"],
		Protocol: v["protocol"], DstPort: v["dstPort"], InInterface: v["inInterface"],
		// Validated values carry RouterOS spellings, so a checkbox reads "yes";
		// a freshly-read row reads "true". Both mean disabled.
		Disabled: v["disabled"] == "yes" || v["disabled"] == "true",
	}
}

// refreshFor re-reads the collector behind a resource's page, so the table shows
// what the router did rather than what it was asked to do.
//
// Keyed on the resource's PAGE rather than on its key, because several
// resources feed one page — bridge and bridgePort both belong to Bridges — and
// a per-key switch would need an entry for each and silently miss the next one.
func (cn *conn) refreshFor(res *resource.Resource) {
	switch res.Page {
	case "dns":
		if cn.rsession.CollectorEnabled("dns") {
			cn.rsession.DNS().RefreshNow()
		}
	case "bridges":
		if cn.rsession.CollectorEnabled("bridges") {
			cn.rsession.Bridges().RefreshNow()
		}
	case "vlans":
		if cn.rsession.CollectorEnabled("vlans") {
			cn.rsession.Vlans().RefreshNow()
		}
	case "firewall":
		// RefreshNow re-reads all four tables, not just the active one: a write
		// can change the ORDER, and order is the one thing the counter refresh
		// never reports.
		if cn.rsession.CollectorEnabled("firewall") {
			cn.rsession.Firewall().RefreshNow()
		}
	case "wifi":
		if cn.rsession.CollectorEnabled("wifi") {
			cn.rsession.Wifi().RefreshNow()
		}
	case "capsman":
		if cn.rsession.CollectorEnabled("capsman") {
			cn.rsession.Capsman().RefreshNow()
		}
	default:
		log.Printf("[res] %s belongs to page %q, which has no collector to refresh",
			res.Key, res.Page)
	}
}

// ── res:move ────────────────────────────────────────────────────────────────

// resMove reorders a row in a table where position is meaning.
//
// FIREWALL ONLY TODAY, and `Ordered` is what says so. Everywhere else the router
// keeps its own order and moving a row would mean nothing.
//
// THE BROWSER SENDS A DIRECTION OR AN ANCHOR, NEVER A POSITION. An arrow says
// which way and a drag says which row to land before; both refuse to name an
// index. The neighbour is resolved here, from a read taken in this same tick, so
// an operator clicking twice quickly — or two operators at once — cannot move a
// rule to an index computed against a table that has already changed underneath
// them. Same reasoning as the fresh read everywhere else, applied to ordering.
func (cn *conn) resMove(raw json.RawMessage) {
	res, req := cn.resolve(raw, true)
	if res == nil {
		return
	}
	if !res.Ordered {
		cn.resErr(res.Key, "bad-request", "", nil)
		return
	}
	// PRESENCE, not emptiness. An anchor of "" means "land at the end", which is
	// a real instruction and different from sending no anchor at all — the
	// difference `hasOwnProperty(r, 'anchor')` carries on the Node side. A
	// struct field cannot hold it, so the raw request is probed for the key.
	anchored := hasJSONKey(raw, "anchor")
	req.HasAnchor = anchored
	up := req.Direction == "up"
	if req.ID == "" || (!anchored && req.Direction != "up" && req.Direction != "down") {
		cn.resErr(res.Key, "bad-request", "", nil)
		return
	}
	name := req.ExpectedIdentity

	err := cn.rsession.InWriteQueue(func() error {
		rows, err := cn.readMenu(res)
		if err != nil {
			return err
		}
		at := -1
		for i, r := range rows {
			if r[".id"] == req.ID {
				at = i
				break
			}
		}
		if at < 0 {
			cn.resErr(res.Key, "stale-row", name, nil)
			return nil
		}
		row := rows[at]
		name = res.IdentityOf(row)
		if req.ExpectedIdentity != "" && name != req.ExpectedIdentity {
			cn.resErr(res.Key, "stale-row", name, nil)
			return nil
		}

		if anchored {
			// The row the drag aimed at must still be there. If it has gone, the
			// table the operator was looking at is not the table on the router,
			// and dropping the rule somewhere approximate is worse than saying so.
			if req.Anchor != "" {
				found := false
				for _, r := range rows {
					if r[".id"] == req.Anchor {
						found = true
						break
					}
				}
				if !found {
					cn.resErr(res.Key, "stale-row", name, nil)
					return nil
				}
			}
			if anchorAt(rows, at) == req.Anchor {
				// Dropped exactly where it already was.
				cn.resErr(res.Key, "at-end", name, nil)
				return nil
			}
		} else if (up && at == 0) || (!up && at == len(rows)-1) {
			// Already where it is going. Not an error worth a banner, but the
			// page should stop drawing an arrow that does nothing.
			cn.resErr(res.Key, "at-end", name, nil)
			return nil
		}

		verdict, gerr := cn.verdictFor(res, "move", histValues(res.RowValues(row)), row)
		if gerr != nil {
			cn.recorder().Denied(audit.Event{
				Action: res.Key + ".move", TargetType: res.Key, RouterID: cn.routerID,
				TargetID: req.ID, TargetName: name, Note: "guard-not-ported: " + gerr.Error(),
			})
			cn.resErr(res.Key, "guard-not-ported", name,
				map[string]any{"message": safe.Message(gerr.Error())})
			return nil
		}
		if gate := ackGate(verdict, req.Ack); gate != nil {
			gate["resource"] = res.Key
			gate["name"] = name
			cn.srv.hub.Send(cn.c, "res:error", gate)
			return nil
		}

		// RouterOS inserts the moved rule BEFORE `destination`. So moving up
		// means "before the rule currently above me", and moving down means
		// "before the rule two below" — with no destination at all when there is
		// nothing below, which sends it to the end.
		dest := req.Anchor
		if !anchored {
			if up {
				dest = rows[at-1][".id"]
			} else if at+2 < len(rows) {
				dest = rows[at+2][".id"]
			} else {
				dest = ""
			}
		}
		// `=numbers=`, not `=.id=`. The move command addresses rows by number,
		// and an `.id` is accepted there where it is not elsewhere.
		args := []string{"=numbers=" + req.ID}
		if dest != "" {
			args = append(args, "=destination="+dest)
		}
		if _, werr := cn.rsession.Exec(routeros.Cmd{Path: res.Menu + "/move", Args: args}); werr != nil {
			return werr
		}

		nowAt := at
		if moved, merr := cn.readMenu(res); merr == nil {
			for i, r := range moved {
				if r[".id"] == req.ID {
					nowAt = i
					break
				}
			}
		}

		how := "down"
		switch {
		case anchored:
			how = "drag"
		case up:
			how = "up"
		}
		cn.recorder().Record(audit.Event{
			Action: res.Key + ".move", TargetType: res.Key, RouterID: cn.routerID,
			TargetID: req.ID, TargetName: name,
			Before: map[string]any{"position": at},
			After:  map[string]any{"position": nowAt},
			Extra:  append([]audit.KV{{Key: "how", Value: how}}, ackExtra(req.Ack)...),
		})

		cn.refreshFor(res)
		// `movedId` is what the page pulses, so the eye can find the row that
		// just changed places in a table of thirty near-identical ones.
		cn.srv.hub.Send(cn.c, "res:ok", map[string]any{
			"resource": res.Key, "action": "move", "name": name, "movedId": req.ID})
		return nil
	})
	if err != nil {
		cn.resErr(res.Key, writeFailCode(err), name, map[string]any{"message": safe.Message(err.Error())})
	}
}

// anchorAt is the id a row currently sits before, or "" when it is last.
//
// An ANCHOR rather than an index, because an anchor survives the table shifting
// underneath it and an ordinal does not.
func anchorAt(rows []routeros.Reply, at int) string {
	if at+1 < len(rows) {
		return rows[at+1][".id"]
	}
	return ""
}

// hasJSONKey reports whether an object literally carries a key, regardless of
// its value.
func hasJSONKey(raw json.RawMessage, key string) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
