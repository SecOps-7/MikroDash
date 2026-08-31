package store

// Does the Go writer produce the same FILE as the live `save()`?
//
// Both apps read settings.json, so equivalent JSON would be functionally fine.
// The reason to go further is the operator: this is a file people diff,
// hand-edit and copy between installs, and two writers that disagree about key
// order or escaping turn the first save after cutover into a 113-line diff that
// means nothing.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fileCases struct {
	SealedMarker string   `json:"sealedMarker"`
	Encrypted    []string `json:"encrypted"`
	Cases        []struct {
		Note    string         `json:"note"`
		Updates map[string]any `json:"updates"`
		File    string         `json:"file"`
	} `json:"cases"`
}

// fixedEnc stands in for AES-GCM. The real one uses a random IV, so its output
// could never be compared byte for byte — the corpus normalises those lines and
// so does this.
type fixedEnc struct{}

func (fixedEnc) Encrypt(string) (string, error) { return "<SEALED>", nil }

func TestTheWrittenFileMatchesTheLiveSave(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "settings-file-cases.json"))
	if err != nil {
		t.Fatalf("no cases — run: node tools/settings-file-cases.js: %v", err)
	}
	var f fileCases
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("no cases; the corpus is not being read")
	}

	for _, c := range f.Cases {
		// The live save merges the updates over `load()`, which is defaults
		// plus an empty file here — so the same starting point.
		next, kept := Merge(Settings{}, func(string) (string, bool) { return "", false }, nil)
		updates := Settings{}
		for k, v := range c.Updates {
			next[k] = v
			updates[k] = v
		}

		dir := t.TempDir()
		if err := SaveSettings(dir, next, updates, kept, fixedEnc{}); err != nil {
			t.Fatalf("%s: %v", c.Note, err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "settings.json"))
		if err != nil {
			t.Fatal(err)
		}

		if string(got) != c.File {
			t.Errorf("%s: the written file differs from the live one\n%s",
				c.Note, firstDifference(string(got), c.File))
		}
	}
	t.Logf("%d files compared byte for byte", len(f.Cases))
}

// firstDifference reports where two files diverge, by LINE, because a character
// offset into a 115-line file says nothing a reader can act on.
func firstDifference(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) && i < len(w); i++ {
		if g[i] != w[i] {
			return "  line " + itoa(i+1) + "\n    port: " + g[i] + "\n    live: " + w[i]
		}
	}
	if len(g) != len(w) {
		return "  the port wrote " + itoa(len(g)) + " lines, the live app wrote " + itoa(len(w))
	}
	return "  (identical line by line, so the difference is a trailing byte)"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

// TestSettingsJSONIsOwnerOnly — it holds sealed credentials, and a
// world-readable settings.json on a shared host hands them to any local account.
// Named apart from the routers.json check in write_test.go: two files, two
// writers, and a permission regression in one says nothing about the other.
func TestSettingsJSONIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	next, kept := Merge(Settings{}, func(string) (string, bool) { return "", false }, nil)
	if err := SaveSettings(dir, next, Settings{}, kept, fixedEnc{}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("settings.json is %o, want 600", perm)
	}
}

// TestAnUnreadableCredentialSurvivesAnUnrelatedSave is the rule `_cipherKeep`
// exists for. A credential that will not decrypt means the key changed or the
// file came from another install — NOT that the operator cleared it. Writing ""
// would destroy it on the first save of any unrelated setting, and a restored
// key would then recover nothing.
func TestAnUnreadableCredentialSurvivesAnUnrelatedSave(t *testing.T) {
	stored := Settings{"telegramBotToken": "CIPHERTEXT-FROM-ANOTHER-KEY", "topN": float64(10)}
	next, kept := Merge(stored, func(string) (string, bool) { return "", false },
		fakeDec{err: os.ErrInvalid})
	if kept["telegramBotToken"] == "" {
		t.Fatal("precondition: the merge did not preserve the ciphertext")
	}

	dir := t.TempDir()
	// An unrelated change.
	next["topN"] = float64(25)
	if err := SaveSettings(dir, next, Settings{"topN": float64(25)}, kept, fixedEnc{}); err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]any
	raw, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk["telegramBotToken"] != "CIPHERTEXT-FROM-ANOTHER-KEY" {
		t.Errorf("the unreadable credential was written as %#v — a restored key "+
			"would now recover nothing", onDisk["telegramBotToken"])
	}
}

// TestAnExplicitUpdateDiscardsPreservedCiphertext — the other half. When the
// operator sets or clears the field, they have spoken about it, and the old
// bytes must not come back.
func TestAnExplicitUpdateDiscardsPreservedCiphertext(t *testing.T) {
	kept := Kept{"telegramBotToken": "CIPHERTEXT-FROM-ANOTHER-KEY"}

	for _, tc := range []struct {
		name string
		val  string
		want string
	}{
		{"cleared", "", ""},
		{"replaced", "NOT-A-REAL-NEW-TOKEN", "<SEALED>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			next, _ := Merge(Settings{}, func(string) (string, bool) { return "", false }, nil)
			next["telegramBotToken"] = tc.val
			updates := Settings{"telegramBotToken": tc.val}
			if err := SaveSettings(dir, next, updates, kept, fixedEnc{}); err != nil {
				t.Fatal(err)
			}
			var onDisk map[string]any
			raw, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
			_ = json.Unmarshal(raw, &onDisk)
			if onDisk["telegramBotToken"] != tc.want {
				t.Errorf("wrote %#v, want %#v", onDisk["telegramBotToken"], tc.want)
			}
		})
	}
}
