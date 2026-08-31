package notify

import (
	"errors"
	"strings"

	"mikrodash/internal/jsval"
)

// MaxStr and MaxCred bound what a user may store.
const (
	MaxStr  = 256
	MaxCred = 512
)

// ErrBadAddress is the live validation's message, verbatim: it is what the
// operator reads.
var ErrBadAddress = errors.New("That does not look like an email address")

// Public is the browser-facing shape of a user's channels.
//
// CREDENTIALS COME BACK AS A MASK WHEN SET AND ” WHEN NOT — never the stored
// value, and never the ciphertext. That is also what makes the form's "leave
// blank to keep current" handling work without a special case: the field the
// browser shows is the mask, and sending it back means "unchanged".
func Public(stored Settings) Settings {
	out := Settings{}
	for _, k := range channelToggles {
		out[k] = stored[k] == true
	}
	for _, k := range strFields {
		out[k] = jsval.String(stored[k])
	}
	for _, k := range credentialFields {
		if jsval.String(stored[k]) != "" {
			out[k] = Mask
		} else {
			out[k] = ""
		}
	}
	return out
}

// Merge applies an update to a user's stored channels.
//
// IT MERGES OVER THE STORED BLOB, and that is the design rather than an
// optimisation: a credential the caller did not send keeps its existing
// ciphertext, and is never decrypted and re-encrypted just to survive an edit to
// an unrelated field.
//
// ── THE RULES DIFFER FROM MergeForTest, DELIBERATELY ────────────────────────
//
//	                test merge                  save merge
//	guard           truthy value                key PRESENT
//	so an empty     keeps the stored value      CLEARS the stored value
//	trimming        none                        on the string fields
//
// Both are right. A test with a blank field should verify what is stored; a save
// with a blank field is how a channel is switched off, and keeping the stored
// value there would make an address impossible to remove.
//
// `encrypt` is applied only to a credential that actually changed. A masked
// value means "unchanged" and is skipped — taken literally it would store eight
// bullet characters as somebody's bot token.
func Merge(updates map[string]any, stored Settings, encrypt func(string) (string, error)) (Settings, error) {
	next := Settings{}
	for k, v := range stored {
		next[k] = v
	}

	for _, k := range channelToggles {
		if v, present := updates[k]; present {
			// `v === true || v === 'true'` — NOT truthiness. The string "yes" and
			// the number 1 are both false here, which matters because a form
			// posting either would otherwise switch a channel on by accident.
			next[k] = v == true || (isStringOrBool(v) && jsval.String(v) == "true")
		}
	}
	for _, k := range strFields {
		if v, present := updates[k]; present {
			next[k] = capRunes(strings.TrimSpace(jsval.String(v)), MaxStr)
		}
	}

	// CHECKED ON THE MERGED VALUE, so an edit to an unrelated field on a config
	// that already holds a bad address fails too. That is the live behaviour and
	// arguably the useful one: a typo fails at the moment it is made rather than
	// silently never arriving.
	if to := jsval.String(next["emailTo"]); to != "" && !strings.Contains(to, "@") {
		return nil, ErrBadAddress
	}

	for _, k := range credentialFields {
		v, present := updates[k]
		if !present {
			continue
		}
		if jsval.String(v) == Mask {
			continue // unchanged — keep the stored ciphertext
		}
		// NOT trimmed: only the string fields are. A credential with a leading
		// space is a credential with a leading space, and silently changing it
		// would fail authentication in a way nobody could explain.
		s := capRunes(jsval.String(v), MaxCred)
		if s == "" {
			next[k] = "" // empty clears the credential
			continue
		}
		if encrypt == nil {
			return nil, errors.New("notify: no encryptor configured")
		}
		enc, err := encrypt(s)
		if err != nil {
			return nil, err
		}
		next[k] = enc
	}
	return next, nil
}

// isStringOrBool keeps `=== 'true'` from matching a number, or an object whose
// String() happens to be "true".
func isStringOrBool(v any) bool {
	switch v.(type) {
	case string, bool:
		return true
	}
	return false
}

// capRunes truncates by RUNES, so a multi-byte value is never cut mid-character.
func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
