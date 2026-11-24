package db

// Share links (#165 follow-up): a token-protected, read-only view of one page
// (or a slideshow, a Dashboard card, a sensor feed) for somebody with no
// account. The rules live in internal/sharelink; this file stores them.
//
// ── THE TOKEN'S HASH, AND THE TOKEN SEALED ──────────────────────────────────
//
// `token_hash` is SHA-256 of the token, and a lookup is by hash. Since
// migration 42 the token is also kept sealed (`token_enc`, AES-256-GCM under
// the install's key) so Settings can copy a link again; see SetShareLinkSecret.
//
// ── IDENTITY COLUMNS ────────────────────────────────────────────────────────
//
// `created_by` and `revoked_by` hold the USER ID (CLAUDE.md's identity-column
// rule). `revoked_by` is NULL when a link was revoked automatically; the
// reason is in `revoke_reason`.

import (
	"database/sql"
	"encoding/json"
	"errors"
)

const shareLinksDDL = `
CREATE TABLE IF NOT EXISTS share_links (
  id            TEXT PRIMARY KEY,
  token_hash    BLOB NOT NULL UNIQUE,
  label         TEXT NOT NULL,
  kind          TEXT NOT NULL CHECK (kind IN ('page','slideshow','card','feed')),
  page          TEXT,
  router_id     TEXT,
  layout        TEXT,
  options       TEXT NOT NULL DEFAULT '{}',
  frame_origins TEXT NOT NULL DEFAULT '[]',
  viewer_cap    INTEGER,
  allowed_cidrs TEXT NOT NULL DEFAULT '[]',
  created_by    TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  expires_at    INTEGER,
  revoked_at    INTEGER,
  revoked_by    TEXT,
  revoke_reason TEXT,
  last_used_at  INTEGER,
  use_count     INTEGER NOT NULL DEFAULT 0,
  token_enc     TEXT NOT NULL DEFAULT '',
  base_url      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS share_links_router ON share_links(router_id);
CREATE INDEX IF NOT EXISTS share_links_creator ON share_links(created_by);
CREATE TABLE IF NOT EXISTS share_link_slides (
  link_id   TEXT NOT NULL REFERENCES share_links(id) ON DELETE CASCADE,
  position  INTEGER NOT NULL,
  page      TEXT NOT NULL,
  router_id TEXT NOT NULL,
  seconds   INTEGER,
  layout    TEXT,
  PRIMARY KEY (link_id, position)
);
CREATE INDEX IF NOT EXISTS share_link_slides_router ON share_link_slides(router_id);
`

// ShareSlide is one slide of a slideshow link.
type ShareSlide struct {
	Page     string
	RouterID string
	Seconds  int // 0: the slideshow's default
	Layout   string
}

// ShareLink is one row of share_links, with its slides.
type ShareLink struct {
	ID           string
	TokenHash    []byte
	Label        string
	Kind         string
	Page         string
	RouterID     string
	Layout       string
	Options      string // JSON, sharelink.Options
	FrameOrigins []string
	ViewerCap    int
	AllowedCIDRs []string
	CreatedBy    string
	CreatedAt    int64
	ExpiresAt    *int64
	RevokedAt    *int64
	RevokedBy    string
	RevokeReason string
	LastUsedAt   *int64
	UseCount     int64
	Slides       []ShareSlide
	// Copyable: the token is kept sealed (migration 42), so the link's address
	// can be shown again. Read only; SetShareLinkSecret writes it.
	Copyable bool
}

// RouterIDs is every router the link shows.
func (l ShareLink) RouterIDs() []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	add(l.RouterID)
	for _, s := range l.Slides {
		add(s.RouterID)
	}
	return out
}

func jsonList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func parseList(s string) []string {
	var out []string
	if json.Unmarshal([]byte(s), &out) != nil || out == nil {
		return []string{}
	}
	return out
}

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func capValue(n int) any {
	if n > 0 {
		return n
	}
	return nil
}

// ── THE TOKEN, SEALED, SO A LINK CAN BE COPIED AGAIN (migration 42) ─────────
//
// `token_enc` is the token sealed with the install's AES-256-GCM key (the
// store's Encrypt, the one router passwords use) and `base_url` the address it
// was made for, so Settings can show a link's address again (the operator,
// 2026-11-24). A lookup is still by `token_hash`; the sealed token is read only
// to show it to a global administrator. Empty on a link made before.

