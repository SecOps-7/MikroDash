package db

import "testing"

// The three writes `alert.Store` needs: HasOpenAlert, InsertAlertEvent and
// ResolveAlertEvent.
//
// Separate from `alertwrite_test.go`, which drives the ROUTES' writes against a
// live corpus. These have no corpus because the live functions they mirror take
// `Date.now()` inline and return an autoincrement id — neither reproducible in a
// recorded case. What they DO have is the property that matters and that a
// corpus could not state: the round trip.

func alertEvalDB(t *testing.T) *DB {
	t.Helper()
	d, _, _ := alertWriteFixture(t)
	return d
}

// A ROUTER-WIDE alert has no subject, and `= NULL` matches nothing in SQL.
//
// This is the whole reason the queries use `subject IS ?`. With `=`, HasOpen
// would answer false forever, so every evaluation would file another row and
// resolve would close none of them — an alert that multiplies once per tick and
// never clears.
func TestARouterWideAlertRoundTrips(t *testing.T) {
	d := alertEvalDB(t)

	if d.HasOpenAlert("r-new", "cpu", "") {
		t.Fatal("an alert nobody filed reads as open")
	}
	id := d.InsertAlertEvent("r-new", "cpu", "", "94%", 1699996400000)
	if id == 0 {
		t.Fatal("the insert returned no id")
	}
	if !d.HasOpenAlert("r-new", "cpu", "") {
		t.Error("a filed router-wide alert does not read as open — `subject IS ?` is the " +
			"only comparison that matches NULL, and this is what a `= ?` would break")
	}

	ids := d.ResolveAlertEvent("r-new", "cpu", "", 1699996500000)
	if len(ids) != 1 || ids[0] != id {
		t.Errorf("resolve returned %v, want [%d]", ids, id)
	}
	if d.HasOpenAlert("r-new", "cpu", "") {
		t.Error("still open after being resolved")
	}
	// A SECOND resolve finds nothing and says so with an EMPTY SLICE, NOT NIL.
	//
	// `len(nil) == 0`, so a length check alone passes against either — a mutant
	// returning nil survived exactly that. The distinction matters because this
	// value is marshalled to the browser's bell: `[]` is "nothing resolved" and
	// `null` is a different shape for the client to handle.
	again := d.ResolveAlertEvent("r-new", "cpu", "", 1699996600000)
	if again == nil {
		t.Error("a second resolve returned nil rather than an empty slice")
	}
	if len(again) != 0 {
		t.Errorf("a second resolve returned %v", again)
	}
}

// A FAILED insert returns 0, and 0 is what the evaluator reads as "not filed".
//
// Induced by closing the database underneath it, which is the only way to make
// the Exec fail without a constraint to violate. A mutant returning a non-zero
// id on error survived until this existed — and that id would have gone into an
// emitted payload as though a row existed.
func TestAFailedInsertReturnsZero(t *testing.T) {
	d := alertEvalDB(t)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if id := d.InsertAlertEvent("r-new", "cpu", "", "94%", 1699996400000); id != 0 {
		t.Errorf("a failed insert returned id %d — the caller reads that as a filed row", id)
	}
	// And the reads answer rather than panicking on the same closed handle.
	if d.HasOpenAlert("r-new", "cpu", "") {
		t.Error("HasOpenAlert said true against a closed database")
	}
	if ids := d.ResolveAlertEvent("r-new", "cpu", "", 1); ids == nil || len(ids) != 0 {
		t.Errorf("ResolveAlertEvent returned %v against a closed database", ids)
	}
}

// A SUBJECT scopes the alert, and two subjects on one type are two alerts.
func TestTwoSubjectsAreTwoAlerts(t *testing.T) {
	d := alertEvalDB(t)
	a := d.InsertAlertEvent("r-new", "ifaceUpDown", "ether1", "down", 1699996400000)
	b := d.InsertAlertEvent("r-new", "ifaceUpDown", "ether2", "down", 1699996400000)
	if a == 0 || b == 0 || a == b {
		t.Fatalf("ids %d and %d", a, b)
	}
	if !d.HasOpenAlert("r-new", "ifaceUpDown", "ether1") ||
		!d.HasOpenAlert("r-new", "ifaceUpDown", "ether2") {
		t.Fatal("one of the two is not open")
	}
	// AND A ROUTER-WIDE ONE OF THE SAME TYPE IS A THIRD THING. `subject IS NULL`
	// must not match a row whose subject is 'ether1'.
	if d.HasOpenAlert("r-new", "ifaceUpDown", "") {
		t.Error("a NULL subject matched a row that has one")
	}

	ids := d.ResolveAlertEvent("r-new", "ifaceUpDown", "ether1", 1699996500000)
	if len(ids) != 1 || ids[0] != a {
		t.Errorf("resolve(ether1) = %v, want [%d]", ids, a)
	}
	if !d.HasOpenAlert("r-new", "ifaceUpDown", "ether2") {
		t.Error("resolving ether1 also resolved ether2")
	}
}

