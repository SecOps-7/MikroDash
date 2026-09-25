package store

// `updateUser` and `deleteUser` — the two writes the Settings page's user form
// needs and this port did not have.
//
// ── EVERY FIELD IS OPTIONAL, AND "ABSENT" IS NOT "EMPTY" ────────────────────
//
// The live signature is `updateUser(id, updates)` over a plain object, and every
// branch is guarded:
//
//	if (updates.username !== undefined) ...
//	if (updates.role     !== undefined) ...
//	if (Array.isArray(updates.allowedRouterIds)) ...
//	if (updates.password !== undefined && updates.password !== '') ...
//
// A Go struct of plain fields cannot express that: the zero value would be
// indistinguishable from "not sent", and a form that submits only the changed
// field would blank the other three. Hence `UserUpdates`, where every field is a
// POINTER and nil means absent.
//
// ── AN EMPTY PASSWORD LEAVES THE CREDENTIAL ALONE ───────────────────────────
//
// `updates.password !== ''` is the second half of that guard, and it is the most
// dangerous rule in the function. The edit form renders an EMPTY password box
// every time it opens, so it submits `""` on any save where the operator did not
// type a new one. A port treating `""` as a value would hash the empty string
// and lock the account out of its own password on every unrelated edit — a
// rename, a role change, a router-list edit.
//
// So the guard is two conditions, not one: the pointer must be non-nil AND the
// value must be non-empty.
//
// ── allowedRouterIds IS ARRAY-GUARDED, WHICH IS A DIFFERENT RULE ────────────
//
// `Array.isArray(...)`, not `!== undefined` — four lines from three fields that
// use the other test. A string, a number or null is IGNORED rather than stored
// or cleared. In Go the pointer already carries "absent", and the array-ness is
// the HTTP layer's job: a JSON body sending `"allowedRouterIds": "rtr-1"` must
// decode to a nil pointer here, not to an error and not to a one-element list.
// `internal/server` owns that; the userwrite corpus pins the three shapes.
//
// ── AN INVALID ROLE RAISES; IT DOES NOT CLAMP ───────────────────────────────
//
// `_validRole` throws, so the write does not happen and the caller answers 400.
// A port falling back to "viewer" would silently demote somebody, and a port
// falling back to the EXISTING role would silently ignore a typo that the
// operator would then have to notice for themselves.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// UserUpdates is one call's patch. A nil field was not sent and is left alone.
type UserUpdates struct {
	Username *string
	Role     *string
	// AllowedRouterIDs nil means "not sent". A non-nil pointer to an EMPTY slice
	// means "remove every router", which is a real thing an operator does and is
	// why this cannot be a plain `[]string` — nil and empty would collapse.
	AllowedRouterIDs *[]string
	// Password empty means "leave it alone", exactly as the live guard does. See
	// the header: this is the rule that would otherwise wipe a credential on
	// every save of the edit form.
	Password *string
}

