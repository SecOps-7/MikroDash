package store

// PublicRouters, against what the LIVE getPublic disclosed.
//
// `tools/routers-public-cases.js` runs the live module over a throwaway
// routers.json and records what came back.
//
// ── THE CORPUS IS REPLAYED IN TWO HALVES, AND THE SPLIT IS HONEST ───────────
//
// Cases with no secret are replayed EXACTLY: the recorded input goes in and the
// recorded output must come out. Cases that exercise the MASK cannot be, because
// the generator redacts its inputs — the recorded input says `<invented>` where
// a real envelope was, and replaying that would test the cannot-decrypt path
// while claiming to test the mask.
//
// So those arms are constructed here with this package's own `Encrypt`, and what
// the corpus supplies for them is the EXPECTED DISCLOSURE — the mask, and
// `hasPassword: true` — which is the part that has to match.
//
// EVERY PASSWORD IN THIS FILE IS INVENTED HERE.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type routerPublicCase struct {
	Why        string           `json:"why"`
	Input      []map[string]any `json:"input"`
	SeededWith string           `json:"seededWith"`
	Public     []map[string]any `json:"public"`
}

func loadRouterPublicCases(t *testing.T) []routerPublicCase {
	t.Helper()
	b, err := os.ReadFile("../../testdata/routers-public-cases.json")
	if err != nil {
		t.Fatalf("corpus missing: %v — run tools/routers-public-cases.js", err)
	}
	var doc struct {
		Cases []routerPublicCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("corpus is empty")
	}
	return doc.Cases
}

// routerStore is a /data with a secret, so Encrypt and Decrypt both work.
func routerStore(t *testing.T, records []map[string]any) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if records != nil {
		b, err := json.Marshal(records)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "routers.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestPublicRoutersMatchesLive(t *testing.T) {
	replayed := 0
	for _, c := range loadRouterPublicCases(t) {
		if c.SeededWith != "" {
			continue // constructed below; see the file header
		}
		c := c
		t.Run(c.Why, func(t *testing.T) {
			s, _ := routerStore(t, c.Input)
			got, err := s.PublicRouters()
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.Public) {
				t.Fatalf("%d records, live disclosed %d", len(got), len(c.Public))
			}
			for i := range got {
				// Both sides marshalled by Go, so key order matches and the
				// comparison is about CONTENT.
				gb, _ := json.Marshal(got[i])
				wb, _ := json.Marshal(c.Public[i])
				if string(gb) != string(wb) {
					t.Errorf("record %d differs\n  got:  %s\n  live: %s", i, gb, wb)
				}
			}
		})
		replayed++
	}
	// The corpus must still carry replayable cases. If every one grew a seed,
	// this suite would pass having compared nothing.
	if replayed < 4 {
		t.Errorf("only %d cases were replayed against the live disclosure", replayed)
	}
}

// TestTheMaskIsOnTheDecryptedValue.
//
// The live code masks a value that has already been through decryption, so:
//
//	a password this install can read  -> the mask
//	a password it CANNOT read         -> ""
//	no password at all                -> ""
//
// The middle one is the arm a port gets wrong by masking whenever the ciphertext
// field is non-empty — which would tell an operator a credential is present on a
// router whose password this install can no longer read, after a rotated
// `.secret`. The corpus records both.
func TestTheMaskIsOnTheDecryptedValue(t *testing.T) {
	s, _ := routerStore(t, nil)
	enc, err := s.Encrypt("an-invented-router-password")
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		why, stored, want string
	}{
		{"a readable password", enc, Mask},
		{"an unreadable one", "not-a-valid-envelope", ""},
		{"an empty one", "", ""},
	} {
		got := s.PublicRouter(map[string]any{"id": "r1", "password": c.stored})
		if got["password"] != c.want {
			t.Errorf("%s: disclosed %q, want %q", c.why, got["password"], c.want)
		}
		// AND THE STORED VALUE NEVER SURVIVES, whichever arm was taken.
		if c.stored != "" && got["password"] == c.stored {
			t.Errorf("%s: the stored value reached the payload", c.why)
		}
	}
}

