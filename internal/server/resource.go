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
	"errors"
	"log"
	"mikrodash/internal/areas"
	"mikrodash/internal/session"
	"net"
	"net/netip"
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
	// Partial says Values names only the fields to CHANGE, the rest being taken
	// from the row as the router holds it. Never decoded from a browser frame:
	// a form always sends its whole self, and BuildArgs clears an omitted
	// clearable field. The assistant's write tool is the opposite — it is
	// documented as "the values to change" — so a partial edit that omitted a
	// comment or a rate was CLEARING it, and RouterOS refuses some fields blank.
	Partial bool `json:"-"`
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
	EvResError.Send(cn.srv.hub, cn.c, m)
}

// resolve turns a browser request into a resource this connection may write,
// or reports why not. A nil return means the caller must stop.
// strValues flattens what the browser sent to the strings the validator works
// in, matching JavaScript's String() for the shapes that actually arrive.
// A null becomes "", which validate() then treats as blank.
func (r *resRequest) strValues() map[string]string { return flattenValues(r.Values) }

// flattenValues is the ONE place a browser's JSON becomes the strings the
// validator works in.
//
// ── IT WAS TWO, AND THEY DISAGREED ────────────────────────────────────
//
// `/api/dns/fleet-add` grew its own copy, which dropped a null and anything that
// was not a string, bool or number instead of rendering them. Two flatteners for
// one job is two ways for a value to reach a router differently depending on
// which button sent it, so the socket's is the one that survived: it is the
// older, it is what `resSave` has always used, and it renders every shape rather
// than discarding the ones nobody had thought about.
func flattenValues(in map[string]any) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
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
		EvResError.Send(cn.srv.hub, cn.c, map[string]any{"code": "bad-request"})
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
		// The permission check rather than after.
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
	return cn.readMenuWhere(res)
}

// readRow is the one row a write is about, read by its id on the router
// (`print ?.id=<id>`), shaped as readMenu shapes it.
//
// ── ONE ROW, NOT THE MENU ───────────────────────────────────────────────────
//
// Every write read the whole menu to find its row, before and after. On the
// operator's router that is 37,111 address-list entries, 6 s each way, to edit
// one (2026-09-18). A write addresses its row by id — and a create learns its
// new id from the add's `ret` (Cmd.Ret) — so it reads that row. A settings
// menu's one row has no id to ask for, and is read whole, as before.
func (cn *conn) readRow(res *resource.Resource, id string) ([]routeros.Reply, error) {
	if res.Singleton || id == "" {
		return cn.readMenu(res)
	}
	return cn.readMenuWhere(res, "?.id="+id)
}

// errNoRet is a create whose add named no new row, so there is nothing to read
// back: the outcome is unknown rather than guessed.
var errNoRet = errors.New("the router did not name the row it created")

func (cn *conn) readMenuWhere(res *resource.Resource, query ...string) ([]routeros.Reply, error) {
	return readMenuOn(cn.rsession, res, query...)
}

