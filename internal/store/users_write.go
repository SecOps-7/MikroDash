package store

// The ONE write this port makes to users.json: a password change.
//
// ── IT WORKS ON RAW RECORDS, NOT ON store.User, AND THAT IS THE POINT ───────
//
// `store.User` models five fields. The real file carries SEVEN — `createdAt`
// and `allowedRouterIds` as well — and a round trip through the struct would
// silently drop both from every user in the file. `allowedRouterIds` is the
// legacy per-user access list, so losing it changes who can see what.
//
// The rule generalises past this file: a writer that decodes into a struct it
// did not define rewrites the parts of the document it does not know about. So
// this decodes into `[]map[string]any`, touches exactly two keys on exactly one
// record, and re-encodes the rest untouched.
//
// ── AND IT STAYS A BARE JSON ARRAY ──────────────────────────────────────────
//
// `_readFile()` in users.js returns `[]` for anything that is not an array. A
// version wrapper, or a move into SQLite, makes a rolled-back binary read ZERO
// users — which re-opens `POST /api/users/setup`, an UNAUTHENTICATED route that
// claims the instance. That is a security property, not a formatting preference.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNoSuchUser is returned when the id names no record.
var ErrNoSuchUser = errors.New("store: no such user")

// SetPassword replaces one user's password, minting a fresh salt.
//
// A NEW SALT EVERY TIME, matching `updateUser`: reusing the old one would make
// two users who chose the same password share a hash, and would let anyone who
// saw an earlier hash confirm a reverted password.
func (s *Store) SetPassword(userID, newPassword string) error {
	if userID == "" {
		return ErrNoSuchUser
	}
	path := filepath.Join(s.Dir, "users.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// `[]json.RawMessage`, so every record this call does not touch reaches disk
	// EXACTLY as it was read.
	//
	// It was `[]map[string]any` until 2026-08-27, which decoded and re-encoded
	// all of them — and Go sorts map keys, so one password change rewrote every
	// user's field order. Harmless to Node, which parses the file, and a maximal
	// diff on the one file whose contents nobody wants to have to eyeball.
	// `write.go` and `routeradd.go` already worked this way; this was the outlier.
	var records []json.RawMessage
	if err := json.Unmarshal(raw, &records); err != nil {
		return fmt.Errorf("store: users.json is not a bare array: %w", err)
	}

	found := -1
	var rec map[string]any
	for i, raw := range records {
		var probe struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			// A record that will not decode is left alone rather than dropped.
			// Failing the whole write here would lock everybody out over one
			// malformed entry; skipping it loses nobody, because a record with no
			// readable id is not the one being asked for.
			continue
		}
		if probe.ID == userID {
			// The ONE record that is rewritten. `map[string]any`, never `User` —
			// see the file header: the struct models five fields and the file
			// carries seven.
			if err := json.Unmarshal(raw, &rec); err != nil {
				return fmt.Errorf("store: user %s: %w", userID, err)
			}
			found = i
			break
		}
	}
	if found < 0 {
		return ErrNoSuchUser
	}

	saltBytes := make([]byte, 32)
	if _, err := rand.Read(saltBytes); err != nil {
		// AN ERROR, NEVER A FALLBACK. A predictable salt is worse than a failed
		// password change: the operator retries the second and never learns
		// about the first.
		return err
	}
	salt := hex.EncodeToString(saltBytes)
	hash := HashPassword(newPassword, salt)
	if hash == "" {
		return errors.New("store: could not hash the new password")
	}
	// EXACTLY TWO KEYS. Everything else in the record, known to this port or
	// not, is carried through untouched.
	rec["salt"] = salt
	rec["passwordHash"] = hash

	encoded, err := encodeRecord(rec)
	if err != nil {
		return err
	}
	records[found] = encoded

	// TWO-SPACE INDENT, NO TRAILING NEWLINE, and `&` left alone — all three are
	// what `JSON.stringify(users, null, 2)` produces, and none of them was true
	// of `json.MarshalIndent` plus a newline. Not cosmetic while both processes
	// may write the file: a whole-file rewrite in a different shape makes every
	// future diff unreadable and hides what actually changed. See
	// internal/store/jsonwrite.go, which is where that reasoning now lives for
	// all four writers rather than in this one comment.
	out, err := encodeDataFile(records)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, out, 0o600)
}

// writeFileAtomic is `_writeFile`: write a temporary file, then rename.
//
// ── MODE 0600, AND THE RENAME IS NOT A DETAIL ───────────────────────────────
//
// The file holds scrypt hashes and salts, so it is owner-only — the live
// comment says so at the same line. And a plain truncating write leaves the file
// EMPTY for the moment between truncate and flush; a reader arriving there sees
// no users, and `users.js` treats an unreadable file as `[]`, which re-opens the
// unauthenticated setup route. Rename is atomic on the same filesystem, so a
// reader sees either the old file or the new one.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	// The temporary file is cleaned up on a failed rename, or a crashed write
	// leaves a `.tmp` beside the real one carrying the same hashes.
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
