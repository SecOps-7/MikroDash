package server

import (
	"strings"
	"testing"

	"mikrodash/internal/notify"
)

// THE UPGRADE PATH, AND WHAT IT REFUSES TO CARRY.
//
// An install whose Telegram section is half-filled never sent anything. Turning
// it into a channel would make the Settings page show a configured destination
// that has never worked and never will, which is worse than showing none.
func TestOnlyEnabledAndCredentialedTransportsBecomeChannels(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]any
		want []string // channel names, in order
	}{
		{
			name: "all four configured",
			cfg: map[string]any{
				"telegramEnabled": true, "telegramBotToken": "111:AAA", "telegramChatId": "-100",
				"pushbulletEnabled": true, "pushbulletApiKey": "o.ABC",
				"ntfyEnabled": true, "ntfyUrl": "https://ntfy.example.net/mikrodash",
				"smtpEnabled": true, "smtpHost": "mail.example.net", "smtpTo": "ops@example.net",
			},
			want: []string{"Telegram", "Pushbullet", "ntfy", "Email"},
		},
		{
			name: "enabled but not credentialed",
			cfg: map[string]any{
				"telegramEnabled": true, "telegramBotToken": "111:AAA", // no chat id
				"pushbulletEnabled": true,                                 // no key
				"ntfyEnabled":       true,                                 // no url
				"smtpEnabled":       true, "smtpHost": "mail.example.net", // no recipient
			},
			want: []string{},
		},
		{
			name: "credentialed but switched off",
			cfg: map[string]any{
				"telegramEnabled": false, "telegramBotToken": "111:AAA", "telegramChatId": "-100",
				"pushbulletEnabled": false, "pushbulletApiKey": "o.ABC",
			},
			want: []string{},
		},
		{
			// `truthy` is JS semantics: the STRING "false" is TRUE. Reproduced
			// deliberately in notify.Truthy, so an install that stored the
			// string keeps the behaviour it already had.
			name: "the string false is still on",
			cfg: map[string]any{
				"telegramEnabled": "false", "telegramBotToken": "111:AAA", "telegramChatId": "-100",
			},
			want: []string{"Telegram"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := installChannels(c.cfg, func(v string) string { return v })
			if len(got) != len(c.want) {
				names := []string{}
				for _, g := range got {
					names = append(names, g.name)
				}
				t.Fatalf("made %v, want %v", names, c.want)
			}
			for i, w := range c.want {
				if got[i].name != w {
					t.Errorf("channel %d is %q, want %q", i, got[i].name, w)
				}
			}
		})
	}
}

// THE CARRIED URL MUST BE ONE `Parse` ACCEPTS, or the channel is created and
// then refuses to send — an upgrade that looks successful and is not.
func TestEveryCarriedURLIsSendable(t *testing.T) {
	cfg := map[string]any{
		"telegramEnabled": true, "telegramBotToken": "8123456789:AAH-token",
		"telegramChatId":    "-1001",
		"pushbulletEnabled": true, "pushbulletApiKey": "o.ABCDEF",
		"ntfyEnabled": true, "ntfyUrl": "https://ntfy.example.net/mikrodash",
		"ntfyToken": "tk_1",
	}
	for _, c := range installChannels(cfg, func(v string) string { return v }) {
		wc, ok := c.cfg.(webhookConfig)
		if !ok {
			continue
		}
		for _, u := range wc.URLs {
			if err := notify.Validate(u); err != nil {
				t.Errorf("%s carried a URL the sender refuses (%v)", c.name, err)
			}
		}
	}
}

// THE OLD ntfy SETTING WAS A WHOLE http(s) URL and the new one is a scheme.
func TestNtfyURLBecomesAScheme(t *testing.T) {
	cases := map[string]string{
		"https://ntfy.example.net/mikrodash": "ntfys://ntfy.example.net/mikrodash",
		"http://ntfy.example.net/mikrodash":  "ntfy://ntfy.example.net/mikrodash",
		"https://ntfy.example.net/topic/":    "ntfys://ntfy.example.net/topic",
		// NO TOPIC IS NOT A DESTINATION. Carrying it would make a channel that
		// posts to the server root and fails on every alert.
		"https://ntfy.example.net": "",
		"":                         "",
	}
	for in, want := range cases {
		if got := ntfyURLToScheme(in, ""); got != want {
			t.Errorf("ntfyURLToScheme(%q) = %q, want %q", in, got, want)
		}
	}
	// The token rides in the query, where `Parse` looks for it.
	got := ntfyURLToScheme("https://ntfy.example.net/topic", "tk_1")
	if !strings.HasSuffix(got, "?token=tk_1") {
		t.Errorf("the token did not survive: %q", got)
	}
	if err := notify.Validate(got); err != nil {
		t.Errorf("the carried ntfy URL is not sendable: %v", err)
	}
}

// AN SMTP CHANNEL KEEPS EVERY PART OF THE MAIL SETUP. Losing the password on
// upgrade would leave a channel that authenticates as nobody.
func TestTheEmailChannelCarriesTheWholeMailSetup(t *testing.T) {
	cfg := map[string]any{
		"smtpEnabled": true, "smtpHost": "mail.example.net", "smtpPort": "465",
		"smtpSecure": true, "smtpUser": "u", "smtpPass": "p",
		"smtpFrom": "md@example.net", "smtpTo": "ops@example.net",
	}
	got := installChannels(cfg, func(v string) string { return v })
	if len(got) != 1 {
		t.Fatalf("made %d channels, want 1", len(got))
	}
	sc, ok := got[0].cfg.(smtpConfigJSON)
	if !ok {
		t.Fatalf("the email channel is %T, not an SMTP config", got[0].cfg)
	}
	want := smtpConfigJSON{
		Host: "mail.example.net", Port: 465, Secure: true, User: "u", Pass: "p",
		From: "md@example.net", To: "ops@example.net",
	}
	if sc != want {
		t.Errorf("carried\n%+v\nwant\n%+v", sc, want)
	}
}

