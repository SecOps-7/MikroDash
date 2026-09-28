package db

// Reading and writing the SSO tables.
//
// THIS FILE ENCRYPTS NOTHING. `client_secret` arrives sealed and leaves sealed,
// exactly as `notify_channels.config` does: the settings envelope lives in
// internal/store and internal/server, and a database layer that also held a key
// would be a second place to look when a secret turns out to be readable.

import (
	"database/sql"
	"errors"
	"time"
)

// SSOProvider is one configured identity provider.
//
// ClientSecret is the SEALED form. Nothing in this package can read it, and
// nothing here should try.
type SSOProvider struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	Issuer        string `json:"issuer"`
	ClientID      string `json:"clientId"`
	ClientSecret  string `json:"-"`
	Scopes        string `json:"scopes"`
	ClaimUsername string `json:"claimUsername"`
	ClaimEmail    string `json:"claimEmail"`
	ClaimName     string `json:"claimName"`
	ClaimRoles    string `json:"claimRoles"`
	IconVersion   int64  `json:"iconVersion"`
	CreatedBy     string `json:"-"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
}

// SSORoleMapping is one claim value and the role it confers.
type SSORoleMapping struct {
	ClaimValue string `json:"claimValue"`
	RoleID     string `json:"roleId"`
}

const ssoProviderCols = `id, name, enabled, issuer, client_id, client_secret, scopes,
	claim_username, claim_email, claim_name, claim_roles, icon_version,
	created_by, created_at, updated_at`

func scanSSOProvider(rows *sql.Rows) (SSOProvider, error) {
	var p SSOProvider
	var createdBy sql.NullString
	err := rows.Scan(&p.ID, &p.Name, &p.Enabled, &p.Issuer, &p.ClientID, &p.ClientSecret,
		&p.Scopes, &p.ClaimUsername, &p.ClaimEmail, &p.ClaimName, &p.ClaimRoles,
		&p.IconVersion, &createdBy, &p.CreatedAt, &p.UpdatedAt)
	p.CreatedBy = createdBy.String
	return p, err
}

// SSOProviders is every configured provider, newest first.
func (d *DB) SSOProviders() ([]SSOProvider, error) {
	return d.ssoProviders(`SELECT ` + ssoProviderCols + ` FROM sso_providers ORDER BY created_at DESC`)
}

// SSOProvidersEnabled is the ones the login page may offer.
//
// A SEPARATE QUERY RATHER THAN A FILTER IN THE CALLER: this one answers an
// UNAUTHENTICATED request, and "enabled" is the whole of what decides whether a
// provider is visible to a stranger. Filtering in Go would put that decision
// somewhere a later edit could reorder.
func (d *DB) SSOProvidersEnabled() ([]SSOProvider, error) {
	return d.ssoProviders(`SELECT ` + ssoProviderCols +
		` FROM sso_providers WHERE enabled = 1 ORDER BY name COLLATE NOCASE`)
}

func (d *DB) ssoProviders(query string) ([]SSOProvider, error) {
	rows, err := d.sql.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SSOProvider{}
	for rows.Next() {
		p, err := scanSSOProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SSOProviderByID is one provider, or sql.ErrNoRows.
func (d *DB) SSOProviderByID(id string) (SSOProvider, error) {
	rows, err := d.sql.Query(`SELECT `+ssoProviderCols+` FROM sso_providers WHERE id = ?`, id)
	if err != nil {
		return SSOProvider{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return SSOProvider{}, err
		}
		return SSOProvider{}, sql.ErrNoRows
	}
	return scanSSOProvider(rows)
}

// UpsertSSOProvider creates or updates one.
//
// `created_by` and `created_at` are NOT in the update branch, for the reason
// UpsertNotifyChannel gives about ownership: an edit changes what a provider is,
// never who first configured it or when.
func (d *DB) UpsertSSOProvider(p SSOProvider) error {
	if p.ID == "" {
		return errors.New("db: an SSO provider needs an id")
	}
	now := time.Now().UnixMilli()
	if p.CreatedAt == 0 {
		p.CreatedAt = now
	}
	_, err := d.sql.Exec(`
		INSERT INTO sso_providers (`+ssoProviderCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (id) DO UPDATE SET
		  name = excluded.name,
		  enabled = excluded.enabled,
		  issuer = excluded.issuer,
		  client_id = excluded.client_id,
		  client_secret = excluded.client_secret,
		  scopes = excluded.scopes,
		  claim_username = excluded.claim_username,
		  claim_email = excluded.claim_email,
		  claim_name = excluded.claim_name,
		  claim_roles = excluded.claim_roles,
		  icon_version = excluded.icon_version,
		  updated_at = excluded.updated_at`,
		p.ID, p.Name, p.Enabled, p.Issuer, p.ClientID, p.ClientSecret, p.Scopes,
		p.ClaimUsername, p.ClaimEmail, p.ClaimName, p.ClaimRoles, p.IconVersion,
		nullIfEmpty(p.CreatedBy), p.CreatedAt, now)
	return err
}

