package server

// The move path, driven against a scripted router.
//
// ── WHAT IS WORTH TESTING HERE ──────────────────────────────────────────────
//
// `moveRow` is now reached from two places: the page's arrows and drag, and the
// assistant's `before`. What each of them must not be able to do is move the
// WRONG rule, or move a rule somewhere nobody asked for. RouterOS makes the
// second easy to get wrong: a `destination` it cannot resolve does not fail, it
// means "the end of the table" — so a destination that has gone would silently
// append, which is the exact failure the assistant's move exists to fix.
//
// Every test here counts the commands the router was sent, because "refused" and
// "sent and then reported as refused" look identical from the outside.

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
	"mikrodash/internal/session"
)

// moveRouter is a firewall filter table that can be printed and moved.
type moveRouter struct {
	mu   sync.Mutex
	rows []routeros.Reply
	// cmds is every command sent, in order, so a test can say "nothing reached
	// the router" and mean it.
	cmds []string
	// frozen makes /move a no-op the router still accepts: RouterOS answering
	// `!done` says the command was taken, not that the table changed.
	frozen bool
}

func fwRow(id, chain, action, comment string) routeros.Reply {
	return routeros.Reply{".id": id, "chain": chain, "action": action, "comment": comment}
}

func (m *moveRouter) session(h *hub.Hub) *session.Session {
	return session.NewForTestWithExec(h, "r-A", func(cmd routeros.Cmd) ([]routeros.Reply, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.cmds = append(m.cmds, cmd.Path+" "+strings.Join(cmd.Args, " "))
		switch {
		case cmd.Path == "/ip/firewall/filter/print":
			for _, a := range cmd.Args {
				if strings.HasPrefix(a, "?.id=") {
					id := strings.TrimPrefix(a, "?.id=")
					for _, r := range m.rows {
						if r[".id"] == id {
							return []routeros.Reply{r}, nil
						}
					}
					return []routeros.Reply{}, nil
				}
			}
			out := make([]routeros.Reply, len(m.rows))
			copy(out, m.rows)
			return out, nil
		case cmd.Path == "/ip/firewall/filter/move":
			if !m.frozen {
				m.apply(cmd.Args)
			}
			return []routeros.Reply{}, nil
		}
		// /user/active/print and /ip/address/print: nothing, so the management
		// path is unresolved and fwGuard fails open. The guards have their own
		// tests; this file is about the ordering.
		return []routeros.Reply{}, nil
	})
}

// apply is RouterOS's own rule: the row lands BEFORE `destination`, or last when
// there is none. Called with m.mu held.
func (m *moveRouter) apply(args []string) {
	var id, dest string
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "=numbers="); ok {
			id = v
		}
		if v, ok := strings.CutPrefix(a, "=destination="); ok {
			dest = v
		}
	}
	var row routeros.Reply
	rest := make([]routeros.Reply, 0, len(m.rows))
	for _, r := range m.rows {
		if r[".id"] == id {
			row = r
			continue
		}
		rest = append(rest, r)
	}
	if row == nil {
		return
	}
	out := make([]routeros.Reply, 0, len(m.rows))
	placed := false
	for _, r := range rest {
		if r[".id"] == dest {
			out = append(out, row)
			placed = true
		}
		out = append(out, r)
	}
	if !placed {
		out = append(out, row)
	}
	m.rows = out
}

func (m *moveRouter) order() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.rows))
	for _, r := range m.rows {
		ids = append(ids, r[".id"])
	}
	return strings.Join(ids, ",")
}

func (m *moveRouter) sent(verb string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.cmds {
		if strings.HasPrefix(c, verb) {
			n++
		}
	}
	return n
}

// moveConn is a connection that may write the Firewall page, with a real audit
// database and grant graph behind it (`writerConn`, ai_write_test.go), and the
// scripted router as its session. `access` is what the SESSION's union says
// about the page, which is the coarse gate `resolve` consults.
func moveConn(t *testing.T, m *moveRouter, access string) (*conn, *db.DB, *hub.Client) {
	t.Helper()
	cn := writerConn(t, resource.FWFilter)
	cn.sess.Pages["firewall"] = access
	c := hub.NewClient("me", 256)
	cn.srv.hub.Add(c)
	cn.c = c
	cn.rsession = m.session(cn.srv.hub)
	return cn, cn.srv.auditDB, c
}

func moveAuditRows(t *testing.T, d *db.DB) []db.Row {
	t.Helper()
	page, err := d.QueryAuditEvents(db.Query{RouterIDs: []string{"r-A"}})
	if err != nil {
		t.Fatal(err)
	}
	return page.Rows
}

func threeRules() *moveRouter {
	return &moveRouter{rows: []routeros.Reply{
		fwRow("*1", "input", "accept", "management"),
		fwRow("*2", "input", "accept", "dns"),
		fwRow("*3", "input", "drop", "everything else"),
	}}
}

