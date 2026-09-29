package db

import (
	"database/sql"
	"errors"
	"testing"
)

func seedProfile(t *testing.T, d *DB, id, name, user string) CredProfile {
	t.Helper()
	p := CredProfile{ID: id, Name: name, Username: user, PermKind: "builtin",
		Builtin: "read", PolicyJSON: "[]", Secret: "sealed-v1", CreatedBy: "u-1"}
	if err := d.UpsertCredProfile(p); err != nil {
		t.Fatalf("seeding %s: %v", id, err)
	}
	got, err := d.CredProfileByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestTheRevisionMovesOnlyForWhatARouterCanSee.
//
// ── THE REVISION IS THE WHOLE OF "WHICH DEVICES OWE AN UPDATE" ──────────────
//
// So what bumps it is a correctness question, not bookkeeping. Bump too
// eagerly and fixing a typo in a description re-writes the account on every
// linked router — a fleet-wide /user write nobody asked for. Bump too rarely
// and a password change reaches nothing, which is the failure that looks like
// the feature simply not working.
//
// Both directions are asserted, because only one of them is obvious.
func TestTheRevisionMovesOnlyForWhatARouterCanSee(t *testing.T) {
	d := openTest(t, t.TempDir())
	p := seedProfile(t, d, "cp1", "NOC", "noc")
	if p.Revision != 1 {
		t.Fatalf("a new profile starts at revision %d, want 1", p.Revision)
	}

	// INVISIBLE TO A ROUTER: no bump.
	p.Description = "the night shift's read-only account"
	p.Name = "NOC Read-only"
	if err := d.UpsertCredProfile(p); err != nil {
		t.Fatal(err)
	}
	after, _ := d.CredProfileByID("cp1")
	if after.Revision != 1 {
		t.Errorf("a description and name edit bumped the revision to %d; every linked "+
			"router would be re-written because somebody fixed a sentence", after.Revision)
	}
	if after.Description == "" || after.Name != "NOC Read-only" {
		t.Errorf("the edit did not save: %+v", after)
	}

	// VISIBLE TO A ROUTER: each of these bumps, one at a time.
	for _, tc := range []struct {
		what string
		edit func(*CredProfile)
	}{
		{"the password", func(p *CredProfile) { p.Secret = "sealed-v2" }},
		{"the username", func(p *CredProfile) { p.Username = "noc2" }},
		{"the built-in group", func(p *CredProfile) { p.Builtin = "write" }},
		{"the permission kind", func(p *CredProfile) {
			p.PermKind, p.GroupName, p.PolicyJSON = "custom", "noc-grp", `["read"]`
		}},
		{"the policy set", func(p *CredProfile) { p.PolicyJSON = `["read","api"]` }},
	} {
		before, _ := d.CredProfileByID("cp1")
		next := before
		tc.edit(&next)
		if err := d.UpsertCredProfile(next); err != nil {
			t.Fatal(err)
		}
		got, _ := d.CredProfileByID("cp1")
		if got.Revision != before.Revision+1 {
			t.Errorf("changing %s moved the revision %d -> %d; the linked routers would "+
				"never be told", tc.what, before.Revision, got.Revision)
		}
	}

	// AND SAVING THE SAME THING TWICE DOES NOT BUMP. Without this the "no bump"
	// case above could pass for a rule that only skips the very first save.
	same, _ := d.CredProfileByID("cp1")
	if err := d.UpsertCredProfile(same); err != nil {
		t.Fatal(err)
	}
	again, _ := d.CredProfileByID("cp1")
	if again.Revision != same.Revision {
		t.Errorf("re-saving an unchanged profile bumped %d -> %d", same.Revision, again.Revision)
	}
}

// TestALinkIsNeverResetByLinkingAgain: pressing Link on a device that already
// has the profile must not throw away the state saying it refused, nor restart
// a backoff that is deliberately long.
func TestALinkIsNeverResetByLinkingAgain(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedProfile(t, d, "cp1", "NOC", "noc")

	if err := d.LinkCredProfile("cp1", "r-1", "u-1"); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkCredLink("cp1", "r-1", "refused", "protected-group-value",
		"the guard said no", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := d.LinkCredProfile("cp1", "r-1", "u-2"); err != nil {
		t.Fatal(err)
	}

	links, err := d.CredLinks("cp1")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("linking twice made %d rows", len(links))
	}
	if links[0].State != "refused" || links[0].Code != "protected-group-value" {
		t.Errorf("re-linking reset the row to %s/%s, discarding why it refused",
			links[0].State, links[0].Code)
	}
}

// TestTheReconcilerIsOfferedWorkAndNotRefusals.
//
// ── BOTH DIRECTIONS, AND THE SECOND IS THE EXPENSIVE ONE ────────────────────
//
// A refusal that keeps being retried writes an audit row every sweep, for ever,
// until the trail is something people have learned to scroll past. A device
// that owes an update and is never offered simply never gets it, which looks
// like the feature not working.
func TestTheReconcilerIsOfferedWorkAndNotRefusals(t *testing.T) {
	d := openTest(t, t.TempDir())
	p := seedProfile(t, d, "cp1", "NOC", "noc")

	for _, rid := range []string{"r-pending", "r-applied", "r-refused", "r-conflict",
		"r-stale", "r-backoff"} {
		if err := d.LinkCredProfile("cp1", rid, "u-1"); err != nil {
			t.Fatal(err)
		}
	}
	mustMark := func(rid, state, code string, rev, next int64) {
		t.Helper()
		if err := d.MarkCredLink("cp1", rid, state, code, "", rev, next); err != nil {
			t.Fatal(err)
		}
	}
	now := int64(1_000_000)
	mustMark("r-refused", "refused", "protected-group-value", 0, 0)
	mustMark("r-conflict", "conflict", "conflict", 0, 0)
	// Applied at the CURRENT revision, before the bump below makes it stale.
	mustMark("r-stale", "applied", "", p.Revision, 0)
	// Failed, with a backoff that has not expired.
	mustMark("r-backoff", "failed", "router-denied", 0, now+60_000)

	// Bump the profile: r-stale now owes an update.
	p.Secret = "sealed-v2"
	if err := d.UpsertCredProfile(p); err != nil {
		t.Fatal(err)
	}
	fresh, _ := d.CredProfileByID("cp1")
	// r-applied is brought up to the NEW revision, so it owes nothing.
	mustMark("r-applied", "applied", "", fresh.Revision, 0)

	due, err := d.CredLinksDue(now)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, l := range due {
		got[l.RouterID] = true
	}
	for _, want := range []string{"r-pending", "r-stale"} {
		if !got[want] {
			t.Errorf("%s owes work and was not offered; it would never converge", want)
		}
	}
	for _, unwanted := range []string{"r-refused", "r-conflict"} {
		if got[unwanted] {
			t.Errorf("%s is a terminal refusal and was offered again; every sweep would "+
				"write another audit row about the same decision", unwanted)
		}
	}
	if got["r-applied"] {
		t.Error("a router that is up to date was offered work")
	}
	if got["r-backoff"] {
		t.Error("a failure was retried before its backoff expired")
	}

	// AND THE BACKOFF DOES EXPIRE. Without this, "not offered" would hold for a
	// rule that never offers a failure again at all.
	later, err := d.CredLinksDue(now + 120_000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range later {
		if l.RouterID == "r-backoff" {
			found = true
		}
	}
	if !found {
		t.Error("a failure was never retried even after its backoff expired")
	}
}

// TestAProfileCannotBeDeletedWhileARouterStillHasIt, and what Forget is for.
//
// The RESTRICT is asserted at the schema level in credprof_schema_test.go; this
// is the pair of methods on top of it, because the difference between them is
// the whole safety story. Delete refuses. Forget abandons, on purpose, with a
// name that says so.
func TestAProfileCannotBeDeletedWhileARouterStillHasIt(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedProfile(t, d, "cp1", "NOC", "noc")
	if err := d.LinkCredProfile("cp1", "r-1", "u-1"); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteCredProfile("cp1"); err == nil {
		t.Error("a profile with a live link was deleted; the account it created would " +
			"have stayed on the router with nothing left that knows about it")
	}
	if _, err := d.CredProfileByID("cp1"); err != nil {
		t.Errorf("the refused delete still removed the profile: %v", err)
	}

	// FORGET IS THE DELIBERATE ESCAPE, and it takes the links with it.
	if err := d.ForgetCredProfile("cp1"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, err := d.CredProfileByID("cp1"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Forget left the profile: %v", err)
	}
	links, err := d.CredLinks("cp1")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Errorf("Forget left %d link(s) pointing at a profile that is gone", len(links))
	}
}

// TestEnqueueLeavesTerminalRefusalsAlone: a revision bump means every device
// has to be told, EXCEPT the ones whose answer will not change. Re-queuing a
// refusal turns one bad profile edit into an audit row per router per sweep.
func TestEnqueueLeavesTerminalRefusalsAlone(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedProfile(t, d, "cp1", "NOC", "noc")
	for _, rid := range []string{"r-ok", "r-refused"} {
		if err := d.LinkCredProfile("cp1", rid, "u-1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.MarkCredLink("cp1", "r-ok", "applied", "", "", 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkCredLink("cp1", "r-refused", "refused", "protected-group-value",
		"", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := d.EnqueueCredLinks("cp1"); err != nil {
		t.Fatal(err)
	}

	links, _ := d.CredLinks("cp1")
	for _, l := range links {
		switch l.RouterID {
		case "r-ok":
			if l.State != "pending" {
				t.Errorf("an applied link was not re-queued by a revision bump: %s", l.State)
			}
		case "r-refused":
			if l.State != "refused" {
				t.Errorf("a refusal was re-queued as %s; the guard's answer does not "+
					"change by being asked again", l.State)
			}
		}
	}
}

// TestAnAppliedLinkClearsItsAttemptsAndAFailureCountsUp: the backoff's input.
// Without the reset, a router that failed twice and then succeeded would keep
// its long delay for ever.
func TestAnAppliedLinkClearsItsAttemptsAndAFailureCountsUp(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedProfile(t, d, "cp1", "NOC", "noc")
	if err := d.LinkCredProfile("cp1", "r-1", "u-1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := d.MarkCredLink("cp1", "r-1", "unreachable", "unreachable",
			"down", 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	links, _ := d.CredLinks("cp1")
	if links[0].Attempts != 3 {
		t.Errorf("three failures recorded %d attempts", links[0].Attempts)
	}
	if links[0].AppliedRevision != 0 {
		t.Errorf("a failure recorded applied_revision %d; the device would look up to "+
			"date while holding nothing", links[0].AppliedRevision)
	}

	if err := d.MarkCredLink("cp1", "r-1", "applied", "", "", 1, 0); err != nil {
		t.Fatal(err)
	}
	links, _ = d.CredLinks("cp1")
	if links[0].Attempts != 0 {
		t.Errorf("success left %d attempts on the row, so the next failure would start "+
			"from a long backoff", links[0].Attempts)
	}
	if links[0].AppliedRevision != 1 || links[0].AppliedAt == 0 {
		t.Errorf("success did not record the revision and time: %+v", links[0])
	}
}

// TestRemovingARouterTakesItsLinksAndNotItsProfile: the purge is scoped by
// router. The profile, and its links to every OTHER router, survive.
func TestRemovingARouterTakesItsLinksAndNotItsProfile(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedProfile(t, d, "cp1", "NOC", "noc")
	for _, rid := range []string{"r-1", "r-2"} {
		if err := d.LinkCredProfile("cp1", rid, "u-1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.DeleteRouterData("r-1"); err != nil {
		t.Fatal(err)
	}
	links, err := d.CredLinks("cp1")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].RouterID != "r-2" {
		t.Errorf("removing r-1 left %+v; want only r-2", links)
	}
	if _, err := d.CredProfileByID("cp1"); err != nil {
		t.Errorf("removing a router removed the profile itself: %v", err)
	}
}
