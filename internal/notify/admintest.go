package notify

import "strconv"

// MergeForAdminTest is the credential merge behind
// `POST /api/settings/test-notification`.
//
// ── TWO GUARDS, AND THEY ARE NOT INTERCHANGEABLE ───────────────────────────
//
// The live route writes fourteen spread guards and uses two different tests:
//
//	`botToken && {...}`               a FALSY value does not override, so an
//	                                  empty field falls back to what is stored.
//	                                  That is what makes "test without saving"
//	                                  work on a form the operator has not filled.
//	`smtpPort !== undefined && {...}` an explicitly-sent value overrides EVEN
//	                                  WHEN FALSY.
//
// So `botToken: ""` keeps the stored token while `smtpSecure: false` really does
// turn TLS off for the test. A port using one guard for all fourteen would be
// wrong on half of them and would fail nothing.
//
// ── AND THE TWO ODD COERCIONS ──────────────────────────────────────────────
//
//	`parseInt(port, 10) || 587`   makes 0, "abc" and null ALL become 587. Zero
//	                              is not a usable port, so this is right, but it
//	                              means a port of 0 is silently 587 rather than
//	                              refused.
//	`x === true || x === 'true'`  makes "yes" and 1 both FALSE. Anything other
//	                              than the boolean or that exact string is off.
//
// Every cap differs by field — 512 for secrets, 256 for addresses — and is taken
// from `testdata/test-notif-cases.json`, which runs the live expression.
func MergeForAdminTest(body map[string]any, stored Settings) Settings {
	out := Settings{}
	for k, v := range stored {
		out[k] = v
	}

	// The truthy-guarded fields: key in the body, key in settings, cap.
	for _, f := range []struct {
		from, to string
		cap      int
	}{
		{"botToken", "telegramBotToken", 512},
		{"chatId", "telegramChatId", 256},
		{"apiKey", "pushbulletApiKey", 512},
		{"smtpHost", "smtpHost", 256},
		{"smtpFrom", "smtpFrom", 256},
		{"smtpTo", "smtpTo", 256},
		{"smtpUser", "smtpUser", 256},
		{"smtpPass", "smtpPass", 512},
		{"ntfyUrl", "ntfyUrl", 512},
		{"ntfyToken", "ntfyToken", 512},
	} {
		// `capRunes` is the package's own, shared with the save path. It cuts on
		// a RUNE boundary where JavaScript's `slice` cuts UTF-16 code units; the
		// two differ only outside the BMP, which these fields — tokens, hostnames
		// and email addresses — cannot reach. Cutting mid-rune would put invalid
		// UTF-8 into a settings map, so the difference is in the safe direction.
		if v, ok := body[f.from]; ok && jsTruthy(v) {
			out[f.to] = capRunes(jsString(v), f.cap)
		}
	}

	// PRESENCE-guarded, not truthiness-guarded. `!== undefined` in JavaScript is
	// "the key was sent"; a JSON null IS sent, so it reaches the coercion below
	// and becomes 587 rather than falling back to the stored port.
	if v, ok := body["smtpPort"]; ok {
		out["smtpPort"] = jsPort(v)
	}
	if v, ok := body["smtpSecure"]; ok {
		out["smtpSecure"] = v == true || v == "true"
	}
	return out
}

// jsTruthy is JavaScript's `!!v` for the shapes a JSON body carries. "" and 0
// and false and null are all falsy; "0" and "false" are not.
func jsTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case bool:
		return t
	case float64:
		return t != 0
	default:
		return true
	}
}

// jsString is `String(v)` for the same shapes — a number in a string field is
// stringified rather than dropped, which is what the live route does.
func jsString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return "null"
	default:
		return ""
	}
}

// jsPort is `parseInt(v, 10) || 587`.
//
// `parseInt` reads a LEADING number and ignores the rest, so "2525abc" is 2525.
// Anything with no leading digits is NaN, and NaN is falsy, so it lands on 587 —
// as do 0 and null.
func jsPort(v any) float64 {
	var n float64
	switch t := v.(type) {
	case float64:
		n = float64(int64(t)) // parseInt truncates toward zero
	case string:
		n = parseIntPrefix(t)
	default:
		n = 0
	}
	if n == 0 {
		return 587
	}
	return n
}

func parseIntPrefix(s string) float64 {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	start := i
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		i++
	}
	digits := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == digits {
		return 0 // NaN, and NaN is falsy
	}
	n, err := strconv.ParseFloat(s[start:i], 64)
	if err != nil {
		return 0
	}
	return n
}
