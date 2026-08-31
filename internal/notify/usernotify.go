package notify

// The per-user notification config's pure decisions — the port of `_pick` and
// `_withInstallMail` in `src/userNotify.js`.
//
// A user chooses WHERE their alerts go. They do not choose WHICH alerts exist:
// that is the install's decision, made once by an administrator, so there are no
// alert-type fields here.

import "strings"

// The field tables. Names mirror the install-wide settings where they overlap,
// so a stored config can be handed straight to `Channels`.
var (
	channelToggles   = []string{"telegramEnabled", "pushbulletEnabled", "ntfyEnabled", "emailEnabled"}
	credentialFields = []string{"telegramBotToken", "pushbulletApiKey", "ntfyToken"}
	strFields        = []string{"telegramChatId", "ntfyUrl", "emailTo"}
)

// Defaults is every allowed key with its zero value. It is also the ALLOWLIST:
// a key absent from it does not survive a read.
func Defaults() Settings {
	d := Settings{}
	for _, k := range channelToggles {
		d[k] = false
	}
	for _, k := range credentialFields {
		d[k] = ""
	}
	for _, k := range strFields {
		d[k] = ""
	}
	return d
}

// Pick applies the allowlist to a stored blob.
//
// ── THIS IS A SECURITY BOUNDARY, NOT TIDYING ────────────────────────────────
//
// The live comment: "Only keys on the allowlist survive a read, so a blob
// written by a newer version (or hand-edited) cannot inject fields into what
// reaches notifier." The blob is a database row and the destination decides
// where to send by inspecting FIELD NAMES — so an injected `smtpHost` would
// point one user's alerts at a server of the attacker's choosing, and an
// injected `smtpTo` would redirect them outright.
//
// `decrypt` is applied ONLY to the credential fields. A non-credential is never
// passed through it, which matters because the decryptor is not a no-op on
// arbitrary text.
func Pick(stored Settings, decrypt func(string) string) Settings {
	out := Defaults()
	if stored == nil {
		return out
	}
	creds := map[string]bool{}
	for _, k := range credentialFields {
		creds[k] = true
	}
	for k, v := range stored {
		if _, allowed := out[k]; !allowed {
			continue // not on the allowlist
		}
		if decrypt != nil && creds[k] {
			out[k] = decrypt(toString(v))
			continue
		}
		out[k] = v
	}
	return out
}

// WithInstallMail folds the install's mail server into a user's opt-in.
//
// Email is the one channel a user does not configure, only OPTS INTO: the server
// is install infrastructure, and asking every user to retype it would be a
// support burden and a way to copy the server's credentials into per-user rows.
// The user supplies the one part genuinely theirs — where to send it.
//
// ── THE RECIPIENT IS THE USER, AND ONLY THE RECIPIENT ───────────────────────
//
// Everything else comes from the install; `smtpTo` comes from the user's
// `emailTo`. A port that copied `smtpTo` across with the rest would send every
// user's alerts to the administrator.
//
// ── A HALF-CONFIGURED INSTALL RESOLVES TO NOTHING ───────────────────────────
//
// If the install has no host or no From, the opt-in yields no channel at all
// rather than an enabled one that cannot deliver — the same cooldown-consuming
// state `Channels` exists to prevent. Nothing about the server is stored per
// user: it is read fresh, so changing it takes effect for everyone at once.
func WithInstallMail(own Settings, install Settings) Settings {
	out := Settings{}
	for k, v := range own {
		out[k] = v
	}
	if !truthy(own["emailEnabled"]) || !truthy(own["emailTo"]) {
		return out
	}
	if !truthy(install["smtpHost"]) || !truthy(install["smtpFrom"]) {
		return out
	}
	out["smtpEnabled"] = true
	// ONLY THE FIELDS THE INSTALL ACTUALLY HAS.
	//
	// The live code assigns each unconditionally, so a missing one becomes
	// `undefined` — and an undefined property is INVISIBLE to `JSON.stringify`,
	// so it never reaches a stored record or a wire payload. A Go map has no
	// undefined: writing `install["smtpPort"]` when the install has no port
	// leaves a PRESENT key holding nil, which a consumer testing presence would
	// read as "configured, blank" rather than "not configured".
	//
	// Copying only what is there reproduces what the original actually emits.
	for _, k := range []string{"smtpHost", "smtpPort", "smtpSecure", "smtpUser", "smtpPass", "smtpFrom"} {
		if v, ok := install[k]; ok {
			out[k] = v
		}
	}
	// The USER's address, not the install's.
	out["smtpTo"] = own["emailTo"]
	return out
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return strings.TrimSpace(jsStringOf(v))
}

func jsStringOf(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		return ""
	}
}
