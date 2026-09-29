package db

// Site links, and the delete that finishes.
//
// ── MEMBERSHIP IS THE GRANT ────────────────────────────────────────────────
//
// A profile linked to a site applies to every router in that site and STOPS
// applying to one that leaves it. The operator chose that, and it has a
// consequence worth stating where the code is: editing a site's membership now
// adds and removes LOGINS on devices.
//
// The expansion is NOT stored. The reconciler resolves sites to routers on
// every sweep, because the answer changes when somebody edits a site rather
// than when anything here is called. Storing it instead would leave a router
// added to the site tomorrow silently without the account — the failure that
// reads as "the feature does not work" rather than as a design choice.

import "time"

// CredProfileSites is the sites one profile is linked to.
func (d *DB) CredProfileSites(profileID string) ([]string, error) {
	rows, err := d.sql.Query(
		`SELECT site_id FROM cred_profile_sites WHERE profile_id = ? ORDER BY linked_at`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SiteLink is one profile-to-site link, with who made it.
type SiteLink struct {
	SiteID   string
	LinkedBy string // a store.User.ID
}

// AllCredProfileSites is every profile's site links, for the reconciler's sweep.
//
// ── linked_by TRAVELS WITH IT, AND THAT IS NOT BOOKKEEPING ─────────────────
//
// A router link derived from a site inherits this id, because it is the audit
// ACTOR for every apply that follows. Nobody presses anything when a router
// joins a site, so without it the trail would show an account appearing on a
// device with no one named - and "who gave this box a login" is the first
// question anyone reading that row will have.
func (d *DB) AllCredProfileSites() (map[string][]SiteLink, error) {
	rows, err := d.sql.Query(`SELECT profile_id, site_id, linked_by FROM cred_profile_sites`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]SiteLink{}
	for rows.Next() {
		var p, s, by string
		if err := rows.Scan(&p, &s, &by); err != nil {
			return nil, err
		}
		out[p] = append(out[p], SiteLink{SiteID: s, LinkedBy: by})
	}
	return out, rows.Err()
}

// LinkCredProfileSite records that a profile applies to a site.
func (d *DB) LinkCredProfileSite(profileID, siteID, by string) error {
	_, err := d.sql.Exec(`INSERT INTO cred_profile_sites (profile_id, site_id, linked_by, linked_at)
	    VALUES (?,?,?,?) ON CONFLICT (profile_id, site_id) DO NOTHING`,
		profileID, siteID, by, time.Now().UnixMilli())
	return err
}

// UnlinkCredProfileSite drops the site link.
//
// It does NOT touch the router links. The reconciler notices that a via='site'
// link is no longer covered by any linked site and takes the account off, which
// is the one path that removes one — so a removal is always CONFIRMED on the
// device rather than assumed by a write here.
func (d *DB) UnlinkCredProfileSite(profileID, siteID string) error {
	_, err := d.sql.Exec(
		`DELETE FROM cred_profile_sites WHERE profile_id = ? AND site_id = ?`, profileID, siteID)
	return err
}

// MarkCredProfileForDelete queues the profile's removal from every router it is
// on, and records that the profile itself goes when they have all confirmed.
//
// ── WHY THIS IS NOT JUST A DELETE ───────────────────────────────────────────
//
// Removing the row now would leave an account on every linked device with
// nothing left that knows it is there. So the accounts come off first, each
// confirmed on its router, and `FinishPendingDeletes` takes the profile when
// the last one has gone. A router that is switched off holds the delete open,
// which is the honest outcome: the profile still exists because the account
// still exists.
func (d *DB) MarkCredProfileForDelete(profileID string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`UPDATE cred_profiles SET pending_delete = 1 WHERE id = ?`, profileID); err != nil {
		return err
	}
	// The site links go now: they are intent, and the intent is withdrawn.
	if _, err := tx.Exec(
		`DELETE FROM cred_profile_sites WHERE profile_id = ?`, profileID); err != nil {
		return err
	}
	// Every router link becomes a removal, INCLUDING the terminal ones. A
	// refused or conflicting link never put an account on that device, so there
	// is nothing to take off and its removal confirms on the first sweep —
	// whereas leaving them alone would hold the delete open for ever on exactly
	// the devices where the profile never did anything.
	if _, err := tx.Exec(`UPDATE cred_profile_links
	    SET state = 'removing', next_attempt_at = 0, attempts = 0, code = '', error = ''
	  WHERE profile_id = ?`, profileID); err != nil {
		return err
	}
	return tx.Commit()
}

// FinishPendingDeletes removes every pending-delete profile whose links have
// all gone, and answers how many went.
func (d *DB) FinishPendingDeletes() (int64, error) {
	res, err := d.sql.Exec(`DELETE FROM cred_profiles
	  WHERE pending_delete = 1
	    AND NOT EXISTS (SELECT 1 FROM cred_profile_links WHERE profile_id = cred_profiles.id)`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CredLinksByVia is one profile's links split by how they came to be, which is
// what the reconciler needs to work out which site-derived ones have expired.
func (d *DB) CredLinksByVia(profileID string) (direct, site map[string]bool, err error) {
	rows, qerr := d.sql.Query(
		`SELECT router_id, via FROM cred_profile_links WHERE profile_id = ?`, profileID)
	if qerr != nil {
		return nil, nil, qerr
	}
	defer rows.Close()
	direct, site = map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var rid, via string
		if err := rows.Scan(&rid, &via); err != nil {
			return nil, nil, err
		}
		if via == "site" {
			site[rid] = true
		} else {
			direct[rid] = true
		}
	}
	return direct, site, rows.Err()
}
