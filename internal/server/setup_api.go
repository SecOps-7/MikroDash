package server

// `POST /api/users/setup` — create the first administrator.
//
// ── AN UNAUTHENTICATED ROUTE THAT MINTS AN ADMINISTRATOR ────────────────────
//
// There is no session to check, because on a fresh install nobody can have one.
// The only things between a stranger and ownership of the instance are the gates
// below, and each is load-bearing:
//
//	zero users   the route closes for ever once one exists
//	the latch    two concurrent requests must not both pass "zero users"
//	the limiter  five a minute, because every attempt is a guess at ownership
//
// The live comment on the latch is exact: "createUser is async, so two
// concurrent requests could both pass the userCount()===0 check before either
// writes. The synchronous latch closes that race so only the first request can
// create the initial admin."
//
// ── REGISTERED ONLY IN STANDALONE MODE ──────────────────────────────────────
//
// `users.js` caches the file at first read and never re-reads it — the same
// `if (_cache) return _cache` pattern as `settings.js` and `routers.js`. A Go
// write is therefore invisible to a running Node, so with both processes up BOTH
// would see zero users and BOTH would mint a first administrator.
//
// That is not a real risk on an install that already has users, since the route
// refuses immediately — but it is exactly the risk on the one install where this
// route does anything at all. Same conditional registration `auth_login.go` uses,
// and `endpoint-audit` cannot see either: a ledger over source text is the wrong
// instrument for a runtime branch, so a Go test pins it instead.

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"sync"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/rbac"
	"mikrodash/internal/safe"
	"mikrodash/internal/store"
)

