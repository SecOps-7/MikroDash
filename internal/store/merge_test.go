package store

// The differential gate for `load()`'s four layers.
//
// Expectations come from running the live src/settings.js against a throwaway
// /data with a controlled environment — see tools/settings-merge-cases.js.

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

type mergeCases struct {
	Cases []struct {
		Note   string            `json:"note"`
		Stored map[string]any    `json:"stored"`
		Env    map[string]string `json:"env"`
		Merged map[string]any    `json:"merged"`
	} `json:"cases"`
}

func loadMerge(t *testing.T) mergeCases {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "settings-merge-cases.json"))
	if err != nil {
		t.Fatalf("no cases — run: node tools/settings-merge-cases.js: %v", err)
	}
	var f mergeCases
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("no cases; the corpus is not being read")
	}
	return f
}

// eq compares one merged value against what the live module produced.
//
// NaN NEEDS ITS OWN RULE, and it is not a technicality. `parseInt('not-a-port')`
// is NaN, that NaN reaches the merged object, and JSON.stringify writes it as
// `null`. Go's encoding/json REFUSES to marshal a NaN at all, so a comparison
// that went through JSON would error rather than disagree — and the obvious
// workaround, treating the Go side's NaN as "no value", would silently accept a
// port that substituted a default there. So: a Go NaN matches a JSON null, and
// nothing else does.
func eq(got, want any) bool {
	if f, ok := got.(float64); ok && math.IsNaN(f) {
		return want == nil
	}
	a, errA := json.Marshal(got)
	b, errB := json.Marshal(want)
	if errA != nil || errB != nil {
		return false
	}
	return string(a) == string(b)
}

func TestMergeMatchesTheLiveLoad(t *testing.T) {
	f := loadMerge(t)
	// The corpus reaches load() through getPublic(), so credentials arrive
	// masked. Their values are gated by settings-public-cases.json; here they
	// would only ever compare a mask against a plaintext.
	skip := map[string]bool{}
	for _, k := range tables.Encrypted {
		skip[k] = true
	}

	for _, c := range f.Cases {
		env := func(name string) (string, bool) {
			v, ok := c.Env[name]
			return v, ok
		}
		got, _ := Merge(Settings(c.Stored), env, nil)

		for k, want := range c.Merged {
			if skip[k] {
				continue
			}
			if !eq(got[k], want) {
				t.Errorf("%s: %s = %#v, the live module says %#v", c.Note, k, got[k], want)
			}
		}
		// AND NOTHING EXTRA. A key the port invents is as wrong as one it drops
		// — this is what catches an unknown stored key being carried through.
		for k := range got {
			if _, ok := c.Merged[k]; !ok {
				t.Errorf("%s: the port produced %q = %#v, which the live module does not",
					c.Note, k, got[k])
			}
		}
	}
	t.Logf("%d merge cases, %d keys each", len(f.Cases), len(f.Cases[0].Merged))
}

// TestTheTablesAreThePortsOnlyCopy — a floor under the generated asset, so a
// truncated or half-written file fails loudly instead of merging almost nothing.
func TestTheTablesAreThePortsOnlyCopy(t *testing.T) {
	if n := len(tables.Defaults); n < 100 {
		t.Errorf("only %d defaults embedded; the live module has ~113", n)
	}
	if n := len(tables.EnvMap); n < 30 {
		t.Errorf("only %d env overrides embedded", n)
	}
	if n := len(tables.PollBounds); n < 20 {
		t.Errorf("only %d clamped intervals embedded", n)
	}
	// Every clamped interval must also be a default, or the clamp is reaching
	// for a key the merge never produces.
	for k := range tables.PollBounds {
		if _, ok := tables.Defaults[k]; !ok {
			t.Errorf("%q is clamped but is not a default", k)
		}
	}
}

// TestDefaultsIsACopy — the table is package-level and shared; a caller writing
// into it would change every later merge in the process.
func TestDefaultsIsACopy(t *testing.T) {
	a := Defaults()
	a["topN"] = "clobbered"
	if b := Defaults(); b["topN"] == "clobbered" {
		t.Fatal("Defaults() hands out the shared table — one caller's edit would " +
			"change every later merge")
	}
}

// TestAnEncryptedFieldIsDecrypted covers the path the corpus cannot: it reaches
// load() through getPublic(), which masks. A fake decrypter stands in for the
// store's AES-GCM.
type fakeDec struct{ err error }

func (f fakeDec) Decrypt(s string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "decrypted:" + s, nil
}

func TestAnEncryptedFieldIsDecrypted(t *testing.T) {
	got, _ := Merge(Settings{"routerPass": "SEALED"}, noEnv, fakeDec{})
	if got["routerPass"] != "decrypted:SEALED" {
		t.Errorf("routerPass = %#v, want the decrypted value", got["routerPass"])
	}

	// AN UNDECRYPTABLE CREDENTIAL BECOMES EMPTY, not the ciphertext. Leaving the
	// sealed bytes in place would send them to the browser masked as though they
	// were a working password, and every login with it would fail for a reason
	// nothing on screen explains.
	bad, kept := Merge(Settings{"routerPass": "SEALED"}, noEnv, fakeDec{err: os.ErrInvalid})
	if bad["routerPass"] != "" {
		t.Errorf("an undecryptable credential came back as %#v", bad["routerPass"])
	}
	// AND THE CIPHERTEXT IS HANDED BACK, so the write path can put it where it
	// found it rather than destroying a credential a restored key would recover.
	if kept["routerPass"] != "SEALED" {
		t.Errorf("the unreadable ciphertext was not preserved: %#v", kept["routerPass"])
	}
}

func noEnv(string) (string, bool) { return "", false }
