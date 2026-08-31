package notify

import (
	"fmt"

	"mikrodash/internal/jsval"
)

// Mask is what a stored credential is rendered back as.
//
// IT MUST NEVER TRAVEL. If a form's value were taken literally, pressing Test
// without retyping would send eight bullet characters to Telegram as a bot
// token — and the failure would read as a bad token rather than as a bug.
const Mask = "••••••••"

// The caps on what a user may make the server send.
//
// This is the one place an ORDINARY ACCOUNT chooses what the server connects to
// — the live comment on the install-wide switch says enabling per-user channels
// "widens what an ordinary account can make the server connect to" — so the
// input is bounded before it becomes a request.
const (
	CredentialCap = 512
	StringCap     = 256
)

// The fields a user may set, split by how far they are trusted.
var (
	CredentialFields = []string{"telegramBotToken", "pushbulletApiKey", "ntfyToken"}
	StrFields        = []string{"telegramChatId", "ntfyUrl", "emailTo"}
)

// enableKeyFor maps the name a USER sees to the flag the sender reads.
//
// "email" rather than "smtp" throughout the per-user surface: a user never sees
// a mail server, and the install's is not theirs to know about.
var enableKeyFor = map[string]string{
	"telegram": "telegramEnabled", "pushbullet": "pushbulletEnabled",
	"ntfy": "ntfyEnabled", "email": "emailEnabled",
}

// MergeForTest builds the settings one channel test should run against.
//
// The form is merged over what is stored so a token can be verified without
// being committed first. Three rules make that safe:
//
//   - A MASKED value means "use the stored one".
//   - An EMPTY value means that too, so clearing a field in the form does not
//     silently test against nothing.
//   - Everything else is length-capped.
//
// The channel is then FORCE-ENABLED: testing before ticking the box would
// otherwise report "not configured" rather than the truth. Only that channel —
// a test of one must not enable another.
func MergeForTest(body map[string]any, stored Settings, channel string) (Settings, error) {
	enableKey, ok := enableKeyFor[channel]
	if !ok {
		return nil, fmt.Errorf("Unknown channel")
	}

	out := Settings{}
	for k, v := range stored {
		out[k] = v
	}
	for _, f := range CredentialFields {
		if v, ok := typedValue(body[f], CredentialCap); ok {
			out[f] = v
		}
	}
	for _, f := range StrFields {
		if v, ok := typedValue(body[f], StringCap); ok {
			out[f] = v
		}
	}
	out[enableKey] = true
	return out, nil
}

// typedValue reports whether a form field overrides the stored value, and what
// it becomes.
//
// THE GUARD IS JAVASCRIPT TRUTHINESS, not "is a non-empty string". The live test
// is `if (body[f] && !isMasked(body[f]))`, and a JSON body is not typed: a chat
// id entered without quotes arrives as a NUMBER, which is truthy and which
// `String(...)` turns into its digits. `jsStringOf` returns "" for anything that
// is not a string, so using it here dropped exactly that value — and a user
// whose chat id is numeric would have tested against their stored one while the
// form showed the new one.
//
// Falsy values are skipped, which covers 0, false, null and the empty string
// alike. A chat id of 0 is not a real chat id, and the live route would skip it
// too.
func typedValue(v any, cap int) (string, bool) {
	if !jsval.Truthy(v) {
		return "", false
	}
	s := jsval.String(v)
	if s == "" || s == Mask {
		return "", false
	}
	// BY RUNES, not bytes. The cap exists to bound what the server will send, and
	// `slice` in JavaScript counts UTF-16 code units — for the ASCII tokens these
	// fields hold the two agree, and for anything else truncating mid-rune would
	// produce a value neither side meant.
	r := []rune(s)
	if len(r) > cap {
		return string(r[:cap]), true
	}
	return s, true
}

// EnableKeyFor exposes the channel-to-flag mapping for a caller that has already
// validated the channel name.
func EnableKeyFor(channel string) (string, bool) {
	k, ok := enableKeyFor[channel]
	return k, ok
}
