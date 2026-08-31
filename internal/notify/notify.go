// Package notify holds the notifier's pure decisions — the port of the two
// functions in `src/notifier.js` that can be reached without sending anything.
//
// The transports (Telegram, Pushbullet, SMTP, ntfy) are NOT ported. What is here
// is the part that decides whether there is anywhere to send, and the part that
// turns a failed send into a sentence somebody can act on.
//
// ── WHY "IS ANY CHANNEL ACTIVE?" IS ONE QUESTION AND NOT TWO ────────────────
//
// The live comment records the bug: the alerter asked that from the *Enabled
// flags ALONE while `send()` checked flags PLUS credentials, so a channel ticked
// without a token "consumed the alert cooldown, sent nothing, and logged
// nothing" — a silent failure with a rate limit attached, which is the worst
// combination available.
//
// So `Channels` is the single answer, and `HasConfigured` is derived from it
// rather than written twice. A caller that wants to SEND walks the same list a
// caller that wants to know IF it can send counted.

package notify

import (
	"encoding/json"
	"strings"
)

// Channel names a transport that is enabled AND has what its send path needs.
type Channel string

const (
	Telegram   Channel = "telegram"
	Pushbullet Channel = "pushbullet"
	SMTP       Channel = "smtp"
	Ntfy       Channel = "ntfy"
)

// Settings is the slice of the settings map this needs. Values arrive from
// settings.json, so they are `any` — a checkbox may be a bool or the STRING
// "false", and both must read as the live side reads them.
type Settings map[string]any

// Channels lists the transports that could actually deliver.
//
// The order is the live `send()`'s order, because a caller walking it produces
// errors in the same sequence the original reports them.
func Channels(s Settings) []Channel {
	if s == nil {
		return nil
	}
	var out []Channel
	if truthy(s["telegramEnabled"]) && truthy(s["telegramBotToken"]) && truthy(s["telegramChatId"]) {
		out = append(out, Telegram)
	}
	if truthy(s["pushbulletEnabled"]) && truthy(s["pushbulletApiKey"]) {
		out = append(out, Pushbullet)
	}
	if truthy(s["smtpEnabled"]) && truthy(s["smtpHost"]) && truthy(s["smtpFrom"]) && truthy(s["smtpTo"]) {
		out = append(out, SMTP)
	}
	if truthy(s["ntfyEnabled"]) && truthy(s["ntfyUrl"]) {
		out = append(out, Ntfy)
	}
	return out
}

// HasConfigured reports whether anything could be delivered, derived from
// `Channels` so the two cannot drift apart.
func HasConfigured(s Settings) bool { return len(Channels(s)) > 0 }

// truthy is JavaScript's `!!x` for the shapes settings.json holds.
//
// AN EMPTY STRING IS FALSY, which is the point: a blank token is the same as a
// missing one, and the live conditions rely on it. The STRING "false" is TRUE,
// which is not a mistake here — `!!"false"` is `true` in JavaScript, so a
// checkbox stored as text is enabled either way, and a port that special-cased
// it would refuse to send where the original sends.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	default:
		return v != nil
	}
}

// Reason pulls a human sentence out of an error response body.
//
// Telegram and ntfy both return one; without it a failure reads as a bare status
// code. The result is PREFIXED with " — " so it can be appended to a message,
// and is empty when there is nothing to say.
//
// Kept short deliberately: a long HTML error page would otherwise flood the log
// or the test-notification response.
func Reason(raw string) string {
	if raw == "" {
		return ""
	}
	msg := ""
	// A JSON body names its reason in one of three fields, in this order. A body
	// that is NOT JSON — an HTML error page, say — is used as-is.
	//
	// PARSING SUCCESSFULLY AND FINDING NOTHING IS NOT THE SAME AS FAILING TO
	// PARSE. `JSON.parse('"just a string"')` succeeds and yields a STRING, whose
	// `.description` is undefined — so the live side returns EMPTY rather than
	// falling back to the raw text. Decoding straight into a map conflates the
	// two, because a JSON string fails to unmarshal into one, and the fallback
	// then quotes the body back at the operator. The corpus caught exactly that.
	// THE `try` COVERS THE PROPERTY ACCESS AS WELL AS THE PARSE, which is what
	// makes the three outcomes below different from each other:
	//
	//   not JSON            -> parse throws          -> the raw text
	//   JSON `null`         -> `null.description` THROWS -> the raw text
	//   JSON string/number  -> `.description` is undefined, no throw -> EMPTY
	//   JSON object         -> the first field that has a value
	//
	// A port that decoded into a map would put `null` and a JSON string in the
	// same bucket, and both in the wrong one. The corpus carries all four.
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil || doc == nil {
		msg = strings.TrimSpace(raw)
	} else if obj, ok := doc.(map[string]any); ok {
		for _, k := range []string{"description", "error", "message"} {
			if s, ok := obj[k].(string); ok && s != "" {
				msg = s
				break
			}
		}
	}
	if msg == "" {
		return ""
	}
	// Collapse every run of whitespace, THEN cut. Cutting first would leave a
	// newline inside the 160 characters.
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 160 {
		msg = msg[:160]
	}
	return " — " + msg
}
