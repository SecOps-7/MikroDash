package server

// `GET /api/localcc` — and specifically the rule that the COUNTRY is for
// everyone while the ADDRESS is not.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"mikrodash/internal/geo"
	"mikrodash/internal/store"
	"path/filepath"
)

func getLocalCC(t *testing.T, h http.Handler, token string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/localcc", nil)
	if token != "" {
		req.Header.Set("Cookie", "mikrodash_sid="+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// TestLocalCCNeedsASession. It reports where this install's uplink is, which is
// network detail and not public.
func TestLocalCCNeedsASession(t *testing.T) {
	h, _ := signedInServer(t, "a-password-for-localcc")
	if code, _ := getLocalCC(t, h, ""); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated answered %d, want 401", code)
	}
}

// TestLocalCCWithNoActiveSessionAnswersEmpty.
//
// The live route: `if (!s) return res.json({ cc: ”, wanIp: ” })`. NOT an
// error — nobody having opened a router yet is an ordinary state on a fresh
// load, and a 500 here would put a red line in the console of every new tab.
//
// This is also the shape the test harness can reach: `signedInServer` stands up
// a server with no router sessions at all, which is exactly "no active session".
func TestLocalCCWithNoActiveSessionAnswersEmpty(t *testing.T) {
	h, token := signedInServer(t, "another-password-for-localcc")
	code, body := getLocalCC(t, h, token)
	if code != http.StatusOK {
		t.Fatalf("answered %d, want 200", code)
	}
	if body["cc"] != "" || body["wanIp"] != "" {
		t.Errorf("no active session should answer empty strings, got %v", body)
	}
	// BOTH KEYS ARE PRESENT, not omitted. The client reads `d.cc` and `d.wanIp`
	// directly; a missing key is `undefined` where the live app sends "".
	for _, k := range []string{"cc", "wanIp"} {
		if _, ok := body[k]; !ok {
			t.Errorf("the answer has no %q key -- the live route always sends both", k)
		}
	}
}

// TestTheWanAddressIsSplitOffItsPrefix.
//
// `dhcpNetworks` resolves the WAN address off `/ip/address`, where it carries a
// CIDR prefix, and the live route does `.split('/')[0]`. Handing
// `203.0.113.7/24` to a geo lookup finds nothing, and the symptom is an empty
// country rather than an error — so nothing downstream would report it.
//
// Asserted on `addressOfCIDR` rather than through the route, because reaching
// the route's version needs a live router session.
//
// IT USED TO CALL `strings.SplitN` DIRECTLY, which proved the standard library
// works and nothing about whether this file uses it correctly — the mutation
// taking the SUFFIX instead of the prefix survived. A test that reimplements the
// thing it is testing cannot fail with it.
func TestTheWanAddressIsSplitOffItsPrefix(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"203.0.113.7/24", "203.0.113.7"},
		{"203.0.113.7", "203.0.113.7"},
		{"", ""},
		// A malformed value keeps its shape rather than being repaired: the geo
		// lookup then finds nothing and the country is empty, which is the same
		// answer the live app gives.
		{"/24", ""},
	} {
		if got := addressOfCIDR(c.in); got != c.want {
			t.Errorf("splitting %q gave %q, want %q", c.in, got, c.want)
		}
	}
}

// TestTheAddressIsWithheldAndTheCountryIsNot.
//
// The rule this endpoint exists to get right, executed rather than read. It was
// unreachable until `localCCPayload` was pulled out of the handler: the only
// server this harness can stand up has no router session, so `wanIP` is empty,
// the early return fires, and the gate below never runs. Two mutations survived
// on exactly that — sending the address unconditionally, and omitting a key.
func TestTheAddressIsWithheldAndTheCountryIsNot(t *testing.T) {
	const ip = "203.0.113.7"
	country := func(string) string { return "ZA" }

	viewer := localCCPayload(ip, false, country, "")
	if viewer["cc"] != "ZA" {
		t.Errorf("a viewer was denied the COUNTRY (%v). It is the world-map arc origin and is "+
			"for everyone; the ADDRESS is the part that is withheld", viewer["cc"])
	}
	if viewer["wanIp"] != "" {
		t.Errorf("a viewer was given the WAN address %v", viewer["wanIp"])
	}

	admin := localCCPayload(ip, true, country, "")
	if admin["wanIp"] != ip {
		t.Errorf("system:settings was denied the address: %v", admin["wanIp"])
	}
	if admin["cc"] != "ZA" {
		t.Errorf("cc = %v, want ZA", admin["cc"])
	}

	// BOTH KEYS, ALWAYS. A missing key is `undefined` in the client where the
	// live app sends "".
	for _, m := range []map[string]any{viewer, admin, localCCPayload("", true, country, "")} {
		for _, k := range []string{"cc", "wanIp"} {
			if _, ok := m[k]; !ok {
				t.Errorf("the answer has no %q key: %v", k, m)
			}
		}
	}

	// NO ADDRESS MEANS NO LOOKUP EITHER. Asking a geo database about "" is a
	// read that can only answer nothing.
	asked := 0
	empty := localCCPayload("", true, func(string) string { asked++; return "ZA" }, "")
	if asked != 0 {
		t.Error("the country was looked up for an empty address")
	}
	if empty["cc"] != "" || empty["wanIp"] != "" {
		t.Errorf("an empty address answered %v, want two empty strings", empty)
	}
}

