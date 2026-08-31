package store

// `createUser` — appending a user to users.json.
//
// ── THE SECOND WRITE THIS PORT MAKES TO THAT FILE ───────────────────────────
//
// `users_write.go` changes an existing user's password. This one adds a record,
// and it exists for `POST /api/users/setup`: the UNAUTHENTICATED route that
// mints the first administrator of a fresh install. That route is reachable only
// when the file holds zero users, so it is a cutover-only path — and it is also
// the single most consequential write in the application, which is what shapes
// everything below.
//
// ── A STRUCT, NOT A MAP, AND FOR ONE REASON ─────────────────────────────────
//
// Everywhere else in this package a record is `map[string]any`, because a writer
// that decodes into a struct it did not define rewrites the parts of the
// document it does not know about. That argument does not apply here: this
// record does not exist yet, so there is nothing to preserve.
//
// The positive reason is key ORDER. Go sorts map keys and `JSON.stringify` does
// not, which `internal/store/jsonwrite.go` records as a known difference — but a
// STRUCT encodes in field-declaration order, so the seven fields below reach
// disk in exactly the order `users.js` writes them. This is the one place the
// port can close that gap for free, so it is closed.
//
// The order is neither decorative nor guessed: `tools/users-create-cases.js`
// RUNS the live `createUser` against a throwaway directory and records the key
// order of the file it produced.
//
// ── VALIDATION THROWS; IT DOES NOT DEFAULT ──────────────────────────────────
//
// `_validRole` refuses an unrecognised role AND an absent one, and writes
// nothing. `rbac.PlanUserGrants` maps an unrecognised role to `viewer` instead.
// Both are deliberate — validation at the write boundary, least privilege at the
// read boundary — and a port sharing one helper between them would either refuse
// a user the live app creates or create one it refuses.
//
// The live comment says why the throw exists: both call sites once read
// `role === 'viewer' ? 'viewer' : 'admin'`, so ANY unrecognised value became an
// administrator — "a typo, a stale client, or a role added later and not yet
// wired up here".

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Roles, in ascending privilege, taken from `users.js`'s exported `ROLES` and
// pinned against it by the generated corpus. A hand-maintained list would
// silently reject a role added upstream — which is how `operator`, the third and
// most recent, would have been refused while the form kept offering it.
var Roles = []string{"viewer", "operator", "admin"}

// ErrInvalidRole carries the live message, which NAMES THE VALID LIST. That is
// how an operator learns a role was added, and it is the only feedback the setup
// wizard can give.
type ErrInvalidRole struct{ Role string }

func (e *ErrInvalidRole) Error() string {
	return "Invalid role: " + e.Role + " (expected one of " + strings.Join(Roles, ", ") + ")"
}

// ErrUsernameTaken is for the HTTP layer, not for this function. See CreateUser.
var ErrUsernameTaken = errors.New("store: username already exists")

// newUserRecord is the seven fields users.json holds, IN ORDER.
//
// `store.User` models five and is the wrong type here for the reason its own
// comment gives: `allowedRouterIds` is the legacy per-user access list, so
// losing it changes who can see what.
//
// AllowedRouterIDs is never nil by the time it is encoded: Go writes a nil slice
// as `null` and `users.js` writes `[]`. A `null` is read back by
// `Array.isArray(x) ? x : []` as unrestricted — the same answer by luck rather
// than by agreement — and by anything less careful as a crash.
type newUserRecord struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
	Salt         string `json:"salt"`
	Role         string `json:"role"`
	// The hash and salt sit BETWEEN username and role in the live record.
	// Nothing depends on that and it costs nothing to match.
	AllowedRouterIDs []string `json:"allowedRouterIds"`
	CreatedAt        int64    `json:"createdAt"`
}

// The PUBLIC VIEW is not built here. `PublicUser` in `users_public.go` is
// already the port of `_toPublic` — a denylist of two over a map, keeping every
// other field including ones no struct declares — and CreateUser derives its
// return value by round-tripping the record through it.
//
// Hand-listing the five public fields instead would work today and would drop a
// field the moment the record gained one, which is the exact failure that
// denylist exists to prevent. One decision, one implementation.

// NewUser is the argument shape, matching the live destructuring.
type NewUser struct {
	Username string
	Password string
	Role     string
	// AllowedRouterIDs empty or nil means UNRESTRICTED once it reaches
	// `rbac.PlanUserGrants`. See the inversion note there; nothing about that
	// decision is made here.
	AllowedRouterIDs []string
}

