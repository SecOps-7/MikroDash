package changelog

// The version whitelist, against the live `VERSION_RE`.
//
// `tools/changelog-cases.js` runs the live regex over 36 inputs, most of them
// attempts on it — the live header calls it "the single most important line in
// this file", because the version arrives from a SOCKET PAYLOAD and is
// interpolated into a URL PATH.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type changelogDoc struct {
	Constants struct {
		MaxBytes  int    `json:"maxBytes"`
		TimeoutMs int    `json:"timeoutMs"`
		CacheMax  int    `json:"cacheMax"`
		NegTTLMs  int    `json:"negTtlMs"`
		Host      string `json:"host"`
	} `json:"constants"`
	Cases []struct {
		Input    string `json:"input"`
		Trimmed  string `json:"trimmed"`
		Accepted bool   `json:"accepted"`
	} `json:"cases"`
}

func load(t *testing.T) changelogDoc {
	t.Helper()
	b, err := os.ReadFile("../../testdata/changelog-cases.json")
	if err != nil {
		t.Fatalf("corpus missing: %v — run tools/changelog-cases.js", err)
	}
	var d changelogDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Cases) == 0 {
		t.Fatal("corpus is empty")
	}
	return d
}

func TestTheWhitelistMatchesLive(t *testing.T) {
	doc := load(t)
	accepted := 0
	for _, c := range doc.Cases {
		// TRIMMED, because `fetchNotes` trims before testing — a port that
		// validated the raw string would reject ' 7.24', which the live one
		// accepts.
		got := ValidVersion.MatchString(c.Trimmed)
		if got != c.Accepted {
			t.Errorf("%q (trimmed %q): port %v, live %v", c.Input, c.Trimmed, got, c.Accepted)
		}
		if c.Accepted {
			accepted++
		}
	}
	if accepted < 4 || accepted == len(doc.Cases) {
		t.Errorf("%d of %d accepted — the corpus no longer discriminates",
			accepted, len(doc.Cases))
	}
}

// TestTheTraversalCasesAreStillPresent — the corpus's whole purpose.
func TestTheTraversalCasesAreStillPresent(t *testing.T) {
	doc := load(t)
	have := map[string]bool{}
	for _, c := range doc.Cases {
		have[c.Input] = true
	}
	for _, must := range []string{"../../etc/passwd", "..", "//evil.example.com/", "7.24?x=1"} {
		if !have[must] {
			t.Errorf("the corpus lost its case for %q, which is the reason the whitelist exists", must)
		}
	}
	// And none of them may be accepted, whatever the corpus says.
	for _, bad := range []string{"../../etc/passwd", "..", "../7.24", "//evil.example.com/",
		"7.24?x=1", "7.24#frag", "7.24%2F..%2F", "7.24/../../.."} {
		if ValidVersion.MatchString(strings.TrimSpace(bad)) {
			t.Errorf("SECURITY: %q passes ValidVersion", bad)
		}
	}
	// A NEWLINE MUST NOT SLIP A SECOND LINE PAST. Go's `$` is the end of the
	// string without the `m` flag, as JavaScript's is — asserted because it is
	// the one way the two could differ silently.
	if ValidVersion.MatchString("7.24\n../../etc/passwd") {
		t.Error("SECURITY: an embedded newline let a second line through; the regex needs no `m`")
	}
}

func TestTheConstantsMatchLive(t *testing.T) {
	doc := load(t)
	if maxBytes != doc.Constants.MaxBytes {
		t.Errorf("maxBytes = %d, live %d", maxBytes, doc.Constants.MaxBytes)
	}
	if int(timeout/time.Millisecond) != doc.Constants.TimeoutMs {
		t.Errorf("timeout = %v, live %dms", timeout, doc.Constants.TimeoutMs)
	}
	if cacheMax != doc.Constants.CacheMax {
		t.Errorf("cacheMax = %d, live %d", cacheMax, doc.Constants.CacheMax)
	}
	if int(negTTL/time.Millisecond) != doc.Constants.NegTTLMs {
		t.Errorf("negTTL = %v, live %dms", negTTL, doc.Constants.NegTTLMs)
	}
	if host != doc.Constants.Host {
		t.Errorf("host = %q, live %q", host, doc.Constants.Host)
	}
}