// DeleteSSOProvider removes it. Its role mappings and identities go with it, by
// the foreign keys - see sso_schema.go on why that cascade is right and why the
// role reference deliberately is not.
func (d *DB) DeleteSSOProvider(id string) error {
	_, err := d.sql.Exec(`DELETE FROM sso_providers WHERE id = ?`, id)
	return err
}

// SSORoleMap is a provider's claim-value to role mappings.
func (d *DB) SSORoleMap(providerID string) ([]SSORoleMapping, error) {
	rows, err := d.sql.Query(`SELECT claim_value, role_id FROM sso_role_map
	    WHERE provider_id = ? ORDER BY claim_value COLLATE NOCASE`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SSORoleMapping{}
	for rows.Next() {
		var m SSORoleMapping
		if err := rows.Scan(&m.ClaimValue, &m.RoleID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReplaceSSORoleMap swaps a provider's whole mapping in one transaction.
//
// REPLACED RATHER THAN MERGED, because the form edits the set as a whole and a
// merge would make a removed row indistinguishable from one the browser simply
// did not send. In a transaction, because a half-applied mapping is a set of
// people who can sign in and a set who cannot, decided by where it stopped.
func (d *DB) ReplaceSSORoleMap(providerID string, entries []SSORoleMapping) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM sso_role_map WHERE provider_id = ?`, providerID); err != nil {
		return err
	}
	for _, m := range entries {
		if m.ClaimValue == "" || m.RoleID == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO sso_role_map (provider_id, claim_value, role_id)
		    VALUES (?,?,?)`, providerID, m.ClaimValue, m.RoleID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SSOIdentityFor is the local user bound to a provider's subject, or "" when
// this is the first time that person has signed in.
func (d *DB) SSOIdentityFor(providerID, subject string) (string, error) {
	var userID string
	err := d.sql.QueryRow(`SELECT user_id FROM sso_identities
	    WHERE provider_id = ? AND subject = ?`, providerID, subject).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return userID, err
}

// UpsertSSOIdentity binds a subject to a local user and records the sighting.
//
// `user_id` is NOT in the update branch. A subject that already resolves to an
// account must not be quietly repointed at a different one: that is exactly the
// takeover the callback's collision rule exists to prevent, and leaving the
// column updatable here would put a second door beside the locked one.
func (d *DB) UpsertSSOIdentity(providerID, subject, userID string) error {
	now := time.Now().UnixMilli()
	_, err := d.sql.Exec(`
		INSERT INTO sso_identities (provider_id, subject, user_id, created_at, last_seen_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT (provider_id, subject) DO UPDATE SET last_seen_at = excluded.last_seen_at`,
		providerID, subject, userID, now, now)
	return err
}

// DeleteSSOIdentitiesForUser unbinds every external identity from a local
// account, for when that account is deleted. Without it a deleted user's subject
// would still resolve, and the next sign-in would hand back an id RBAC no longer
// knows anything about.
func (d *DB) DeleteSSOIdentitiesForUser(userID string) error {
	_, err := d.sql.Exec(`DELETE FROM sso_identities WHERE user_id = ?`, userID)
	return err
}
