package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIdentityColumns: each shared database column gets the right KIND of
// identity written into it.
//
// ── WHY THIS CANNOT BE A ROUND-TRIP TEST ────────────────────────────────────
//
// There is no blanket rule. `grants.principal_id`, `audit_events.actor_id` and
// `user_layouts.user_id` hold a user ID; `alert_events.acknowledged_by` and
// `audit_events.actor_name` hold a USERNAME. Which is which was decided by the
// application that created the schema, and both are strings.
//
// Two bugs on 2026-08-27 were a writer reaching for the other one, and NEITHER
// was visible to any test: a round trip through one implementation agrees with
// itself whatever it wrote. Keying the nav layout on the username instead of the
// id gave one account two rows, one per half. Both were found by reading the real
// table.
//
// So this asserts the CALL SITE, not the round trip: the expression that supplies
// each column, and how many places supply it.
//
// ── WHY THE COUNT IS PART OF THE LEDGER ─────────────────────────────────────
//
// `sites` is what makes a one-place change visible. Acknowledge and
// un-acknowledge both write `alert_events.acknowledged_by`; recording only "the
// expression appears" would let one of them change to the wrong identity while
// the other kept the test green.
type identityColumn struct {
	column string
	// kind is what the column holds: an id, a username, or a value the caller
	// supplies. Documentation for the reader; the assertion is on `site`.
	kind  string
	file  string
	site  string
	sites int
	why   string
}

var identityColumns = []identityColumn{
	{
		column: "user_layouts.user_id", kind: "id",
		file: "internal/server/navprefs_api.go", site: "s.userIDFor(sess.Username)", sites: 1,
		why: "the nav preference and both saved layouts. Keying on the username gave one account " +
			"two rows, one per half — found 2026-08-27 by reading the table.",
	},
	{
		column: "audit_events.actor_name", kind: "username",
		file: "internal/server/auth_login.go", site: "audit.ForLogin(username, clientIPOf(r))", sites: 1,
		why: "a login is a PRE-authentication event: it records the CLAIMED username and a null " +
			"id, because a failed login may name a user that does not exist and that is worth seeing.",
	},
	{
		column: "audit_events.actor_id", kind: "id",
		file: "internal/server/audit.go", site: "audit.ForUser(s.userIDFor(name), name, clientIPOf(r))", sites: 1,
		why: "every non-login event records the session's user id beside the username, and the " +
			"Audit page filters on both.",
	},
	{
		column: "alert_events.acknowledged_by", kind: "username",
		file: "internal/server/alerts_api.go", site: "who = sess.Username", sites: 2,
		why: "acknowledge AND un-acknowledge. Two sites is the point: one could change to the " +
			"wrong identity while the other kept this green.",
	},
	{
		column: "grants.principal_id", kind: "caller",
		file: "internal/db/grantwrite.go", site: "s.PrincipalID", sites: 2,
		why: "supplied by the caller rather than resolved here; the writer must not substitute.",
	},
	{
		column: "grants.created_by", kind: "caller",
		file: "internal/db/grantwrite.go", site: "s.CreatedBy", sites: 2,
		why: "as above, and distinct from principal_id — swapping them is silent.",
	},
}

func TestIdentityColumns(t *testing.T) {
	root := repoRoot(t)

	for _, c := range identityColumns {
		full := filepath.Join(root, c.file)
		b, err := os.ReadFile(full)
		if err != nil {
			t.Errorf("%s: %s does not exist — the entry names a file that has moved or been "+
				"deleted", c.column, c.file)
			continue
		}
		// Comments are stripped so this file's own prose, and the call site
		// quoted in a comment beside the code, cannot count as a writer.
		var code strings.Builder
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			code.WriteString(line)
			code.WriteString("\n")
		}
		found := strings.Count(code.String(), c.site)
		if found != c.sites {
			t.Errorf("%s (%s): %s has %d site(s) passing %q, recorded as %d.\n"+
				"    Either a writer was ADDED — check it passes the same kind of identity — or "+
				"one changed and now writes the wrong one.\n    %s",
				c.column, c.kind, c.file, found, c.site, c.sites, c.why)
		}
	}

	// The ledger is hand-written, so an empty or truncated one would pass in
	// silence. Six columns is what this port writes today.
	if len(identityColumns) < 6 {
		t.Fatalf("the ledger holds %d columns; it had 6 — entries were removed rather than "+
			"the writers being fixed", len(identityColumns))
	}
	t.Logf("%d shared identity columns checked, each at its recorded number of call sites",
		len(identityColumns))
}
