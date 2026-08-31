package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"mikrodash/internal/store"
)

// Credentials must reach a transport DECRYPTED.
//
// ── THE BUG THIS EXISTS FOR ────────────────────────────────────────────────
//
// The live app seals six settings fields at rest. `store.Settings()` reads the
// file and nothing else, so those come back as AES-GCM ciphertext;
// `mergedSettings()` is what decrypts them. Three consumers called the former:
// the Test buttons, the alert dispatcher and the report mailer. Telegram
// answered HTTP 404 to a bot id that was really a base64 blob, and SMTP would
// have failed auth the same way.
//
// EVERY EXISTING TEST PUT PLAINTEXT IN THE MAP, which is precisely what made the
// ciphertext path unreachable from the suite and reachable from production. So
// this fixture SEALS the value first, the way the real file holds it.

func credStore(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"routers.json": `[]`, "settings.json": `{}`,
		".secret": "test-secret", "users.json": `[]`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{store: st}
}

func TestSealedCredentialsAreDecryptedForTheTransports(t *testing.T) {
	s := credStore(t)
	const plain = "123456:not-a-real-bot-token"

	sealed, err := s.store.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if sealed == plain {
		t.Fatal("Encrypt returned the plaintext — this fixture would prove nothing")
	}
	body, _ := json.Marshal(map[string]any{
		"telegramEnabled": true, "telegramBotToken": sealed, "telegramChatId": "42",
	})
	if err := os.WriteFile(filepath.Join(s.store.Dir, "settings.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	// The raw file still holds ciphertext — that is what makes the mistake easy.
	raw, err := s.store.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if raw["telegramBotToken"] == plain {
		t.Fatal("store.Settings() decrypted — this test's premise is gone; " +
			"if the raw read now decrypts, the merged/raw distinction has changed")
	}

	merged, err := s.mergedSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := merged["telegramBotToken"].(string); got != plain {
		t.Errorf("mergedSettings gave %q, want the decrypted token. A transport handed "+
			"this posts ciphertext as its credential; Telegram answers HTTP 404.", got)
	}
}

// ── AND THE CONSUMERS MUST ACTUALLY CALL IT ───────────────────────────────
//
// The behavioural test above proves `mergedSettings` decrypts. It cannot prove
// that the notification and report paths USE it — and that was the whole bug:
// the accessor already existed and three call sites reached past it.
func TestCredentialConsumersUseTheMergedSettings(t *testing.T) {
	// Each file, and the encrypted field it ends up handing to a transport.
	for _, f := range []struct{ file, why string }{
		{"test_notif_api.go", "the Test buttons post telegramBotToken / ntfyToken / smtpPass"},
		{"alert_wire.go", "every dispatched alert authenticates with them"},
		{"reports_run.go", "the report mailer authenticates with smtpUser and smtpPass"},
	} {
		b, err := os.ReadFile(f.file)
		if err != nil {
			t.Fatalf("reading %s: %v", f.file, err)
		}
		src := string(b)
		// Comments legitimately mention the raw accessor; code must not call it.
		code := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(src, "")
		if strings.Contains(code, "s.store.Settings()") {
			t.Errorf("%s calls s.store.Settings(), which returns SEALED credentials — %s. "+
				"Use s.mergedSettings().", f.file, f.why)
		}
		if !strings.Contains(code, "s.mergedSettings()") {
			t.Errorf("%s never calls s.mergedSettings(); it cannot be reading real "+
				"credentials (%s)", f.file, f.why)
		}
	}
}