func moveFrame(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAMoveWithoutPermissionIsRefusedAndAudited.
//
// A move is a write, and it goes through `resolve` exactly as a save and a
// delete do — the denial is recorded, and nothing reaches the router. A denial
// that is refused but not recorded leaves no trace that anybody tried.
func TestAMoveWithoutPermissionIsRefusedAndAudited(t *testing.T) {
	m := threeRules()
	// Read but not write: the page is visible, the move is not permitted.
	cn, d, c := moveConn(t, m, "read")

	cn.resMove(moveFrame(t, map[string]any{
		"resource": "fwFilter", "id": "*3", "direction": "up",
	}))

	if n := m.sent("/ip/firewall/filter/move"); n != 0 {
		t.Errorf("%d move commands reached the router from a viewer who may not write", n)
	}
	if got := m.order(); got != "*1,*2,*3" {
		t.Errorf("the table is %s; a refused move changed it", got)
	}
	if out := strings.Join(frames(c), "|"); !strings.Contains(out, `"code":"denied"`) {
		t.Errorf("the page was not told it was denied: %s", out)
	}
	rows := moveAuditRows(t, d)
	if len(rows) != 1 {
		t.Fatalf("%d audit rows for a denied move, want 1", len(rows))
	}
	if rows[0].Outcome != "denied" {
		t.Errorf("the denied move is recorded with outcome %q", rows[0].Outcome)
	}
	if rows[0].TargetID == nil || *rows[0].TargetID != "*3" {
		t.Errorf("the denial does not say which row was attempted: %v", rows[0].TargetID)
	}
}

// TestAMoveAddressesTheRowByIdentityNotByTheIdAlone.
//
// The browser sends an `.id` and the identity it believed that row had. The id
// is how the row is ADDRESSED; the identity is what authorises the move. A rule
// deleted and replaced between the render and the click reuses neither in
// practice — but `find` and `prepareWrite` make the same check for a save, and a
// move that skipped it would reorder a rule nobody was looking at.
func TestAMoveAddressesTheRowByIdentityNotByTheIdAlone(t *testing.T) {
	m := threeRules()
	cn, _, c := moveConn(t, m, "write")

	// The identity of *3 on the router is the drop rule. The request claims it
	// is something else, which is what a stale table looks like.
	cn.resMove(moveFrame(t, map[string]any{
		"resource": "fwFilter", "id": "*3", "direction": "up",
		"expectedIdentity": resource.FWFilter.IdentityOf(fwRow("*3", "input", "accept", "dns")),
	}))

	if n := m.sent("/ip/firewall/filter/move"); n != 0 {
		t.Errorf("%d move commands were sent for a row whose identity had changed", n)
	}
	if out := strings.Join(frames(c), "|"); !strings.Contains(out, `"code":"stale-row"`) {
		t.Errorf("a changed row was not reported as stale: %s", out)
	}

	// THE CONTROL. The same move with the identity the router actually holds
	// must go through, or this test would pass against a move that refuses
	// everything.
	cn.resMove(moveFrame(t, map[string]any{
		"resource": "fwFilter", "id": "*3", "direction": "up",
		"expectedIdentity": resource.FWFilter.IdentityOf(fwRow("*3", "input", "drop", "everything else")),
	}))
	if got := m.order(); got != "*1,*3,*2" {
		t.Errorf("the table is %s, want *1,*3,*2: the matching identity was refused too", got)
	}
}

// TestADestinationThatIsNotInTheTableIsRefusedRatherThanAppended.
//
// ── THE ONE ROUTEROS BEHAVIOUR THAT MAKES THIS DANGEROUS ────────────────────
//
// `/ip/firewall/filter/move` places the row before `destination`, "placed at the
// end of the list if not specified" (MikroTik, Scripting, menu commands). A
// destination the router cannot resolve is therefore not an error: it is the end
// of the table. So a rule aimed at a row that has been deleted would land LAST —
// in a firewall, behind the drop-all, which is the rule that never runs.
//
// The anchor is checked against the table read in this same slot, before the
// command is built.
func TestADestinationThatIsNotInTheTableIsRefusedRatherThanAppended(t *testing.T) {
	m := threeRules()
	cn, _, c := moveConn(t, m, "write")

	cn.resMove(moveFrame(t, map[string]any{
		"resource": "fwFilter", "id": "*3", "anchor": "*404",
	}))

	if n := m.sent("/ip/firewall/filter/move"); n != 0 {
		t.Errorf("%d move commands were sent for a destination that is not in the table; "+
			"RouterOS reads an unresolvable destination as the END of the list", n)
	}
	if got := m.order(); got != "*1,*2,*3" {
		t.Errorf("the table is %s; the rule moved anyway", got)
	}
	if out := strings.Join(frames(c), "|"); !strings.Contains(out, `"code":"stale-row"`) {
		t.Errorf("an unknown destination was not reported as stale: %s", out)
	}

	// THE END OF THE TABLE IS STILL REACHABLE, deliberately: an anchor that is
	// present and EMPTY means "last", and must not be caught by the check above.
	cn.resMove(moveFrame(t, map[string]any{
		"resource": "fwFilter", "id": "*1", "anchor": "",
	}))
	if got := m.order(); got != "*2,*3,*1" {
		t.Errorf("the table is %s, want *2,*3,*1: the empty anchor no longer means the end", got)
	}
}

// TestARowCannotBeMovedBeforeItself. The drag cannot produce it; `before` can,
// because it is a model's word for a row it chose.
func TestARowCannotBeMovedBeforeItself(t *testing.T) {
	m := threeRules()
	cn, _, c := moveConn(t, m, "write")

	cn.resMove(moveFrame(t, map[string]any{
		"resource": "fwFilter", "id": "*2", "anchor": "*2",
	}))
	if n := m.sent("/ip/firewall/filter/move"); n != 0 {
		t.Errorf("%d move commands were sent for a row aimed at itself", n)
	}
	if out := strings.Join(frames(c), "|"); !strings.Contains(out, `"code":"bad-request"`) {
		t.Errorf("a row aimed at itself was not refused: %s", out)
	}
}

// TestAnAppliedMoveIsAuditedWithTheObservedPositionsAndItsProvenance.
//
// The audit row is the record of record for an ordered table, because position
// IS the configuration there and nothing else records it. It must say where the
// row was, where it ended up as READ BACK, and — when the assistant asked — that
// the assistant asked.
func TestAnAppliedMoveIsAuditedWithTheObservedPositionsAndItsProvenance(t *testing.T) {
	m := threeRules()
	cn, d, _ := moveConn(t, m, "write")

	out := cn.moveRow(resource.FWFilter,
		&resRequest{Resource: "fwFilter", ID: "*3", HasAnchor: true, Anchor: "*1"}, "agent")
	if out.Code != "" {
		t.Fatalf("the move was refused: %s %v", out.Code, out.Detail)
	}
	if got := m.order(); got != "*3,*1,*2" {
		t.Fatalf("the table is %s, want *3,*1,*2", got)
	}

	rows := moveAuditRows(t, d)
	if len(rows) != 1 {
		t.Fatalf("%d audit rows for one move, want 1", len(rows))
	}
	r := rows[0]
	if r.Action != "fwFilter.move" {
		t.Errorf("the move is recorded as %q", r.Action)
	}
	if r.Outcome == "denied" || r.Outcome == "failed" {
		t.Errorf("an applied move is recorded with outcome %q", r.Outcome)
	}
	if r.TargetName == nil || *r.TargetName == "" {
		t.Error("the audit row does not say which rule moved")
	}
	detail := ""
	if r.Detail != nil {
		detail = *r.Detail
	}
	// It was third and is now first: both numbers must be in the row, or it
	// records that something moved without recording where.
	for _, want := range []string{`"field":"position"`, `"from":2`, `"to":0`,
		`"via":"agent"`, `"how":"drag"`} {
		if !strings.Contains(detail, want) {
			t.Errorf("the audit detail is missing %s: %s", want, detail)
		}
	}
}

// TestAMoveTheRouterDidNotMakeIsNotReportedAsOne.
//
// RouterOS answering `!done` says the command was accepted, not that the table
// changed. `commitMove` reads the order back; when it does not hold, the outcome
// is unknown — not success, and not failure either, because the move may well
// have happened.
func TestAMoveTheRouterDidNotMakeIsNotReportedAsOne(t *testing.T) {
	m := threeRules()
	m.frozen = true
	cn, d, _ := moveConn(t, m, "write")

	out := cn.moveRow(resource.FWFilter,
		&resRequest{Resource: "fwFilter", ID: "*3", HasAnchor: true, Anchor: "*1"}, "agent")
	if out.Code != "outcome-unknown" {
		t.Fatalf("a move the router did not make reports %q", out.Code)
	}
	rows := moveAuditRows(t, d)
	if len(rows) != 1 {
		t.Fatalf("%d audit rows, want 1", len(rows))
	}
	detail := ""
	if rows[0].Detail != nil {
		detail = *rows[0].Detail
	}
	if !strings.Contains(detail, "outcome-unknown") {
		t.Errorf("the trail records an unconfirmed move as an ordinary one: %s", detail)
	}
	if strings.Contains(detail, `"field":"position"`) {
		t.Errorf("the trail vouches for a position it could not confirm: %s", detail)
	}
}