// readMenuOn is readMenu against a given session: the assistant's snapshot,
// which is not the connection's live field (see conn).
func readMenuOn(rs *session.Session, res *resource.Resource, query ...string) ([]routeros.Reply, error) {
	rows, err := rs.Exec(routeros.Cmd{Path: res.Menu + "/print", Args: query})
	if err != nil {
		return nil, err
	}
	out := make([]routeros.Reply, 0, len(rows))
	for _, r := range rows {
		// A singleton's one row has no `.id`; StampID gives it SingletonID, so
		// every id-based path below treats it as the row it is.
		r = routeros.Reply(res.StampID(r))
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

// ── THE PIPELINE AND THE REPORTING ARE NOW SEPARATE (#98) ───────────────────
//
// This used to be one function that wrote and emitted browser events as it
// went. The AI Agent needs the SAME sequence — permission, rate limit, fresh
// read, staleness, read-only rows, guards, the write, read-back confirmation,
// history, audit — but has to learn the result as a value it can hand back to a
// model rather than as a frame sent to a page.
//
// The alternative was a second copy of the sequence, and `dnsfleet_api.go` shows
// what that costs: it reuses `Validate` and `BuildArgs` and states the rest
// itself, which is defensible for one endpoint and would not be for a writer a
// MODEL drives. A second copy is how one of them eventually loses the read-back
// or the guard, and the copy that loses it is the one nobody is watching.
//
// So `writeRow` decides and `resSave` reports. Nothing about the order changed.

// writeOutcome is what the pipeline decided, with no opinion about who is
// asking. An empty Code is success.
//
// `Detail` carries whatever the refusal needs — the validator's errors, the
// router's message, or a guard's warning and fingerprint. A guard gate is NOT a
// special case here: `resErr` already builds exactly the frame the gate path
// used to send by hand, so it travels as an ordinary refusal with a fingerprint
// in it.
type writeOutcome struct {
	Code   string
	Action string
	Name   string
	Detail map[string]any
}

// resSave is the socket handler: resolve, write, tell the browser.
func (cn *conn) resSave(raw json.RawMessage) {
	res, req := cn.resolve(raw, true)
	if res == nil {
		return
	}
	// A human at a form: no provenance to add.
	out := cn.writeRow(res, req, "")
	if out.Code == "" {
		EvResOk.Send(cn.srv.hub, cn.c, map[string]any{
			"resource": res.Key, "action": out.Action, "name": out.Name})
		return
	}
	cn.resErr(res.Key, out.Code, out.Name, out.Detail)
}

// writeRow is the write path itself. It performs every side effect a write has
// — the audit rows, the history push, the collector refresh — and returns what
// happened instead of announcing it.
//
// ── `via` IS A PARAMETER, WHICH IS THE WHOLE POINT ──────────────────────────
//
// It records WHO ASKED, and it is passed by the caller that knows. It is not
// read off the request, because the request comes from a browser and a browser
// could claim to be the agent — or claim not to be. And it is not held on the
// connection, because the agent answers on a goroutine while a form on the same
// socket may be mid-save: a field there is a race whose loser writes the wrong
// provenance into an audit row that is supposed to be the record of record.
//
// An empty `via` adds nothing at all, so a human at a form produces exactly the
// audit Extra it always did.
// preparedWrite is a write that has been CHECKED but not performed.
//
// ── IT EXISTS SO THE PROPOSAL AND THE WRITE CANNOT DISAGREE (#98) ───────────
//
// With confirmation prompts on, the agent shows the operator what it intends
// before anything happens. That dialog must be built from what the SERVER
// worked out — the row as the router currently holds it, the guard's verdict,
// the exact command — and never from the model's own account of what it is
// about to do.
//
// Building that separately would have meant a second sequence that validates,
// reads and runs guards, which is the duplication the writeRow split was for.
// So the checking half is named and shared: `prepareWrite` decides, and either
// `commitWrite` performs it or a proposal describes it.
type preparedWrite struct {
	res       *resource.Resource
	req       *resRequest
	validated resource.Validated
	before    routeros.Reply
	editing   bool
	action    string
	name      string
	verdict   guard.Verdict
}

// prepareWrite validates, reads the menu fresh, checks the row is still there
// and still writable, and runs the guards. It changes nothing.
//
// A nil prepared write means the returned outcome is a refusal and the caller
// must stop. Every refusal it can produce is one the write would have produced
// anyway, at the same point, for the same reason.
func (cn *conn) prepareWrite(res *resource.Resource, req *resRequest) (*preparedWrite, writeOutcome) {
	editing := req.ID != ""

	// A resource that cannot be created refuses before anything is read. The
	// browser draws no Add and no Duplicate for it, so arriving here is a
	// hand-built request, and it is recorded as the refusal it is.
	if !editing && res.NoCreate {
		cn.recorder().Denied(audit.Event{
			Action: res.Key + ".create", TargetType: res.Key, RouterID: cn.routerID,
			Note: "not-creatable",
		})
		return nil, writeOutcome{Code: "not-creatable"}
	}
	// And one that cannot be edited refuses an update the same way: the form
	// draws no Save for it, so this is a hand-built request or the assistant.
	if editing && res.NoEdit {
		cn.recorder().Denied(audit.Event{
			Action: res.Key + ".update", TargetType: res.Key, RouterID: cn.routerID,
			TargetID: req.ID, Note: "not-editable",
		})
		return nil, writeOutcome{Code: "not-editable"}
	}

	// READ FIRST WHEN THE VALUES ARE PARTIAL, because what is being validated is
	// then the stored row with the caller's changes laid over it.
	var rows []routeros.Reply
	var readErr error
	if editing && req.Partial {
		rows, readErr = cn.readRow(res, req.ID)
	}
	submitted := req.strValues()
	if editing && req.Partial && readErr == nil {
		submitted = overStoredRow(res, find(res, rows, req.ID, req.ExpectedIdentity), submitted)
	}
	validated, errs := res.Validate(submitted, editing)
	if len(errs) > 0 {
		return nil, writeOutcome{Code: "invalid", Detail: map[string]any{"errors": errs}}
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

	// A CREATE READS NOTHING FIRST: it used to read the whole menu only to know
	// which ids already existed, and now learns its new row's id from the add.
	err := readErr
	if editing && rows == nil && err == nil {
		rows, err = cn.readRow(res, req.ID)
	}
	if err != nil {
		return nil, writeOutcome{Code: writeFailCode(err), Name: name,
			Detail: map[string]any{"message": safe.Message(err.Error())}}
	}
	var before routeros.Reply
	if editing {
		before = find(res, rows, req.ID, req.ExpectedIdentity)
		if before == nil {
			return nil, writeOutcome{Code: "stale-row", Name: name}
		}
		// THE AUDIT NAME COMES FROM THE ROW, as it does for a delete. An edit
		// that does not send the identity field — the assistant's partial
		// change_row sends only what changes, and a Display name is never sent
		// at all — otherwise audited its refusals with an EMPTY target_name.
		// Measured on the CHR on 2026-09-18: `ipService.update | | denied`.
		if name == "" {
			name = res.IdentityOf(before)
		}
	}
	if before != nil && res.ReadOnlyWhen != nil && res.ReadOnlyWhen(before) {
		cn.recorder().Denied(audit.Event{
			Action: res.Key + ".update", TargetType: res.Key, RouterID: cn.routerID,
			TargetID: req.ID, TargetName: name, Note: "read-only-row",
		})
		return nil, writeOutcome{Code: res.ReadOnlyReason, Name: name}
	}

	// The guard runs AFTER the fresh read and BEFORE the write, so the verdict
	// is about the row as it is now rather than as the browser remembers it.
	//
	// AND THE SAME IS TRUE OF A PROPOSAL. An approval re-runs this whole
	// function rather than trusting what was computed when the proposal was
	// raised, so a row that changed while the operator was reading the dialog is
	// caught by the staleness check and the guard, not waved through by them.
	action := "update"
	if !editing {
		action = "create"
	}
	verdict, gerr := cn.verdictFor(res, action, validated.Values, before)
	if gerr != nil {
		// No equivalent in Node, which has every guard. Recorded as a denial
		// rather than only logged, because "the port refused a write it could
		// not check" is exactly the kind of gap that must be visible in the
		// trail rather than in a container log nobody reads.
		cn.recorder().Denied(audit.Event{
			Action: res.Key + "." + action, TargetType: res.Key, RouterID: cn.routerID,
			TargetID: req.ID, TargetName: name, Note: "guard-not-ported: " + gerr.Error(),
		})
		return nil, writeOutcome{Code: "guard-not-ported", Name: name,
			Detail: map[string]any{"message": safe.Message(gerr.Error())}}
	}
	if r := cn.guardRefusal(res, action, req.ID, name, verdict); r != nil {
		return nil, *r
	}

	return &preparedWrite{
		res: res, req: req, validated: validated, before: before,
		editing: editing, action: action, name: name, verdict: verdict,
	}, writeOutcome{}
}

// overStoredRow lays partial values over the row as the router holds it.
//
// A field the caller did not name keeps its stored value rather than being
// cleared. A secret is not in RowValues, so it stays absent and BuildArgs leaves
// the stored one alone. A nil row (no longer there) yields the values as sent,
// and the staleness check reports it.
func overStoredRow(res *resource.Resource, row routeros.Reply, sent map[string]string) map[string]string {
	if row == nil {
		return sent
	}
	merged := histValues(res.RowValues(row))
	for k, v := range sent {
		merged[k] = v
	}
	return merged
}

// commitWrite performs a prepared write and records it.
//
// It does NOT re-check anything: `prepareWrite` ran immediately before it, under
// the same write-queue slot, so re-reading here would only widen the window
// rather than narrow it.
func (cn *conn) commitWrite(p *preparedWrite, via string) writeOutcome {
	res, req := p.res, p.req

	args := res.BuildArgs(p.validated)
	verb := "/add"
	if p.editing {
		verb = "/set"
		args = append(res.IDWords(req.ID), args...)
	}
	var ret string
	if _, err := cn.rsession.Exec(routeros.Cmd{Path: res.Menu + verb, Args: args, Ret: &ret}); err != nil {
		return writeOutcome{Code: writeFailCode(err), Name: p.name,
			Detail: map[string]any{"message": safe.Message(err.Error())}}
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
	// ── CONFIRMED BY READING IT BACK (#97) ─────────────────────────────
	//
	// The router's answer says the command was accepted, not what the table
	// holds, so the menu is read again before anything reports success: an
	// edited row must still be there, and a create must have produced exactly
	// one new row. Otherwise the outcome is unknown. See write_verify.go.
	readID := req.ID
	if !p.editing {
		readID = ret
	}
	var after []routeros.Reply
	rerr := errNoRet
	if readID != "" {
		after, rerr = cn.readRow(res, readID)
	}
	var observed routeros.Reply
	confirmed := false
	if rerr == nil {
		observed = rowByID(after, readID)
		confirmed = observed != nil
	}
	if !confirmed {
		return cn.unknownOutcome(res, res.Key+"."+p.action, req.ID, p.name, req.Ack, via)
	}
	newID := observed[".id"]
	if newID != "" {
		var beforeHist map[string]string
		if p.before != nil {
			beforeHist = histValues(res.RowValues(p.before))
		}
		cn.histPush(res.Key, history.Build(res.Key, res.Label, p.action,
			newID, p.name, beforeHist, p.validated.Values))
	}

	var beforeVals map[string]any
	if p.before != nil {
		beforeVals = auditValues(res, res.RowValues(p.before))
	} else {
		beforeVals = map[string]any{}
	}
	cn.recorder().Record(audit.Event{
		Action: res.Key + "." + p.action, TargetType: res.Key, RouterID: cn.routerID,
		TargetID: newID, TargetName: p.name,
		Before: beforeVals,
		// OBSERVED, not requested: the row as read back. See write_verify.go.
		After: auditValues(res, observedValues(res, observed, p.validated.Values)),
		Extra: writeExtra(req.Ack, via),
	})

	cn.refreshFor(res)
	return writeOutcome{Action: p.action, Name: p.name}
}

// writeRow is prepare, then the acknowledgement gate, then commit — all under
// one write-queue slot, which is what keeps the guard's verdict and the write it
// guards from being separated by another writer.
func (cn *conn) writeRow(res *resource.Resource, req *resRequest, via string) writeOutcome {
	var out writeOutcome
	err := cn.inWriteQueue(func() error {
		p, refusal := cn.prepareWrite(res, req)
		if p == nil {
			out = refusal
			return nil
		}
		if gate := ackGate(p.verdict, req.Ack); gate != nil {
			// The code travels in the outcome; the warning and fingerprint stay
			// in the detail, which is the same frame the caller used to build.
			code, _ := gate["code"].(string)
			delete(gate, "code")
			out = writeOutcome{Code: code, Name: p.name, Detail: gate}
			return nil
		}
		out = cn.commitWrite(p, via)
		return nil
	})
	if err != nil {
		// The limiter refuses BEFORE the closure runs, so there is no validated
		// name to report here. The browser's sentence for `rate-limited` is
		// fixed and does not read one.
		return writeOutcome{Code: writeFailCode(err),
			Detail: map[string]any{"message": safe.Message(err.Error())}}
	}
	return out
}

func (cn *conn) resRemove(raw json.RawMessage) {
	res, req := cn.resolve(raw, true)
	if res == nil {
		return
	}
	// A human at a form: no provenance to add.
	out := cn.removeRow(res, req, "")
	if out.Code == "" {
		EvResOk.Send(cn.srv.hub, cn.c, map[string]any{
			"resource": res.Key, "action": "delete", "name": out.Name})
		return
	}
	cn.resErr(res.Key, out.Code, out.Name, out.Detail)
}

// preparedRemove is a delete that has been CHECKED but not performed: the row
// the router holds now, its identity, and the guard's verdict. The delete twin
// of `preparedWrite`, and for the same reason — so the assistant's approval
// dialog and the delete itself are built from one server-side reading.
type preparedRemove struct {
	res     *resource.Resource
	req     *resRequest
	row     routeros.Reply
	name    string
	verdict guard.Verdict
}

// removeRow is the delete path itself, shared by `res:remove` and the assistant.
// Like `writeRow` it performs every side effect and RETURNS what happened; `via`
// is the caller's provenance and never read off the request.
func (cn *conn) removeRow(res *resource.Resource, req *resRequest, via string) writeOutcome {
	var out writeOutcome
	err := cn.inWriteQueue(func() error {
		p, refusal := cn.prepareRemove(res, req)
		if p == nil {
			out = refusal
			return nil
		}
		if gate := ackGate(p.verdict, req.Ack); gate != nil {
			code, _ := gate["code"].(string)
			delete(gate, "code")
			out = writeOutcome{Code: code, Name: p.name, Detail: gate}
			return nil
		}
		out = cn.commitRemove(p, via)
		return nil
	})
	if err != nil {
		return writeOutcome{Code: writeFailCode(err), Name: req.ExpectedIdentity,
			Detail: map[string]any{"message": safe.Message(err.Error())}}
	}
	return out
}

// prepareRemove reads the row fresh and decides whether it may go. It changes
// nothing on the router; a refused delete is audited as the denial it is.
func (cn *conn) prepareRemove(res *resource.Resource, req *resRequest) (*preparedRemove, writeOutcome) {
	// Only until the row is read. Everything after `find` uses the identity of
	// the row the SERVER actually found — see below.
	name := req.ExpectedIdentity
	if req.ID == "" {
		return nil, writeOutcome{Code: "invalid", Name: name}
	}

	rows, err := cn.readRow(res, req.ID)
	if err != nil {
		return nil, writeOutcome{Code: writeFailCode(err), Name: name,
			Detail: map[string]any{"message": safe.Message(err.Error())}}
	}
	row := find(res, rows, req.ID, req.ExpectedIdentity)
	if row == nil {
		return nil, writeOutcome{Code: "stale-row", Name: name}
	}
	// ── THE AUDIT NAME COMES FROM THE ROW, NOT FROM THE REQUEST ────────────
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
		return nil, writeOutcome{Code: res.ReadOnlyReason, Name: name}
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
		return nil, writeOutcome{Code: "not-removable", Name: name}
	}
	// A delete always counts for the guard: removing a port and disabling
	// it cut the same link.
	verdict, gerr := cn.verdictFor(res, "delete", nil, row)
	if gerr != nil {
		cn.recorder().Denied(audit.Event{
			Action: res.Key + ".delete", TargetType: res.Key, RouterID: cn.routerID,
			TargetID: req.ID, TargetName: name, Note: "guard-not-ported: " + gerr.Error(),
		})
		return nil, writeOutcome{Code: "guard-not-ported", Name: name,
			Detail: map[string]any{"message": safe.Message(gerr.Error())}}
	}
	if r := cn.guardRefusal(res, "delete", req.ID, name, verdict); r != nil {
		return nil, *r
	}
	return &preparedRemove{res: res, req: req, row: row, name: name, verdict: verdict}, writeOutcome{}
}

// commitRemove performs a checked delete and confirms the row is gone.
func (cn *conn) commitRemove(p *preparedRemove, via string) writeOutcome {
	res, req, name, row := p.res, p.req, p.name, p.row
	if _, err := cn.rsession.Exec(routeros.Cmd{
		Path: res.Menu + "/remove", Args: res.IDWords(req.ID)}); err != nil {
		return writeOutcome{Code: writeFailCode(err), Name: name,
			Detail: map[string]any{"message": safe.Message(err.Error())}}
	}
	// The row must be GONE before the delete is reported (#97).
	if after, rerr := cn.readRow(res, req.ID); rerr != nil || !confirmRemoved(after, req.ID) {
		return cn.unknownOutcome(res, res.Key+".delete", req.ID, name, req.Ack, via)
	}
	// Recorded BEFORE the audit row and from the row as it was, because the
	// row is gone now and its values are the only way back.
	//
	// NOT for a resource that cannot be created. Undoing a delete is an `add`,
	// and for a NoCreate resource that add would not restore the row — undoing a
	// certificate's removal would have made an unsigned TEMPLATE with its name,
	// and a file's would have made an empty file. A delete that cannot be undone
	// offers no undo (found building Files, 2026-09-18).
	if res.UndoesRemoval() {
		cn.histPush(res.Key, history.Build(res.Key, res.Label, "delete",
			req.ID, name, histValues(res.RowValues(row)), nil))
	}

	// after is `{}` for a delete: Diff walks the keys of `after`, so an empty
	// one reports NOTHING changed, which is right — a delete is described by
	// the row that went away, and the row itself is the target, not a diff.
	// Matches index.js, which passes `after: {}` here for the same reason.
	cn.recorder().Record(audit.Event{
		Action: res.Key + ".delete", TargetType: res.Key, RouterID: cn.routerID,
		TargetID: req.ID, TargetName: name,
		Before: auditValues(res, res.RowValues(row)),
		After:  map[string]any{},
		Extra:  writeExtra(req.Ack, via),
	})

	cn.refreshFor(res)
	return writeOutcome{Action: "delete", Name: name}
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

// writeExtra is ackExtra plus who asked for the write.
//
// ── NO SCHEMA CHANGE, WHICH IS WHY THE TRAIL CAN ANSWER THIS AT ALL ─────────
//
// `audit.Event.Extra` already carries arbitrary pairs — it is how
// `selfCutoffAcknowledged` is recorded — so "what did the assistant change" is a
// question the existing Audit Trail can answer without a migration or a new
// column. A separate agent log would have been a second record of the same
// events, and the one nobody thinks to read during an incident.
//
// EMPTY MEANS A PERSON, and adds nothing. Stamping every human write with
// `via: ui` would rewrite the shape of every audit row in every install to say
// something already implied by the absence of anything else.
func writeExtra(ack, via string) []audit.KV {
	kv := ackExtra(ack)
	if via != "" {
		kv = append(kv, audit.KV{Key: "via", Value: via})
	}
	return kv
}

// writeFailCode separates "the router refused" from "we could not reach it".
// The page says different things about them, and conflating the two makes a
// permissions problem look like an outage.
func writeFailCode(err error) string {
	m := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, errWriteRateLimited):
		return "rate-limited"
	case errors.Is(err, errOutcomeUnknown):
		return "outcome-unknown"
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
		EvResError.Send(cn.srv.hub, cn.c, map[string]any{"code": "bad-request"})
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
	EvResSchema.Send(cn.srv.hub, cn.c, out)
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
	// A human at a form: no provenance to add.
	out := cn.runRowAction(res, req, "")
	if out.Code == "" {
		EvResOk.Send(cn.srv.hub, cn.c, map[string]any{
			"resource": res.Key, "action": out.Action, "name": out.Name})
		return
	}
	cn.resErr(res.Key, out.Code, out.Name, out.Detail)
}

// runRowAction is the row-action path itself, shared by `res:action` and the
// assistant's run_action. Like `writeRow` and `removeRow` it performs every side
// effect — the fresh read, the row's own When, the guard, the command, the
// read-back, history, audit and refresh — and RETURNS what happened; `via` is
// the caller's provenance, recorded on the audit row.
func (cn *conn) runRowAction(res *resource.Resource, req *resRequest, via string) writeOutcome {
	def := res.ActionByKey(req.Action)
	if def == nil || req.ID == "" {
		return writeOutcome{Code: "bad-request"}
	}
	action := res.Key + "." + def.Key

	var out writeOutcome
	err := cn.inWriteQueue(func() error {
		rows, err := cn.readRow(res, req.ID)
		if err != nil {
			return err
		}
		row := find(res, rows, req.ID, req.ExpectedIdentity)
		if row == nil {
			out = writeOutcome{Code: "stale-row"}
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
			out = writeOutcome{Code: "not-applicable", Name: name}
			return nil
		}

		verdict, gerr := cn.verdictFor(res, def.Key, histValues(res.RowValues(row)), row)
		if gerr != nil {
			cn.recorder().Denied(audit.Event{
				Action: action, TargetType: res.Key, RouterID: cn.routerID,
				TargetID: req.ID, TargetName: name, Note: "guard-not-ported: " + gerr.Error(),
			})
			out = writeOutcome{Code: "guard-not-ported", Name: name,
				Detail: map[string]any{"message": safe.Message(gerr.Error())}}
			return nil
		}
		if r := cn.guardRefusal(res, def.Key, req.ID, name, verdict); r != nil {
			out = writeOutcome{Code: r.Code, Name: name, Detail: r.Detail}
			return nil
		}
		if gate := ackGate(verdict, req.Ack); gate != nil {
			code, _ := gate["code"].(string)
			delete(gate, "code")
			out = writeOutcome{Code: code, Name: name, Detail: gate}
			return nil
		}

		if _, err := cn.rsession.Exec(routeros.Cmd{
			Path: res.Menu + "/" + def.Verb, Args: res.IDWords(req.ID)}); err != nil {
			return err
		}
		// The row must show the verb took before it is reported (#97).
		after, rerr := cn.readRow(res, req.ID)
		if rerr != nil {
			out = cn.unknownOutcome(res, action, req.ID, name, req.Ack, via)
			return nil
		}
		if _, ok := confirmAction(after, req.ID, def.Verb); !ok {
			out = cn.unknownOutcome(res, action, req.ID, name, req.Ack, via)
			return nil
		}

		// enable and disable invert each other, so they are recorded. A verb
		// with no inverse — make-static — yields nothing, and history.Build says
		// so by returning nil.
		cn.histPush(res.Key, history.Build(res.Key, res.Label, def.Key, req.ID, name, nil, nil))

		ev := audit.Event{
			Action: action, TargetType: res.Key, RouterID: cn.routerID,
			TargetID: req.ID, TargetName: name, Note: def.Note,
		}
		if via != "" {
			ev.Extra = []audit.KV{{Key: "via", Value: via}}
		}
		cn.recorder().Record(ev)

		cn.refreshFor(res)
		out = writeOutcome{Action: def.Key, Name: name}
		return nil
	})
	if err != nil {
		return writeOutcome{Code: writeFailCode(err), Detail: map[string]any{"message": safe.Message(err.Error())}}
	}
	return out
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
	EvResNew.Send(cn.srv.hub, cn.c, map[string]any{
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
	EvResPreview.Send(cn.srv.hub, cn.c, map[string]any{
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
	rows, err := cn.readRow(res, req.ID)
	if err != nil {
		cn.resErr(res.Key, writeFailCode(err), "", map[string]any{"message": safe.Message(err.Error())})
		return
	}
	row := find(res, rows, req.ID, req.ExpectedIdentity)
	if row == nil {
		cn.resErr(res.Key, "stale-row", "", nil)
		return
	}
	EvResRow.Send(cn.srv.hub, cn.c, map[string]any{
		"resource": res.Key,
		"id":       req.ID,
		"identity": res.IdentityOf(row),
		"readOnly": res.ReadOnlyWhen != nil && res.ReadOnlyWhen(row),
		// So the form can leave Delete off a row the server would refuse to remove.
		"removable": res.RemovableWhen == nil || res.RemovableWhen(row),
		"actions":   res.ActionsFor(row),
		"values":    res.RowValues(row),
		"options":   cn.resOptions(res),
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
func (cn *conn) managementPath() guard.ManagementPath { return managementPathOf(cn.rsession) }

// managementPathOf is managementPath for any router's session: Config
// Management asks it of routers the viewer is not looking at.
func managementPathOf(sn *session.Session) guard.ManagementPath {
	var active, addrs []routeros.Reply
	if rows, err := sn.Exec(routeros.Cmd{Path: "/user/active/print"}); err == nil {
		active = rows
	}
	// No proplist: selfPath needs `actual-interface`, which no page asks for.
	// It differs from `interface` exactly where it matters — an address on a
	// bridge reports the physical port as the actual one.
	if rows, err := sn.Exec(routeros.Cmd{Path: "/ip/address/print"}); err == nil {
		addrs = rows
	}
	return guard.ResolveManagementInterfaces(active, addrs, []string{sn.Username()})
}

// ackGate turns a verdict into a refusal the page can act on, or nil to proceed.
//
// A warning is shown once and acknowledged by its FINGERPRINT, which is
// recomputed from a fresh read on the retry — so an acknowledgement cannot be
// carried from one row to another or replayed against a different write. An ack
// that no longer matches is `stale-warning`: the ground moved between the
// prompt and the answer, and the operator must look again.
// guardRefusal is a refused verdict as the outcome every write path reports, or
// nil. The refusal is AUDITED, unlike a warning: a warning is a question, and a
// refusal is an attempt that was stopped. It is checked before ackGate, so no
// acknowledgement can carry a refused write through.
func (cn *conn) guardRefusal(res *resource.Resource, action, id, name string, v guard.Verdict) *writeOutcome {
	if !v.Refused() {
		return nil
	}
	cn.recorder().Denied(audit.Event{
		Action: res.Key + "." + action, TargetType: res.Key, RouterID: cn.routerID,
		TargetID: id, TargetName: name, Note: "guard-refused: " + v.Code,
	})
	return &writeOutcome{Code: "guard-refused", Name: name, Detail: map[string]any{"rule": v.Code, "value": v.Detail["value"]}}
}

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
	"routePath": true, "addressPath": true, "queueThrottle": true, "selfAccount": true,
	"listLockout": true, "serviceLockout": true, "certLockout": true, "codeGate": true,
	"rulePath": true, "ipsecPath": true, "tunnelDefault": true, "dhcpClientPath": true,
	"tableInUse": true,
}

// errUnportedGuard is returned when a resource declares a guard this server
// cannot evaluate.
type errUnportedGuard struct{ kind string }

func (e errUnportedGuard) Error() string { return "guard not ported: " + e.kind }

// verdictFor runs whichever guards the resource declares.
//
// ── EVERY GUARD RUNS, AND A REFUSAL OUTRANKS ANY WARNING ────────────────────
//
// It returned on the first warning, in declared order, so a refusing guard
// declared after a warning one never ran: VRRP (selfPath, codeGate) and DHCP
// client (dhcpClientPath, tunnelDefault, codeGate) let a non-admin who
// acknowledged a cut-off warning write code the router runs (review
// 2026-09-19). Now each declared guard is evaluated and strongestVerdict picks:
// a refusal first, else the FIRST warning, because a second dialog after the
// first is answered is how somebody learns to click both without reading
// either.
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
	var counted []guard.Verdict
	for _, kind := range res.Guard {
		if v, ok := cn.guardVerdict(kind, res, action, values, before); ok {
			counted = append(counted, v)
		}
	}
	return strongestVerdict(counted), nil
}

// strongestVerdict is the verdict a write answers to: any refusal, else the
// first warning, else none.
func strongestVerdict(vs []guard.Verdict) guard.Verdict {
	for _, v := range vs {
		if v.Refused() {
			return v
		}
	}
	for _, v := range vs {
		if v.Warned() {
			return v
		}
	}
	return guard.Verdict{Level: "none"}
}

// guardVerdict is one guard's verdict, and whether it counts. Each guard keeps
// the level it has always spoken at: the lockout guards that warn count only
// when they warn, the ones that refuse (serviceLockout, certLockout, codeGate)
// only when they refuse, and selfAccount either way.
//
// EACH GUARD DECIDES WHAT IT NEEDS. The interface-target shortcut below is
// selfPath's alone: it asks which interface carries us, so an edit naming no
// interface cannot concern it. fwGuard asks whether a RULE could match our
// traffic, and a rule that names no interface is the loudest case there —
// `chain=input action=drop` matches everything. Returning early on empty
// targets for both would have silenced the guard on exactly the write it
// exists for.
func (cn *conn) guardVerdict(kind string, res *resource.Resource, action string,
	values, before map[string]string) (guard.Verdict, bool) {

	var v guard.Verdict
	switch kind {
	case "selfPath":
		targets := res.GuardTargets(action, values, before)
		if len(targets) == 0 {
			return v, false
		}
		v = guard.CheckInterfaceEdit(cn.managementPath(), targets, action)
	case "fwGuard":
		v = cn.fwVerdict(res, action, values, before)
	case "wifiInherit":
		v = cn.wifiVerdict(res, action, values, before)
	case "capsmanPush":
		v = cn.capsVerdict(res, action, values, before)
	case "routePath":
		v = cn.routeVerdict(res, action, values, before)
	case "rulePath":
		v = cn.ruleVerdict(res, action, values, before)
	case "tableInUse":
		v = cn.tableVerdict(res, action, values, before)
	case "ipsecPath":
		v = cn.ipsecVerdict(res, action, values, before)
	case "tunnelDefault":
		v = cn.tunnelDefaultVerdict(res, action, values, before)
	case "dhcpClientPath":
		v = cn.dhcpClientVerdict(res, action, values, before)
	case "addressPath":
		v = cn.addressVerdict(res, action, values, before)
	case "queueThrottle":
		v = cn.queueVerdict(res, action, values, before)
	case "listLockout":
		v = cn.listVerdict(res, action, values, before)
	case "serviceLockout":
		v = cn.serviceVerdict(action, values, before)
		return v, v.Refused()
	case "certLockout":
		v = cn.certVerdict(action, before)
		return v, v.Refused()
	case "codeGate":
		v = codeDecision(res, action, values, before, cn.codeAllowed())
		return v, v.Refused()
	case "selfAccount":
		v = cn.selfAccountVerdict(res, action, values, before)
		return v, v.Warned() || v.Refused()
	default:
		return v, false
	}
	return v, v.Warned()
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

// routeVerdict asks the route lockout guard about one route write (#97).
//
// Where the router sees us from is read FRESH, in the same tick as the write, as
// the other guards read it; so are the configured addresses, IPv4 and IPv6, since
// an address on a connected subnet does not depend on a static route. A menu that
// cannot be read contributes nothing, and the guard then warns that it cannot
// tell rather than passing the change as safe.
func (cn *conn) routeVerdict(res *resource.Resource, action string,
	values, before map[string]string) guard.Verdict {

	var active, addrs []routeros.Reply
	if rows, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/active/print"}); err == nil {
		active = rows
	}
	for _, menu := range []string{"/ip/address/print", "/ipv6/address/print"} {
		if rows, err := cn.rsession.Exec(routeros.Cmd{Path: menu}); err == nil {
			addrs = append(addrs, rows...)
		}
	}

	var was, now guard.RouteChange
	if before != nil {
		was = routeChangeOf(histValues(res.RowValues(before)), guard.RouteChange{})
	}
	if action != "delete" && values != nil {
		// An EDIT is laid over the stored row: a form that did not send a field has
		// not changed it, and comparing a blank with the stored value would call a
		// comment-only edit a forwarding change.
		now = routeChangeOf(values, was)
	}
	return guard.CheckRouteEdit(active, addrs, []string{cn.rsession.Username()}, action, was, now)
}

// ruleVerdict asks the routing-rule guard about one /routing/rule write.
// /user/active is read fresh, in the same tick as the write, as the route
// guard reads it.
func (cn *conn) ruleVerdict(res *resource.Resource, action string,
	values, before map[string]string) guard.Verdict {

	var active []routeros.Reply
	if rows, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/active/print"}); err == nil {
		active = rows
	}
	var was, now guard.RuleChange
	if before != nil {
		was = ruleChangeOf(histValues(res.RowValues(before)))
	}
	if action != "delete" && values != nil {
		now = ruleChangeOf(values)
	}
	return guard.CheckRuleEdit(active, []string{cn.rsession.Username()}, action, was, now)
}

// tableVerdict asks whether a routing-table write takes a table out of service
// that enabled routing rules look routes up in. The rules are read FRESH; a read
// that fails leaves the guard quiet, as listLockout's does, because this warning
// is about a rule going inactive, not about MikroDash's own connection.
func (cn *conn) tableVerdict(res *resource.Resource, action string,
	values, before map[string]string) guard.Verdict {

	if before == nil {
		return guard.Verdict{Level: "none"}
	}
	rows, err := cn.rsession.Exec(routeros.Cmd{Path: "/routing/rule/print",
		Args: []string{"=.proplist=.id,table,disabled"}})
	if err != nil {
		return guard.Verdict{Level: "none"}
	}
	return tableDecision(res, action, values, before, rows)
}

// tableDecision is tableVerdict without the read. `before` is the raw row;
// `values` may be partial (the assistant sends only what changes), so a field it
// does not carry is the row's own.
func tableDecision(res *resource.Resource, action string, values, before map[string]string,
	ruleRows []routeros.Reply) guard.Verdict {

	was := histValues(res.RowValues(before))
	// TWO SPELLINGS: the row reads "true"/"false" and validated form values
	// "yes"/"no". Reading only "true" took every form save as unsetting FIB.
	state := func(v map[string]string) guard.TableState {
		return guard.TableState{Present: true, Disabled: isTruthy(v["disabled"]), FIB: isTruthy(v["fib"])}
	}
	after := guard.TableState{}
	if action != "delete" {
		merged := make(map[string]string, len(was))
		for k, v := range was {
			merged[k] = v
		}
		for k, v := range values {
			merged[k] = v
		}
		after = state(merged)
	}
	rules := make([]guard.TableRule, 0, len(ruleRows))
	for _, r := range ruleRows {
		rules = append(rules, guard.TableRule{ID: r[".id"], Table: r["table"], Disabled: isTruthy(r["disabled"])})
	}
	return guard.CheckTableChange(action, was["name"], before[".id"], state(was), after, rules)
}

// tunnelDefaultVerdict judges a tunnel client's `add-default-route` as the route
// it installs: 0.0.0.0/0 through the client's own interface, present while the
// client is enabled with the option on. The route guard then answers exactly as
// it would for the same static route — including leaving a management address on
// a connected subnet alone, which does not depend on the default route.
func (cn *conn) tunnelDefaultVerdict(res *resource.Resource, action string,
	values, before map[string]string) guard.Verdict {

	defaultRoute := tunnelDefaultRoute
	var was, now guard.RouteChange
	if before != nil {
		was = defaultRoute(histValues(res.RowValues(before)))
	}
	if action != "delete" {
		now = defaultRoute(values)
	}
	var active, addrs []routeros.Reply
	if rows, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/active/print"}); err == nil {
		active = rows
	}
	for _, menu := range []string{"/ip/address/print", "/ipv6/address/print"} {
		if rows, err := cn.rsession.Exec(routeros.Cmd{Path: menu}); err == nil {
			addrs = append(addrs, rows...)
		}
	}
	return guard.CheckRouteEdit(active, addrs, []string{cn.rsession.Username()}, action, was, now)
}

// tunnelDefaultRoute is the default route a tunnel client installs: present while
// the client is enabled with add-default-route on, and absent otherwise. `v` is
// in the registry's field names, with RouterOS's or the form's spelling of a
// checkbox.
func tunnelDefaultRoute(v map[string]string) guard.RouteChange {
	yes := func(k string) bool { return v[k] == "true" || v[k] == "yes" }
	// A DHCP client's `special-classless` installs the default route too.
	on := yes("addDefaultRoute") || v["addDefaultRoute"] == "special-classless"
	if v == nil || !on || yes("disabled") {
		return guard.RouteChange{}
	}
	// PPPoE installs it at its own default-route-distance; OpenVPN has none, so 1.
	distance := v["defaultRouteDistance"]
	if distance == "" {
		distance = "1"
	}
	via := v["name"]
	if via == "" {
		via = v["interface"] // a DHCP client is not named after its interface
	}
	return guard.RouteChange{Present: true, Dst: "0.0.0.0/0", Gateway: via, Distance: distance, Table: "main"}
}

// dhcpClientVerdict asks whether a DHCP client write takes away the address
// MikroDash dials. The dialled host is this session's own, resolved when it is a
// name; the address the client holds comes from the stored row.
func (cn *conn) dhcpClientVerdict(res *resource.Resource, action string,
	values, before map[string]string) guard.Verdict {

	var was, now guard.DHCPClientChange
	if before != nil {
		b := histValues(res.RowValues(before))
		was = guard.DHCPClientChange{Present: true, Disabled: b["disabled"] == "true",
			Interface: b["interface"], Address: b["address"]}
	}
	if action != "delete" && values != nil {
		d := values["disabled"]
		now = guard.DHCPClientChange{Present: true, Disabled: d == "true" || d == "yes", Interface: values["interface"]}
	}
	return guard.CheckDHCPClientEdit(dialledAddresses(cn.rsession.Host()), action, was, now)
}

// dialledAddresses is the address MikroDash reaches a router at: the host as
// configured, or what a name resolves to here. A name that does not resolve
// gives nothing, and the DHCP client guard then fails open.
func dialledAddresses(host string) []string {
	if _, err := netip.ParseAddr(host); err == nil {
		return []string{host}
	}
	addrs, err := net.LookupHost(host)
	if err != nil {
		return nil
	}
	return addrs
}

// ipsecVerdict asks the IPsec guard about a policy, peer or identity write.
// /user/active is read fresh; for a peer or identity so are the policies, which
// decide whether that peer carries MikroDash.
func (cn *conn) ipsecVerdict(res *resource.Resource, action string,
	values, before map[string]string) guard.Verdict {

	var active []routeros.Reply
	if rows, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/active/print"}); err == nil {
		active = rows
	}
	users := []string{cn.rsession.Username()}
	var was map[string]string
	if before != nil {
		was = histValues(res.RowValues(before))
	}
	if res.Key == "ipsecPolicy" {
		var b, a guard.IPsecPolicy
		if was != nil {
			b = ipsecPolicyChangeOf(was)
		}
		if action != "delete" && values != nil {
			a = ipsecPolicyChangeOf(values)
		}
		return guard.CheckIPsecPolicyEdit(active, users, action, b, a)
	}
	// A peer or an identity: which peer it belongs to BEFORE the change, and
	// whether anything but the comment moves.
	peer := ""
	if was != nil {
		peer = was["peer"]
		if res.Key == "ipsecPeer" {
			peer = was["name"]
		}
	}
	changed := action == "delete" || ipsecChanged(was, values)
	policies, err := cn.rsession.Exec(routeros.Cmd{Path: "/ip/ipsec/policy/print",
		Args: []string{"=.proplist=dst-address,protocol,action,peer,disabled,template"}})
	if err != nil {
		policies = nil
	}
	return guard.CheckIPsecPeerEdit(active, users, policies, action, peer, changed)
}

// ipsecPolicyChangeOf reads a policy in the registry's field names. A missing key
// is a cleared one, as for routing rules: a form sends every field, and a partial
// edit has been merged with the stored row before it gets here.
func ipsecPolicyChangeOf(v map[string]string) guard.IPsecPolicy {
	yes := func(k string) bool { return v[k] == "true" || v[k] == "yes" }
	return guard.IPsecPolicy{Present: true, Disabled: yes("disabled"), Template: yes("template"),
		Dst: v["dstAddress"], Protocol: v["protocol"], Action: v["action"], Peer: v["peer"]}
}

// ipsecChanged reports whether an edit moves anything but the comment. A secret
// left blank keeps its stored value, so a blank secret is no change.
func ipsecChanged(was, now map[string]string) bool {
	if was == nil || now == nil {
		return true
	}
	for k, v := range now {
		if k == "comment" || ((k == "secret" || k == "password" || k == "ppkSecret") && v == "") {
			continue
		}
		if was[k] != v {
			if (v == "yes" && was[k] == "true") || (v == "no" && was[k] == "false") {
				continue
			}
			return true
		}
	}
	return false
}

// ruleChangeOf reads a rule in the registry's field names.
//
// NOT LAID OVER THE STORED ROW, unlike routeChangeOf: a form sends every field,
// and a partial edit has already been merged with the stored row before it gets
// here, so a missing key is a cleared one — and a cleared destination is the
// widest rule there is, which is exactly the change this guard must not miss.
func ruleChangeOf(v map[string]string) guard.RuleChange {
	d := v["disabled"]
	return guard.RuleChange{Present: true, Disabled: d == "true" || d == "yes",
		Dst: v["dstAddress"], Src: v["srcAddress"], Mark: v["routingMark"],
		Interface: v["interface"], Action: v["action"], Table: v["table"]}
}

// addressVerdict asks the address lockout guard about one IP address write, of
// either family. /user/active is read fresh, in the same tick as the write, as
// the route guard reads it.
func (cn *conn) addressVerdict(res *resource.Resource, action string,
	values, before map[string]string) guard.Verdict {

	var active []routeros.Reply
	if rows, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/active/print"}); err == nil {
		active = rows
	}
	var was, now guard.AddressChange
	if before != nil {
		was = addressChangeOf(histValues(res.RowValues(before)), guard.AddressChange{})
	}
	if action != "delete" && values != nil {
		// Laid over the stored row, as for routes: a form that did not send a
		// field has not changed it.
		now = addressChangeOf(values, was)
	}
	return guard.CheckAddressEdit(active, []string{cn.rsession.Username()}, action, was, now)
}

// listVerdict asks the list-membership guard about one address-list entry or
// interface-list member write.
//
// ── WHAT IT READS, AND WHEN ─────────────────────────────────────────────────
//
// The management path and the input-chain rules, FRESH, in the same tick as the
// write, as fwVerdict does: a rule enabled since the Firewall page last read the
// table is exactly the one that matters. The filter is read through a
// proplist of the clauses the guard compares, so the read is small; rules
// without a list clause are dropped in listDecision. Both reads FAIL SOFT:
// a menu this account cannot read costs the warning, never the write.
//
// Which clause and which field depend on the list kind, and nothing else does.
func (cn *conn) listVerdict(res *resource.Resource, action string,
	values, before map[string]string) guard.Verdict {

	path := cn.managementPath()
	if !path.Resolved {
		return guard.Verdict{Level: "none"} // no rule read for a guard that cannot speak
	}
	clause, menus := listClause(res)
	var rules []routeros.Reply
	for _, menu := range menus {
		rows, err := cn.rsession.Exec(routeros.Cmd{Path: menu, Args: []string{
			"=.proplist=.id,chain,action,disabled,src-address,protocol,dst-port,in-interface," + clause}})
		if err == nil {
			rules = append(rules, rows...)
		}
	}
	return listDecision(res, action, values, before, path, cn.rsession.APIPort(), rules)
}

// serviceVerdict asks the IP-services guard about one /ip/service write. Which
// service is ours comes from the session's TLS setting; where the router sees us
// from is read FRESH from /user/active, and a denied read leaves it unresolved,
// which the guard treats as "cannot show an address restriction admits us".
func (cn *conn) serviceVerdict(action string, values, before map[string]string) guard.Verdict {
	var active []routeros.Reply
	if rows, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/active/print"}); err == nil {
		active = rows
	}
	self, resolved := guard.SelfAddresses(active, []string{cn.rsession.Username()})
	return serviceDecision(cn.rsession.UsesTLS(), self, resolved, action, values, before)
}

// serviceDecision is serviceVerdict without the reads. `before` is the row as
// RouterOS returned it, keyed by RouterOS names; `values` are the write's, keyed
// by the ipService FIELD names.
//
// ── THE ADDRESS RESTRICTION HAS TWO NAMES ───────────────────────────────────
//
// RouterOS 7.24 renamed `address` to `available-from` on /ip/service ("backwards
// compatible via deprecation", changelog 7.24), and prints only the new name. A
// row from an older router carries `address`. The stored restriction is read
// under either, so the guard judges an older router's row correctly too.
func serviceDecision(tls bool, self []string, resolved bool, action string,
	values, before map[string]string) guard.Verdict {

	ours := "api"
	if tls {
		ours = "api-ssl"
	}
	if before == nil {
		return guard.Verdict{Level: "none"} // /ip/service rows are fixed: nothing is created
	}
	row := func(v map[string]string, base guard.ServiceRow, addrKeys ...string) guard.ServiceRow {
		r := base
		for k, dst := range map[string]*string{"name": &r.Name, "port": &r.Port, "vrf": &r.VRF} {
			if x, ok := v[k]; ok {
				*dst = x
			}
		}
		for _, k := range addrKeys {
			if x, ok := v[k]; ok {
				r.Address = x
			}
		}
		if x, ok := v["disabled"]; ok {
			r.Disabled = x == "yes" || x == "true"
		}
		return r
	}
	// The older name first, so a row carrying both reads the current one.
	was := row(before, guard.ServiceRow{}, "address", "available-from")
	now := was
	switch {
	case action == "delete":
		now.Disabled = true // not possible on /ip/service, and refused as the same cut
	case action == "disable":
		now.Disabled = true
	case action == "enable":
		now.Disabled = false
	case values != nil:
		now = row(values, was, "availableFrom")
	}
	return guard.CheckServiceEdit(ours, self, resolved, was, now)
}

// codeAllowed is whether this session may change RouterOS code or run it: a
// SIGNED-IN global administrator, the raw-command gate's first condition.
// `isGlobalAdmin` answers true with sign-in switched off, which is right for
// reading the principal graph and wrong here, as rawCommandGate says.
func (cn *conn) codeAllowed() bool {
	return cn.sess != nil && cn.sess.AuthMode != "none" && cn.srv.isGlobalAdmin(cn.sess)
}

// codeDecision is the codeGate verdict: a write that CHANGES a Code field, or
// a row action that runs code, is refused unless `allowed`. Every write path
// reaches it through verdictFor — a save, an undo or redo, a row action — so
// no route around it exists. A rename or a comment is not a code change.
func codeDecision(res *resource.Resource, action string, values, before map[string]string, allowed bool) guard.Verdict {
	if allowed {
		return guard.Verdict{Level: "none"}
	}
	changes := res.RunsCode(action)
	if (action == "create" || action == "update") && res.CodeChange(values, before) {
		changes = true
	}
	if !changes {
		return guard.Verdict{Level: "none"}
	}
	return guard.Verdict{Level: "refuse", Code: "code-requires-admin",
		Detail: map[string]any{"value": res.Label}}
}

// certVerdict asks the certificate guard about one certificate write. Only a
// delete can take away the certificate api-ssl presents; the service row is read
// FRESH, because which certificate it names is the whole question.
func (cn *conn) certVerdict(action string, before map[string]string) guard.Verdict {
	if action != "delete" || before == nil {
		return guard.Verdict{Level: "none"}
	}
	rows, err := cn.rsession.Exec(routeros.Cmd{Path: "/ip/service/print",
		Args: []string{"?name=api-ssl", "=.proplist=name,certificate,dynamic,connection"}})
	return certDecision(cn.rsession.UsesTLS(), rows, err, before)
}

// certDecision is certVerdict without the read. When MikroDash speaks api-ssl
// and the service row cannot be read, the removal is REFUSED: the guard cannot
// show the certificate is not the one it depends on, and the cost of being wrong
// is a site visit, as for the service guard.
func certDecision(tls bool, serviceRows []routeros.Reply, readErr error, before map[string]string) guard.Verdict {
	if !tls {
		return guard.Verdict{Level: "none"}
	}
	if readErr != nil {
		return guard.Verdict{Level: "refuse", Code: "certificate-unknown",
			Detail: map[string]any{"value": before["name"]}}
	}
	// THE SERVICE ROW, NOT A CONNECTION. RouterOS 7.24 lists live connections in
	// /ip/service as dynamic rows with the service's name and no certificate, so
	// `?name=api-ssl` answers with the service AND MikroDash's own session. This
	// took the last match, the connection, read "no certificate", and let the
	// CHR's api-ssl certificate be deleted (2026-09-18, restored the same hour).
	used := ""
	for _, r := range serviceRows {
		if r["name"] == "api-ssl" && r["dynamic"] != "true" && r["connection"] != "true" {
			used = r["certificate"]
		}
	}
	return guard.CheckCertificateRemove(tls, used, before["name"])
}

// listClause is which firewall clause matches this resource's lists, and which
// filter menus to read for it. Address lists are IPv4
// (`/ip/firewall/address-list`), so only the IPv4 filter matches them; an
// interface list is matched by both families' filters.
func listClause(res *resource.Resource) (string, []string) {
	if res.Key == "ifListMember" || res.Key == "ifList" {
		return "in-interface-list", []string{"/ip/firewall/filter/print", "/ipv6/firewall/filter/print"}
	}
	return "src-address-list", []string{"/ip/firewall/filter/print"}
}

// listDecision is listVerdict without the reads: the row before, the values
// after, the management path and the filter rows in, the verdict out. Split so
// the mapping — field names, the overlay of a partial edit, which rows count —
// is testable without a router.
func listDecision(res *resource.Resource, action string, values, before map[string]string,
	path guard.ManagementPath, apiPort int, filterRows []routeros.Reply) guard.Verdict {

	kind, field := "address", "address"
	if res.Key == "ifListMember" {
		kind, field = "interface", "interface"
	}
	clause, _ := listClause(res)
	ctx := guard.FWContext{Resolved: path.Resolved, Addresses: path.Addresses,
		Interfaces: path.Interfaces, APIPort: apiPort}
	rules := listRules(filterRows, clause)
	if res.Key == "ifList" {
		// A list's DEFINITION: its name, and what it includes and excludes.
		def := func(v map[string]string, base guard.ListDef) guard.ListDef {
			d := base
			d.Present = true
			if x, ok := v["name"]; ok {
				d.Name = x
			}
			if x, ok := v["include"]; ok {
				d.Include = x
			}
			if x, ok := v["exclude"]; ok {
				d.Exclude = x
			}
			return d
		}
		var was, now guard.ListDef
		if before != nil {
			was = def(histValues(res.RowValues(before)), guard.ListDef{})
		}
		if action != "delete" && values != nil {
			now = def(values, was)
		}
		return guard.CheckListDefinition(ctx, rules, action, was, now)
	}
	of := func(v map[string]string, base guard.ListMember) guard.ListMember {
		m := base
		m.Present = true
		if x, ok := v["list"]; ok {
			m.List = x
		}
		if x, ok := v[field]; ok {
			m.Value = x
		}
		if x, ok := v["disabled"]; ok {
			m.Disabled = x == "yes" || x == "true"
		}
		return m
	}
	var was, now guard.ListMember
	if before != nil {
		was = of(histValues(res.RowValues(before)), guard.ListMember{})
	}
	if action != "delete" && values != nil {
		// Laid over the stored row: a partial edit leaves the rest as it was.
		now = of(values, was)
	}
	return guard.CheckListMember(ctx, kind, rules, action, was, now)
}

// listRules is the input-chain filter rows that carry this list clause.
func listRules(filterRows []routeros.Reply, clause string) []guard.ListRule {
	var rules []guard.ListRule
	for _, r := range filterRows {
		if r[clause] == "" || r["chain"] != "input" {
			continue
		}
		rules = append(rules, guard.ListRule{ID: r[".id"], Match: r[clause], Rule: guard.FWRule{
			Chain: r["chain"], Action: r["action"], SrcAddress: r["src-address"],
			Protocol: r["protocol"], DstPort: r["dst-port"], InInterface: r["in-interface"],
			Disabled: r["disabled"] == "true",
		}})
	}
	return rules
}

// queueVerdict asks the self-throttle guard about one simple queue write.
//
// Where the router sees us from is read FRESH from /user/active, as the other
// guards read it, and a router that denies it yields no addresses: this guard
// FAILS OPEN (see guard/queueguard.go), unlike the lockout guards.
func (cn *conn) queueVerdict(res *resource.Resource, action string,
	values, before map[string]string) guard.Verdict {

	var active []routeros.Reply
	if rows, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/active/print"}); err == nil {
		active = rows
	}
	self, _ := guard.SelfAddresses(active, []string{cn.rsession.Username()})
	var was map[string]string
	if before != nil {
		was = histValues(res.RowValues(before))
	}
	return queueThrottleVerdict(self, action, values, was)
}

// queueThrottleVerdict is the pure half of queueVerdict, in the registry's field
// names (target, maxLimit, disabled).
//
//   - create and update are checked as the values that will be in force, an
//     update laid over the row it edits, and an update is only a warning when it
//     makes things worse than that row;
//   - enable is the moment a throttle takes effect: the row's values, checked as
//     enabled and with no "before", since a disabled queue was not in force;
//   - delete, disable and move cannot tighten anything.
func queueThrottleVerdict(self []string, action string, values, before map[string]string) guard.Verdict {
	none := guard.Verdict{Level: "none"}
	of := func(v map[string]string, base guard.SimpleQueueValues) guard.SimpleQueueValues {
		q := base
		if x, ok := v["target"]; ok && x != "" {
			q.Target = x
		}
		if x, ok := v["maxLimit"]; ok {
			q.MaxLimit = guard.ParsePair(x)
		}
		if d, ok := v["disabled"]; ok {
			q.Disabled = d == "true" || d == "yes"
		}
		return q
	}
	switch action {
	case "create":
		return guard.CheckSimpleQueue(self, of(values, guard.SimpleQueueValues{}), nil, guard.SelfThrottleFloorBps)
	case "update":
		if before == nil {
			return guard.CheckSimpleQueue(self, of(values, guard.SimpleQueueValues{}), nil, guard.SelfThrottleFloorBps)
		}
		was := of(before, guard.SimpleQueueValues{})
		return guard.CheckSimpleQueue(self, of(values, was), &was, guard.SelfThrottleFloorBps)
	case "enable":
		q := of(values, guard.SimpleQueueValues{})
		q.Disabled = false
		return guard.CheckSimpleQueue(self, q, nil, guard.SelfThrottleFloorBps)
	}
	return none
}

// selfAccountVerdict asks the lockout guard about a /user or /user/group write.
//
// The three tables are read FRESH, as the Router Users handlers read them, and a
// table that cannot be read leaves MikroDash unidentified, which REFUSES: this
// guard fails closed (see guard/selfguard.go), because a missed refusal here
// breaks the login and the fix is WinBox.
func (cn *conn) selfAccountVerdict(res *resource.Resource, action string,
	values, before map[string]string) guard.Verdict {

	var users, active []routeros.Reply
	if rows, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/print"}); err == nil {
		users = rows
	}
	if rows, err := cn.rsession.Exec(routeros.Cmd{Path: "/user/active/print"}); err == nil {
		active = rows
	}
	self := guard.ResolveSelf(users, active, []string{cn.rsession.Username()})
	return selfAccountDecision(self, res.Menu, action, values, routeros.Reply(before))
}

// selfAccountDecision is the pure half: the guard's Refusal as a Verdict, with
// the same values the Router Users handlers passed.
//
//   - a user write always carries its name and group as values, so moving any
//     user INTO MikroDash's group is refused, and so is editing a user already
//     in it: the guard's own "deliberately blunt" rule, unchanged;
//   - a group write carries its name;
//   - delete targets the row; enable and disable are edits of the row.
func selfAccountDecision(self guard.Self, menu, action string, values map[string]string,
	before routeros.Reply) guard.Verdict {

	verb := "set"
	switch action {
	case "create":
		verb = "add"
	case "delete":
		verb = "remove"
	}
	var target routeros.Reply
	if before != nil {
		target = routeros.Reply{"name": before["name"], "group": before["group"]}
	}
	pick := func(keys ...string) (map[string]string, map[string]bool) {
		vals, set := map[string]string{}, map[string]bool{}
		if verb == "remove" {
			return vals, set
		}
		for _, k := range keys {
			v, ok := values[k]
			if !ok && before != nil {
				v, ok = before[k], before[k] != ""
			}
			if ok {
				vals[k], set[k] = strings.TrimSpace(v), true
			}
		}
		return vals, set
	}
	var r guard.Refusal
	switch menu {
	case "/user":
		vals, set := pick("name", "group")
		r = guard.CheckUser(self, guard.UserAction{Verb: verb, Target: target, Values: vals, ValueSet: set})
	case "/user/group":
		if target != nil {
			target = routeros.Reply{"name": before["name"]}
		}
		vals, set := pick("name")
		r = guard.CheckGroup(self, guard.UserAction{Verb: verb, Target: target, Values: vals, ValueSet: set})
	default:
		// Declared on a menu this guard knows nothing about: refuse rather than
		// pass, as an unported guard does.
		r = guard.Refusal{Code: "self-unresolved"}
	}
	if r.OK {
		return guard.Verdict{Level: "none"}
	}
	return guard.Verdict{Level: "refuse", Code: r.Code, Detail: map[string]any{"value": r.Detail}}
}

// addressChangeOf reads an address in the registry's field names, laid over base.
func addressChangeOf(v map[string]string, base guard.AddressChange) guard.AddressChange {
	r := base
	r.Present = true
	if x, ok := v["address"]; ok && x != "" {
		r.Address = x
	}
	if x, ok := v["interface"]; ok && x != "" {
		r.Interface = x
	}
	if d, ok := v["disabled"]; ok {
		r.Disabled = d == "true" || d == "yes"
	}
	return r
}

// routeChangeOf reads a route in the registry's field names, laid over base.
func routeChangeOf(v map[string]string, base guard.RouteChange) guard.RouteChange {
	r := base
	r.Present = true
	set := func(dst *string, key string) {
		if x, ok := v[key]; ok && x != "" {
			*dst = x
		}
	}
	set(&r.Dst, "dstAddress")
	set(&r.Gateway, "gateway")
	set(&r.Distance, "distance")
	set(&r.Table, "routingTable")
	if d, ok := v["disabled"]; ok {
		r.Disabled = d == "true" || d == "yes"
	}
	return r
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
	// TWO NAMESPACES ON ONE LINE, AND ONLY ONE OF THEM MOVED. The case label is
	// a PAGE key and was renamed to `wifi-networks` on 2026-09-01;
	// `CollectorEnabled("wifi")` is a COLLECTOR key and was not renamed at all.
	// Changing both would break the refresh; changing neither left it dead.
	case "wifi-networks":
		if cn.rsession.CollectorEnabled("wifi") {
			cn.rsession.Wifi().RefreshNow()
		}
	case "capsman":
		if cn.rsession.CollectorEnabled("capsman") {
			cn.rsession.Capsman().RefreshNow()
		}
	case "netwatch":
		// RefreshNow re-reads past the cache, so the table shows the router's answer.
		if cn.rsession.CollectorEnabled("netwatch") {
			cn.rsession.Netwatch().RefreshNow()
		}
	case "interfaces":
		if cn.rsession.CollectorEnabled("ifStatus") {
			cn.rsession.IfStatus().RefreshNow()
		}
	case "users":
		if cn.rsession.CollectorEnabled("rosusers") {
			cn.rsession.RosUsers().RefreshNow()
		}
	case "queues":
		// Rates are counter deltas, and a set or a reset-counters can zero a
		// counter: the next window would be measured against a baseline the
		// router no longer agrees with, so the baseline goes first. Forgetting
		// a baseline starts nothing, so only the re-read is gated.
		cn.rsession.Queues().ForgetRates()
		if cn.rsession.CollectorEnabled("queues") {
			cn.rsession.Queues().RefreshNow()
		}
	case "dhcp":
		// THE LEASE TABLE FIRST, THEN THE SUBNETS BUILT FROM IT. Leases are read
		// on the router's DHCP interval (290 s on the Standard profile), so without
		// this a lease saved on the DHCP page appeared only when that ran out:
		// reported 2026-09-19, and the log said "dhcpLease belongs to page "dhcp",
		// which has no collector to refresh" on every save. The networks re-read
		// after it, so the pool gauge counts the new lease too.
		if cn.rsession.CollectorEnabled("dhcpLeases") {
			cn.rsession.DHCPLeases().RefreshNow()
		}
		if cn.rsession.CollectorEnabled("dhcpNetworks") {
			cn.rsession.DHCPNetworks().RefreshNow()
		}
	// FOUND BY TestEveryWritablePageIsRefreshedAfterAWrite with the DHCP case:
	// a saved static route or WireGuard peer waited for the next scheduled read
	// too, with the same log line as the only trace.
	case "routing":
		if cn.rsession.CollectorEnabled("routing") {
			cn.rsession.Routing().RefreshNow()
		}
	// ── `case "vpn"` WAS HERE, AND ITS RESOURCE MOVED ────────────────────
	//
	// The VPN page's only writable rows were the WireGuard peers, and those
	// belong to the WireGuard page now. No resource names page "vpn", so that
	// case could never run again — which the refresh ledger says out loud, in
	// the direction that catches a case nothing can reach.
	//
	// ── TWO REFRESHES, BECAUSE THE PAGE HAS TWO SOURCES ──────────────────
	//
	// A saved INTERFACE is a generated-area row and a saved PEER is in the vpn
	// payload. Naming the page here takes it out of the area default below, so
	// the area refresh has to be spelled out — and the ledger that checks this
	// accepts a case that does only one of the two, which is exactly why it is
	// written down here instead.
	case "wireguard":
		cn.rsession.Areas().RefreshNow("wireguard")
		if cn.rsession.CollectorEnabled("vpn") {
			cn.rsession.VPN().RefreshNow()
		}
	case "ppp":
		// The PPP collector reads its config tables — profiles, servers and the
		// secrets — only every `pppConfigEvery` ticks, so without this a saved
		// subscriber would not appear for up to a minute. `RefreshNow` resets
		// that counter, which is the whole reason it exists rather than being a
		// plain Tick.
		if cn.rsession.CollectorEnabled("ppp") {
			cn.rsession.PPP().RefreshNow()
		}
	default:
		// ── A GENERATED PAGE, AND ONE CASE FOR ALL OF THEM ──────────────────
		//
		// An area's rows come from the `areas` collector, which holds one payload
		// per area and re-reads that area alone. Without this a write through the
		// generated page's own dialog reached the router and the table kept
		// showing the old value until the next poll — measured on the CHR, where
		// an edited comment sat unchanged for a minute behind a dialog that had
		// already closed.
		if _, ok := areas.ByKey(res.Page); ok {
			if cn.rsession.CollectorEnabled("areas") {
				cn.rsession.Areas().RefreshNow(res.Page)
			}
			return
		}
		log.Printf("[res] %s belongs to page %q, which has no collector to refresh",
			res.Key, res.Page)
	}
}

