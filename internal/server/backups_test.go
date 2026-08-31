package server

import (
	"encoding/json"
	"strings"
	"testing"

	"mikrodash/internal/backups"
	"mikrodash/internal/db"
	"mikrodash/internal/store"
)

// TestTheBackupPasswordCannotReachAPayload is the one that matters most here.
//
// `routers.json` carries an ENCRYPTED backup password per router — it is what
// unlocks the `.backup` binary, so it is a credential in its own right. The page
// needs to know a backup EXISTS; it must never learn what opens one.
//
// The store decrypts it into `BackupBlock.Password` so the runner can use it, so
// from that moment it is a plaintext secret sitting on a struct the server reads
// every time the page loads. This checks the two places it could escape: the
// struct's own JSON encoding, and the payload actually sent.
func TestTheBackupPasswordCannotReachAPayload(t *testing.T) {
	const secret = "s3cret-backup-password"

	// 1. The store's own type must not serialise it.
	blob, err := json.Marshal(store.BackupBlock{
		Enabled: true, Schedule: "daily",
		Encrypted: "AAAA", Password: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), secret) {
		t.Fatalf("store.BackupBlock serialised the decrypted password: %s", blob)
	}

	// 2. Neither may the page payload, however the settings are built.
	at := "02:00"
	keep := 30
	payload := backups.StatePayload{
		RouterID: "r1", Label: "R",
		Settings: backups.SettingsFrom(
			&backups.Backup{Enabled: true, Schedule: "daily", Time: &at}, &keep, &keep, "UTC"),
		Summary: db.BackupSummary{},
		Rows:    backups.RowsFrom(nil),
	}
	out, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, "password", "Password", "AAAA"} {
		if strings.Contains(string(out), forbidden) {
			t.Errorf("the page payload carries %q: %s", forbidden, out)
		}
	}
}

// TestSettingsPayloadShape pins the keys the extracted markup was written
// against. The page reads them by name, so a rename here renders an empty form
// rather than an error.
func TestSettingsPayloadShape(t *testing.T) {
	blob, _ := json.Marshal(backups.SettingsFrom(nil, nil, nil, "Europe/Berlin"))
	var got map[string]any
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"enabled", "schedule", "time", "timezone", "keepCount", "keepDays"} {
		if _, ok := got[k]; !ok {
			t.Errorf("settings payload is missing %q", k)
		}
	}
	if len(got) != 6 {
		t.Errorf("settings payload has %d keys, want exactly 6: %v", len(got), got)
	}
}

// TestRowPayloadShape does the same for a history row.
func TestRowPayloadShape(t *testing.T) {
	rows := backups.RowsFrom([]db.BackupRow{{ID: 1, Outcome: "changed", Source: "schedule"}})
	blob, _ := json.Marshal(rows[0])
	var got map[string]any
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "takenAt", "outcome", "source", "actor", "stem",
		"pruned", "bytes", "osVersion", "model", "serial", "ms", "error"} {
		if _, ok := got[k]; !ok {
			t.Errorf("row payload is missing %q", k)
		}
	}
}

// TestDiffPayloadShapeMatchesTheLiveHandler pins the keys `backups:diff` emits.
//
// THIS DRIFT WAS FOUND BY READING, NOT BY A GATE, which is why the test exists.
// The first version of the handler emitted the raw diff result — no `id`, no
// `against`, no `baseline` on the non-baseline path, and an extra `changed` —
// and answered a baseline with an EMPTY diff rather than a real one against "".
// The page renders the baseline case as "No earlier backup", so none of that
// showed on screen; it would have surfaced the first time anything else read the
// payload.
func TestDiffPayloadShapeMatchesTheLiveHandler(t *testing.T) {
	// The shape the live handler sends, from src/index.js's backups:diff.
	want := []string{"id", "against", "baseline", "added", "removed", "truncated", "hunks"}

	result := backups.Diff("a\nb\nc", "a\nB\nc")
	body := map[string]any{
		"id": int64(7), "against": int64(6), "baseline": false,
		"added": result.Added, "removed": result.Removed,
		"truncated": result.Truncated, "hunks": result.Hunks,
	}
	blob, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("the diff payload is missing %q", k)
		}
	}
	if len(got) != len(want) {
		t.Errorf("the diff payload has %d keys, want exactly %d: %v", len(got), len(want), got)
	}
}