// usernameRe is `/^[a-zA-Z0-9_.\-]{1,64}$/` — letters, digits, underscore, dot
// and hyphen, and NOT a space. Anchored at both ends, which is the part that
// matters: an unanchored copy accepts anything CONTAINING a valid run, so
// "bad name" would pass.
var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_.\-]{1,64}$`)

// `minPasswordLen` is declared in account_api.go and SHARED deliberately: both
// routes enforce the same live floor (`String(password).length < 4`), so two
// copies would be two places for it to drift. Four is not much and is not this
// port's decision to revise — raising it would refuse a password the live app
// accepts, on the one screen an operator cannot get past.

func (s *Server) registerSetup(mux *http.ServeMux) {
	// FIVE A MINUTE, matching `setupLimiter` — twelve times tighter than the
	// write routes, because this one is reachable without a session.
	lim := newRateLimiter(5, time.Minute).limit
	mux.HandleFunc("POST /api/users/setup", lim(s.usersSetup))
}

// setupMu replaces the live `_setupClaimed` boolean.
//
// ── A MUTEX, NOT A FLAG, AND THE DIFFERENCE IS DELIBERATE ───────────────────
//
// The JavaScript latch exists because `createUser` is async and the check and
// the write are separated by an `await`: a synchronous flag is the only thing
// that can be claimed between them. Go has no such gap inside one handler, but
// it has real concurrency — two goroutines genuinely run at once where two Node
// requests only interleave — so a flag alone would be WEAKER here, not
// equivalent.
//
// Holding a lock across count-then-create gives the same observable: exactly one
// administrator. It also gets retry-after-failure for free, which the live code
// arranges by hand (`_setupClaimed = false` in a catch) and which is easy to
// lose — a claim that is never released turns one failed attempt into a
// permanently unusable install.
var setupMu sync.Mutex

func (s *Server) usersSetup(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "user store unavailable")
		return
	}

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	// A malformed body reads as an empty one, exactly as `req.body || {}` does:
	// the checks below refuse it on the username, which is the answer the live
	// route gives.
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&body)

	// THE LOCK COVERS THE COUNT AND THE CREATE. Taking it later would leave
	// exactly the window the live latch exists to close.
	setupMu.Lock()
	defer setupMu.Unlock()

	n, err := s.store.UserCount()
	if err != nil {
		// A users.json that cannot be read is NOT zero users. The live
		// `_readFile` returns `[]` here, so the route would open on a corrupted
		// file; `store.UserCount` returns the error instead, and this is where
		// that decision is spent.
		log.Printf("[setup] counting users: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, safe.Message(err.Error()))
		return
	}
	if n > 0 {
		// 409, and the live wording. Not 403: nothing is forbidden to this
		// caller, the state simply no longer exists.
		writeJSONErr(w, http.StatusConflict, "Setup already complete")
		return
	}

	// THE ORDER OF THESE TWO IS THE LIVE ORDER, and it shows: a request with a
	// bad username AND a short password is told about the username.
	if !usernameRe.MatchString(body.Username) {
		writeJSONErr(w, http.StatusBadRequest, "Invalid username")
		return
	}
	if len(body.Password) < minPasswordLen {
		writeJSONErr(w, http.StatusBadRequest, "Password too short")
		return
	}

	user, err := s.store.CreateUser(store.NewUser{
		Username: body.Username,
		Password: body.Password,
		// ADMIN, hard-coded. This creates the FIRST account, and an install whose
		// only user is a viewer has nobody who can promote them.
		Role: "admin",
		// EMPTY MEANS UNRESTRICTED once it reaches the grant planner — see the
		// inversion note in internal/rbac/syncgrants.go. It is what gives the
		// first administrator a global grant rather than none.
		AllowedRouterIDs: []string{},
	})
	if err != nil {
		log.Printf("[setup] creating the first administrator: %v", err)
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}

	// ── THE GRANTS, WITHOUT WHICH THE ACCOUNT IS USELESS ────────────────────
	//
	// The live comment: "Without this the very first administrator of a fresh
	// install holds no grants, and every guard refuses them — locked out of
	// their own instance the moment setup completes."
	//
	// It runs AFTER the user exists and its failure does NOT undo the user. That
	// matches the live route and is the better of two bad outcomes: an account
	// with no grants can be repaired by someone with database access, while a
	// deleted account on a claimed install cannot be recreated through this
	// route at all — it has already closed.
	id, _ := user["id"].(string)
	name, _ := user["username"].(string)
	if gerr := s.grantFirstAdmin(id, name); gerr != nil {
		log.Printf("[setup] granting %s: %v — the account exists but holds no grants", name, gerr)
	}

	// A PUBLIC ROUTE THAT MINTS AN ADMINISTRATOR, and the live repo records that
	// it "had no record of any kind" until this event was added. `ForLogin` is
	// the right actor: there is no session, and the id column stays null because
	// the account being named is the one just created.
	s.loginRecorder(r, name).Record(audit.Event{
		Action: "auth.setup", TargetType: "user", TargetID: id, TargetName: name,
		Note: "initial administrator created",
	})

	writeJSON(w, map[string]any{"ok": true, "user": user})
}

// grantFirstAdmin applies `Rbac.syncUserGrants` to the account just created.
//
// The DECISION is `rbac.PlanUserGrants`, which is pure and pinned against the
// live implementation; this is only the part that writes. Splitting them is what
// let the decision be tested at all — the writes need a database and the
// decision needed twenty-three cases.
func (s *Server) grantFirstAdmin(userID, username string) error {
	if s.auditDB == nil {
		// No database means no grants table. Worth naming rather than silently
		// succeeding: the account exists and can do nothing.
		return errNoGrantStore
	}

	// The live `new Set(Routers.loadAll().map(r => r.id))`. On a fresh install
	// this is empty, which is fine — an empty `allowedRouterIds` never consults
	// it and produces one global grant.
	live := map[string]bool{}
	if s.store != nil {
		all, _ := s.store.Routers()
		for _, rec := range all {
			live[rec.ID] = true
		}
	}

	plan := rbac.PlanUserGrants(rbac.UserForGrants{
		ID: userID, Username: username, Role: "admin",
		AllowedRouterIDs: []string{},
	}, live)
	for _, warn := range plan.Warnings {
		log.Print(warn)
	}

	for _, step := range plan.Steps {
		switch step.Op {
		case "delete":
			// Unconditional in the live function, and kept: here it deletes
			// nothing, but a caller that skipped a step would not be applying the
			// plan it asked for.
			if _, err := s.auditDB.DeleteGrantsForPrincipal("user", userID); err != nil {
				return err
			}
		case "upsert":
			if err := s.auditDB.UpsertGrant(db.GrantSpec{
				PrincipalType: "user", PrincipalID: userID,
				Role: step.Role, ScopeType: step.ScopeType, ScopeID: step.ScopeID,
				CreatedBy: username,
			}); err != nil {
				return err
			}
		}
	}
	if plan.Made == 0 {
		// Zero grants is the LOCKOUT case. It cannot happen for an empty router
		// list, so reaching it means the plan changed underneath this caller.
		return errNoGrantsMade
	}
	return nil
}

// Two errors that are only ever logged, never compared against — so a string
// type is the whole of what the job needs.
type setupErr string

func (e setupErr) Error() string { return string(e) }

const (
	errNoGrantStore = setupErr("setup: no database, so the new administrator holds no grants")
	errNoGrantsMade = setupErr("setup: the grant plan produced nothing, so the account is locked out")
)