// UpdateUser applies a patch to one record and returns its public view.
//
// Returns ErrNoSuchUser when the id names nothing — the live function answers
// `null` there and the caller turns it into a 404.
func (s *Store) UpdateUser(id string, up UserUpdates) (map[string]any, error) {
	if id == "" {
		return nil, ErrNoSuchUser
	}

	path := filepath.Join(s.Dir, "users.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// `[]json.RawMessage`, so every record this call does not touch reaches disk
	// exactly as it was read — the rule `users_write.go`'s header sets out, and
	// the reason it is there is that Go sorts map keys and would otherwise
	// rewrite every user's field order on one edit.
	var records []json.RawMessage
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, fmt.Errorf("store: users.json is not a bare array: %w", err)
	}

	found := -1
	var rec map[string]any
	for i, r := range records {
		var probe struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(r, &probe); err != nil {
			// A record whose id will not even decode is left alone rather than
			// dropped. It is somebody's account, and the failure is ours.
			continue
		}
		if probe.ID == id {
			if err := json.Unmarshal(r, &rec); err != nil {
				return nil, fmt.Errorf("store: user %s: %w", id, err)
			}
			found = i
			break
		}
	}
	if found < 0 {
		return nil, ErrNoSuchUser
	}

	if up.Username != nil {
		// TRIMMED, as `String(updates.username).trim()` does. The live call also
		// stringifies, which Go's type does for us.
		rec["username"] = strings.TrimSpace(*up.Username)
	}
	if up.Role != nil {
		// ── VALIDATED HERE, AFTER THE LOOKUP, AND THE ORDER IS OBSERVABLE ──
		//
		// This validated BEFORE reading the file until 2026-08-28, on the
		// reasoning `CreateUser` gives — refuse before writing anything. That
		// reasoning is sound and it is NOT what the live function does:
		//
		//	const idx = users.findIndex(u => u.id === id);
		//	if (idx === -1) return null;          <- the lookup comes first
		//	...
		//	if (updates.role !== undefined) updated.role = _validRole(...);
		//
		// So an unknown id with an invalid role answers "no such user" on the
		// live side, not "invalid role". Nothing is written either way — the
		// throw happens before `_writeFile` — so the safety argument is
		// unaffected; only which error comes back changes.
		//
		// It was found by mutation: with the ORDER wrong here, the HTTP route's
		// own role check became redundant and deleting it survived the suite.
		// See `internal/server/users_write_api.go`.
		v, err := validRole(*up.Role)
		if err != nil {
			return nil, err
		}
		rec["role"] = v
	}
	if up.AllowedRouterIDs != nil {
		ids := *up.AllowedRouterIDs
		if ids == nil {
			// A nil slice encodes as JSON `null`; the live field is always an
			// array. Only reachable via a pointer to a nil slice, which is
			// exactly what a caller writing `&someNilSlice` produces.
			ids = []string{}
		}
		rec["allowedRouterIds"] = ids
	}
	// THE TWO-CONDITION GUARD. See the header.
	if up.Password != nil && *up.Password != "" {
		saltBytes := make([]byte, 32)
		if _, err := rand.Read(saltBytes); err != nil {
			// An error, never a fallback — `CreateUser` says why: a predictable
			// salt is worse than a failed write, because the operator retries the
			// second and never learns about the first.
			return nil, err
		}
		salt := hex.EncodeToString(saltBytes)
		hash := HashPassword(*up.Password, salt)
		if hash == "" {
			return nil, errors.New("store: could not hash the password")
		}
		// BOTH, together. A new salt with the old hash is an account nobody can
		// log into, and the old salt with a new hash lets anyone who saw an
		// earlier hash confirm a reverted password.
		rec["salt"] = salt
		rec["passwordHash"] = hash
	}

	// NOTHING TOUCHES `createdAt` or `id`. They are not in the live update path
	// at all, and a port that rebuilt the record from a struct would stamp
	// `createdAt` anew — which is invisible until a date is rendered.

	encoded, err := encodeRecord(rec)
	if err != nil {
		return nil, err
	}
	records[found] = encoded
	out, err := encodeDataFile(records)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, out, 0o600); err != nil {
		return nil, err
	}
	// THE PUBLIC VIEW, through `PublicUser` rather than assembled here — so a
	// field added to the record appears automatically, which is what the live
	// denylist (`const { passwordHash, salt, ...pub } = user`) guarantees and a
	// hand-written list would not.
	return PublicUser(rec), nil
}

// DeleteUser removes one record.
//
// Returns whether anything was removed, matching the live `deleteUser`, which
// answers `false` for an unknown id rather than raising. The caller turns false
// into a 404.
//
// ── IT DOES NOT CHECK FOR THE LAST ADMINISTRATOR ────────────────────────────
//
// Neither does the live function. That check lives in the HTTP layer
// (`db.WouldOrphanGlobalAdmin`, already ported), and putting a second copy here
// would be two implementations of one rule — the failure this repo keeps
// finding. What this must not do is make the check impossible to apply, which is
// why it reports what it did rather than silently succeeding.
func (s *Store) DeleteUser(id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	path := filepath.Join(s.Dir, "users.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var records []json.RawMessage
	if err := json.Unmarshal(raw, &records); err != nil {
		return false, fmt.Errorf("store: users.json is not a bare array: %w", err)
	}

	kept := make([]json.RawMessage, 0, len(records))
	for _, r := range records {
		var probe struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(r, &probe); err == nil && probe.ID == id {
			continue
		}
		kept = append(kept, r)
	}
	if len(kept) == len(records) {
		return false, nil
	}

	out, err := encodeDataFile(kept)
	if err != nil {
		return false, err
	}
	if err := writeFileAtomic(path, out, 0o600); err != nil {
		return false, err
	}
	return true, nil
}
