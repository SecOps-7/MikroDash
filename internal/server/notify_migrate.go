package server

// Carrying the four fixed transports into channels, once.
//
// ── WHY IT IS NOT OPTIONAL ─────────────────────────────────────────────────
//
// An install with Telegram and SMTP working must keep having them working.
// Channels replace the flat keys as the thing delivery reads, so without this
// step an upgrade silently stops notifying — the worst possible failure for the
// feature whose job is to say when something stopped.
//
// ── PLACEMENT ──────────────────────────────────────────────────────────────
//
// Called from `cmd/mikrodash` once the server exists, not from `db.Open` or
// `store.Open`, for the reason `RenamePageGrants` gives: `cmd/compat` opens a
// production /data through a read-only mount and must not be asked to write.
// `cmd/compat` never builds a Server, so this is on the right side of that line.
// It needs the Server because only the Server can decrypt a stored credential
// and seal a new one.
//
// ── IDEMPOTENT BY TABLE, NOT BY FLAG ───────────────────────────────────────
//
// It runs only when there are no channels at all. Deliberately blunt: an
// operator who has made even one channel has adopted the new model, and
// re-seeding the old keys underneath them would resurrect destinations they may
// have deleted on purpose.

import (
	"log"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/alert"
	"mikrodash/internal/db"
	"mikrodash/internal/notify"
)

// webhookConfig and smtpConfigJSON are the two `config` shapes.
type webhookConfig struct {
	URLs []string `json:"urls"`
}

type smtpConfigJSON struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Secure bool   `json:"secure"`
	User   string `json:"user"`
	Pass   string `json:"pass"`
	From   string `json:"from"`
	To     string `json:"to"`
}

// SeedNotifyChannels converts the install's configured transports, and each
// user's own, into channels. It reports how many it made.
func (s *Server) SeedNotifyChannels() (int, error) {
	if s.auditDB == nil {
		return 0, nil
	}
	have, err := s.auditDB.CountNotifyChannels()
	if err != nil || have > 0 {
		return 0, err
	}
	cfg, err := s.mergedSettings()
	if err != nil {
		return 0, err
	}
	events := alert.DefaultEvents(s.eventGate())
	now := time.Now().UnixMilli()

	made := 0
	for _, c := range installChannels(cfg) {
		if err := s.writeSeeded(db.InstallOwner, c, events, now); err != nil {
			// ONE FAILURE DOES NOT ABANDON THE REST. A channel that cannot be
			// sealed is one lost destination; stopping here would lose all of
			// them, on an upgrade, silently.
			log.Printf("[notify] could not carry %q across: %v", c.name, err)
			continue
		}
		made++
	}

	users, err := s.auditDB.ListUserNotifyConfigs()
	if err != nil {
		log.Printf("[notify] could not read per-user notification settings: %v", err)
		users = nil
	}
	for userID, data := range users {
		for _, c := range userChannels(s, data) {
			if err := s.writeSeeded(userID, c, events, now); err != nil {
				log.Printf("[notify] could not carry %q for user %s: %v", c.name, userID, err)
				continue
			}
			made++
		}
	}
	return made, nil
}

// seeded is one channel about to be written.
type seeded struct {
	name string
	kind string
	cfg  any
}

func (s *Server) writeSeeded(owner string, c seeded, events []string, now int64) error {
	sealed, err := s.sealChannelConfig(c.cfg)
	if err != nil {
		return err
	}
	id, err := newUUID()
	if err != nil {
		return err
	}
	return s.auditDB.UpsertNotifyChannel(db.NotifyChannel{
		ID: id, Owner: owner, Name: c.name, Kind: c.kind, Enabled: 1,
		Config: sealed, Events: jsonList(events), Routers: "[]",
		CreatedAt: now, UpdatedAt: now,
	})
}

