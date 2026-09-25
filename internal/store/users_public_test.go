package store

// The user disclosure boundary, against the live `listUsers()`.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type userCases struct {
	Stripped []string `json:"stripped"`
	Cases    []struct {
		Note   string           `json:"note"`
		Users  []map[string]any `json:"users"`
		Public []map[string]any `json:"public"`
	} `json:"cases"`
}

func loadUserCases(t *testing.T) userCases {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "users-public-cases.json"))
	if err != nil {
		t.Fatalf("no cases — run: node tools/users-public-cases.js: %v", err)
	}
	var f userCases
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("no cases; the corpus is not being read")
	}
	return f
}

func TestPublicUsersMatchesTheLiveListUsers(t *testing.T) {
	f := loadUserCases(t)
	for _, c := range f.Cases {
		got := make([]map[string]any, 0, len(c.Users))
		for _, u := range c.Users {
			got = append(got, PublicUser(u))
		}
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(c.Public)
		if string(a) != string(b) {
			t.Errorf("%s:\n  port: %s\n  live: %s", c.Note, a, b)
		}
	}
	t.Logf("%d cases", len(f.Cases))
}

// TestTheStrippedListMatchesTheLiveModule — the list itself, so a field the live
// module starts removing is caught before it is disclosed here.
func TestTheStrippedListMatchesTheLiveModule(t *testing.T) {
	f := loadUserCases(t)
	if len(f.Stripped) != len(UserSecretFields) {
		t.Fatalf("port strips %v, the live module strips %v", UserSecretFields, f.Stripped)
	}
	have := map[string]bool{}
	for _, k := range UserSecretFields {
		have[k] = true
	}
	for _, k := range f.Stripped {
		if !have[k] {
			t.Errorf("%q is stripped by the live module and not by this port", k)
		}
	}
}

// TestNoSecretSurvivesInAnyCase is the assertion that matters most, and it looks
// at the SERIALISED payload rather than at keys: a hash reaching the browser
// under a different key name is the same leak.
func TestNoSecretSurvivesInAnyCase(t *testing.T) {
	f := loadUserCases(t)
	for _, c := range f.Cases {
		for _, u := range c.Users {
			body, _ := json.Marshal(PublicUser(u))
			for _, field := range UserSecretFields {
				if v, ok := u[field].(string); ok && v != "" {
					if strings.Contains(string(body), v) {
						t.Errorf("%s: the value of %s (%q) is in the payload: %s",
							c.Note, field, v, body)
					}
				}
			}
		}
	}
}

// TestPublicUserDoesNotMutateItsInput — the same record is read to VERIFY a
// password. Deleting in place would remove the hash from under that check, and
// every login would then fail against a user whose record had merely been
// listed.
func TestPublicUserDoesNotMutateItsInput(t *testing.T) {
	in := map[string]any{"id": "u1", "passwordHash": "DEADBEEF", "salt": "CAFE"}
	_ = PublicUser(in)
	if in["passwordHash"] != "DEADBEEF" || in["salt"] != "CAFE" {
		t.Fatalf("PublicUser stripped its INPUT: %v — the record used to verify a "+
			"password would have lost its hash", in)
	}
}

// TestAnEmptyFileIsAnEmptyListNotNull — the card renders `[]` and `null`
// differently.
func TestAnEmptyFileIsAnEmptyListNotNull(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "users.json"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := s.PublicUsers()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	if string(b) != "[]" {
		t.Errorf("an empty users.json serialised as %s, want []", b)
	}
}