// ── res:move ────────────────────────────────────────────────────────────────

// resMove is the socket handler: resolve, move, tell the browser. The pipeline
// itself is `moveRow`, split from the reporting for the reason `writeRow` and
// `removeRow` are — the assistant needs the same sequence as a value it can
// hand back to a model rather than as a frame sent to a page.
func (cn *conn) resMove(raw json.RawMessage) {
	res, req := cn.resolve(raw, true)
	if res == nil {
		return
	}
	// PRESENCE, not emptiness. An anchor of "" means "land at the end", which is
	// a real instruction and different from sending no anchor at all — the
	// difference `hasOwnProperty(r, 'anchor')` carries on the Node side. A
	// struct field cannot hold it, so the raw request is probed for the key.
	req.HasAnchor = hasJSONKey(raw, "anchor")
	// A human at a form: no provenance to add.
	out := cn.moveRow(res, req, "")
	if out.Code == "" {
		// `movedId` is what the page pulses, so the eye can find the row that
		// just changed places in a table of thirty near-identical ones.
		EvResOk.Send(cn.srv.hub, cn.c, map[string]any{
			"resource": res.Key, "action": out.Action, "name": out.Name, "movedId": req.ID})
		return
	}
	cn.resErr(res.Key, out.Code, out.Name, out.Detail)
}