// installChannels reads the four flat transports out of the decrypted settings.
//
// EACH IS SKIPPED UNLESS IT IS BOTH ENABLED AND CREDENTIALED, the same pair of
// conditions `notify.Channels` uses to decide a transport is usable. A
// half-filled Telegram section that never sent anything must not become a
// channel that looks configured.
func installChannels(cfg map[string]any) []seeded {
	out := []seeded{}
	str := func(k string) string { v, _ := cfg[k].(string); return strings.TrimSpace(v) }
	on := func(k string) bool { return notify.Truthy(cfg[k]) }

	if on("telegramEnabled") && str("telegramBotToken") != "" && str("telegramChatId") != "" {
		out = append(out, seeded{"Telegram", notify.KindWebhook, webhookConfig{
			URLs: []string{"tgram://" + str("telegramBotToken") + "/" + str("telegramChatId")},
		}})
	}
	if on("pushbulletEnabled") && str("pushbulletApiKey") != "" {
		out = append(out, seeded{"Pushbullet", notify.KindWebhook, webhookConfig{
			URLs: []string{"pbul://" + str("pushbulletApiKey")},
		}})
	}
	if on("ntfyEnabled") && str("ntfyUrl") != "" {
		if u := ntfyURLToScheme(str("ntfyUrl"), str("ntfyToken")); u != "" {
			out = append(out, seeded{"ntfy", notify.KindWebhook, webhookConfig{URLs: []string{u}}})
		}
	}
	if on("smtpEnabled") && str("smtpHost") != "" && str("smtpTo") != "" {
		port, _ := strconv.Atoi(str("smtpPort"))
		out = append(out, seeded{"Email", notify.KindSMTP, smtpConfigJSON{
			Host: str("smtpHost"), Port: port, Secure: on("smtpSecure"),
			User: str("smtpUser"), Pass: str("smtpPass"),
			From: str("smtpFrom"), To: str("smtpTo"),
		}})
	}
	return out
}

// ntfyURLToScheme turns the stored `https://ntfy.sh/topic` into `ntfys://…`.
//
// The old setting held a whole http(s) URL; the scheme form is what `Parse`
// speaks. A URL with no topic yields "" and is skipped, because a channel
// pointed at an ntfy server with no topic delivers nowhere.
func ntfyURLToScheme(raw, token string) string {
	scheme := "ntfy"
	rest := raw
	switch {
	case strings.HasPrefix(raw, "https://"):
		scheme, rest = "ntfys", strings.TrimPrefix(raw, "https://")
	case strings.HasPrefix(raw, "http://"):
		rest = strings.TrimPrefix(raw, "http://")
	}
	rest = strings.Trim(rest, "/")
	if !strings.Contains(rest, "/") {
		return ""
	}
	out := scheme + "://" + rest
	if token != "" {
		out += "?token=" + token
	}
	return out
}

// userChannels reads one user's stored "My Alerts" row.
//
// The per-user store keeps the same four transports under its own keys, already
// allow-listed by `notify.Pick`. Email is special: a user supplies only an
// address and the install's mail server carries it — so a user's email channel
// is seeded from the INSTALL's SMTP settings with the user's address as the
// recipient, which is exactly what `userNotifyTest` does today.
func userChannels(s *Server, data map[string]any) []seeded {
	out := []seeded{}
	str := func(k string) string {
		v, _ := data[k].(string)
		return strings.TrimSpace(s.decryptSetting(v))
	}
	plain := func(k string) string { v, _ := data[k].(string); return strings.TrimSpace(v) }
	on := func(k string) bool { return notify.Truthy(data[k]) }

	if on("telegramEnabled") && str("telegramBotToken") != "" && plain("telegramChatId") != "" {
		out = append(out, seeded{"Telegram", notify.KindWebhook, webhookConfig{
			URLs: []string{"tgram://" + str("telegramBotToken") + "/" + plain("telegramChatId")},
		}})
	}
	if on("pushbulletEnabled") && str("pushbulletApiKey") != "" {
		out = append(out, seeded{"Pushbullet", notify.KindWebhook, webhookConfig{
			URLs: []string{"pbul://" + str("pushbulletApiKey")},
		}})
	}
	if on("ntfyEnabled") && plain("ntfyUrl") != "" {
		if u := ntfyURLToScheme(plain("ntfyUrl"), str("ntfyToken")); u != "" {
			out = append(out, seeded{"ntfy", notify.KindWebhook, webhookConfig{URLs: []string{u}}})
		}
	}
	if on("emailEnabled") && plain("emailTo") != "" {
		if mc, from, ok := s.smtpConfig(); ok {
			out = append(out, seeded{"Email", notify.KindSMTP, smtpConfigJSON{
				Host: mc.Host, Port: mc.Port, Secure: mc.Secure,
				User: mc.User, Pass: mc.Pass, From: from, To: plain("emailTo"),
			}})
		}
	}
	return out
}
