package store

// `AddRouter` against the LIVE `Routers.add`, recorded by
// The router-add corpus.
//
// The cases run IN ORDER against one growing store, because the label is a
// function of the fleet rather than of the body: two routers called "Depot"
// become "Depot" and "Depot - [2]".

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type addCorpus struct {
	Cases map[string]struct {
		Body    map[string]any `json:"body"`
		Record  map[string]any `json:"record"`
		Refused bool           `json:"refused"`
	} `json:"cases"`
}

// The order the generator used. Written out because JSON objects have no order
// and the label cases depend on it.
var addOrder = []string{
	"minimal", "fullySpecified", "duplicateLabel", "duplicateLabelAgain",
	"tlsStringFalse", "tlsStringTrue", "tlsAbsent", "tlsInsecureStringTrue",
	// The tlsInsecure spellings, added 2026-08-29 with upstream `dccbf62`. The
	// corpus had `'true'` and `true` and NOT the string "false" — the one value
	// the old coercion inverted — which is why an upstream security fix left
	// every gate here green.
	"tlsInsecureStringFalse", "tlsInsecureLiteralFalse", "tlsInsecureAbsent",
	"tlsInsecureOne", "tlsInsecureYes", "tlsInsecureUpperTrue", "tlsInsecureOn",
	"maskedPassword", "emptyPassword",
	"bwZero", "bwNegative", "bwUnparseable", "bwStrings",
	"connDownZero", "connDownMax", "connDownOver", "connDownNegative", "connDownAbsent",
	"siteIdsArray", "siteIdScalar", "siteIdsEmpty", "siteNone",
	"paddedFields", "longLabel",
	"noHost", "emptyHost", "portZero", "portTooHigh",
}

func TestAddRouterMatchesLive(t *testing.T) {
	b, err := os.ReadFile("../../testdata/router-add-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c addCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("corpus is empty -- this test would pass against nothing")
	}
	if len(addOrder) != len(c.Cases) {
		t.Fatalf("the order list has %d names and the corpus %d cases -- they have "+
			"drifted, and the label cases depend on the order", len(addOrder), len(c.Cases))
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range addOrder {
		tc, ok := c.Cases[name]
		if !ok {
			t.Fatalf("the order list names %q, which the corpus does not have", name)
		}
		t.Run(name, func(t *testing.T) {
			rec, err := s.AddRouter(tc.Body)

			if tc.Refused {
				if err == nil {
					t.Errorf("body %v was ACCEPTED; the live code refuses it", tc.Body)
				}
				return
			}
			if err != nil {
				t.Fatalf("body %v was refused: %v", tc.Body, err)
			}

			// Compared through the FILE, not the returned struct: this port's
			// Router type is a subset of the record, so a struct comparison would
			// silently skip the four fields it does not carry — which are exactly
			// the ones a re-marshalling implementation would have dropped.
			got := lastRouterRecord(t, dir)
			delete(got, "id")
			delete(got, "addedAt")

			// ── THE PASSWORD IS COMPARED SEPARATELY, AND FOR THE OPPOSITE
			//    PROPERTY ─────────────────────────────────────────────────────
			//
			// The corpus holds what `Routers.add` RETURNED, which carries the
			// plaintext. The file holds what `_writeFile` STORED, which is
			// ciphertext — and ciphertext differs on every run, so it cannot be
			// compared to anything. What is checked instead is that the two are
			// NOT equal when a password was supplied, which is the property that
			// matters: a plaintext credential must never reach the disk.
			wantPlain, _ := tc.Record["password"].(string)
			gotStored, _ := got["password"].(string)
			delete(got, "password")
			delete(tc.Record, "password")

			if wantPlain == "" {
				if gotStored != "" {
					t.Errorf("no password was supplied but %q was stored", gotStored)
				}
			} else {
				if gotStored == "" {
					t.Error("a password was supplied and nothing was stored")
				}
				if gotStored == wantPlain {
					t.Errorf("THE PASSWORD IS ON DISK IN PLAINTEXT: %q", gotStored)
				}
				// ...and it decrypts back to what was given.
				if plain, err := s.Decrypt(gotStored); err != nil || plain != wantPlain {
					t.Errorf("the stored value does not decrypt to the supplied password "+
						"(%q, %v)", plain, err)
				}
			}

			if !reflect.DeepEqual(got, tc.Record) {
				t.Errorf("record differs:\n  got  %s\n  live %s\n  missing %v\n  extra %v",
					mustJSON(got), mustJSON(tc.Record),
					missingKeys(tc.Record, got), missingKeys(got, tc.Record))
			}
			if rec == nil || rec.ID == "" {
				t.Error("AddRouter returned no record")
			}
		})
	}
}

// TestAddingARouterDoesNotStripFieldsFromTheOthers.
//
// This port's `Router` struct has no `pingTarget`, `alertsEnabled`,
// `connDownThresholdSec` or `addedAt`. An implementation that decoded the fleet
// and re-marshalled it would drop those four from EVERY existing record — an
// operator would add one device and lose the connectivity thresholds on all the
// others, with nothing to see.
func TestAddingARouterDoesNotStripFieldsFromTheOthers(t *testing.T) {
	dir := t.TempDir()
	// A record carrying fields this port does not model, plus one it invents.
	const seeded = `[{"id":"r1","label":"Existing","host":"198.51.100.1","port":8728,
	  "username":"u","password":"","pingTarget":"192.0.2.9","alertsEnabled":true,
	  "connDownThresholdSec":90,"addedAt":1700000000000,"somethingFuture":"keep me"}]`
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(seeded), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.AddRouter(map[string]any{"host": "198.51.100.2"}); err != nil {
		t.Fatal(err)
	}

	all := allRouterRecords(t, dir)
	if len(all) != 2 {
		t.Fatalf("%d records after adding one to a fleet of one", len(all))
	}
	kept := all[0]
	for k, want := range map[string]any{
		"pingTarget": "192.0.2.9", "alertsEnabled": true,
		"connDownThresholdSec": float64(90), "addedAt": float64(1700000000000),
		"somethingFuture": "keep me",
	} {
		got, present := kept[k]
		if !present {
			t.Errorf("%s was STRIPPED from the existing router by adding a different one", k)
			continue
		}
		if got != want {
			t.Errorf("%s = %#v, want %#v", k, got, want)
		}
	}
}

func lastRouterRecord(t *testing.T, dir string) map[string]any {
	t.Helper()
	all := allRouterRecords(t, dir)
	if len(all) == 0 {
		t.Fatal("routers.json is empty")
	}
	return all[len(all)-1]
}

func allRouterRecords(t *testing.T, dir string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("routers.json is not valid JSON: %v", err)
	}
	return out
}

func missingKeys(want, got map[string]any) []string {
	var out []string
	for k := range want {
		if _, ok := got[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}