// TestAFailedGrantLookupWithholdsTheAddress.
//
// Fail closed. The mutation `err != nil || may` survived while this rule was
// inline in the handler, where no test could reach it — a database blip would
// have disclosed the WAN address to anybody asking.
func TestAFailedGrantLookupWithholdsTheAddress(t *testing.T) {
	if disclosureAllowed(true, errors.New("database is gone")) {
		t.Error("a FAILED grant lookup disclosed the address. The rule is fail-closed: an " +
			"error is not a yes, and a blip must not hand out network detail")
	}
	if disclosureAllowed(false, errors.New("database is gone")) {
		t.Error("a failed lookup that also said no disclosed the address")
	}
	if disclosureAllowed(false, nil) {
		t.Error("a successful lookup that said NO disclosed the address")
	}
	if !disclosureAllowed(true, nil) {
		t.Error("a successful lookup that said yes was refused -- an operator with " +
			"system:settings may see the address")
	}
}

// TestTheCountryComesFromTheRealDatabase, when there is one.
//
// SKIPS rather than fails without it: the geo data ships with the Node app and
// is not in this repo, so a hard failure would make the suite depend on an
// artefact it does not own. What it protects is the WIRING — that `countryOf`
// asks the shared database and returns the country field rather than, say, the
// region.
func TestTheCountryComesFromTheRealDatabase(t *testing.T) {
	dir := os.Getenv("MIKRODASH_GEO_DIR")
	if dir == "" {
		t.Skip("MIKRODASH_GEO_DIR is not set; the geo database ships with the Node app")
	}
	if _, ok := geo.Shared(dir); !ok {
		t.Skipf("the geo database at %s could not be loaded: %s", dir, geo.Reason())
	}
	// A well-known address with a stable country. If this ever starts failing,
	// check the assignment before the code.
	if got := countryOf("8.8.8.8"); got != "US" {
		t.Errorf("countryOf(8.8.8.8) = %q, want US", got)
	}
	// A private address is in no country, and must not produce a stray value.
	if got := countryOf("192.168.1.1"); got != "" {
		t.Errorf("countryOf(192.168.1.1) = %q, want empty", got)
	}
}

// TestLocalCCLooksTheSessionUpRatherThanCreatingOne.
//
// A SOURCE TEST, and deliberately only for the one property that cannot be
// executed here: standing a router session up needs a router. Everything this
// used to assert about the disclosure ordering is now checked by running
// `localCCPayload`, which is strictly better — reading the source proves the
// lines are in an order, not that the answer is right.
//
// What remains is worth pinning precisely because it is invisible at runtime in
// a test: `Manager.Acquire` OPENS A ROUTER CONNECTION, and this is a GET that
// every browser makes on page load. The live `_globalEntry` is
// `_routerSessions.get(id)` — a lookup with no fallback that stands one up.
func TestLocalCCLooksTheSessionUpRatherThanCreatingOne(t *testing.T) {
	src := readSource(t, "localcc_api.go")
	if strings.Contains(src, ".Acquire(") {
		t.Error("localcc calls Acquire, which OPENS A ROUTER CONNECTION on an endpoint every " +
			"browser hits on page load. The live route is a pool LOOKUP")
	}
	if !strings.Contains(src, ".Live()") {
		t.Error("localcc no longer looks the session up with Live()")
	}
}