// THE MIGRATION READS THE RAW FILE, NOT THE MERGED MAP.
//
// ── THE BUG THIS EXISTS FOR ────────────────────────────────────────────────
//
// `store.Merge` drops any key that is not a default, and `telegram*`,
// `pushbullet*` and `ntfy*` stopped being defaults the moment they became
// channels. `SeedNotifyChannels` read `mergedSettings()`, so on a real install
// with Telegram configured and working it found nothing at all and carried
// nothing across — the notifications simply stopped.
//
// Every unit test passed, because each one hands `installChannels` a map
// directly and never goes through the merge. It was found by deploying the
// build and asking the API how many channels existed: zero, against a
// settings.json that plainly had `"telegramEnabled": true`.
//
// This pins the half a unit test can reach: that the credentials are taken
// through `dec`, because in the raw file they are sealed.
func TestTheMigrationUnsealsCredentialsFromTheRawFile(t *testing.T) {
	// The raw file's shape: enable flags in the clear, credentials sealed.
	raw := map[string]any{
		"telegramEnabled": true, "telegramBotToken": "SEALED", "telegramChatId": "-100",
		"smtpEnabled": true, "smtpHost": "mail.example.net", "smtpTo": "ops@example.net",
		"smtpUser": "SEALED-USER", "smtpPass": "SEALED-PASS",
	}
	unseal := func(v string) string {
		switch v {
		case "SEALED":
			return "111:AAA"
		case "SEALED-USER":
			return "postmaster"
		case "SEALED-PASS":
			return "hunter2"
		}
		return v
	}

	got := installChannels(raw, unseal)
	if len(got) != 2 {
		t.Fatalf("carried %d channels, want Telegram and Email", len(got))
	}

	wc, ok := got[0].cfg.(webhookConfig)
	if !ok || len(wc.URLs) != 1 {
		t.Fatalf("the Telegram channel is %+v", got[0].cfg)
	}
	if wc.URLs[0] != "tgram://111:AAA/-100" {
		t.Errorf("carried %q — the token was not unsealed, so the channel would "+
			"post ciphertext as its bot token and Telegram would answer 404", wc.URLs[0])
	}

	sc, ok := got[1].cfg.(smtpConfigJSON)
	if !ok {
		t.Fatalf("the email channel is %T", got[1].cfg)
	}
	if sc.User != "postmaster" || sc.Pass != "hunter2" {
		t.Errorf("the mail credentials were not unsealed: user=%q pass=%q", sc.User, sc.Pass)
	}
}

// THE PORT ARRIVES AS A NUMBER, AND THAT IS THE SHAPE THAT WAS BROKEN.
//
// `settings.json` is written by `encoding/json`, so a stored port is a float64
// on the way back in — never the string the test above happens to use. The
// migration read it with the same `str` helper as the host and the recipient,
// which type-asserts to string, missed the float64 and produced port 0. An
// upgraded install would have had a mail channel that looked complete on the
// page and dialled nowhere.
//
// It is the second time this exact shape has bitten in this feature:
// `smtpFromSettings` in test_notif_api.go parsed float64 and int but not the
// string `DecodeChannel` writes. So both directions are pinned here, over every
// form the map can actually hold.
//
// A real value, 587, rather than a round number, so a zero cannot be mistaken
// for a default that happened to be right.
func TestTheMigratedMailPortSurvivesEveryFormTheFileCanHold(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    any
	}{
		{"float64, which is what encoding/json hands back", float64(587)},
		{"int, which a Go caller building the map in memory writes", 587},
		{"string, which a hand-edited settings.json holds", "587"},
		{"string with space, because an operator edited it", " 587 "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := installChannels(map[string]any{
				"smtpEnabled": true, "smtpHost": "mail.example.net",
				"smtpTo": "ops@example.net", "smtpPort": tc.v,
			}, func(v string) string { return v })
			if len(got) != 1 {
				t.Fatalf("made %d channels, want 1", len(got))
			}
			sc, ok := got[0].cfg.(smtpConfigJSON)
			if !ok {
				t.Fatalf("the email channel is %T", got[0].cfg)
			}
			if sc.Port != 587 {
				t.Errorf("port came through as %d from %T(%v) — a channel on port "+
					"%d connects to nothing, and the page would still show the mail "+
					"server as configured", sc.Port, tc.v, tc.v, sc.Port)
			}
		})
	}

	// AN ABSENT PORT IS STILL ZERO, deliberately. The mailer's own default
	// applies then, and inventing 587 here would hide a settings file that lost
	// the key.
	got := installChannels(map[string]any{
		"smtpEnabled": true, "smtpHost": "mail.example.net", "smtpTo": "ops@example.net",
	}, func(v string) string { return v })
	if sc := got[0].cfg.(smtpConfigJSON); sc.Port != 0 {
		t.Errorf("a missing port was invented as %d", sc.Port)
	}
}
