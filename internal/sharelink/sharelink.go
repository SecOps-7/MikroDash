// Package sharelink holds the rules of share links that need no I/O: the
// token and its hash, the view ticket, what a link may show, its options, its
// limits and whether it is still valid. internal/server applies them; nothing
// here touches a database, a router or a request.
//
// ── A SHARE IS NOT A SESSION ────────────────────────────────────────────────
//
// A share link gives somebody with no account a read-only view of one page
// (or a slideshow, a Dashboard card, a sensor feed) on routers an
// administrator chose. Nothing in this package produces anything a session
// check would accept: the token opens the share endpoints and nothing else.
// The design is the #165 share-links plan and the operator's rulings of
// 2026-11-20 recorded there.
package sharelink

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ── Kinds ───────────────────────────────────────────────────────────────────

// Kind is what a link shows.
type Kind string

const (
	KindPage      Kind = "page"
	KindSlideshow Kind = "slideshow"
	KindCard      Kind = "card"
	KindFeed      Kind = "feed"
)

// Valid reports whether k is one of the four kinds.
func (k Kind) Valid() bool {
	return k == KindPage || k == KindSlideshow || k == KindCard || k == KindFeed
}

// Viewable kinds open a page in a browser; a feed answers JSON.
func (k Kind) Viewable() bool { return k == KindPage || k == KindSlideshow || k == KindCard }

// ── Limits ──────────────────────────────────────────────────────────────────

const (
	// DefaultViewerCap is the operator's ruling of 2026-11-20.
	DefaultViewerCap = 5
	// MaxViewerCap is the highest cap an administrator may set.
	//
	// OPEN QUESTION FOR THE OPERATOR: 25 was proposed and has not been
	// decided. It is one constant so the answer is one edit.
	MaxViewerCap = 25

	// Slideshow limits (the operator's ruling of 2026-11-20).
	MinSlides        = 2
	MaxSlides        = 20
	MinSlideSecs     = 10
	MaxSlideSecs     = 3600
	DefaultSlideSecs = 30
	// MaxWarmUpSecs bounds the warm-up option (off, 0, by default).
	MaxWarmUpSecs = 10

	// TicketTTL is how long a view ticket lasts before it must be renewed.
	TicketTTL = 10 * time.Minute

	// MaxLabel bounds a link's label.
	MaxLabel = 80
	// MaxListEntries bounds the address and origin lists.
	MaxListEntries = 32
	// FeedPollsPerMinute is a sensor feed's poll limit per link.
	FeedPollsPerMinute = 6
)

// ── The token ───────────────────────────────────────────────────────────────

