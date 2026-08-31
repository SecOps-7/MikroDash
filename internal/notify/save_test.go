package notify

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type saveCorpus struct {
	Mask             string   `json:"mask"`
	MaxStr           int      `json:"maxStr"`
	MaxCred          int      `json:"maxCred"`
	ChannelToggles   []string `json:"channelToggles"`
	CredentialFields []string `json:"credentialFields"`
	StrFields        []string `json:"strFields"`
	Cases            []struct {
		Name    string         `json:"name"`
		Stored  map[string]any `json:"stored"`
		Updates map[string]any `json:"updates"`
		Error   *string        `json:"error"`
		Next    map[string]any `json:"next"`
		Public  map[string]any `json:"public"`
	} `json:"cases"`
}

func loadSaveCorpus(t *testing.T) saveCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/usernotify-save-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c saveCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("corpus is empty -- these tests would pass against nothing")
	}
	// The caps and the mask come from the LIVE source via the generator.
	if c.MaxStr != MaxStr || c.MaxCred != MaxCred {
		t.Fatalf("live caps are %d/%d, this port has %d/%d", c.MaxStr, c.MaxCred, MaxStr, MaxCred)
	}
	if c.Mask != Mask {
		t.Fatalf("the live mask is %q, this port has %q", c.Mask, Mask)
	}
	return c
}

// enc is the corpus's stub encryptor. The corpus pins WHEN a value is encrypted,
// not how -- the real cipher belongs to the store and is tested there.
func enc(v string) (string, error) { return "enc(" + v + ")", nil }

