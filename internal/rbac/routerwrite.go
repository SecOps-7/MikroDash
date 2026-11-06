package rbac

// Which fields of a router write only an administrator may set.
//
// ── WHY MEMBERSHIP IS ADMINISTRATOR-ONLY ────────────────────────────────────
//
// `PUT /api/routers/:id` is gated on `router:manage` for the TARGET router,
// which write access to the Devices page confers and which is NOT global-only.
//
// Before multi-site (#117) that let a non-administrator MOVE a device between
// sites: an escalation, but a self-limiting and LOUD one, because the device
// vanished from the old site's users. Many-to-many made it purely ADDITIVE — a
// repeatable, invisible way to inject a device into any scope, with every site
// id enumerable from an ungated `GET /api/sites`.
//
// So the fields are DROPPED rather than the request refused: the rest of the
// edit is legitimate and the live route applies it. A refusal would also tell
// the caller which fields are privileged, which the original does not.
//
// ── BOTH KEYS, AND THE MIRROR IS NOT OPTIONAL ───────────────────────────────
//
// `siteId` is the scalar mirror of the first entry, and the store keeps the two
// in step — so honouring it while dropping `siteIds` leaves the whole escalation
// open through the older field. The live code deletes both, in that order, and
// so does this.
//
// ── AND WHERE THE DEVICE IS REACHED, AS WHOM ────────────────────────────────
//
// host, port, tls and tlsInsecure decide where MikroDash sends the device's
// stored password, and username, password and loginProfileId decide which
// account it signs in as. A delegate who could set the first could aim the
// device at a host they run and receive a password no response shows them
// (GHSA-mcwr-rm79-fr4h). The route refuses such a CHANGE out loud
// (routerEndpointEditAllowed); these are dropped here as well, so a value the
// gate misjudged still never reaches the store. Defence in depth: that bug
// was one refusal, at one layer, that did not cover the default case.
//
// ── PORTED AHEAD OF ITS CALLER, DELIBERATELY ────────────────────────────────
//
// This port has no router write endpoint yet. The rule is written and pinned now
// rather than left as a note for whoever adds one, because a privilege check
// that has to be REMEMBERED at the call site is the kind that gets forgotten —
// the same reasoning `portedGuards` applies to RouterOS writes.

// privilegedRouterFields is every field only an administrator may set.
var privilegedRouterFields = []string{"siteIds", "siteId",
	"host", "port", "tls", "tlsInsecure", "username", "password", "loginProfileId"}

// StripPrivilegedRouterFields removes the fields only a principal who can manage
// principals may set. It mutates `body` and reports which keys it dropped, so a
// caller can audit the attempt rather than silently swallowing it.
//
// A nil body is not an error: there is nothing to strip.
func StripPrivilegedRouterFields(body map[string]any, mayManagePrincipals bool) []string {
	if body == nil || mayManagePrincipals {
		return nil
	}
	var dropped []string
	// BOTH, and `siteIds` first, matching the original. `siteId` alone would be
	// enough to inject a device into a scope. Then the endpoint and the
	// credential: see the header.
	for _, k := range privilegedRouterFields {
		if _, ok := body[k]; ok {
			delete(body, k)
			dropped = append(dropped, k)
		}
	}
	return dropped
}