// NewToken returns a fresh token (32 random bytes, base64url, 43 characters)
// and the hash that is the only thing stored.
func NewToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken is SHA-256 of the token. The token has 256 bits of entropy, so a
// slow hash would add nothing, and the lookup runs on every share request.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// WellFormedToken reports whether s could be a token, so a malformed one is
// refused before any lookup.
func WellFormedToken(s string) bool {
	if len(s) != 43 {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(b) == 32
}

// SameHash compares two token hashes in constant time.
func SameHash(a, b []byte) bool { return len(a) == len(b) && subtle.ConstantTimeCompare(a, b) == 1 }

// ── The view ticket ─────────────────────────────────────────────────────────
//
// A ticket proves only WHICH link a share view was opened through, for a short
// time. It never short-cuts a check: every request still re-checks the link.
// Format: base64url("<linkID>|<unix expiry>") + "." + base64url(HMAC-SHA256).

// SignTicket returns a ticket for linkID valid until exp.
func SignTicket(key []byte, linkID string, exp time.Time) string {
	body := linkID + "|" + strconv.FormatInt(exp.Unix(), 10)
	m := hmac.New(sha256.New, key)
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString([]byte(body)) + "." +
		base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// VerifyTicket returns the link id a ticket names, if its signature is good and
// it has not lapsed.
func VerifyTicket(key []byte, ticket string, now time.Time) (linkID string, exp time.Time, ok bool) {
	if len(key) == 0 || len(ticket) > 512 {
		return "", time.Time{}, false
	}
	bodyB64, sigB64, found := strings.Cut(ticket, ".")
	if !found {
		return "", time.Time{}, false
	}
	body, err1 := base64.RawURLEncoding.DecodeString(bodyB64)
	sig, err2 := base64.RawURLEncoding.DecodeString(sigB64)
	if err1 != nil || err2 != nil {
		return "", time.Time{}, false
	}
	m := hmac.New(sha256.New, key)
	m.Write(body)
	if !hmac.Equal(sig, m.Sum(nil)) {
		return "", time.Time{}, false
	}
	id, expS, found := strings.Cut(string(body), "|")
	if !found || id == "" {
		return "", time.Time{}, false
	}
	unix, err := strconv.ParseInt(expS, 10, 64)
	if err != nil {
		return "", time.Time{}, false
	}
	exp = time.Unix(unix, 0)
	if !now.Before(exp) {
		return "", time.Time{}, false
	}
	return id, exp, true
}

// ── Embedding origins ───────────────────────────────────────────────────────
//
// An entry is an exact origin (http or https, a host, an optional port, no
// path) or a wildcard origin within the operator's limits of 2026-11-20:
// https only; one `*` label at the far left only; at least two labels after
// it; no bare `*`; no scheme or port wildcards (a port may be given exactly).

var errOrigin = errors.New("not an origin")

// NormalizeOrigin validates one embedding origin and returns it in the form
// it is sent in frame-ancestors (lower-case scheme and host, no trailing
// slash), or an error naming what is wrong.
func NormalizeOrigin(entry string) (string, error) {
	e := strings.TrimSpace(entry)
	if e == "" || e == "*" {
		return "", fmt.Errorf("%q: a bare wildcard is not allowed", entry)
	}
	scheme, rest, found := strings.Cut(e, "://")
	if !found {
		return "", fmt.Errorf("%q: give the scheme, for example https://ha.example.com", entry)
	}
	scheme = strings.ToLower(scheme)
	if strings.Contains(scheme, "*") {
		return "", fmt.Errorf("%q: the scheme cannot be a wildcard", entry)
	}
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("%q: only http and https origins", entry)
	}
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" || strings.ContainsAny(rest, "/?#@ \t") {
		return "", fmt.Errorf("%q: an origin has no path, query or user part", entry)
	}
	host, port := rest, ""
	if strings.HasPrefix(rest, "[") {
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return "", fmt.Errorf("%q: %w", entry, errOrigin)
		}
		tail := rest[end+1:]
		if tail != "" && !strings.HasPrefix(tail, ":") {
			return "", fmt.Errorf("%q: %w", entry, errOrigin)
		}
		host, port = rest[:end+1], strings.TrimPrefix(tail, ":")
		if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err != nil {
			return "", fmt.Errorf("%q: %w", entry, errOrigin)
		}
	} else if i := strings.LastIndexByte(rest, ':'); i >= 0 {
		host, port = rest[:i], rest[i+1:]
	}
	if port != "" || strings.HasSuffix(rest, ":") {
		if port == "*" {
			return "", fmt.Errorf("%q: the port cannot be a wildcard; give it exactly", entry)
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("%q: %q is not a port", entry, port)
		}
		port = strconv.Itoa(n)
	}
	host = strings.ToLower(host)
	switch {
	case strings.Contains(host, "*"):
		if scheme != "https" {
			return "", fmt.Errorf("%q: a wildcard origin must be https", entry)
		}
		if !strings.HasPrefix(host, "*.") || strings.Count(host, "*") != 1 {
			return "", fmt.Errorf("%q: one wildcard label, at the far left only (https://*.example.com)", entry)
		}
		after := strings.TrimPrefix(host, "*.")
		if !strings.Contains(after, ".") {
			return "", fmt.Errorf("%q: too broad; the part after the wildcard needs at least two labels", entry)
		}
		if !validHostname(after) {
			return "", fmt.Errorf("%q: %w", entry, errOrigin)
		}
	case strings.HasPrefix(host, "["):
		// An IPv6 literal, checked above.
	case validHostname(host):
	default:
		if _, err := netip.ParseAddr(host); err != nil {
			return "", fmt.Errorf("%q: %w", entry, errOrigin)
		}
	}
	out := scheme + "://" + host
	if port != "" {
		out += ":" + port
	}
	return out, nil
}

