package routeros

import (
	"regexp"
	"strings"
)

// MIKRODASH PATCH (see PATCHES.md): the debug trace masks credentials.
//
// RunArgsContext and ListenArgsQueueContext log every word they send at slog's
// Debug level. A MikroDash install with RouterOS debug logging on therefore wrote
// backup passwords, RouterOS user passwords, Wi-Fi passphrases and WireGuard keys
// to its log in clear text. Code scanning alert #158.
//
// credentialKey is the rule tools/capture-fixtures.js uses to keep credentials out
// of fixtures, so the repository has one answer to "is this key a secret".
// `public-key` and `minimum-password-length` do not match; `preshared-key`,
// WireGuard's spelling, does.
var credentialKey = regexp.MustCompile(`(?i)((passphrase|password|secret|psk)|(private|pre-?shared)-key)$`)

// redactSentence returns the words with every credential attribute's value
// replaced by "***". The key stays, so the trace still says what was set.
func redactSentence(words []string) []string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = w
		if len(w) < 2 || w[0] != '=' {
			continue
		}
		eq := strings.IndexByte(w[1:], '=')
		if eq < 0 {
			continue
		}
		if credentialKey.MatchString(w[1 : 1+eq]) {
			out[i] = w[:2+eq] + "***"
		}
	}
	return out
}
