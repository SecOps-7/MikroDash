package store

// What `Routers.getPublic()` discloses.
//
// ── THE PORT WAS SENDING ELEVEN FIELDS OF TWENTY-THREE ──────────────────────
//
// Found by live verification on 2026-08-28: the Go server and Node were asked
// for `/api/routers` with the same cookie against the same /data, and the port's
// answer was missing twelve keys —
//
//	addedAt  alertsEnabled  backup  connDownThresholdSec  geo  model
//	osVersion  password  pingTarget  serial  siteId  tlsInsecure
//
// — because `Routers()` decodes the file into `store.Router`, which models
// eleven, and an undeclared key is dropped. The consequences are concrete: the
// Routers page shows `model` and `osVersion`, and the Add/Edit modal reads
// `pingTarget`, `tlsInsecure`, `backup` and `geo`, so it would seed defaults and
// a save would write them over the operator's values.
//
// This is the failure `users_public.go`'s header describes — "a writer that
// decodes into a struct it did not define rewrites the parts of the document it
// does not know about" — in the READ direction, on another endpoint. Hence the
// same answer: read the raw records and strip a denylist of two.
//
// ── THE LIVE FUNCTION IS FOUR LINES AND NONE IS A FIELD LIST ────────────────
//
//	const out = { ...r, password: r.password ? MASK : '' };
//	if (r.backup) {
//	  const { password, ...rest } = r.backup;
//	  out.backup = { ...rest, hasPassword: !!password };
//	}
//
// Spread, keep everything, mask one field and fold one nested one. So this must
// not grow a field list either: `tools/routers-public-cases.js` pins that a key
// no struct declares survives, which is the property that would be lost first.
//
// ── THE MASK IS ON THE DECRYPTED VALUE, NOT THE CIPHERTEXT ──────────────────
//
// `_readFile` decrypts before `getPublic` sees the record, so a password this
// install CANNOT read — a rotated `.secret`, a corrupt envelope — discloses `''`
// rather than the mask. The distinction is real: `''` means "no password, or one
// we cannot read", and the mask means "there is one". Both are in the corpus.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
)

// PublicRouters reads routers.json and strips the credential material.
//
// Returns an empty slice rather than nil for an empty file, so the payload is
// `[]` and not `null` — the same reason `PublicUsers` gives.
func (s *Store) PublicRouters() ([]map[string]any, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, "routers.json"))
	if os.IsNotExist(err) {
		// A fresh install has no file yet, and that is zero routers rather than
		// an error — `_readFile` answers `[]` for it too.
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw []map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		// NOT `[]`. The live `_readFile` swallows this and answers an empty list,
		// which would show an operator a fleet of zero routers on a file that is
		// merely unparseable — and the Routers page reads that as "add your first
		// router".
		return nil, err
	}

	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, s.PublicRouter(r))
	}
	return out, nil
}

// SiteIDRe is the live `_SITE_ID_RE`. Site ids come from the browser, so they
// are validated rather than trusted.
var siteIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// normalizeSites is `_normalizeSites`, which `_readFile` applies to EVERY record
// before `getPublic` ever sees it.
//
// ── IT IS NOT A WRITE, AND THAT IS WHY IT BELONGS HERE ──────────────────────
//
// The live comment: "Site membership is normalised HERE rather than by a
// migration that rewrites the file. A pre-#117 record has a scalar siteId and no
// list; it reads as a one-element list, and a record nobody edits is never
// touched on disk."
//
// So a record carrying NEITHER key still discloses `siteIds: []` and
// `siteId: null` — which is what the corpus caught this function missing. The
// page reads `siteIds` to draw membership; an absent key is `undefined`, and
// `undefined.length` is what a renderer hits next.
//
// `siteId` is kept in step as the ROLLBACK MIRROR: position 0 of the list, or
// null. A port emitting only the list would leave an older binary reading no
// membership at all.
func normalizeSites(pub map[string]any) {
	var src any = pub["siteIds"]
	if _, isList := src.([]any); !isList {
		src = pub["siteId"]
	}
	// ONE RULE, ONE PLACE — as of 2026-08-28. `cleanSiteIDs` now applies
	// `siteIDRe` itself, matching the live chain `_cleanSiteIds` -> `_cleanSiteId`,
	// so this no longer filters on top.
	//
	// It used to, and the note here said why: widening the write-path helper was
	// "a separate decision with its own tests". The operator made that decision;
	// the helper was widened, `tools/siteid-cases.js` pins it against the live
	// functions, and the compensating filter that lived here is gone rather than
	// left as a second implementation of the same rule.
	ids := cleanSiteIDs(src)
	pub["siteIds"] = ids
	if len(ids) > 0 {
		pub["siteId"] = ids[0]
	} else {
		// NULL, not "". The live code assigns `null`, and the two are different
		// on the wire even though both are falsy in the page.
		pub["siteId"] = nil
	}
}

// PublicRouter strips one record.
//
// A COPY, not a delete in place, for the reason `PublicUser` gives: deleting
// from the caller's map would remove the credential from whatever else holds
// that record.
func (s *Store) PublicRouter(r map[string]any) map[string]any {
	pub := make(map[string]any, len(r))
	for k, v := range r {
		pub[k] = v
	}

	// THE PASSWORD. Masked when this install can actually read one, empty
	// otherwise — which is what the live code answers, because it masks a value
	// that has already been through decryption.
	pub["password"] = ""
	if enc, _ := r["password"].(string); enc != "" {
		if plain, derr := s.Decrypt(enc); derr == nil && plain != "" {
			pub["password"] = Mask
		}
	}

	// SITE MEMBERSHIP, normalised on READ exactly as `_readFile` does it — see
	// normalizeSites. A record carrying neither key still discloses both.
	normalizeSites(pub)

	// THE BACKUP PASSWORD IS REMOVED, NOT MASKED, and replaced by a boolean. The
	// live comment says why: "Nothing in the UI edits it, so there is no field
	// for a mask to stand in for, and a masked secret invites a round trip that
	// could write the mask back."
	//
	// AND NO BACKUP BLOCK IS INVENTED. `if (r.backup)` — a record without one
	// gets no `hasPassword`, and a port that always emitted the key would tell
	// the page a backup is configured when none is.
	if bk, ok := r["backup"].(map[string]any); ok {
		nb := make(map[string]any, len(bk))
		for k, v := range bk {
			if k == "password" {
				continue
			}
			nb[k] = v
		}
		has := false
		if enc, _ := bk["password"].(string); enc != "" {
			if plain, derr := s.Decrypt(enc); derr == nil && plain != "" {
				has = true
			}
		}
		nb["hasPassword"] = has
		pub["backup"] = nb
	}
	return pub
}