func validHostname(h string) bool {
	if h == "" || len(h) > 253 || strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// IsWildcardOrigin reports whether a normalised origin holds a wildcard.
func IsWildcardOrigin(o string) bool { return strings.Contains(o, "*") }

// NormalizeOrigins validates a list, dropping blanks and duplicates.
func NormalizeOrigins(entries []string) ([]string, error) {
	if len(entries) > MaxListEntries {
		return nil, fmt.Errorf("at most %d embedding origins", MaxListEntries)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, e := range entries {
		if strings.TrimSpace(e) == "" {
			continue
		}
		o, err := NormalizeOrigin(e)
		if err != nil {
			return nil, err
		}
		if !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	return out, nil
}

// FrameAncestors is the value of the CSP directive for a link: 'none' when no
// origin is allowed, otherwise the origins, each re-validated so a value
// written straight into the database cannot widen it.
func FrameAncestors(origins []string) string {
	var ok []string
	for _, o := range origins {
		if n, err := NormalizeOrigin(o); err == nil {
			ok = append(ok, n)
		}
	}
	if len(ok) == 0 {
		return "frame-ancestors 'none'"
	}
	return "frame-ancestors " + strings.Join(ok, " ")
}

// ── IP allow-list ───────────────────────────────────────────────────────────

// NormalizeAllowList validates addresses and CIDR ranges; a bare address
// becomes a /32 or /128. /0 is refused (it would allow everything while
// looking like a restriction).
func NormalizeAllowList(entries []string) ([]string, error) {
	if len(entries) > MaxListEntries {
		return nil, fmt.Errorf("at most %d allowed addresses", MaxListEntries)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		p, err := parsePrefix(e)
		if err != nil {
			return nil, fmt.Errorf("%q is not an address or a range", e)
		}
		if p.Bits() == 0 {
			return nil, fmt.Errorf("%q would allow every address; leave the list empty instead", e)
		}
		s := p.String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		a := p.Addr()
		bits := p.Bits()
		if a.Is4In6() {
			a = a.Unmap()
			bits -= 96
			if bits < 0 {
				return netip.Prefix{}, errors.New("bad prefix")
			}
		}
		return netip.PrefixFrom(a, bits).Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// AddressAllowed reports whether a client address is on the list. An empty
// list allows every address; an unparsable address is never allowed by a
// non-empty list.
func AddressAllowed(list []string, client string) bool {
	if len(list) == 0 {
		return true
	}
	a, err := netip.ParseAddr(strings.TrimSpace(client))
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, e := range list {
		p, err := parsePrefix(e)
		if err == nil && p.Contains(a) {
			return true
		}
	}
	return false
}

// ── Link address ────────────────────────────────────────────────────────────

// NormalizeBase validates the address viewers use to reach MikroDash (the
// form's "link address"): http or https, a host, an optional port, no path.
func NormalizeBase(s string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("%q: give an address like https://mikrodash.example.com", s)
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

// MixedContent reports the warning case: an https site cannot embed an http
// link, so an https embedding origin with an http link address will not work.
func MixedContent(base string, origins []string) bool {
	if !strings.HasPrefix(base, "http://") {
		return false
	}
	for _, o := range origins {
		if strings.HasPrefix(o, "https://") {
			return true
		}
	}
	return false
}