func TestMergeMatchesLiveSave(t *testing.T) {
	for _, tc := range loadSaveCorpus(t).Cases {
		t.Run(tc.Name, func(t *testing.T) {
			stored := Settings{}
			for k, v := range tc.Stored {
				stored[k] = v
			}
			got, err := Merge(tc.Updates, stored, enc)

			if tc.Error != nil {
				if err == nil || err.Error() != *tc.Error {
					t.Fatalf("err = %v, live %q", err, *tc.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for k, want := range tc.Next {
				if plain(got[k]) != plain(want) {
					t.Errorf("%s = %v, live %v", k, got[k], want)
				}
			}
			if len(got) != len(tc.Next) {
				t.Errorf("%d keys, live %d", len(got), len(tc.Next))
			}
		})
	}
}

func TestPublicMatchesLive(t *testing.T) {
	for _, tc := range loadSaveCorpus(t).Cases {
		if tc.Public == nil {
			continue
		}
		stored := Settings{}
		for k, v := range tc.Next {
			stored[k] = v
		}
		got := Public(stored)
		for k, want := range tc.Public {
			if plain(got[k]) != plain(want) {
				t.Errorf("%s: %s = %v, live %v", tc.Name, k, got[k], want)
			}
		}
		if len(got) != len(tc.Public) {
			t.Errorf("%s: %d keys, live %d", tc.Name, len(got), len(tc.Public))
		}
	}
}

// TestPublicNeverDisclosesACredential, independently of the corpus.
//
// Whatever is stored -- plaintext, ciphertext, anything -- what reaches the
// browser is a mask or an empty string. This is the function standing between a
// stored bot token and a page anyone signed in can open.
func TestPublicNeverDisclosesACredential(t *testing.T) {
	for _, stored := range []Settings{
		{"telegramBotToken": "enc(secret-token)", "ntfyToken": "enc(secret-ntfy)"},
		{"telegramBotToken": "plaintext-token"},
		{"pushbulletApiKey": "o.abcdef", "telegramBotToken": ""},
	} {
		pub := Public(stored)
		blob, _ := json.Marshal(pub)
		for _, secret := range []string{"secret-token", "secret-ntfy", "plaintext-token",
			"o.abcdef", "enc("} {
			if strings.Contains(string(blob), secret) {
				t.Errorf("%v disclosed %q", stored, secret)
			}
		}
		for _, k := range credentialFields {
			v := plain(pub[k])
			if v != "" && v != Mask {
				t.Errorf("credential %s came back as %q", k, v)
			}
		}
	}
	// ...and a set credential must be distinguishable from an unset one, or the
	// form cannot show whether anything is stored.
	pub := Public(Settings{"telegramBotToken": "x"})
	if pub["telegramBotToken"] != Mask {
		t.Error("a set credential is not shown as set")
	}
	if pub["ntfyToken"] != "" {
		t.Error("an unset credential is shown as set")
	}
}

// TestAnUnknownKeyNeverReachesTheBrowser.
//
// The sender decides where to send by inspecting FIELD NAMES, so an injected
// `smtpHost` would point one user's alerts at a server of somebody else's
// choosing. Public is the allowlist on the way out.
func TestAnUnknownKeyNeverReachesTheBrowser(t *testing.T) {
	got, err := Merge(map[string]any{
		"smtpHost": "evil.example.com", "smtpTo": "attacker@example.net",
		"emailTo": "real@example.com",
	}, Settings{}, enc)
	if err != nil {
		t.Fatal(err)
	}
	pub := Public(got)
	for _, k := range []string{"smtpHost", "smtpTo"} {
		if _, present := pub[k]; present {
			t.Errorf("%s survived into the browser-facing shape", k)
		}
	}
	if pub["emailTo"] != "real@example.com" {
		t.Errorf("the legitimate field was lost: %v", pub["emailTo"])
	}
}

// TestSaveAndTestMergeDisagreeOnPurpose.
//
// The two merges answer different questions and the difference is easy to
// "correct" into a bug: a save with a blank field is how a channel is switched
// off, while a TEST with a blank field should verify what is stored.
func TestSaveAndTestMergeDisagreeOnPurpose(t *testing.T) {
	stored := Settings{"ntfyUrl": "https://ntfy.sh/kept", "ntfyEnabled": true}

	saved, err := Merge(map[string]any{"ntfyUrl": ""}, stored, enc)
	if err != nil {
		t.Fatal(err)
	}
	if saved["ntfyUrl"] != "" {
		t.Errorf("a save with a blank url kept %v -- an address could never be removed",
			saved["ntfyUrl"])
	}

	tested, err := MergeForTest(map[string]any{"ntfyUrl": ""}, stored, "ntfy")
	if err != nil {
		t.Fatal(err)
	}
	if tested["ntfyUrl"] != "https://ntfy.sh/kept" {
		t.Errorf("a TEST with a blank url discarded the stored one: %v", tested["ntfyUrl"])
	}

	// And the trimming, the other way round.
	saved2, _ := Merge(map[string]any{"ntfyUrl": "  https://x/y  "}, stored, enc)
	if saved2["ntfyUrl"] != "https://x/y" {
		t.Errorf("a save did not trim: %q", saved2["ntfyUrl"])
	}
	tested2, _ := MergeForTest(map[string]any{"ntfyUrl": "  https://x/y  "}, stored, "ntfy")
	if tested2["ntfyUrl"] != "  https://x/y  " {
		t.Errorf("a TEST trimmed, which the live route does not: %q", tested2["ntfyUrl"])
	}
}

// TestAnUntouchedCredentialIsNeverReEncrypted: re-encrypting to survive an
// unrelated edit would change the ciphertext on every save, which is pointless
// work and a way to lose a credential to a half-done key rotation.
func TestAnUntouchedCredentialIsNeverReEncrypted(t *testing.T) {
	calls := 0
	counting := func(v string) (string, error) { calls++; return "enc(" + v + ")", nil }
	stored := Settings{"telegramBotToken": "enc(original)", "telegramChatId": "c"}

	got, err := Merge(map[string]any{"telegramChatId": "new-chat"}, stored, counting)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("the encryptor ran %d times for an edit that touched no credential", calls)
	}
	if got["telegramBotToken"] != "enc(original)" {
		t.Errorf("the stored ciphertext changed: %v", got["telegramBotToken"])
	}

	// A masked value is the same story: the form sends it back untouched.
	got2, _ := Merge(map[string]any{"telegramBotToken": Mask}, stored, counting)
	if calls != 0 {
		t.Errorf("a masked credential was encrypted (%d calls)", calls)
	}
	if got2["telegramBotToken"] != "enc(original)" {
		t.Errorf("a masked credential replaced the stored one: %v", got2["telegramBotToken"])
	}
}

// plain renders a corpus value for comparison. Only strings and booleans reach
// these fields.
func plain(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	return ""
}