// moveRow reorders a row in a table where position is meaning.
//
// ORDERED TABLES ONLY, and `Ordered` is what says so. Everywhere else the router
// keeps its own order and moving a row would mean nothing.
//
// THE CALLER SENDS A DIRECTION OR AN ANCHOR, NEVER A POSITION. An arrow says
// which way, a drag — and the assistant's `before` — says which row to land
// before; none of them may name an index. The neighbour is resolved from a read
// taken inside this write-queue slot, so an operator clicking twice quickly, two
// operators at once, or a model working from a list it read a minute ago cannot
// move a rule to an index computed against a table that has already changed
// underneath them. Same reasoning as the fresh read everywhere else, applied to
// ordering.
//
// Like `writeRow` it performs every side effect and RETURNS what happened; `via`
// is the caller's provenance and is never read off the request.
func (cn *conn) moveRow(res *resource.Resource, req *resRequest, via string) writeOutcome {
	if !res.Ordered {
		return writeOutcome{Code: "bad-request"}
	}
	if req.ID == "" ||
		(!req.HasAnchor && req.Direction != "up" && req.Direction != "down") {
		return writeOutcome{Code: "bad-request"}
	}
	// A ROW CANNOT LAND BEFORE ITSELF. The drag path cannot produce it — it
	// ignores a pointer over the dragged row — but `before` is a model's word
	// for a row it chose, and RouterOS is not the place to find out.
	if req.HasAnchor && req.Anchor == req.ID {
		return writeOutcome{Code: "bad-request"}
	}
	var out writeOutcome
	err := cn.inWriteQueue(func() error {
		p, refusal := cn.prepareMove(res, req)
		if p == nil {
			out = refusal
			return nil
		}
		if gate := ackGate(p.verdict, req.Ack); gate != nil {
			code, _ := gate["code"].(string)
			delete(gate, "code")
			out = writeOutcome{Code: code, Name: p.name, Detail: gate}
			return nil
		}
		out = cn.commitMove(p, via)
		return nil
	})
	if err != nil {
		return writeOutcome{Code: writeFailCode(err), Name: req.ExpectedIdentity,
			Detail: map[string]any{"message": safe.Message(err.Error())}}
	}
	return out
}

