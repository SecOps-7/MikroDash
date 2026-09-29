package db

// Reading and writing the credential profile tables.
//
// THIS FILE ENCRYPTS NOTHING. `secret` arrives sealed and leaves sealed,
// exactly as `sso_providers.client_secret` and `notify_channels.config` do: the
// settings envelope lives in internal/store and internal/server, and a database
// layer that also held a key would be a second place to look when a secret
// turns out to be readable.

import (
	"database/sql"
	"errors"
	"time"
)

// CredProfile is one profile. Secret is the SEALED form; nothing in this
// package can read it, and nothing here should try.
type CredProfile struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Username      string `json:"username"`
	PermKind      string `json:"permKind"`
	Builtin       string `json:"builtinGroup"`
	GroupName     string `json:"groupName"`
	PolicyJSON    string `json:"-"`
	Secret        string `json:"-"`
	Revision      int64  `json:"revision"`
	IsDefault     bool   `json:"isDefault"`
	PendingDelete bool   `json:"pendingDelete"`
	CreatedBy     string `json:"-"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
}

// CredLink is one profile's standing on one router.
type CredLink struct {
	ProfileID       string `json:"profileId"`
	RouterID        string `json:"routerId"`
	State           string `json:"state"`
	Code            string `json:"code"`
	Error           string `json:"error"`
	AppliedRevision int64  `json:"appliedRevision"`
	Attempts        int64  `json:"attempts"`
	NextAttemptAt   int64  `json:"nextAttemptAt"`
	LastAttemptAt   int64  `json:"lastAttemptAt"`
	AppliedAt       int64  `json:"appliedAt"`
	Via             string `json:"via"`
	LinkedBy        string `json:"-"`
	LinkedAt        int64  `json:"linkedAt"`
}

const credProfileCols = `id, name, description, ros_username,
	group_name, policy_json, secret, revision, is_default, pending_delete,
	created_by, created_at, updated_at`

const credLinkCols = `profile_id, router_id, state, code, error, applied_revision,
	attempts, next_attempt_at, last_attempt_at, applied_at, via, linked_by, linked_at`

func scanCredProfile(rows *sql.Rows) (CredProfile, error) {
	var p CredProfile
	err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Username,
		&p.GroupName, &p.PolicyJSON, &p.Secret, &p.Revision, &p.IsDefault, &p.PendingDelete,
		&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func scanCredLink(rows *sql.Rows) (CredLink, error) {
	var l CredLink
	err := rows.Scan(&l.ProfileID, &l.RouterID, &l.State, &l.Code, &l.Error,
		&l.AppliedRevision, &l.Attempts, &l.NextAttemptAt, &l.LastAttemptAt,
		&l.AppliedAt, &l.Via, &l.LinkedBy, &l.LinkedAt)
	return l, err
}

// CredProfiles is every profile, by name.
func (d *DB) CredProfiles() ([]CredProfile, error) {
	rows, err := d.sql.Query(`SELECT ` + credProfileCols +
		` FROM cred_profiles ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CredProfile{}
	for rows.Next() {
		p, err := scanCredProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CredProfileByID is one profile, or sql.ErrNoRows.
func (d *DB) CredProfileByID(id string) (CredProfile, error) {
	rows, err := d.sql.Query(`SELECT `+credProfileCols+` FROM cred_profiles WHERE id = ?`, id)
	if err != nil {
		return CredProfile{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return CredProfile{}, err
		}
		return CredProfile{}, sql.ErrNoRows
	}
	return scanCredProfile(rows)
}

// UpsertCredProfile creates or updates one.
//
// ── THE REVISION IS DECIDED HERE, NOT BY THE CALLER ─────────────────────────
//
// `revision` is the whole of "which devices owe an update", so what bumps it is
// a correctness question rather than a bookkeeping one, and it belongs in one
// place. It moves when a ROUTER COULD TELL THE DIFFERENCE — the username, the
// password, the permissions — and stays put for a description edit, which no
// device can observe. Bumping on every save would re-write every linked router
// because somebody fixed a typo in a sentence.
//
// `created_by` and `created_at` are not in the update branch, for the reason
// UpsertSSOProvider gives: an edit changes what a profile is, never who made it.
func (d *DB) UpsertCredProfile(p CredProfile) error {
	if p.ID == "" {
		return errors.New("db: a credential profile needs an id")
	}
	now := time.Now().UnixMilli()
	if p.CreatedAt == 0 {
		p.CreatedAt = now
	}

	// Decide the revision against what is stored, before writing.
	p.Revision = 1
	if old, err := d.CredProfileByID(p.ID); err == nil {
		p.Revision = old.Revision
		// `is_default` is DELIBERATELY ABSENT from this comparison. It changes
		// nothing a router can observe, so bumping the revision for it would
		// re-write the account on every linked device because somebody ticked a
		// box about which profile a wizard offers first.
		if old.Username != p.Username || old.Secret != p.Secret ||
			old.GroupName != p.GroupName || old.PolicyJSON != p.PolicyJSON {
			p.Revision++
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	_, err := d.sql.Exec(`
		INSERT INTO cred_profiles (`+credProfileCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (id) DO UPDATE SET
		  name = excluded.name,
		  description = excluded.description,
		  ros_username = excluded.ros_username,
		  group_name = excluded.group_name,
		  policy_json = excluded.policy_json,
		  secret = excluded.secret,
		  revision = excluded.revision,
		  is_default = excluded.is_default,
		  updated_at = excluded.updated_at`,
		p.ID, p.Name, p.Description, p.Username,
		p.GroupName, p.PolicyJSON, p.Secret, p.Revision, p.IsDefault, p.PendingDelete,
		p.CreatedBy, p.CreatedAt, now)
	return err
}

// DeleteCredProfile removes it.
//
// It FAILS while any link survives — `ON DELETE RESTRICT`, deliberately. A
// caller that wants the rows gone anyway uses ForgetCredProfile, which is a
// different act with a different name and audits what it abandons.
func (d *DB) DeleteCredProfile(id string) error {
	_, err := d.sql.Exec(`DELETE FROM cred_profiles WHERE id = ?`, id)
	return err
}

// ForgetCredProfile drops the profile AND its links without touching any
// router.
//
// ── THIS IS THE DANGEROUS ONE, AND IT IS NAMED SO ───────────────────────────
//
// Every account this profile placed stays on its device, at whatever privilege
// it carried, with nothing left in MikroDash that knows it is there. It exists
// because the alternative — a profile that can never be deleted once a router
// has gone for good — is worse. The caller audits every device first; this
// function does not, because a database layer that wrote audit rows would be a
// second place they come from.
func (d *DB) ForgetCredProfile(id string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM cred_profile_links WHERE profile_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM cred_profiles WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// CredLinks is every link, or those of one profile when profileID is set.
func (d *DB) CredLinks(profileID string) ([]CredLink, error) {
	query := `SELECT ` + credLinkCols + ` FROM cred_profile_links`
	args := []any{}
	if profileID != "" {
		query += ` WHERE profile_id = ?`
		args = append(args, profileID)
	}
	query += ` ORDER BY linked_at`
	return d.credLinks(query, args...)
}

// CredLinksForRouter is every profile linked to one router.
func (d *DB) CredLinksForRouter(routerID string) ([]CredLink, error) {
	return d.credLinks(`SELECT `+credLinkCols+
		` FROM cred_profile_links WHERE router_id = ? ORDER BY linked_at`, routerID)
}

// CredLinksDue is the work the reconciler should pick up: a device that owes an
// update, or a retryable failure whose backoff has expired.
//
// ── THE TERMINAL STATES ARE EXCLUDED IN SQL, NOT IN GO ──────────────────────
//
// `refused` and `conflict` need a human. Filtering them in the caller would put
// the decision somewhere a later edit could reorder, and the cost of getting it
// wrong is an audit row per router per sweep, for ever, until the trail is
// something people have learned to scroll past. Same reasoning as
// SSOProvidersEnabled being its own query.
func (d *DB) CredLinksDue(now int64) ([]CredLink, error) {
	return d.credLinks(`
		SELECT `+credLinkCols+` FROM cred_profile_links l
		JOIN cred_profiles p ON p.id = l.profile_id
		WHERE l.state NOT IN ('refused', 'conflict')
		  AND l.next_attempt_at <= ?
		  AND (l.applied_revision < p.revision
		       OR l.state IN ('pending', 'failed', 'unreachable', 'unknown',
		                      'removing', 'orphaned'))
		ORDER BY l.next_attempt_at`, now)
}

func (d *DB) credLinks(query string, args ...any) ([]CredLink, error) {
	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CredLink{}
	for rows.Next() {
		l, err := scanCredLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LinkCredProfile records the intent to put a profile on a router.
//
// An existing link is left ALONE rather than reset: pressing Link on a device
// that already has it should not throw away the state that says it refused, nor
// restart a backoff that is deliberately long.
func (d *DB) LinkCredProfile(profileID, routerID, via, by string) error {
	now := time.Now().UnixMilli()
	_, err := d.sql.Exec(`
		INSERT INTO cred_profile_links (`+credLinkCols+`)
		VALUES (?,?,'pending','','',0,0,0,0,0,?,?,?)
		ON CONFLICT (profile_id, router_id) DO NOTHING`,
		profileID, routerID, via, by, now)
	return err
}

// MarkCredLink records the outcome of one attempt.
//
// ── A FAILED REMOVAL KEEPS ITS ROW ──────────────────────────────────────────
//
// Nothing here deletes a link. The only path that does is DropCredLink, called
// when a removal has been CONFIRMED on the router. Deleting on failure is how
// MikroDash forgets an account it created, which is the worst thing this
// feature can do: the login stays on the device and nothing is left that knows.
func (d *DB) MarkCredLink(profileID, routerID, state, code, errText string,
	appliedRevision, nextAttemptAt int64) error {

	now := time.Now().UnixMilli()
	applied := int64(0)
	if state == "applied" {
		applied = now
	}
	_, err := d.sql.Exec(`
		UPDATE cred_profile_links SET
		  state = ?, code = ?, error = ?,
		  applied_revision = CASE WHEN ? > 0 THEN ? ELSE applied_revision END,
		  attempts = CASE WHEN ? = 'applied' THEN 0 ELSE attempts + 1 END,
		  next_attempt_at = ?,
		  last_attempt_at = ?,
		  applied_at = CASE WHEN ? > 0 THEN ? ELSE applied_at END
		WHERE profile_id = ? AND router_id = ?`,
		state, code, errText,
		appliedRevision, appliedRevision,
		state,
		nextAttemptAt, now,
		applied, applied,
		profileID, routerID)
	return err
}

// DropCredLink removes the row, and is called ONLY once a removal has been
// confirmed on the router. See MarkCredLink.
func (d *DB) DropCredLink(profileID, routerID string) error {
	_, err := d.sql.Exec(
		`DELETE FROM cred_profile_links WHERE profile_id = ? AND router_id = ?`,
		profileID, routerID)
	return err
}

// EnqueueCredLinks puts every link of one profile back in the queue, which is
// what a revision bump means: each device has to be told.
func (d *DB) EnqueueCredLinks(profileID string) error {
	_, err := d.sql.Exec(`
		UPDATE cred_profile_links
		   SET state = 'pending', attempts = 0, next_attempt_at = 0, code = '', error = ''
		 WHERE profile_id = ? AND state NOT IN ('refused', 'conflict')`, profileID)
	return err
}
