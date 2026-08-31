package server

// The Access Management card's three reads: groups, roles and grants.
//
// ── ALL THREE ARE `requireGlobalAdmin`, WHICH IS ONE PERMISSION ────────────
//
// `can(session, 'system:principals')` — not a role name, and not "is this user
// an admin". The live comment on `/api/settings` gives the reason it matters:
// an administrator whose grant is held through a GROUP has role 'viewer' on
// their user record, so anything keying on the stored role refuses a real
// administrator.
//
// ── A DENIED GET IS NOT AUDITED, AND THAT IS DELIBERATE ────────────────────
//
// `_auditDenied` opens with
// `if (!/^(POST|PUT|PATCH|DELETE)$/.test(req.method)) return;` — only mutating
// methods reach the trail. Auditing every refusal would fill it with a viewer's
// browser polling admin endpoints it was never going to be shown; auditing none
// would lose the record of an attempted write. These are all GETs, so none of
// them records a denial, and that is the original's behaviour rather than an
// omission here.
//
// ── THE WRITES ARE NOT HERE ────────────────────────────────────────────────
//
// Creating a group, editing a role or issuing a grant all mutate the database
// Node owns, and Node holds `Rbac.bump()` to invalidate its own caches — a write
// from this side would leave that stale. Same family as the settings write; see
// PORT-QUEUE.md.

import (
	_ "embed"
	"encoding/json"
	"log"
	"net/http"

	"mikrodash/internal/db"
	"mikrodash/internal/rbac"
)

//go:embed pages_table.json
var pagesTableJSON []byte

type pageEntry struct {
	Key         string  `json:"key"`
	Title       string  `json:"title"`
	SettingsKey *string `json:"settingsKey"`
}

var pageCatalogue = mustPages()

func mustPages() []pageEntry {
	var f struct {
		Pages []pageEntry `json:"pages"`
	}
	if err := json.Unmarshal(pagesTableJSON, &f); err != nil {
		panic("server: pages_table.json: " + err.Error())
	}
	return f.Pages
}

const principalsPrefix = "/api"

func (s *Server) registerPrincipals(mux *http.ServeMux) {
	mux.HandleFunc("GET "+principalsPrefix+"/groups", s.principalsGuard(s.groupsGet))
	mux.HandleFunc("GET "+principalsPrefix+"/roles", s.principalsGuard(s.rolesGet))
	mux.HandleFunc("GET "+principalsPrefix+"/grants", s.principalsGuard(s.grantsGet))
	mux.HandleFunc("GET "+principalsPrefix+"/users", s.principalsGuard(s.usersGet))
}

// usersGet is `GET /api/users`.
//
// ── THE GRANTS ARE JOINED HERE, AND THAT IS THE POINT OF THE ROUTE ──────────
//
// The live comment says why: "Grants are joined here the way /api/groups already
// does, so the Users card can render real access instead of the legacy role +
// allowedRouterIds pair (issue #108). One fetch, and the two principal types
// stay symmetric."
//
// A card that had to fetch grants per user would make N+1 requests and, worse,
// would render each row as it arrived — so a user's access would appear to
// change while the page settled.
//
// ── THE STRIP IS `PublicUsers`, WHICH IS A DENYLIST ON PURPOSE ──────────────
//
// `store.PublicUsers` was ported ahead of this route with its own generated
// corpus. It works over RAW JSON rather than the typed `store.User`, because the
// typed one would drop what it does not declare and invent zero values for what
// a record lacks — and on the Users card "no access" and "access to nothing" are
// different claims. See its header; this is the call site it was waiting for.
func (s *Server) usersGet(w http.ResponseWriter, _ *http.Request, _ *Session) {
	if s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "user store unavailable")
		return
	}
	users, err := s.store.PublicUsers()
	if err != nil {
		// NOT AN EMPTY LIST. `users.json` failing to parse must not read as "this
		// install has no users" — that is the failure the bare-array rule exists
		// to prevent, and the card would show an empty table with no error.
		log.Printf("[principals] users: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read users")
		return
	}

	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		id, _ := u["id"].(string)
		grants, err := s.auditDB.ListGrants(db.GrantFilter{PrincipalType: "user", PrincipalID: id})
		if err != nil {
			log.Printf("[principals] grants for %s: %v", id, err)
			writeJSONErr(w, http.StatusInternalServerError, "could not read grants")
			return
		}
		// A COPY, not a mutation of the stripped record: `PublicUsers` returns
		// maps this route does not own, and adding a key to one would be a
		// surprise for any future caller sharing the slice.
		row := make(map[string]any, len(u)+1)
		for k, v := range u {
			row[k] = v
		}
		row["grants"] = grants
		out = append(out, row)
	}
	writeJSON(w, map[string]any{"ok": true, "users": out})
}