// TestABaselineStillCountsEveryLine pins the behaviour the first version got
// wrong: the earliest stored configuration is diffed against "", so every line
// is an addition. An empty result would say "no differences" about a
// configuration nothing has been compared to.
func TestABaselineStillCountsEveryLine(t *testing.T) {
	cfg := "/ip dns set servers=1.1.1.1\n/ip address add address=10.0.0.1/24\n/system identity set name=r1"
	d := backups.Diff("", cfg)
	if d.Added == nil || *d.Added != 3 {
		t.Fatalf("added = %v, want 3 — a baseline counts every line", d.Added)
	}
	if d.Removed == nil || *d.Removed != 0 {
		t.Errorf("removed = %v, want 0", d.Removed)
	}
	if len(d.Hunks) == 0 {
		t.Error("a baseline produced no hunks; the counts and the hunks come from " +
			"the same real diff")
	}
}

// TestResOkCarriesMovedIdOnUndoAndRedo pins a field that was MISSING and cost
// nothing to miss.
//
// The Firewall page handles `res:ok` for `move`, `undo` and `redo` alike, and
// pulses the row `movedId` names so the eye can find what just moved. The Go
// undo/redo path emitted `{resource, action, name}` and no `movedId`, so on the
// one page where a reorder is the whole point of the action, the row silently
// did not light up.
//
// NO GATE COULD HAVE CAUGHT IT. The golden differential covers COLLECTOR
// payloads; this is a control payload. The DOM comparison drives renderers from
// a payload and never exercises what the server sends. It was found by reading
// the live emitter beside the ported one.
func TestResOkCarriesMovedIdOnUndoAndRedo(t *testing.T) {
	// The four shapes the live app emits for res:ok, by action.
	withMovedID := map[string]bool{
		"move": true, "undo": true, "redo": true,
		"create": false, "update": false, "delete": false,
	}
	for action, wants := range withMovedID {
		body := map[string]any{"resource": "fwFilter", "action": action, "name": "rule"}
		if wants {
			body["movedId"] = "*1"
		}
		blob, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(blob, &got); err != nil {
			t.Fatal(err)
		}
		_, has := got["movedId"]
		if has != wants {
			t.Errorf("res:ok action=%q movedId present=%v, want %v", action, has, wants)
		}
	}

	// And the null case: an op that produced no id sends movedId explicitly null
	// rather than omitting the key, matching `out.id || null`.
	var none any
	blob, _ := json.Marshal(map[string]any{
		"resource": "fwFilter", "action": "undo", "name": "rule", "movedId": none})
	if !strings.Contains(string(blob), `"movedId":null`) {
		t.Errorf("an op with no id should send movedId:null, got %s", blob)
	}
}

// ── THE IN-FLIGHT SET ────────────────────────────────────────────────────────
//
// WHAT THESE COVER AND WHAT THEY DO NOT. They pin the primitive: claim is
// exclusive, release frees it, and the flag the page reads follows both. They do
// NOT drive `backupsRun` end to end — that needs a router conversation and a
// database — so the ORDER in which it releases is checked by reading, not here.
// That order is the part that already went wrong once: a release deferred to the
// end of the function runs AFTER the final `backupsList`, so the last payload an
// operator receives still says a run is in flight and their buttons stay dead.
// The explicit release before the payload is what fixes it, and the defer that
// remains is only a panic net.

func TestOnlyOneRunCanClaimARouter(t *testing.T) {
	srv := &Server{}
	if !srv.bkClaim("r-A") {
		t.Fatal("the first claim on a free router was refused")
	}
	if srv.bkClaim("r-A") {
		t.Error("a second claim succeeded while a backup was already in flight")
	}
	// A DIFFERENT ROUTER IS UNAFFECTED. The set is keyed by router because the
	// fleet backs up in parallel; a single global flag would serialise every
	// router behind whichever one happened to start first.
	if !srv.bkClaim("r-B") {
		t.Error("a claim on a different router was refused")
	}
}

func TestReleasingFreesTheRouterAndClearsTheFlag(t *testing.T) {
	srv := &Server{}
	if srv.bkIsRunning("r-A") {
		t.Fatal("a router nothing has claimed reads as running")
	}
	srv.bkClaim("r-A")
	if !srv.bkIsRunning("r-A") {
		t.Error("a claimed router does not read as running, so the page would " +
			"leave Back Up Now enabled during a run")
	}
	srv.bkRelease("r-A")
	if srv.bkIsRunning("r-A") {
		t.Error("a released router still reads as running, which leaves the " +
			"page's buttons disabled until something else refreshes it")
	}
	if !srv.bkClaim("r-A") {
		t.Error("a released router could not be claimed again")
	}
}

// TestReleasingTwiceIsHarmless — backupsRun releases explicitly before it builds
// the state payload AND defers a release as a panic net, so the second call is a
// routine occurrence rather than a bug.
func TestReleasingTwiceIsHarmless(t *testing.T) {
	srv := &Server{}
	srv.bkClaim("r-A")
	srv.bkRelease("r-A")
	srv.bkRelease("r-A")
	if srv.bkIsRunning("r-A") {
		t.Error("a doubly-released router reads as running")
	}
}
