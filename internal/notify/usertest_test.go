package notify

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type userTestCorpus struct {
	Mask             string            `json:"mask"`
	CredentialFields []string          `json:"credentialFields"`
	StrFields        []string          `json:"strFields"`
	EnableKey        map[string]string `json:"enableKey"`
	Stored           map[string]any    `json:"stored"`
	Cases            []struct {
		Name    string         `json:"name"`
		Body    map[string]any `json:"body"`
		Channel string         `json:"channel"`
		Out     struct {
			Settings map[string]any `json:"settings"`
			Error    string         `json:"error"`
		} `json:"out"`
	} `json:"cases"`
}

func loadUserTestCorpus(t *testing.T) userTestCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/usernotify-test-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c userTestCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("corpus is empty -- this test would pass against nothing")
	}
	// The mask, the field lists and the enable map are read out of the LIVE
	// source by the generator. If any moves, this fails rather than the port
	// quietly masking with a different string or trusting a field the live app
	// does not.
	if c.Mask != Mask {
		t.Fatalf("the live mask is %q, this port has %q", c.Mask, Mask)
	}
	if strings.Join(c.CredentialFields, ",") != strings.Join(CredentialFields, ",") {
		t.Fatalf("live credential fields %v, this port %v", c.CredentialFields, CredentialFields)
	}
	if strings.Join(c.StrFields, ",") != strings.Join(StrFields, ",") {
		t.Fatalf("live string fields %v, this port %v", c.StrFields, StrFields)
	}
	for ch, key := range c.EnableKey {
		if got, _ := EnableKeyFor(ch); got != key {
			t.Fatalf("live maps %q to %q, this port to %q", ch, key, got)
		}
	}
	return c
}

func TestMergeForTestMatchesLive(t *testing.T) {
	c := loadUserTestCorpus(t)
	stored := Settings{}
	for k, v := range c.Stored {
		stored[k] = v
	}

	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := MergeForTest(tc.Body, stored, tc.Channel)
			if tc.Out.Error != "" {
				if err == nil || err.Error() != tc.Out.Error {
					t.Fatalf("err = %v, live %q", err, tc.Out.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for k, want := range tc.Out.Settings {
				g, present := got[k]
				if !present {
					t.Errorf("%s is missing", k)
					continue
				}
				if jsStringOf(g) != jsStringOf(want) {
					t.Errorf("%s = %v, live %v", k, g, want)
				}
			}
			if len(got) != len(tc.Out.Settings) {
				t.Errorf("%d keys, live %d", len(got), len(tc.Out.Settings))
			}
		})
	}
}

// TestTheMaskNeverTravels is the one that matters, and it is asserted
// independently of the corpus: whatever a form sends, eight bullets must never
// reach a notification provider as a credential.
func TestTheMaskNeverTravels(t *testing.T) {
	stored := Settings{"telegramBotToken": "real-token", "telegramChatId": "real-chat"}
	for _, body := range []map[string]any{
		{"telegramBotToken": Mask},
		{"telegramBotToken": Mask, "telegramChatId": Mask},
		{"telegramBotToken": Mask, "ntfyToken": Mask, "pushbulletApiKey": Mask},
	} {
		got, err := MergeForTest(body, stored, "telegram")
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range got {
			if s, ok := v.(string); ok && strings.Contains(s, Mask) {
				t.Errorf("%s carries the mask: %q", k, s)
			}
		}
		if got["telegramBotToken"] != "real-token" {
			t.Errorf("the stored token was replaced by %v", got["telegramBotToken"])
		}
	}
}

// TestTestingOneChannelDoesNotEnableAnother.
func TestTestingOneChannelDoesNotEnableAnother(t *testing.T) {
	stored := Settings{
		"telegramEnabled": false, "pushbulletEnabled": false,
		"ntfyEnabled": false, "emailEnabled": false,
	}
	for ch, key := range map[string]string{
		"telegram": "telegramEnabled", "pushbullet": "pushbulletEnabled",
		"ntfy": "ntfyEnabled", "email": "emailEnabled",
	} {
		got, err := MergeForTest(map[string]any{}, stored, ch)
		if err != nil {
			t.Fatal(err)
		}
		if got[key] != true {
			t.Errorf("%s: %s was not force-enabled", ch, key)
		}
		for _, other := range []string{"telegramEnabled", "pushbulletEnabled", "ntfyEnabled", "emailEnabled"} {
			if other == key {
				continue
			}
			if got[other] == true {
				t.Errorf("testing %s also enabled %s", ch, other)
			}
		}
	}
}

// TestTheCapsBoundWhatAUserCanMakeTheServerSend.
func TestTheCapsBoundWhatAUserCanMakeTheServerSend(t *testing.T) {
	long := strings.Repeat("x", 5000)
	got, err := MergeForTest(map[string]any{
		"telegramBotToken": long, "ntfyUrl": long,
	}, Settings{}, "telegram")
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(got["telegramBotToken"].(string))); n != CredentialCap {
		t.Errorf("credential capped at %d, want %d", n, CredentialCap)
	}
	if n := len([]rune(got["ntfyUrl"].(string))); n != StringCap {
		t.Errorf("string field capped at %d, want %d", n, StringCap)
	}
	// A multi-byte value must not be cut mid-rune.
	multi := strings.Repeat("é", 5000)
	got2, _ := MergeForTest(map[string]any{"ntfyUrl": multi}, Settings{}, "ntfy")
	s := got2["ntfyUrl"].(string)
	if !utf8Valid(s) {
		t.Error("the cap truncated a multi-byte value mid-rune")
	}
	if len([]rune(s)) != StringCap {
		t.Errorf("multi-byte value capped at %d runes, want %d", len([]rune(s)), StringCap)
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}
