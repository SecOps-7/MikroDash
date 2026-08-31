package notify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type corpus struct {
	Reasons []struct {
		Name string  `json:"name"`
		Raw  *string `json:"raw"`
		Want string  `json:"want"`
	} `json:"reasons"`
	Channels []struct {
		Name     string   `json:"name"`
		Settings Settings `json:"settings"`
		Want     bool     `json:"want"`
	} `json:"channels"`
}

func load(t *testing.T) corpus {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "notifier-cases.json"))
	if err != nil {
		t.Fatalf("corpus: %v (regenerate with tools/notifier-cases.js)", err)
	}
	var c corpus
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Reasons) < 10 || len(c.Channels) < 10 {
		t.Fatal("the corpus is not the generated one")
	}
	return c
}

func TestReasonMatchesTheLiveModule(t *testing.T) {
	for _, c := range load(t).Reasons {
		t.Run(c.Name, func(t *testing.T) {
			raw := ""
			if c.Raw != nil {
				raw = *c.Raw
			}
			if got := Reason(raw); got != c.Want {
				t.Errorf("Reason(%q)\n  got  %q\n  live %q", raw, got, c.Want)
			}
		})
	}
}

func TestHasConfiguredMatchesTheLiveModule(t *testing.T) {
	for _, c := range load(t).Channels {
		t.Run(c.Name, func(t *testing.T) {
			if got := HasConfigured(c.Settings); got != c.Want {
				t.Errorf("HasConfigured = %v, live says %v (settings %v)", got, c.Want, c.Settings)
			}
		})
	}
}

// THE BUG THE LIVE COMMENT RECORDS. "Is any channel active?" must mean enabled
// AND configured — asking the flags alone let a channel ticked without a token
// consume the alert cooldown, send nothing, and log nothing.
//
// Asserted here as well as through the corpus because it is the REASON this
// function exists, and a corpus can be regenerated into agreement with a broken
// implementation while this cannot.
func TestEnabledWithoutCredentialsIsNotConfigured(t *testing.T) {
	cases := []Settings{
		{"telegramEnabled": true, "telegramChatId": "c"},
		{"telegramEnabled": true, "telegramBotToken": "t"},
		{"pushbulletEnabled": true},
		{"smtpEnabled": true, "smtpHost": "h", "smtpFrom": "f"},
		{"ntfyEnabled": true},
	}
	for _, s := range cases {
		if HasConfigured(s) {
			t.Errorf("%v counted as configured", s)
		}
		if ch := Channels(s); len(ch) != 0 {
			t.Errorf("%v produced channels %v", s, ch)
		}
	}
}

// `Channels` and `HasConfigured` cannot disagree, because one is derived from
// the other — pinned so a later "optimisation" that reintroduces two readings
// fails here.
func TestTheTwoAnswersCannotDiverge(t *testing.T) {
	for _, c := range load(t).Channels {
		if (len(Channels(c.Settings)) > 0) != HasConfigured(c.Settings) {
			t.Errorf("%s: Channels and HasConfigured disagree", c.Name)
		}
	}
}

// An empty string is FALSY, so a blank token is a missing one.
func TestABlankCredentialIsNotConfigured(t *testing.T) {
	if HasConfigured(Settings{"telegramEnabled": true, "telegramBotToken": "", "telegramChatId": "c"}) {
		t.Error("a blank token counted as configured")
	}
}
