package rbac

// The capability payload `GET /api/auth/status` returns — the pages a principal
// may see and the routers it may read, computed HERE rather than asked of Node.
//
// ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
//
// During coexistence Node answers the question and `internal/server/auth.go`
// forwards it, which is the right arrangement while both halves run. After
// cutover there is nobody to ask. This is the same computation Node's
// `_capsFor` does, over the same grant graph in the same database.
//
// ── THE UNION IS WHAT NODE SENDS, AND IT IS DELIBERATELY COARSE ─────────────
//
// `caps.pages` is the union across every readable router — Node itself calls it
// "what the first paint needs" while "the per-router answer is authoritative and
// arrives over the socket". Reproducing the union rather than tightening it is
// the point: `(*conn).canPage` intersects it with the per-router answer from
// `CanPage`, so the union can only ever be the LOOSER of the two gates and
// tightening it here would change what the page draws without changing what the
// server permits.
//
// WRITE BEATS READ when two roles disagree about a page. That is a union, and a
// union of permissions takes the greater — a principal holding dns:write on one
// router and dns:read on another can see the write controls at first paint, and
// the socket then refuses the write on the router that does not allow it.

import "sort"

// conferred is the fail-closed rule for the four flags: true only when the
// lookup SUCCEEDED and said yes.
//
// ── ITS OWN FUNCTION BECAUSE THE ERROR ARM IS OTHERWISE UNTESTED ────────────
//
// Inline, `cerr == nil && ok` mutated to `cerr != nil || ok` SURVIVED — nothing
// can make the grant graph fail through this path, so a database blip would have
// drawn every admin control for everybody. The controls only decide what is
// DRAWN and the server refuses the write regardless, so the consequence is
// confusion rather than access; a blip should hide a button, never offer one.
//
// The third extraction of this shape today, after `disclosureAllowed` and
// `permitted`. Taking `(bool, error)` positionally so it wraps the call.
func conferred(ok bool, err error) bool { return err == nil && ok }

// Capabilities is `capsFor(session)` — the first-paint answer.
//
// ── THE FOUR FLAGS ARE NOT DECORATION, AND LEAVING THEM OUT HIDES ADMIN UI ──
//
// The first version of this struct carried Pages and Readable alone. That is
// what `/api/auth/status` then sent, and `web/src/caps.ts` reads
// `managePrincipals`, `manageSettings` and `createRouters` directly: an ABSENT
// key is `undefined`, which is falsy, so an administrator got the Add Router
// button hidden and Save Settings disabled with "Administrator access required".
//
// Invisible during coexistence, because Node answers `/api/auth/status` there.
// It would have appeared at cutover and looked like a permissions failure rather
// than a missing field.
type Capabilities struct {
	// The four install-wide flags, each a plain `can()` with no router.
	ManagePrincipals bool `json:"managePrincipals"`
	ManageSettings   bool `json:"manageSettings"`
	ManageDB         bool `json:"manageDb"`
	CreateRouters    bool `json:"createRouters"`
	// Pages maps a page key to "read" or "write", unioned across Readable.
	Pages map[string]string `json:"pages"`
	// Routers is the SIX per-permission id lists the live payload carries.
	Routers CapabilityRouters `json:"routers"`
	// Readable is `Routers.Readable`, kept as its own field because
	// `internal/server` already gates on it.
	Readable []string `json:"-"`
}

// CapabilityRouters is `caps.routers` — one sorted id list per router-scoped
// permission. All six are sent because the client decides what to draw from
// them, and a missing list reads as "no routers" rather than as "not answered".
type CapabilityRouters struct {
	Readable    []string `json:"readable"`
	Manageable  []string `json:"manageable"`
	History     []string `json:"history"`
	Ackable     []string `json:"ackable"`
	Diagnosable []string `json:"diagnosable"`
	Scannable   []string `json:"scannable"`
}