// preparedMove is a reorder that has been CHECKED but not performed: the row the
// router holds now, its identity, where RouterOS will be told to put it, and the
// guard's verdict. The move twin of `preparedWrite` and `preparedRemove`, and
// for the same reason — so the assistant's dialog and the move itself are built
// from one server-side reading.
type preparedMove struct {
	res  *resource.Resource
	req  *resRequest
	name string
	at   int
	// dest is the id RouterOS is told to place the row BEFORE, "" for the end of
	// the table. Resolved here from the same read the checks ran against.
	dest string
	// how is what the audit row records about the gesture: an arrow, a drag, or
	// the assistant naming the row to land before.
	how     string
	verdict guard.Verdict
}

// prepareMove reads the menu fresh, finds the row, resolves the neighbour the
// move is relative to and runs the guards. It changes nothing.
//
// A nil prepared move means the returned outcome is a refusal and the caller
// must stop.
func (cn *conn) prepareMove(res *resource.Resource, req *resRequest) (*preparedMove, writeOutcome) {
	name := req.ExpectedIdentity
	rows, err := cn.readMenu(res)
	if err != nil {
		return nil, writeOutcome{Code: writeFailCode(err), Name: name,
			Detail: map[string]any{"message": safe.Message(err.Error())}}
	}
	at := -1
	for i, r := range rows {
		if r[".id"] == req.ID {
			at = i
			break
		}
	}
	if at < 0 {
		return nil, writeOutcome{Code: "stale-row", Name: name}
	}
	row := rows[at]
	// THE ROW'S OWN IDENTITY, never the request's: an audit row that took its
	// name from an optional field records a move without saying what moved.
	name = res.IdentityOf(row)
	if req.ExpectedIdentity != "" && name != req.ExpectedIdentity {
		return nil, writeOutcome{Code: "stale-row", Name: name}
	}

	up := req.Direction == "up"
	how, dest := "down", ""
	if req.HasAnchor {
		how, dest = "drag", req.Anchor
		// The row the move aims at must still be there. If it has gone, the
		// table the caller was looking at is not the table on the router, and
		// dropping the rule somewhere approximate — at the END, which is what an
		// unresolvable destination means to RouterOS — is worse than saying so.
		if req.Anchor != "" && rowByID(rows, req.Anchor) == nil {
			return nil, writeOutcome{Code: "stale-row", Name: name}
		}
		if anchorAt(rows, at) == req.Anchor {
			// Dropped exactly where it already was.
			return nil, writeOutcome{Code: "at-end", Name: name}
		}
	} else {
		if up {
			how = "up"
		}
		if (up && at == 0) || (!up && at == len(rows)-1) {
			// Already where it is going. Not an error worth a banner, but the
			// page should stop drawing an arrow that does nothing.
			return nil, writeOutcome{Code: "at-end", Name: name}
		}
		// RouterOS inserts the moved rule BEFORE `destination`. So moving up
		// means "before the rule currently above me", and moving down means
		// "before the rule two below" — with no destination at all when there is
		// nothing below, which sends it to the end.
		if up {
			dest = rows[at-1][".id"]
		} else if at+2 < len(rows) {
			dest = rows[at+2][".id"]
		}
	}

	verdict, gerr := cn.verdictFor(res, "move", histValues(res.RowValues(row)), row)
	if gerr != nil {
		cn.recorder().Denied(audit.Event{
			Action: res.Key + ".move", TargetType: res.Key, RouterID: cn.routerID,
			TargetID: req.ID, TargetName: name, Note: "guard-not-ported: " + gerr.Error(),
		})
		return nil, writeOutcome{Code: "guard-not-ported", Name: name,
			Detail: map[string]any{"message": safe.Message(gerr.Error())}}
	}
	if r := cn.guardRefusal(res, "move", req.ID, name, verdict); r != nil {
		return nil, *r
	}
	return &preparedMove{res: res, req: req, name: name, at: at, dest: dest, how: how,
		verdict: verdict}, writeOutcome{}
}

