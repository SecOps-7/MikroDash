package asn

import "strings"

// Name turns a registered organisation name into the one an operator reads on
// a badge.
//
// ── WHY THIS EXISTS AT ALL ─────────────────────────────────────────────────
//
// DB-IP carries the name an AS is REGISTERED to, which is a legal name: "Google
// LLC", "Cloudflare, Inc.", "Amazon.com, Inc.", "Deutsche Telekom AG". On a
// badge beside an IP address the suffix is noise, and in two cases the legal
// name is not the name anyone uses — AS32934 is still filed as "Facebook, Inc."
//
// So: strip the legal form, then apply a SHORT alias table for the handful
// where stripping is not enough. Stripping is mechanical and applies to every
// network on the internet; the alias table is judgement, and is kept small
// enough to read.
func Name(raw string) string {
	n := strip(strings.TrimSpace(raw))
	if a, ok := aliases[n]; ok {
		return a
	}
	if n == "" {
		// A name that is NOTHING BUT a legal form is not improved by returning
		// the empty string, which the caller reads as "no organisation".
		return strings.TrimSpace(raw)
	}
	return n
}

// aliases maps a stripped name to the brand. Keyed AFTER stripping so one entry
// covers every legal variant: "Akamai Technologies, Inc." and "Akamai
// International B.V." both arrive here as two different strings and leave as
// one, which matters because the Connections page FOLDS destinations onto this
// name — two spellings mean two nodes for one company.
var aliases = map[string]string{
	"Facebook":                   "Meta",
	"Amazon.com":                 "Amazon",
	"Akamai Technologies":        "Akamai",
	"Akamai International":       "Akamai",
	"Google Asia Pacific":        "Google",
	"Telegram Messenger":         "Telegram",
	"Netflix Streaming Services": "Netflix",
	"Zoom Video Communications":  "Zoom",
	"Cisco OpenDNS":              "OpenDNS",
	"nextdns":                    "NextDNS",
	"AdGuard Software":           "AdGuard",
}

// suffixes are the legal forms stripped from the end of a name, longest first
// so "Pte. Ltd." is taken whole rather than leaving a dangling "Pte.".
//
// CONSERVATIVE ON PURPOSE. "A/S" and a bare "AS" are Scandinavian company forms
// AND the abbreviation for an autonomous system, and stripping a trailing "AS"
// would mangle any network whose name genuinely ends in it. A suffix that is
// ambiguous is left on: a slightly long badge beats a wrong name.
var suffixes = []string{
	"(Pty) Ltd", "Pte. Ltd.", "Pte Ltd", "Co., Ltd.", "Co. Ltd",
	"Corporation", "Incorporated", "Limited", "Company",
	"S.p.A.", "S.A.S.", "S.A.R.L.", "S.r.l.", "L.L.C.", "N.V.", "B.V.",
	"Inc.", "Inc", "LLC", "LLP", "Ltd.", "Ltd", "PLC", "plc",
	"GmbH", "AG", "SAS", "SARL", "SRL", "PBC", "LP", "BV", "NV", "AB", "OY",
}

// strip removes trailing legal forms and the punctuation that joined them,
// repeatedly: "Google Asia Pacific Pte. Ltd." loses both parts.
//
// THE SUFFIX IS MATCHED BEFORE ANY PUNCTUATION IS TRIMMED, and that order is
// the whole of it. Trimming first turns "Akamai International B.V." into
// "…B.V", which then matches no suffix at all and ships the legal form to the
// badge. Punctuation is only ever trimmed from what a successful match LEFT
// BEHIND.
func strip(n string) string {
	n = strings.TrimSpace(n)
	for {
		rest, ok := trimAnySuffix(n)
		if !ok {
			return n
		}
		n = strings.TrimRight(rest, " ,.")
		if n == "" {
			return ""
		}
	}
}

// trimAnySuffix removes the first matching legal form, longest first so
// "Pte. Ltd." is taken whole rather than leaving a dangling "Pte.".
func trimAnySuffix(n string) (string, bool) {
	for _, suf := range suffixes {
		if rest, ok := trimWord(n, suf); ok {
			return rest, true
		}
	}
	return n, false
}

// trimWord removes suf from the end of n when it stands as its own word, and
// reports whether it did. Case-sensitive: these are proper legal forms, and
// matching "inc" inside a word is how a name loses its ending.
func trimWord(n, suf string) (string, bool) {
	if !strings.HasSuffix(n, suf) {
		return n, false
	}
	rest := n[:len(n)-len(suf)]
	if rest == "" {
		return n, false
	}
	switch rest[len(rest)-1] {
	case ' ', ',', '.':
		return rest, true
	}
	return n, false
}