// CapabilitiesFor computes the first-paint answer for one user.
//
// A DATABASE THAT CANNOT BE OPENED YIELDS NOTHING, never everything. The
// resolver is the only thing standing between a principal and a page after
// cutover, and a failure that opened every page would be the worst possible
// direction to fail in — see Available().
func (r *Resolver) CapabilitiesFor(userID string) (Capabilities, error) {
	caps := Capabilities{Pages: map[string]string{}, Readable: []string{}}
	// EVERY LIST IS NON-NIL FROM THE START. They are JSON-encoded and the client
	// iterates them; a nil marshals to `null`, and `null.length` takes out the
	// first paint rather than showing an empty section.
	caps.Routers = CapabilityRouters{
		Readable: []string{}, Manageable: []string{}, History: []string{},
		Ackable: []string{}, Diagnosable: []string{}, Scannable: []string{},
	}
	if !r.Available() || userID == "" {
		return caps, nil
	}

	// The four install-wide flags. A FAILED LOOKUP LEAVES THEM FALSE rather than
	// aborting: the flags only decide what is DRAWN, and the server refuses the
	// write regardless — so a database blip should hide a button, never take the
	// whole payload down.
	for _, f := range []struct {
		perm string
		into *bool
	}{
		{"system:principals", &caps.ManagePrincipals},
		{"system:settings", &caps.ManageSettings},
		{"system:db", &caps.ManageDB},
		{"router:create", &caps.CreateRouters},
	} {
		ok, cerr := r.Can(userID, f.perm, "")
		*f.into = cerr == nil && ok
	}

	// The six scoped lists. `EffectiveRouterIDs` already sorts.
	for _, l := range []struct {
		perm string
		into *[]string
	}{
		{"router:read", &caps.Routers.Readable},
		{"router:manage", &caps.Routers.Manageable},
		{"router:history", &caps.Routers.History},
		{"router:ack", &caps.Routers.Ackable},
		{"router:diagnose", &caps.Routers.Diagnosable},
		{"router:scan", &caps.Routers.Scannable},
	} {
		ids, lerr := r.EffectiveRouterIDs(userID, l.perm)
		if lerr != nil {
			return caps, lerr
		}
		sort.Strings(ids)
		*l.into = ids
	}
	readable := caps.Routers.Readable
	caps.Readable = readable

	// The roles are collected per router and DEDUPLICATED before their pages are
	// read: a global grant is in scope for every router, and reading its pages
	// once per router would be N identical queries on a fleet of N.
	//
	// THE DEDUPLICATION IS AN EFFICIENCY, NOT A CORRECTNESS PROPERTY, and it is
	// recorded here because a mutation removing it SURVIVED and that is the
	// honest reading rather than a gap. Writing the same page/access pair twice
	// is idempotent, so the answer is identical either way; only the query count
	// changes. No test defends it and none should be contrived to — the comment
	// above says what it buys, and this says what it does not.
	seen := map[string]bool{}
	for _, rid := range readable {
		roles, err := r.roleSetsInScope(userID, rid)
		if err != nil {
			return caps, err
		}
		for _, roleID := range roles {
			seen[roleID] = true
		}
	}
	for roleID := range seen {
		// THROUGH RoleByID, NEVER RolePages DIRECTLY. That distinction is the
		// whole of the next paragraph and it is not a style preference.
		role, err := r.db.RoleByID(roleID)
		if err != nil {
			return caps, err
		}
		if role == nil {
			// A grant naming a role that no longer exists confers nothing,
			// matching CanPage and rbac.js's _roleDef.
			continue
		}
		// ── BUILTIN IS STRUCTURAL, NOT TABLE-DRIVEN ─────────────────────
		//
		// Administrator confers EVERY page at write without a single
		// `role_pages` row. The first version of this function read the table
		// directly and answered `pages: {}` for an administrator — the exact
		// failure `db.go`'s Role comment warns about: "a port that only read
		// the table would deny an administrator everything — silently, and
		// only on installs that have one".
		//
		// SILENTLY AND ONLY ON SOME INSTALLS is why no test caught it. The
		// unit tests build custom roles, which have rows; the fixture that
		// would have exposed it is an install whose admin holds a builtin
		// grant. It was found by running the server in standalone mode against
		// the real /data and reading the payload — `readable` listed all three
		// routers while `pages` was empty, so the app would have drawn a
		// navigation with nothing in it for the one principal guaranteed to be
		// able to see everything.
		if role.Builtin {
			for page := range r.pages {
				caps.Pages[page] = "write"
			}
			continue
		}
		for _, p := range role.Pages {
			// WRITE WINS. Without this the answer depends on map iteration
			// order, so a principal with two roles would see the write controls
			// on some page loads and not others.
			if caps.Pages[p.Page] == "write" {
				continue
			}
			caps.Pages[p.Page] = p.Access
		}
	}
	return caps, nil
}
