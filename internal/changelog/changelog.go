// Package changelog fetches RouterOS release notes for the Update dialog.
//
// ── THE ROUTER DOES NOT HAVE THESE ──────────────────────────────────────────
//
// The live header, checked against live hardware rather than assumed:
// `/system/package/update/print` returns exactly channel, mode,
// check-certificate, ip-version, installed-version, latest-version and status,
// and there is no `changelog` node anywhere under it. WinBox's "Check for
// Updates" window fetches the text over HTTP; it does not read it off the
// device.
//
// ── THE SECOND OUTBOUND DESTINATION IN THE WHOLE SERVER ─────────────────────
//
// `internal/notify` is the only other one, and it only ever talks to channels
// the operator configured. A deliberate departure, and the reason everything
// here fails soft: an install with no route to the internet must still be able
// to upgrade its routers, so a failure renders "unavailable" in a box and
// changes nothing else.
package changelog

import (
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const host = "upgrade.mikrotik.com"

// ValidVersion is the live `VERSION_RE`, and its header calls it "the single
// most important line in this file".
//
// `version` arrives from a SOCKET PAYLOAD and is interpolated into a URL PATH.
// Without an anchored whitelist that is a path traversal (`../../`) and an
// open-redirect-shaped fetch (`//evil.example.com/`) in one. Digits and dots
// only, anchored at both ends.
//
// Go's `^` and `$` match at LINE boundaries under the `m` flag and at string
// boundaries without it — this has no `m`, so `$` is the end of the string and
// an embedded newline cannot slip a second line past. JavaScript's is the same
// without `m`. Stated because it is the one way this could differ silently.
var ValidVersion = regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`)

// A feature-release changelog is ~38 KB; a patch is under 2 KB.
const maxBytes = 256 * 1024

const timeout = 8 * time.Second

// Released changelogs are immutable, so a hit never needs the network again.
//
// BOUNDED because the key comes from a caller: without the cap, a socket
// spamming distinct valid-looking versions grows this until the process dies.
// Entries are small, so a plain FIFO trim is enough.
const cacheMax = 32

// A failure is cached too, briefly. An isolated install would otherwise pay the
// full timeout every time the dialog is opened, which reads as the dialog being
// broken rather than the lookup being unavailable.
const negTTL = 60 * time.Second

type entry struct {
	notes string
	err   string
	at    time.Time
}

// Client fetches and remembers.
//
// A struct rather than package state, unlike the live module: the live one is a
// singleton because Node has one process per install, and this port's tests want
// a fresh cache without a `_resetCache` seam.
type Client struct {
	mu sync.Mutex
	// order is the FIFO the trim walks. A Go map has no insertion order, where
	// the live `Map` does — so the order the trim needs is kept explicitly
	// rather than borrowed from iteration.
	order []string
	cache map[string]entry

	// HTTP is the client used; nil means http.DefaultClient with the timeout.
	HTTP *http.Client
	// Now is the clock, injectable so the negative TTL can be tested without
	// waiting a minute.
	Now func() time.Time
}

func New() *Client { return &Client{cache: map[string]entry{}} }

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) remember(version string, e entry) {
	if c.cache == nil {
		c.cache = map[string]entry{}
	}
	if _, seen := c.cache[version]; !seen {
		if len(c.order) >= cacheMax {
			delete(c.cache, c.order[0])
			c.order = c.order[1:]
		}
		c.order = append(c.order, version)
	}
	c.cache[version] = e
}

// ErrBadVersion is what a version failing the whitelist produces. Named because
// the caller must NOT report it on the upgrade channel — see the live comment on
// the socket handler: that channel renders `denied` as "You do not have
// permission to update this router", which would be false and alarming for
// someone who can update perfectly well and merely cannot be shown a changelog.
var ErrBadVersion = errors.New("bad version")

// Notes fetches the release notes for a RouterOS version.
func (c *Client) Notes(version string) (string, error) {
	v := strings.TrimSpace(version)
	if !ValidVersion.MatchString(v) {
		return "", ErrBadVersion
	}

	c.mu.Lock()
	hit, ok := c.cache[v]
	c.mu.Unlock()
	if ok && hit.notes != "" {
		return hit.notes, nil
	}
	if ok && hit.err != "" && c.now().Sub(hit.at) < negTTL {
		return "", errors.New(hit.err)
	}

	notes, err := c.fetch(v)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.remember(v, entry{err: err.Error(), at: c.now()})
		return "", err
	}
	c.remember(v, entry{notes: notes})
	return notes, nil
}

func (c *Client) fetch(v string) (string, error) {
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	req, err := http.NewRequest(http.MethodGet, "https://"+host+"/routeros/"+v+"/CHANGELOG", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain")
	res, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		// Drained rather than left half-read holding the connection open.
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxBytes))
		return "", errors.New("HTTP " + itoa(res.StatusCode))
	}

	// THE CAP IS ENFORCED WHILE READING, not after buffering — "a check at the
	// end is not a cap, it is a report on how much was already accepted". One
	// byte past the limit is read so the difference between "exactly at" and
	// "over" is observable.
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return "", errors.New("read failed")
	}
	if len(body) > maxBytes {
		return "", errors.New("too large")
	}
	notes := strings.TrimSpace(string(body))
	if notes == "" {
		return "", errors.New("empty")
	}
	return notes, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