// SetShareLinkSecret stores a link's sealed token and the address it was made for.
func (d *DB) SetShareLinkSecret(id, tokenEnc, baseURL string) error {
	_, err := d.sql.Exec(`UPDATE share_links SET token_enc = ?, base_url = ? WHERE id = ?`, tokenEnc, baseURL, id)
	return err
}

// ShareLinkSecret is a link's sealed token and address; both empty for a link
// made before they were kept.
func (d *DB) ShareLinkSecret(id string) (tokenEnc, baseURL string, err error) {
	err = d.sql.QueryRow(`SELECT token_enc, base_url FROM share_links WHERE id = ?`, id).Scan(&tokenEnc, &baseURL)
	return tokenEnc, baseURL, err
}

// InsertShareLink stores a new link and its slides in one transaction.
func (d *DB) InsertShareLink(l ShareLink) error {
	if d == nil || d.sql == nil {
		return errors.New("db not open")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO share_links (id, token_hash, label, kind, page, router_id, layout,
	  options, frame_origins, viewer_cap, allowed_cidrs, created_by, created_at, expires_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		l.ID, l.TokenHash, l.Label, l.Kind, nul(l.Page), nul(l.RouterID), nul(l.Layout),
		orJSONObject(l.Options), jsonList(l.FrameOrigins), capValue(l.ViewerCap), jsonList(l.AllowedCIDRs),
		l.CreatedBy, l.CreatedAt, nullInt(l.ExpiresAt)); err != nil {
		return err
	}
	if err := insertSlides(tx, l.ID, l.Slides); err != nil {
		return err
	}
	return tx.Commit()
}

func insertSlides(tx *sql.Tx, id string, slides []ShareSlide) error {
	for i, s := range slides {
		var secs any
		if s.Seconds > 0 {
			secs = s.Seconds
		}
		if _, err := tx.Exec(`INSERT INTO share_link_slides (link_id, position, page, router_id, seconds, layout)
		  VALUES (?,?,?,?,?,?)`, id, i, s.Page, s.RouterID, secs, nul(s.Layout)); err != nil {
			return err
		}
	}
	return nil
}

const shareLinkCols = `id, token_hash, label, kind, COALESCE(page,''), COALESCE(router_id,''),
  COALESCE(layout,''), options, frame_origins, COALESCE(viewer_cap,0), allowed_cidrs, created_by,
  created_at, expires_at, revoked_at, COALESCE(revoked_by,''), COALESCE(revoke_reason,''),
  last_used_at, use_count, token_enc <> ''`

func scanShareLink(row interface{ Scan(...any) error }) (ShareLink, error) {
	var l ShareLink
	var origins, cidrs string
	var exp, rev, used sql.NullInt64
	if err := row.Scan(&l.ID, &l.TokenHash, &l.Label, &l.Kind, &l.Page, &l.RouterID, &l.Layout,
		&l.Options, &origins, &l.ViewerCap, &cidrs, &l.CreatedBy, &l.CreatedAt, &exp, &rev,
		&l.RevokedBy, &l.RevokeReason, &used, &l.UseCount, &l.Copyable); err != nil {
		return ShareLink{}, err
	}
	l.FrameOrigins, l.AllowedCIDRs = parseList(origins), parseList(cidrs)
	if exp.Valid {
		v := exp.Int64
		l.ExpiresAt = &v
	}
	if rev.Valid {
		v := rev.Int64
		l.RevokedAt = &v
	}
	if used.Valid {
		v := used.Int64
		l.LastUsedAt = &v
	}
	return l, nil
}

func (d *DB) slidesFor(ids []string) (map[string][]ShareSlide, error) {
	out := map[string][]ShareSlide{}
	if len(ids) == 0 {
		return out, nil
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	rows, err := d.sql.Query(`SELECT link_id, page, router_id, COALESCE(seconds,0), COALESCE(layout,'')
	  FROM share_link_slides ORDER BY link_id, position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var s ShareSlide
		if err := rows.Scan(&id, &s.Page, &s.RouterID, &s.Seconds, &s.Layout); err != nil {
			return nil, err
		}
		if want[id] {
			out[id] = append(out[id], s)
		}
	}
	return out, rows.Err()
}

func (d *DB) oneShareLink(where string, arg any) (*ShareLink, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	l, err := scanShareLink(d.sql.QueryRow(`SELECT `+shareLinkCols+` FROM share_links WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	slides, err := d.slidesFor([]string{l.ID})
	if err != nil {
		return nil, err
	}
	l.Slides = slides[l.ID]
	return &l, nil
}

// ShareLinkByHash finds a link by its token's hash; nil, nil when none.
func (d *DB) ShareLinkByHash(hash []byte) (*ShareLink, error) {
	return d.oneShareLink("token_hash = ?", hash)
}

// ShareLinkByID finds a link by id; nil, nil when none.
func (d *DB) ShareLinkByID(id string) (*ShareLink, error) {
	return d.oneShareLink("id = ?", id)
}

// ShareLinks is every link, newest first, with slides.
func (d *DB) ShareLinks() ([]ShareLink, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	rows, err := d.sql.Query(`SELECT ` + shareLinkCols + ` FROM share_links ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ShareLink{}
	var ids []string
	for rows.Next() {
		l, err := scanShareLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
		ids = append(ids, l.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slides, err := d.slidesFor(ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Slides = slides[out[i].ID]
	}
	return out, nil
}

// UpdateShareLink rewrites a link's editable fields and its slides. The token,
// the creator and the revocation are never changed here, and a revoked link
// cannot be edited.
func (d *DB) UpdateShareLink(l ShareLink) error {
	if d == nil || d.sql == nil {
		return errors.New("db not open")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`UPDATE share_links SET label=?, page=?, router_id=?, layout=?, options=?,
	  frame_origins=?, viewer_cap=?, allowed_cidrs=?, expires_at=? WHERE id=? AND revoked_at IS NULL`,
		l.Label, nul(l.Page), nul(l.RouterID), nul(l.Layout), orJSONObject(l.Options),
		jsonList(l.FrameOrigins), capValue(l.ViewerCap), jsonList(l.AllowedCIDRs), nullInt(l.ExpiresAt), l.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("no such active link")
	}
	if _, err := tx.Exec(`DELETE FROM share_link_slides WHERE link_id = ?`, l.ID); err != nil {
		return err
	}
	if err := insertSlides(tx, l.ID, l.Slides); err != nil {
		return err
	}
	return tx.Commit()
}

// RevokeShareLink revokes a link that is not already revoked. revokedBy is a
// user id, or "" for an automatic revocation. It reports whether a row changed.
func (d *DB) RevokeShareLink(id, revokedBy, reason string, at int64) (bool, error) {
	if d == nil || d.sql == nil {
		return false, errors.New("db not open")
	}
	res, err := d.sql.Exec(`UPDATE share_links SET revoked_at=?, revoked_by=?, revoke_reason=?
	  WHERE id=? AND revoked_at IS NULL`, at, nul(revokedBy), reason, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteShareLink removes a link (and its slides). It reports whether a row
// was removed.
func (d *DB) DeleteShareLink(id string) (bool, error) {
	if d == nil || d.sql == nil {
		return false, errors.New("db not open")
	}
	res, err := d.sql.Exec(`DELETE FROM share_links WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// TouchShareLink records a use.
func (d *DB) TouchShareLink(id string, at int64) error {
	if d == nil || d.sql == nil {
		return errors.New("db not open")
	}
	_, err := d.sql.Exec(`UPDATE share_links SET last_used_at=?, use_count=use_count+1 WHERE id=?`, at, id)
	return err
}

// ActiveShareLinkIDsForRouter is every link not yet revoked that shows the
// router, as its subject or in a slide.
func (d *DB) ActiveShareLinkIDsForRouter(routerID string) ([]string, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	rows, err := d.sql.Query(`SELECT id FROM share_links WHERE revoked_at IS NULL AND (router_id = ?
	  OR id IN (SELECT link_id FROM share_link_slides WHERE router_id = ?))`, routerID, routerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