// commitMove sends the move and confirms it by reading the table back.
func (cn *conn) commitMove(p *preparedMove, via string) writeOutcome {
	res, req := p.res, p.req

	// `=numbers=`, not `=.id=`. The move command addresses rows by number, and
	// an `.id` is accepted there where it is not elsewhere.
	args := []string{"=numbers=" + req.ID}
	if p.dest != "" {
		args = append(args, "=destination="+p.dest)
	}
	if _, err := cn.rsession.Exec(routeros.Cmd{Path: res.Menu + "/move", Args: args}); err != nil {
		return writeOutcome{Code: writeFailCode(err), Name: p.name,
			Detail: map[string]any{"message": safe.Message(err.Error())}}
	}

	// The ORDER must be the one asked for before the move is reported (#97): the
	// row sits immediately before its destination, or last when there is none.
	// A read-back that fails, or an order that does not hold, is an unknown
	// outcome rather than the ordinary success it used to be.
	moved, merr := cn.readMenu(res)
	nowAt, placed := -1, false
	if merr == nil {
		nowAt, placed = confirmMoved(moved, req.ID, p.dest)
	}
	if !placed {
		return cn.unknownOutcome(res, res.Key+".move", req.ID, p.name, req.Ack, via)
	}

	cn.recorder().Record(audit.Event{
		Action: res.Key + ".move", TargetType: res.Key, RouterID: cn.routerID,
		TargetID: req.ID, TargetName: p.name,
		Before: map[string]any{"position": p.at},
		After:  map[string]any{"position": nowAt},
		Extra:  append([]audit.KV{{Key: "how", Value: p.how}}, writeExtra(req.Ack, via)...),
	})

	cn.refreshFor(res)
	return writeOutcome{Action: "move", Name: p.name}
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
