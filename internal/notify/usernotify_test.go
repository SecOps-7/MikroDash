package notify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type unCorpus struct {
	Defaults Settings `json:"defaults"`
	Picks    []struct {
		Name    string   `json:"name"`
		Stored  Settings `json:"stored"`
		Decrypt bool     `json:"decrypt"`
		Want    Settings `json:"want"`
	} `json:"picks"`
	Mails []struct {
		Name    string   `json:"name"`
		Own     Settings `json:"own"`
		Install Settings `json:"install"`
		Want    Settings `json:"want"`
	} `json:"mails"`
}

func loadUN(t *testing.T) unCorpus {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "usernotify-cases.json"))
	if err != nil {
		t.Fatalf("corpus: %v (regenerate with tools/usernotify-cases.js)", err)
	}
	var c unCorpus
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Picks) < 8 || len(c.Mails) < 4 {
		t.Fatal("the corpus is not the generated one")
	}
	return c
}

// The generator's decryptor is the identity wrapped in `dec(...)`, so the port
// is handed the same one — what decryption actually does is `internal/store`'s
// business and is pinned there.
func fakeDecrypt(s string) string { return "dec(" + s + ")" }

func TestDefaultsMatchTheLiveTables(t *testing.T) {
	live := loadUN(t).Defaults
	got := Defaults()
	if !reflect.DeepEqual(got, live) {
		t.Errorf("Defaults\n  got  %v\n  live %v", got, live)
	}
}

func TestPickMatchesTheLiveModule(t *testing.T) {
	for _, c := range loadUN(t).Picks {
		t.Run(c.Name, func(t *testing.T) {
			var dec func(string) string
			if c.Decrypt {
				dec = fakeDecrypt
			}
			got := Pick(c.Stored, dec)
			if !reflect.DeepEqual(got, c.Want) {
				t.Errorf("Pick\n  got  %v\n  live %v", got, c.Want)
			}
		})
	}
}

func TestWithInstallMailMatchesTheLiveModule(t *testing.T) {
	for _, c := range loadUN(t).Mails {
		t.Run(c.Name, func(t *testing.T) {
			got := WithInstallMail(c.Own, c.Install)
			if !reflect.DeepEqual(got, c.Want) {
				t.Errorf("WithInstallMail\n  got  %v\n  live %v", got, c.Want)
			}
		})
	}
}

// THE ALLOWLIST IS A SECURITY BOUNDARY. An injected transport field would point
// a user's alerts at a server of somebody else's choosing, or redirect them
// outright — asserted directly as well as through the corpus, because a corpus
// can be regenerated into agreement with a broken implementation and this
// cannot.
func TestInjectedTransportFieldsDoNotSurvive(t *testing.T) {
	stored := Settings{
		"telegramEnabled": true,
		"smtpHost":        "evil.example",
		"smtpTo":          "attacker@example",
		"smtpEnabled":     true,
		"nonsense":        1,
	}
	got := Pick(stored, nil)
	for _, k := range []string{"smtpHost", "smtpTo", "smtpEnabled", "nonsense"} {
		if _, present := got[k]; present {
			t.Errorf("%q survived the allowlist", k)
		}
	}
	if got["telegramEnabled"] != true {
		t.Error("a legitimate field did not survive")
	}
}

// A HALF-CONFIGURED INSTALL YIELDS NO CHANNEL — the same cooldown-consuming
// state `Channels` refuses. Checked end to end, because the two functions are
// what a recipient record passes through.
func TestAHalfConfiguredInstallYieldsNoChannel(t *testing.T) {
	own := Settings{"emailEnabled": true, "emailTo": "u@example"}
	for _, install := range []Settings{
		{},
		{"smtpHost": "h"},
		{"smtpFrom": "f"},
	} {
		folded := WithInstallMail(own, install)
		if HasConfigured(folded) {
			t.Errorf("install %v produced a usable channel", install)
		}
	}
	ready := WithInstallMail(own, Settings{"smtpHost": "h", "smtpFrom": "f"})
	if !HasConfigured(ready) {
		t.Error("a ready install produced no channel")
	}
	if ready["smtpTo"] != "u@example" {
		t.Errorf("smtpTo = %v; the recipient must be the USER, not the install", ready["smtpTo"])
	}
}