// TestTheBackupPasswordIsRemovedNotMasked.
//
// The live comment: "Nothing in the UI edits it, so there is no field for a mask
// to stand in for, and a masked secret invites a round trip that could write the
// mask back." So the key is DELETED and a boolean takes its place.
func TestTheBackupPasswordIsRemovedNotMasked(t *testing.T) {
	s, _ := routerStore(t, nil)
	enc, err := s.Encrypt("an-invented-backup-password")
	if err != nil {
		t.Fatal(err)
	}

	with := s.PublicRouter(map[string]any{
		"id": "r1",
		"backup": map[string]any{
			"enabled": true, "schedule": "daily", "keepCount": 7.0, "password": enc,
		},
	})
	bk, ok := with["backup"].(map[string]any)
	if !ok {
		t.Fatalf("the backup block is %T", with["backup"])
	}
	if _, leaked := bk["password"]; leaked {
		t.Error("the backup password survived; it must be REMOVED, not masked")
	}
	if bk["hasPassword"] != true {
		t.Errorf("hasPassword = %v, want true", bk["hasPassword"])
	}
	// Everything else in the block is untouched.
	if bk["schedule"] != "daily" || bk["keepCount"] != 7.0 || bk["enabled"] != true {
		t.Errorf("the fold changed the rest of the block: %+v", bk)
	}

	without := s.PublicRouter(map[string]any{
		"id": "r2", "backup": map[string]any{"enabled": true},
	})
	if without["backup"].(map[string]any)["hasPassword"] != false {
		t.Error("hasPassword is not false for a block with no password")
	}

	// NO BLOCK IS INVENTED. A port that always emitted `backup` would tell the
	// page a backup is configured on a router that has none.
	none := s.PublicRouter(map[string]any{"id": "r3"})
	if _, ok := none["backup"]; ok {
		t.Errorf("a backup block was invented: %+v", none["backup"])
	}
}

// TestEveryOtherFieldSurvives — the point of the spread, and the defect this
// file was written for.
//
// The live function is `{ ...r }` plus two rules. A port that listed fields
// would pass every test above and still drop the twelve keys that started this.
func TestEveryOtherFieldSurvives(t *testing.T) {
	s, _ := routerStore(t, nil)
	in := map[string]any{
		"id": "r1", "label": "Full", "host": "198.51.100.6", "port": 8729.0,
		"tls": true, "tlsInsecure": false, "username": "ops", "defaultIf": "ether1",
		"pingTarget": "1.1.1.1", "disabled": false, "addedAt": 1700000000000.0,
		"alertsEnabled": true, "connDownThresholdSec": 60.0, "model": "hAP ax3",
		"osVersion": "7.24", "serial": "INVENTED123", "siteId": "site-a",
		"siteIds": []any{"site-a"}, "bwDownMbps": 100.0, "bwUpMbps": 50.0,
		"geo":                map[string]any{"auto": map[string]any{"ip": "198.51.100.200"}},
		"aFieldNobodyModels": "survives the spread",
	}
	got := s.PublicRouter(in)

	for k, want := range in {
		if k == "password" || k == "backup" {
			continue
		}
		wb, _ := json.Marshal(want)
		gb, _ := json.Marshal(got[k])
		if string(gb) != string(wb) {
			t.Errorf("%s: got %s, want %s", k, gb, wb)
		}
	}
	if got["aFieldNobodyModels"] != "survives the spread" {
		t.Error("a field no struct declares was dropped — which is the whole point")
	}
	// The twelve that were missing, named so a failure says which. `backup` is
	// absent from this record on purpose and must stay absent.
	for _, k := range []string{
		"addedAt", "alertsEnabled", "connDownThresholdSec", "geo", "model",
		"osVersion", "password", "pingTarget", "serial", "siteId", "tlsInsecure",
	} {
		if _, ok := got[k]; !ok {
			t.Errorf("%s is missing — it is one of the twelve that started this", k)
		}
	}

	// THE INPUT IS NOT MUTATED. A delete in place would strip the credential from
	// whatever else holds the record.
	if _, ok := in["password"]; ok {
		t.Error("the caller's map gained a password key")
	}
}

// TestAnUnparseableFileIsAnErrorNotAnEmptyFleet.
//
// The live `_readFile` answers `[]` for a file it cannot parse, so the page would
// show zero routers and invite the operator to add their first. This port returns
// the error — a deliberate divergence, and the same one `UserCount` makes.
func TestAnUnparseableFileIsAnErrorNotAnEmptyFleet(t *testing.T) {
	s, dir := routerStore(t, nil)
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PublicRouters(); err == nil {
		t.Errorf("a corrupt routers.json returned %d records and no error", len(got))
	}

	// A MISSING file genuinely is zero routers — the fresh-install state — and
	// must be an empty slice rather than nil, so the payload is `[]` not `null`.
	s2, _ := routerStore(t, nil)
	got, err := s2.PublicRouters()
	if err != nil {
		t.Fatalf("a missing routers.json errored: %v", err)
	}
	if got == nil {
		t.Error("a missing routers.json returned nil, which marshals as null")
	}
	if len(got) != 0 {
		t.Errorf("a missing routers.json returned %d records", len(got))
	}
}