// principalsGuard is `Rbac.requireGlobalAdmin` for a GET.
//
// FAILS CLOSED, including when the resolver is unavailable: the principal graph
// is the answer to "who may do what", and serving it to somebody whose access
// could not be determined is the one outcome worth refusing outright.
func (s *Server) principalsGuard(h func(http.ResponseWriter, *http.Request, *Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.auth.Validate(r.Header.Get("Cookie"))
		if err != nil {
			writeJSONErr(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if !s.isGlobalAdmin(sess) {
			// No audit: this is a GET. See the file header.
			writeJSONErr(w, http.StatusForbidden, "Administrator access required")
			return
		}
		if s.auditDB == nil {
			writeJSONErr(w, http.StatusServiceUnavailable, "principal store unavailable")
			return
		}
		h(w, r, sess)
	}
}

func (s *Server) isGlobalAdmin(sess *Session) bool {
	if sess.AuthMode == "none" {
		return true
	}
	if s.rbac == nil {
		return false
	}
	ok, err := s.rbac.Can(s.userIDFor(sess.Username), "system:principals", "")
	if err != nil {
		log.Printf("[principals] permission check failed for %s: %v", sess.Username, err)
		return false
	}
	return ok
}

// groupsGet joins each group to its members and its grants.
//
// THE JOIN IS THE POINT, and the live comment says so: the same shape is used
// for users "so the two principal types stay symmetric". A group without its
// grants renders as a group with no access.
func (s *Server) groupsGet(w http.ResponseWriter, _ *http.Request, _ *Session) {
	groups, err := s.auditDB.ListGroups()
	if err != nil {
		log.Printf("[principals] groups: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read groups")
		return
	}

	out := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		members, err := s.auditDB.GroupMembers(g.ID)
		if err != nil {
			log.Printf("[principals] members of %s: %v", g.ID, err)
			writeJSONErr(w, http.StatusInternalServerError, "could not read group members")
			return
		}
		grants, err := s.auditDB.ListGrants(db.GrantFilter{PrincipalType: "group", PrincipalID: g.ID})
		if err != nil {
			log.Printf("[principals] grants for %s: %v", g.ID, err)
			writeJSONErr(w, http.StatusInternalServerError, "could not read grants")
			return
		}
		out = append(out, map[string]any{
			"id": g.ID, "name": g.Name, "description": g.Description, "created_at": g.CreatedAt,
			"memberUserIds": members, "grants": grants,
		})
	}
	writeJSON(w, map[string]any{"ok": true, "groups": out})
}

// rolesGet is `_roleView` over every role, plus the two catalogues the card
// needs to draw its matrix.
//
// `writeCapablePages` IS DERIVED FROM THE PROJECTION TABLE, never restated — the
// live comment calls it "what greys out a Write toggle that would confer
// nothing". Restating it here would be a second list that can disagree with the
// one actually consulted when a grant is evaluated.
func (s *Server) rolesGet(w http.ResponseWriter, _ *http.Request, _ *Session) {
	roles, err := s.auditDB.ListRoles()
	if err != nil {
		log.Printf("[principals] roles: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read roles")
		return
	}

	out := make([]map[string]any, 0, len(roles))
	for _, r := range roles {
		view, err := s.roleView(r)
		if err != nil {
			log.Printf("[principals] view of %s: %v", r.ID, err)
			writeJSONErr(w, http.StatusInternalServerError, "could not read roles")
			return
		}
		out = append(out, view)
	}

	writeJSON(w, map[string]any{
		"ok": true, "roles": out, "pages": pageCatalogue,
		"writeCapablePages": rbac.WriteCapablePages(),
	})
}

// roleView is the live `_roleView`:
//
//	const _roleView = (r) => ({ ...r, builtin: !!r.builtin,
//	                            pages: db.rolePages(r.id),
//	                            grants: db.countGrantsForRole(r.id) });
//
// ONE implementation, shared by the read endpoint and the three write routes.
// It was inline in `rolesGet` until the write routes needed it; a second copy is
// how the page remapping below would have been fixed in one place and not the
// other.
func (s *Server) roleView(r db.RoleRow) (map[string]any, error) {
	pages, err := s.auditDB.RolePages(r.ID)
	if err != nil {
		return nil, err
	}
	n, err := s.auditDB.CountGrantsForRole(r.ID)
	if err != nil {
		return nil, err
	}
	// THE PAGES ARE REMAPPED, not passed through.
	//
	// `db.RolePage` is an internal decision type with no JSON tags, so
	// marshalling it directly emitted `{"Page":…,"Access":…}` — capitalised,
	// because Go falls back to the FIELD NAME. The live app sends
	// `{"page":…,"access":…}`, and the Access Management card reads `p.page`,
	// which was `undefined` for every row.
	//
	// Found by live verification on 2026-08-28, and only after the URLs used to
	// reach this endpoint were corrected: the earlier run had asked
	// `/api/principals/roles`, which neither server serves, and two 404s
	// compared equal.
	//
	// Remapped HERE rather than tagging the struct: `RolePage` answers
	// `canPage`, and giving an internal type JSON tags for one endpoint's
	// benefit is how a decision type quietly becomes a wire format.
	pageRows := make([]map[string]any, 0, len(pages))
	for _, p := range pages {
		pageRows = append(pageRows, map[string]any{"page": p.Page, "access": p.Access})
	}
	return map[string]any{
		"id": r.ID, "name": r.Name, "description": r.Description,
		"builtin": r.Builtin, "created_at": r.CreatedAt,
		"pages": pageRows, "grants": n,
	}, nil
}

// grantsGet lists grants, narrowed by the two query parameters the original
// accepts. An absent parameter is not a filter — see db.GrantFilter.
func (s *Server) grantsGet(w http.ResponseWriter, r *http.Request, _ *Session) {
	q := r.URL.Query()
	grants, err := s.auditDB.ListGrants(db.GrantFilter{
		PrincipalType: q.Get("principalType"),
		PrincipalID:   q.Get("principalId"),
	})
	if err != nil {
		log.Printf("[principals] grants: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read grants")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "grants": grants})
}