// EVERY matching open row is closed, not one.
//
// The live comment calls resolving all of them "the tell that duplicates were
// being created" — duplicates happen because an evaluator dropped and rebuilt
// has no memory of what it reported. A resolve that closed one would leave the
// rest open forever.
func TestResolveClosesEveryDuplicate(t *testing.T) {
	d := alertEvalDB(t)
	var want []int64
	for i := 0; i < 3; i++ {
		want = append(want, d.InsertAlertEvent("r-new", "routerUpdate", "", "7.24", 1699996400000))
	}
	ids := d.ResolveAlertEvent("r-new", "routerUpdate", "", 1699996500000)
	if len(ids) != 3 {
		t.Fatalf("resolved %d of 3 duplicates: %v", len(ids), ids)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("id %d = %d, want %d", i, ids[i], id)
		}
	}
	if d.HasOpenAlert("r-new", "routerUpdate", "") {
		t.Error("still open")
	}
}

// The ROUTER scopes it too: the same type and subject on another router is a
// different alert.
func TestAlertsAreScopedToTheirRouter(t *testing.T) {
	d := alertEvalDB(t)
	d.InsertAlertEvent("r-one", "ping", "8.8.8.8", "timeout", 1699996400000)
	if d.HasOpenAlert("r-two", "ping", "8.8.8.8") {
		t.Error("one router's alert reads as open on another")
	}
	if ids := d.ResolveAlertEvent("r-two", "ping", "8.8.8.8", 1699996500000); len(ids) != 0 {
		t.Errorf("resolving on the wrong router closed %v", ids)
	}
	if !d.HasOpenAlert("r-one", "ping", "8.8.8.8") {
		t.Error("the original was closed by a resolve aimed elsewhere")
	}
}

// A RESOLVED row does not block a new one.
//
// The dedup rule is "at most one UNRESOLVED row", so a condition that recurs
// after clearing must be able to fire again.
func TestAResolvedAlertCanFireAgain(t *testing.T) {
	d := alertEvalDB(t)
	first := d.InsertAlertEvent("r-new", "vpn", "wg0", "down", 1699996400000)
	d.ResolveAlertEvent("r-new", "vpn", "wg0", 1699996500000)
	if d.HasOpenAlert("r-new", "vpn", "wg0") {
		t.Fatal("still open after resolve")
	}
	second := d.InsertAlertEvent("r-new", "vpn", "wg0", "down", 1699996600000)
	if second == 0 || second == first {
		t.Fatalf("the second firing reused id %d", second)
	}
	if !d.HasOpenAlert("r-new", "vpn", "wg0") {
		t.Error("the second firing is not open")
	}
	// And resolving now closes ONLY the second — the first is already closed.
	ids := d.ResolveAlertEvent("r-new", "vpn", "wg0", 1699996700000)
	if len(ids) != 1 || ids[0] != second {
		t.Errorf("resolve = %v, want [%d]", ids, second)
	}
}

// A NIL database answers rather than panicking. The live functions all guard on
// `if (!_db)`, and the evaluator runs whether or not history is configured.
func TestTheWritesSurviveNoDatabase(t *testing.T) {
	var d *DB
	if d.HasOpenAlert("r", "cpu", "") {
		t.Error("HasOpenAlert said true with no database")
	}
	if id := d.InsertAlertEvent("r", "cpu", "", "94%", 1); id != 0 {
		t.Errorf("InsertAlertEvent returned %d", id)
	}
	if ids := d.ResolveAlertEvent("r", "cpu", "", 1); ids == nil || len(ids) != 0 {
		t.Errorf("ResolveAlertEvent returned %v, want an empty non-nil slice", ids)
	}
}

// `detail` is nullable and the empty string must become NULL, not "".
func TestAnEmptyDetailIsStoredAsNull(t *testing.T) {
	d := alertEvalDB(t)
	id := d.InsertAlertEvent("r-new", "bgp", "peer1", "", 1699996400000)
	row, err := d.alertRowByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if row == nil {
		t.Fatal("the row is missing")
	}
	if row.Detail != nil {
		t.Errorf("detail stored as %q, want NULL", *row.Detail)
	}
	if row.Subject == nil || *row.Subject != "peer1" {
		t.Errorf("subject = %v, want peer1", row.Subject)
	}
}