// TestABadVersionNeverReachesTheNetwork — the property the whitelist exists for.
func TestABadVersionNeverReachesTheNetwork(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	defer srv.Close()
	c := New()
	c.HTTP = srv.Client()
	if _, err := c.Notes("../../etc/passwd"); err != ErrBadVersion {
		t.Errorf("err = %v, want ErrBadVersion", err)
	}
	if reached {
		t.Error("a rejected version still made a request")
	}
}

func TestTheCapIsEnforcedWhileReading(t *testing.T) {
	big := strings.Repeat("x", maxBytes+10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()
	c := New()
	c.HTTP = srv.Client()
	// Point the request at the test server by overriding the transport's target.
	c.HTTP = &http.Client{Transport: rewriteHost{srv.URL, srv.Client().Transport}}
	if _, err := c.Notes("7.24"); err == nil || err.Error() != "too large" {
		t.Errorf("err = %v, want `too large`", err)
	}
	// EXACTLY AT the cap is fine — the difference between "at" and "over" is
	// what reading one byte past the limit exists to see.
	c2 := New()
	exact := strings.Repeat("y", maxBytes)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(exact))
	}))
	defer srv2.Close()
	c2.HTTP = &http.Client{Transport: rewriteHost{srv2.URL, srv2.Client().Transport}}
	got, err := c2.Notes("7.24")
	if err != nil {
		t.Fatalf("a body exactly at the cap failed: %v", err)
	}
	if len(got) != maxBytes {
		t.Errorf("got %d bytes, want %d", len(got), maxBytes)
	}
}

// TestTheNegativeCacheExpires — a failure is remembered briefly so an isolated
// install does not pay the full timeout on every open, and forgotten after.
func TestTheNegativeCacheExpires(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	now := time.Unix(1700000000, 0)
	c := New()
	c.HTTP = &http.Client{Transport: rewriteHost{srv.URL, srv.Client().Transport}}
	c.Now = func() time.Time { return now }

	if _, err := c.Notes("7.24"); err == nil {
		t.Fatal("a 404 succeeded")
	}
	if _, err := c.Notes("7.24"); err == nil {
		t.Fatal("the cached failure succeeded")
	}
	if hits != 1 {
		t.Errorf("%d requests; the second should have been served from the negative cache", hits)
	}
	now = now.Add(negTTL + time.Second)
	if _, err := c.Notes("7.24"); err == nil {
		t.Fatal("after expiry it succeeded")
	}
	if hits != 2 {
		t.Errorf("%d requests; the entry should have expired and been re-fetched", hits)
	}
}

// TestTheCacheIsBounded — the key comes from a caller, so an unbounded map is a
// socket spamming distinct valid versions until the process dies.
func TestTheCacheIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("notes"))
	}))
	defer srv.Close()
	c := New()
	c.HTTP = &http.Client{Transport: rewriteHost{srv.URL, srv.Client().Transport}}
	for i := 0; i < cacheMax+10; i++ {
		if _, err := c.Notes("7." + itoa(i)); err != nil {
			t.Fatalf("7.%d: %v", i, err)
		}
	}
	if len(c.cache) > cacheMax {
		t.Errorf("the cache holds %d entries, cap is %d", len(c.cache), cacheMax)
	}
	if len(c.order) != len(c.cache) {
		t.Errorf("the FIFO holds %d and the cache %d; they must agree or the trim drops the "+
			"wrong key", len(c.order), len(c.cache))
	}
}

// rewriteHost sends every request to the test server, keeping the path — so the
// real code builds the real URL and only the destination moves.
type rewriteHost struct {
	base string
	next http.RoundTripper
}

func (r rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	u := req.URL
	u.Scheme = "http"
	u.Host = strings.TrimPrefix(r.base, "http://")
	return r.next.RoundTrip(req)
}
