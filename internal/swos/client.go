package swos

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/netguard"
)

// Timeout bounds one request: the switch's own page waits about as long.
const Timeout = 15 * time.Second

// maxBody caps a reply. The largest real page is about 5 KB; a reply is
// untrusted, and one that never ends must not fill memory.
const maxBody = 1 << 20

// ErrAuth is a switch refusing the login.
var ErrAuth = errors.New("swos: the switch refused the username or password")

// ErrNotFound is a switch, or something else, without the page asked for.
var ErrNotFound = errors.New("swos: no such page")

// pageName is the only thing a caller may ask for: a `.b` page, nothing with a
// slash, a query or a dot elsewhere. The client can therefore never be pointed
// at an arbitrary path on the switch, or anywhere else.
var pageName = regexp.MustCompile(`^!?[a-z0-9]+\.b$`)

// Client reads one switch.
//
// ── GET ONLY, BY CONSTRUCTION ──────────────────────────────────────────────
//
// There is no method that sends a body or any verb but GET. A write is a
// whole-blob POST that the firmware applies without answering, and the login
// it would use is the switch's only admin account; adding one is a separate,
// guarded change, not an option on this type.
//
// ONE REQUEST IN FLIGHT. The switch serves its own page one request at a
// time, and the digest nonce count must go up in the order requests are sent.
type Client struct {
	base       string // scheme://host:port
	user, pass string
	http       *http.Client

	mu    sync.Mutex
	chal  *challenge
	count uint32
}

type challenge struct {
	realm, nonce, opaque, algorithm string
	qopAuth                         bool
}

// New is a client for one switch. scheme is "http" or "https"; reach is how
// far the connection may go, the same rule a router connection follows
// (routeros.DialReach), judged after DNS.
func New(scheme, host string, port int, user, pass string, reach netguard.Reach) (*Client, error) {
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("swos: scheme %q is neither http nor https", scheme)
	}
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, "/?#@ ") {
		return nil, fmt.Errorf("swos: %q is not a host", host)
	}
	if port <= 0 || port > 65535 {
		if scheme == "https" {
			port = 443
		} else {
			port = 80
		}
	}
	dialer := &net.Dialer{Timeout: Timeout, Control: netguard.Control(reach)}
	tr := &http.Transport{
		Proxy:                  nil, // a proxy would be checked instead of the switch
		DialContext:            dialer.DialContext,
		TLSHandshakeTimeout:    10 * time.Second,
		MaxConnsPerHost:        1,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 16 << 10,
	}
	return &Client{
		base: scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)),
		user: user, pass: pass,
		http: &http.Client{
			Transport: tr,
			Timeout:   Timeout,
			// A redirect would carry the next request somewhere this client
			// did not check.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Get reads and decodes one page.
func (c *Client) Get(ctx context.Context, page string) (Value, error) {
	b, err := c.GetRaw(ctx, page)
	if err != nil {
		return Value{}, err
	}
	return Parse(b)
}

// GetRaw reads one page as the switch sent it.
func (c *Client) GetRaw(ctx context.Context, page string) ([]byte, error) {
	if !pageName.MatchString(page) {
		return nil, fmt.Errorf("swos: %q is not a page", page)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	path := "/" + page
	// Twice at most: the first answer may be the 401 carrying the challenge,
	// or a stale nonce.
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
		if err != nil {
			return nil, err
		}
		answered := c.chal != nil
		if answered {
			c.count++
			req.Header.Set("Authorization", c.authorization(path))
		}
		res, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		body, rerr := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
		res.Body.Close()
		if rerr != nil {
			return nil, rerr
		}
		switch {
		case res.StatusCode == http.StatusUnauthorized:
			h := res.Header.Get("WWW-Authenticate")
			ch := parseChallenge(h)
			// A second refusal of an answer is a wrong login, unless the
			// switch says only that the nonce went stale.
			if ch == nil || answered && attempt > 0 && !strings.Contains(strings.ToLower(h), "stale=true") {
				return nil, ErrAuth
			}
			c.chal, c.count = ch, 0
			continue
		case res.StatusCode == http.StatusNotFound:
			return nil, ErrNotFound
		case res.StatusCode < 200 || res.StatusCode > 299:
			return nil, fmt.Errorf("swos: %s answered HTTP %d", page, res.StatusCode)
		}
		if len(body) > maxBody {
			return nil, fmt.Errorf("swos: %s is larger than %d bytes", page, maxBody)
		}
		return body, nil
	}
	return nil, ErrAuth
}

// authorization is the RFC 2617 Digest header for one request. With qop=auth
// the nonce count goes up on every request, which the switch checks.
func (c *Client) authorization(path string) string {
	ch := c.chal
	ha1 := md5hex(c.user + ":" + ch.realm + ":" + c.pass)
	ha2 := md5hex("GET:" + path)
	parts := []string{
		`username="` + quote(c.user) + `"`,
		`realm="` + quote(ch.realm) + `"`,
		`nonce="` + quote(ch.nonce) + `"`,
		`uri="` + path + `"`,
	}
	if ch.qopAuth {
		nc := fmt.Sprintf("%08x", c.count)
		cnonce := newCnonce()
		parts = append(parts,
			`response="`+md5hex(ha1+":"+ch.nonce+":"+nc+":"+cnonce+":auth:"+ha2)+`"`,
			"qop=auth", "nc="+nc, `cnonce="`+cnonce+`"`)
	} else {
		parts = append(parts, `response="`+md5hex(ha1+":"+ch.nonce+":"+ha2)+`"`)
	}
	if ch.algorithm != "" {
		parts = append(parts, "algorithm="+ch.algorithm)
	}
	if ch.opaque != "" {
		parts = append(parts, `opaque="`+quote(ch.opaque)+`"`)
	}
	return "Digest " + strings.Join(parts, ", ")
}

// newCnonce is the client nonce; crypto/rand, because a predictable one
// weakens the digest.
func newCnonce() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// quote escapes a value for a quoted-string. A username with a quote in it
// must not be able to add a parameter of its own.
func quote(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

var challengeParam = regexp.MustCompile(`([a-zA-Z0-9_-]+)\s*=\s*(?:"([^"]*)"|([^\s,]+))`)

// parseChallenge reads a `WWW-Authenticate: Digest ...` header; nil for
// anything else, MD5 being the only algorithm this client signs with.
func parseChallenge(h string) *challenge {
	if len(h) < 6 || !strings.EqualFold(h[:6], "digest") {
		return nil
	}
	f := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(h[6:], -1) {
		v := m[2]
		if v == "" {
			v = m[3]
		}
		f[strings.ToLower(m[1])] = v
	}
	if f["realm"] == "" || f["nonce"] == "" {
		return nil
	}
	if a := f["algorithm"]; a != "" && !strings.EqualFold(a, "MD5") {
		return nil
	}
	ch := &challenge{realm: f["realm"], nonce: f["nonce"], opaque: f["opaque"], algorithm: f["algorithm"]}
	for _, q := range strings.Split(f["qop"], ",") {
		if strings.TrimSpace(q) == "auth" {
			ch.qopAuth = true
		}
	}
	return ch
}
