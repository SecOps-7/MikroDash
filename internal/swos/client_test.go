package swos

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"mikrodash/internal/netguard"
)

// digestServer is a switch's login, written from RFC 2617 independently of the
// client: it checks the response hash and refuses a nonce count it has seen.
type digestServer struct {
	user, pass, realm, nonce string
	body                     string

	mu      sync.Mutex
	methods []string
	seenNC  map[string]bool
}

var authParam = regexp.MustCompile(`(\w+)=(?:"([^"]*)"|([^\s,]+))`)

func (d *digestServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.methods = append(d.methods, r.Method)
	if r.URL.Path == "/elsewhere" {
		// Where a redirect points: answers anyone, so a client that followed
		// it would get a 200 here.
		_, _ = w.Write([]byte("{}"))
		return
	}
	h := r.Header.Get("Authorization")
	challenge := func() {
		w.Header().Set("WWW-Authenticate", `Digest realm="`+d.realm+`", nonce="`+d.nonce+`", qop="auth"`)
		w.WriteHeader(http.StatusUnauthorized)
	}
	if !strings.HasPrefix(h, "Digest ") {
		challenge()
		return
	}
	f := map[string]string{}
	for _, m := range authParam.FindAllStringSubmatch(h, -1) {
		f[m[1]] = m[2] + m[3]
	}
	ha1 := md5hex(d.user + ":" + d.realm + ":" + d.pass)
	ha2 := md5hex(r.Method + ":" + f["uri"])
	want := md5hex(ha1 + ":" + d.nonce + ":" + f["nc"] + ":" + f["cnonce"] + ":auth:" + ha2)
	if f["username"] != d.user || f["response"] != want || f["uri"] != r.URL.RequestURI() || d.seenNC[f["nc"]] {
		challenge()
		return
	}
	d.seenNC[f["nc"]] = true
	switch r.URL.Path {
	case "/sys.b":
		_, _ = w.Write([]byte(d.body))
	case "/big.b":
		_, _ = w.Write([]byte(strings.Repeat("x", maxBody+10)))
	case "/away.b":
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	default:
		http.NotFound(w, r)
	}
}

func newSwitch(t *testing.T, pass string) (*digestServer, *Client) {
	t.Helper()
	d := &digestServer{user: "admin", pass: "secret", realm: "CSS610", nonce: "abc123",
		body: "{i06:'322e3231',i07:'435353363130'}", seenNC: map[string]bool{}}
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	c, err := New("http", u.Hostname(), port, "admin", pass, netguard.Any)
	if err != nil {
		t.Fatal(err)
	}
	return d, c
}

func TestTheClientSignsEveryRequestWithARisingNonceCount(t *testing.T) {
	d, c := newSwitch(t, "secret")
	for i := 0; i < 3; i++ {
		v, err := c.Get(context.Background(), "sys.b")
		if err != nil {
			t.Fatalf("read %d: %v", i+1, err)
		}
		if s, _ := DetectScheme(v); s != SchemeLite {
			t.Errorf("read %d decoded to %+v", i+1, v)
		}
	}
	// One challenge, then three signed requests; the server refuses a nonce
	// count it has seen, so a client that did not count up would have failed.
	if len(d.seenNC) != 3 {
		t.Errorf("%d distinct nonce counts, want 3", len(d.seenNC))
	}
	for _, m := range d.methods {
		if m != http.MethodGet {
			t.Errorf("the client sent a %s", m)
		}
	}
}

func TestAWrongPasswordIsAnAuthError(t *testing.T) {
	_, c := newSwitch(t, "wrong")
	if _, err := c.GetRaw(context.Background(), "sys.b"); !errors.Is(err, ErrAuth) {
		t.Errorf("err = %v, want ErrAuth", err)
	}
}

func TestTheClientRefusesWhatIsNotAPage(t *testing.T) {
	d, c := newSwitch(t, "secret")
	for _, page := range []string{"../etc/passwd", "sys.b?x=1", "sys", "/sys.b", "a/b.b", "SYS.B", ""} {
		if _, err := c.GetRaw(context.Background(), page); err == nil {
			t.Errorf("%q was requested", page)
		}
	}
	if len(d.methods) != 0 {
		t.Errorf("%d requests reached the switch for names that are not pages", len(d.methods))
	}
	if _, err := c.GetRaw(context.Background(), "nope.b"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing page: %v", err)
	}
	if _, err := c.GetRaw(context.Background(), "big.b"); err == nil {
		t.Error("a reply over the size cap was accepted")
	}
	if _, err := c.GetRaw(context.Background(), "away.b"); err == nil {
		t.Error("a redirect was followed")
	}
}

// TestTheClientHasNoWayToWrite pins the method set: v1 reads, and a method
// that could send a body has to be added on purpose, past this test.
func TestTheClientHasNoWayToWrite(t *testing.T) {
	var got []string
	ty := reflect.TypeOf(&Client{})
	for i := 0; i < ty.NumMethod(); i++ {
		got = append(got, ty.Method(i).Name)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "Get,GetRaw" {
		t.Errorf("Client's methods are %v; only Get and GetRaw may exist in the read-only version", got)
	}
}

// TestTheClientKeepsToTheAddressRule: loopback is out of the default reach,
// as it is for a router.
func TestTheClientKeepsToTheAddressRule(t *testing.T) {
	d := &digestServer{seenNC: map[string]bool{}}
	srv := httptest.NewServer(d)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	c, err := New("http", u.Hostname(), port, "admin", "x", netguard.Private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetRaw(context.Background(), "sys.b"); err == nil {
		t.Error("a loopback switch was reached under the private rule")
	}
	if len(d.methods) != 0 {
		t.Error("the request reached the server")
	}
	if _, err := New("ftp", "198.51.100.1", 21, "a", "b", netguard.Private); err == nil {
		t.Error("a scheme other than http or https was accepted")
	}
}