// readSource reads a file in this package for the source-level assertions.
// Several tests here do it — `ws_test.go` explains why: standing a connection up
// needs a router, so some properties can only be pinned by reading the code.
func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ── THE ARC ORIGIN FOR A ROUTER BEHIND ANOTHER ROUTER (issue #120) ──────────
//
// The Connections map draws every arc FROM the local country, and that country
// came only from a live geo lookup of the WAN address. A router whose WAN is
// private geolocates to nothing, so the map coloured countries, counted them,
// and drew no arcs at all — with no setting that helped, because the town the
// operator had picked was never consulted.
//
// The fallback is STRICTLY ADDITIVE: it may only fill in an answer that was
// already empty. That is the property worth pinning, because the tempting
// version — prefer the configured place, as the Devices map does — would change
// what every install with a public address already sees.
func TestTheConfiguredPlaceOnlyFillsInAnEmptyCountry(t *testing.T) {
	public := func(string) string { return "DE" }
	none := func(string) string { return "" }

	t.Run("a WAN that geolocates wins", func(t *testing.T) {
		got := localCCPayload("203.0.113.7", false, public, "GB")
		if got["cc"] != "DE" {
			t.Errorf("cc = %v, want DE — a live lookup of the real address must "+
				"not be overridden by a stored place", got["cc"])
		}
	})

	t.Run("a private WAN falls back to the configured place", func(t *testing.T) {
		got := localCCPayload("192.168.0.50", false, none, "GB")
		if got["cc"] != "GB" {
			t.Errorf("cc = %v, want GB — this is the whole bug: a router behind "+
				"another router has no geolocatable address, so without the "+
				"fallback the map draws no arcs and nothing the operator sets "+
				"can change that", got["cc"])
		}
	})

	t.Run("no WAN and no place is still empty", func(t *testing.T) {
		got := localCCPayload("", true, public, "")
		if got["cc"] != "" || got["wanIp"] != "" {
			t.Errorf("got %v, want two empty strings", got)
		}
	})

	t.Run("a place answers even before a session exists", func(t *testing.T) {
		// The record and the sites table are on disk, so this needs no router
		// connection — an operator who picked a town gets arcs on a fresh tab
		// rather than after the first poll.
		got := localCCPayload("", true, public, "GB")
		if got["cc"] != "GB" {
			t.Errorf("cc = %v, want GB", got["cc"])
		}
		if got["wanIp"] != "" {
			t.Errorf("wanIp = %v; there is no address to disclose", got["wanIp"])
		}
	})

	t.Run("the address is still withheld from a viewer", func(t *testing.T) {
		got := localCCPayload("192.168.0.50", false, none, "GB")
		if got["wanIp"] != "" {
			t.Errorf("wanIp = %v leaked to a viewer through the fallback path",
				got["wanIp"])
		}
	})
}

// activeRouterPlaceCC is the half `localCCPayload` cannot test: the pure
// function takes the country as an argument, so every case above passes even if
// nothing ever reads a router record. This drives the resolver itself.
func TestTheActiveRoutersTownSuppliesTheCountry(t *testing.T) {
	newServer := func(t *testing.T, geo string) *Server {
		t.Helper()
		dir := t.TempDir()
		routers := `[{"id":"r1","label":"One","host":"192.168.0.50","port":8728,
		  "username":"u","password":""` + geo + `}]`
		for name, body := range map[string]string{
			".secret":       "test-secret",
			"settings.json": `{"activeRouterId":"r1"}`,
			"routers.json":  routers,
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		st, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		return &Server{store: st}
	}

	t.Run("a town picked on the device", func(t *testing.T) {
		s := newServer(t, `,"geo":{"place":{"name":"Marl",
		  "region":"North Rhine-Westphalia","cc":"DE","lat":51.6567,"lon":7.09038}}`)
		if cc := s.activeRouterPlaceCC(); cc != "DE" {
			t.Errorf("activeRouterPlaceCC() = %q, want DE — the town the operator "+
				"picked is the only thing that can give a router behind another "+
				"router an arc origin", cc)
		}
	})

	t.Run("no location at all", func(t *testing.T) {
		s := newServer(t, ``)
		if cc := s.activeRouterPlaceCC(); cc != "" {
			t.Errorf("activeRouterPlaceCC() = %q, want empty", cc)
		}
	})

	t.Run("no active router", func(t *testing.T) {
		s := newServer(t, ``)
		// Overwrite the settings so nothing is selected.
		if err := os.WriteFile(filepath.Join(s.store.Dir, "settings.json"),
			[]byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if cc := s.activeRouterPlaceCC(); cc != "" {
			t.Errorf("activeRouterPlaceCC() = %q with no active router", cc)
		}
	})

	t.Run("no store is not a panic", func(t *testing.T) {
		if cc := (&Server{}).activeRouterPlaceCC(); cc != "" {
			t.Errorf("got %q", cc)
		}
	})
}

// And that the handler actually asks. Both suites above pass with the resolver
// never called — the shape that shipped three times this week.
func TestTheLocalCCHandlerConsultsTheConfiguredPlace(t *testing.T) {
	// Read by name from the package directory, as the ws.go wiring check does.
	b, err := os.ReadFile("localcc_api.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "activeRouterPlaceCC()") {
		t.Error("the localCC handler never calls activeRouterPlaceCC, so the " +
			"fallback exists and is unreachable — a router behind another router " +
			"still gets no arcs")
	}
}
