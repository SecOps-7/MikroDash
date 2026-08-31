package server

// `GET /api/localcc` — the country the WAN address sits in, and (for an
// operator who may see it) the address itself.
//
// It is the Connections map's arc ORIGIN. Without it `localCC` stays ZZ and no
// arc is drawn — and because the map still colours countries, the failure looks
// like a geo defect rather than a missing endpoint. That is why it was worth
// finding: it was the LAST endpoint a standalone run still answered 502 on.

import (
	"net/http"
	"strings"

	"mikrodash/internal/geo"
)

func (s *Server) registerLocalCC(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/localcc", s.localCC)
}

// localCC answers `{cc, wanIp}`.
//
// ── THE COUNTRY IS FOR EVERYONE; THE ADDRESS IS NOT ─────────────────────────
//
// The live comment: "Viewers only need the country code (world-map arc origin);
// the WAN IP is withheld from them like the rest of the router network detail.
// Same reasoning as GET /api/settings: resolve through grants, not the role
// field, which cannot see access conferred by a group."
//
// So `cc` is computed first and unconditionally, and the address is added only
// after the capability check. Writing it the other way — build the whole answer,
// then strip — is how `devices.go` describes the same rule going wrong, and it
// puts the secret in the payload for the length of one function.
func (s *Server) localCC(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}

	wanIP := s.activeWanIP()
	maySeeIP := false
	if wanIP != "" {
		// EVALUATED ONLY WHEN THERE IS SOMETHING TO WITHHOLD. A grant lookup
		// for an answer that is empty either way is a database read on every
		// page load of a fresh tab.
		//
		// A FAILED LOOKUP WITHHOLDS. The country still goes out, because the
		// map is the point and a grant read that errored is not a reason to
		// break it.
		maySeeIP = disclosureAllowed(s.rbac.Can(s.userIDFor(sess.Username), "system:settings", ""))
	}
	writeJSON(w, localCCPayload(wanIP, maySeeIP, countryOf))
}

// countryOf is the default lookup: the shared geo database, or "" when it could
// not be loaded.
//
// `Current()`, not `Shared(dir)`: the database is loaded once at startup by
// `cmd/mikrodash`, and this is the accessor `internal/session` already uses for
// the same singleton. Calling `Shared` here would pass a directory that the
// `sync.Once` ignores anyway — right by accident, and wrong the moment anything
// calls it before startup does.
func countryOf(ip string) string {
	db, ok := geo.Current()
	if !ok {
		return ""
	}
	loc, found := db.Lookup(ip)
	if !found {
		return ""
	}
	return loc.Country
}

// disclosureAllowed is the fail-closed rule for the WAN address: permitted only
// when the grant lookup SUCCEEDED and said yes.
//
// ── ITS OWN FUNCTION BECAUSE THE ERROR ARM WAS UNTESTED ─────────────────────
//
// It was `cerr == nil && may` inline, and the mutation to `cerr != nil || may`
// SURVIVED — a database blip would then have disclosed the address to anybody.
// The arm is unreachable from the route in a test for the same reason the rest
// of this file was: the only server the harness can stand up has no router
// session, so the gate never runs.
//
// Taking `(bool, error)` positionally so it wraps `rbac.Can` at the call site
// and there is nowhere to get the order wrong.
func disclosureAllowed(may bool, err error) bool {
	return err == nil && may
}

// localCCPayload builds the answer.
//
// ── PULLED OUT SO THE DISCLOSURE RULE CAN BE EXECUTED BY A TEST ─────────────
//
// It was inline, and two mutations survived because of it: sending the address
// unconditionally, and omitting a key. Neither could be caught, because the only
// route test this harness can stand up has NO ACTIVE ROUTER SESSION — so `wanIP`
// is empty, the function returns early, and the gate never runs at all.
//
// That is the same shape this port and the live repo have each now found in the
// other's work: an operation reachable only through machinery a test cannot
// build is an operation nothing checks. Extracting it is not a testing
// convenience; it is what makes the rule checkable.
//
// ── THE COUNTRY IS FOR EVERYONE; THE ADDRESS IS NOT ─────────────────────────
//
// The live comment: "Viewers only need the country code (world-map arc origin);
// the WAN IP is withheld from them like the rest of the router network detail."
//
// BOTH KEYS ARE ALWAYS PRESENT. The client reads `d.cc` and `d.wanIp` directly,
// and a missing key is `undefined` where the live app sends "".
func localCCPayload(wanIP string, maySeeIP bool, country func(string) string) map[string]any {
	// `if (!s) return res.json({ cc: '', wanIp: '' })` — no session, no answer,
	// and NOT an error. Nobody having opened a router yet is an ordinary state
	// on a fresh load, and a 500 would put a red line in every new tab.
	if wanIP == "" {
		return map[string]any{"cc": "", "wanIp": ""}
	}
	out := map[string]any{"cc": country(wanIP), "wanIp": ""}
	if maySeeIP {
		out["wanIp"] = wanIP
	}
	return out
}

// activeWanIP is `(s.state.lastWanIp || ”).split('/')[0]` for the ACTIVE
// router's session.
//
// ── IT LOOKS UP AN EXISTING SESSION AND NEVER CREATES ONE ───────────────────
//
// The live `_globalEntry` reads `_routerSessions.get(id)` — a lookup in the pool,
// with no fallback that would stand a session up. `Manager.Acquire` is the
// obvious Go spelling and is the wrong one: it OPENS A ROUTER CONNECTION, so an
// HTTP GET that anybody's browser makes on page load would dial the router.
// `Live()` is the lookup.
//
// ── AND IT SPLITS ON '/', BECAUSE THE VALUE IS A CIDR ───────────────────────
//
// `dhcpNetworks` resolves the WAN address off `/ip/address`, where it carries a
// prefix. Handing `203.0.113.7/24` to a geo lookup finds nothing, and the
// symptom is an empty country rather than an error.
func (s *Server) activeWanIP() string {
	if s.store == nil || s.sessions == nil {
		return ""
	}
	cfg, err := s.store.Settings()
	if err != nil {
		return ""
	}
	activeID, _ := cfg["activeRouterId"].(string)
	if activeID == "" {
		return ""
	}
	sess := s.sessions.Live()[activeID]
	if sess == nil {
		return ""
	}
	last := sess.DHCPNetworks().Last()
	if last == nil {
		return ""
	}
	return addressOfCIDR(last.WanIP)
}

// addressOfCIDR is `(lastWanIp || ”).split('/')[0]`.
//
// `dhcpNetworks` resolves the WAN address off `/ip/address`, where it carries a
// prefix. Handing `203.0.113.7/24` to a geo lookup finds nothing, and the
// symptom is an EMPTY COUNTRY rather than an error — so nothing downstream
// reports it and the map simply never draws an arc.
//
// Its own function because the test for it was otherwise a test of
// `strings.SplitN`: exercising the standard library proves nothing about
// whether this file calls it correctly, and the mutation taking the SUFFIX
// instead of the prefix survived on exactly that.
func addressOfCIDR(v string) string {
	return strings.SplitN(v, "/", 2)[0]
}
