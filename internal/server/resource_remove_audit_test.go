package server

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The delete audit row must name what was deleted, from the SERVER'S row.
//
// ── FOUND BY DELETING SOMETHING ON A REAL ROUTER ──────────────────────────
//
// `res:remove` took its audit `TargetName` from `req.ExpectedIdentity`, which is
// client-supplied and OPTIONAL — `find` accepts an empty one. A remove that
// omitted it wrote `dnsStatic.delete` with `target_name` EMPTY: a record that
// something was deleted and not which thing, on the one row that exists to
// answer that question.
//
// No unit test could see it. The suite drives `res:remove` with a request it
// constructs, and every such request carries an identity because the fixture
// author had one to hand. The real client usually sends it too, which is why the
// gap survived. It was measured on hAP AC2 on 2026-08-29.
//
// ── WHY A SOURCE PIN AND NOT A BEHAVIOURAL ONE ─────────────────────────────
//
// The property is WHERE the value comes from. A behavioural test would have to
// omit `expectedIdentity` and assert a non-empty name — which passes just as
// well if someone re-derives the name from the request by another route. This
// asserts the row is the source, which is the actual rule.
func TestTheDeleteAuditNamesTheRowNotTheRequest(t *testing.T) {
	b, err := os.ReadFile("resource.go")
	if err != nil {
		t.Fatalf("reading resource.go: %v", err)
	}
	src := string(b)

	i := strings.Index(src, `Action: res.Key + ".delete"`)
	if i < 0 {
		t.Fatal("no delete audit call found — this test is measuring nothing")
	}
	// The remove handler: from its `name :=` up to the last delete audit call.
	start := strings.LastIndex(src[:i], "name := req.ExpectedIdentity")
	if start < 0 {
		t.Fatal("the remove handler's `name :=` anchor has moved")
	}
	last := strings.LastIndex(src, `Action: res.Key + ".delete"`)
	body := src[start:last]

	// ── THE VALUE MUST REACH `name`, NOT MERELY BE COMPUTED ───────────────
	//
	// Checking only that `res.IdentityOf(row)` APPEARS let a mutant survive that
	// called it and threw the result away (`_ = n`). The call is not the
	// property; the assignment is.
	call := regexp.MustCompile(`(\w+)\s*:?=\s*res\.IdentityOf\(row\)`).FindStringSubmatch(body)
	if call == nil {
		t.Fatal("the remove path never calls res.IdentityOf(row): its audit rows take " +
			"their name from the client's optional expectedIdentity, so a request that " +
			"omits it records a delete without saying what was deleted")
	}
	if lhs := call[1]; lhs != "name" {
		// An intermediate is fine, but it has to be assigned onward.
		if !regexp.MustCompile(`\bname\s*=\s*` + lhs + `\b`).MatchString(body) {
			t.Errorf("res.IdentityOf(row) is assigned to %q and %q never reaches `name`, "+
				"so the audit rows still carry the request's identity", lhs, lhs)
		}
	}
	// And the assignment must come BEFORE the audit calls, or it names nothing.
	assign := strings.Index(body, "res.IdentityOf(row)")
	firstAudit := strings.Index(body, `Action: res.Key + ".delete"`)
	if assign >= 0 && firstAudit >= 0 && assign > firstAudit {
		t.Error("res.IdentityOf(row) is computed AFTER the first delete audit row, " +
			"so that row still carries the request's name")
	}
}

// TestAnEditsRefusalNamesTheRow is the same rule for an edit, found the same way:
// the assistant's partial change_row sends no identity, and `ipService` never
// sends its Display name, so a guard refusal was audited with target_name EMPTY
// (measured on the CHR, 2026-09-18). In prepareWrite, once the row is found and
// while the name is still empty, it must be taken from that row, before the
// read-only denial and the guard refusal that audit it.
func TestAnEditsRefusalNamesTheRow(t *testing.T) {
	b, err := os.ReadFile("resource.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "func (cn *conn) prepareWrite(")
	if start < 0 {
		t.Fatal("prepareWrite has moved; this test is measuring nothing")
	}
	body := src[start:]
	body = body[:strings.Index(body, "\n}\n")]
	assign := regexp.MustCompile(`\bname\s*=\s*res\.IdentityOf\(before\)`).FindStringIndex(body)
	if assign == nil {
		t.Fatal("prepareWrite never assigns res.IdentityOf(before) to name: an edit sent without its " +
			"identity is audited with an empty target_name")
	}
	for _, anchor := range []string{`Note: "read-only-row"`, "cn.guardRefusal("} {
		at := strings.Index(body, anchor)
		if at < 0 {
			t.Fatalf("%s is no longer in prepareWrite; this test has lost its anchor", anchor)
		}
		if assign[0] > at {
			t.Errorf("the name is taken from the row AFTER %s, so that audit row still has none", anchor)
		}
	}
}
