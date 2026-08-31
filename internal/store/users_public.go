package store

// What a user record may be shown as.
//
// ── A DENYLIST OF TWO, AND THAT IS WHY THIS IS NOT THE TYPED User ──────────
//
// `_toPublic` in src/users.js is `const { passwordHash, salt, ...pub } = user`.
// Two fields removed, EVERYTHING ELSE KEPT — including fields no struct here
// declares. Real records carry `allowedRouterIds` and `createdAt`, and the shape
// is free to grow.
//
// The typed `User` above models five fields, which makes it the wrong tool for
// this endpoint twice over: it would DROP whatever it does not declare, and it
// would ADD zero values for fields a particular record does not have. Both are
// visible on the Users card — a missing `allowedRouterIds` renders as no access,
// and an invented empty one renders as access to nothing, which are different
// claims about a person's account.
//
// So this works over the raw JSON. `User` stays for the paths that need typed
// fields (VerifyPassword, the id lookup); it is not being replaced.

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// UserSecretFields are the two fields never disclosed.
//
// A DENYLIST IS CORRECT HERE, unlike the settings viewer payload, and the
// difference is worth stating: a settings key added upstream is a new disclosure
// decision, so that one is an allow-list. A user record's fields are all
// displayable by construction except the credential material — so a field added
// upstream SHOULD appear, and an allow-list would silently hide it.
var UserSecretFields = []string{"passwordHash", "salt"}

// PublicUsers reads users.json and strips the credential material.
//
// Returns an empty slice rather than nil for an empty file, so the payload is
// `[]` and not `null` — the card renders the two differently.
func (s *Store) PublicUsers() ([]map[string]any, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, "users.json"))
	if err != nil {
		return nil, err
	}
	var raw []map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		// Same reasoning as Users(): a bare array is a security property, and
		// silently seeing zero users is the failure that rule exists to prevent.
		return nil, err
	}

	out := make([]map[string]any, 0, len(raw))
	for _, u := range raw {
		out = append(out, PublicUser(u))
	}
	return out, nil
}

// PublicUser strips one record.
//
// A COPY, not a delete in place. Deleting from the caller's map would remove the
// hash from whatever else holds that record — and the one caller that matters
// reads users.json to verify a password.
func PublicUser(u map[string]any) map[string]any {
	pub := make(map[string]any, len(u))
	for k, v := range u {
		pub[k] = v
	}
	for _, secret := range UserSecretFields {
		delete(pub, secret)
	}
	return pub
}