// CreateUser appends a user and returns the public view.
//
// NO UNIQUENESS CHECK, because the live `createUser` has none: it pushes and
// writes. Adding one would refuse a write the live app accepts, which is a
// behaviour change even though the behaviour looks like a defect — the live app
// checks at the HTTP layer, and `ErrUsernameTaken` exists for that caller to use
// rather than for this function to enforce.
func (s *Store) CreateUser(in NewUser) (map[string]any, error) {
	role, err := validRole(in.Role)
	if err != nil {
		// REFUSED BEFORE ANYTHING IS READ OR WRITTEN. A port that appended and
		// then validated would leave a user with an invalid role in the file, and
		// this check is the only thing between a typo and an administrator.
		return nil, err
	}

	saltBytes := make([]byte, 32)
	if _, err := rand.Read(saltBytes); err != nil {
		// An error, never a fallback: a predictable salt is worse than a failed
		// creation, because the operator retries the second and never learns
		// about the first.
		return nil, err
	}
	salt := hex.EncodeToString(saltBytes)
	hash := HashPassword(in.Password, salt)
	if hash == "" {
		return nil, errors.New("store: could not hash the password")
	}
	id, err := newUUID()
	if err != nil {
		return nil, err
	}

	ids := in.AllowedRouterIDs
	if ids == nil {
		ids = []string{}
	}
	rec := newUserRecord{
		ID:               id,
		Username:         strings.TrimSpace(in.Username),
		PasswordHash:     hash,
		Salt:             salt,
		Role:             role,
		AllowedRouterIDs: ids,
		// `Date.now()` — a millisecond epoch, not RFC 3339. A port reaching for
		// `time.Now().Format(...)` writes a string `users.js` reads back as NaN,
		// and nothing would notice until a date was rendered.
		CreatedAt: time.Now().UnixMilli(),
	}

	path := filepath.Join(s.Dir, "users.json")
	// `[]json.RawMessage`, so every existing record reaches disk exactly as it
	// was read — the rule `users_write.go` and the router writers follow.
	var records []json.RawMessage
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &records); err != nil {
			// A file that will not parse is NOT overwritten. `_readFile` returns
			// `[]` for an unreadable one, so the live app would create a first
			// administrator over the top of it; doing the same here means a
			// corrupted users.json silently becomes an empty one, and an
			// unauthenticated route is what fills it.
			return nil, fmt.Errorf("store: users.json is not a bare array: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	encoded, err := encodeRecord(rec)
	if err != nil {
		return nil, err
	}
	out, err := encodeDataFile(append(records, encoded))
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, out, 0o600); err != nil {
		return nil, err
	}

	// THE PUBLIC VIEW, derived from the record that was just written rather than
	// assembled beside it. `PublicUser` is the ported `_toPublic`; going through
	// it means a field added to `newUserRecord` appears here automatically,
	// which is what the live denylist guarantees and a hand-written list would
	// not.
	var m map[string]any
	if err := json.Unmarshal(encoded, &m); err != nil {
		return nil, err
	}
	return PublicUser(m), nil
}

// UserCount is `userCount()`: how many records the file holds.
//
// It is what makes `POST /api/users/setup` refuse a second call, so A READ ERROR
// MUST NOT READ AS ZERO. The live `_readFile` returns `[]` for an unreadable
// file and `userCount` therefore answers 0, which re-opens that route; this
// returns the error instead and leaves the refusal to the caller.
func (s *Store) UserCount() (int, error) {
	raw, err := os.ReadFile(filepath.Join(s.Dir, "users.json"))
	if os.IsNotExist(err) {
		// A missing file genuinely is zero users, and is the state a fresh
		// install starts in — the one state setup is FOR.
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var records []json.RawMessage
	if err := json.Unmarshal(raw, &records); err != nil {
		return 0, fmt.Errorf("store: users.json is not a bare array: %w", err)
	}
	return len(records), nil
}

// validRole is `_validRole`: an exact match, refusing rather than defaulting.
func validRole(role string) (string, error) {
	for _, r := range Roles {
		if r == role {
			return role, nil
		}
	}
	// The live message for an ABSENT role is `Invalid role: undefined`, because
	// `String(undefined)` is "undefined". Go has no undefined and an absent JSON
	// field decodes to "", so the two disagree on that one word. The corpus
	// records the live text; the test asserts the SHAPE and the valid list rather
	// than the word, and says why.
	return "", &ErrInvalidRole{Role: role}
}
