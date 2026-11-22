package db

// The Apps Usage page's custom apps, rules and categories (#167), migration
// 41. The rules themselves are internal/apps/rules.go's; this file keeps them.
//
//	app_rules        one custom app, or one built-in app's additions, as JSON
//	app_categories   the administrators' categories
//
// They apply to every router: one install, one set of rules.
//
// ── THE RULE IS ONE JSON VALUE ──────────────────────────────────────────────
//
// `rule` holds apps.Rule as JSON. It is read whole and written whole, and the
// package that judges it (internal/apps) is the one that knows its shape, so
// this file does not take it apart into columns that would have to follow
// every change to it.
//
// ── IDENTITY COLUMNS ────────────────────────────────────────────────────────
//
// `created_by` and `updated_by` hold the USER ID (CLAUDE.md's identity-column
// rule).
//
// ── A DELETED APP'S HISTORY ─────────────────────────────────────────────────
//
// FoldAppUsage moves a deleted custom app's stored hours into the unnamed row
// of the same router and hour, so the totals stay true and a later app given
// the same id does not inherit them.
//
// Shared by `freshSchemaDDL` and `portMigrations[41]`, the rule
// monitorruns_schema.go gives.

import "errors"

const appRulesDDL = `
CREATE TABLE IF NOT EXISTS app_rules (
  id         TEXT PRIMARY KEY,
  rule       TEXT NOT NULL,
  created_by TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_by TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS app_categories (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  tone       TEXT NOT NULL,
  created_by TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
`

// AppRuleRow is one stored rule; Rule is apps.Rule as JSON.
type AppRuleRow struct {
	ID        string
	Rule      string
	CreatedBy string
	CreatedAt int64
	UpdatedBy string
	UpdatedAt int64
}

// AppCategoryRow is one custom category.
type AppCategoryRow struct {
	ID        string
	Name      string
	Tone      string
	CreatedBy string
	CreatedAt int64
}

// ErrAppRuleNotFound is a rule or category that is not there.
var ErrAppRuleNotFound = errors.New("not found")

// AppRules is every stored rule, oldest first, so the merge order is the order
// they were made in.
func (d *DB) AppRules() ([]AppRuleRow, error) {
	rows, err := d.sql.Query(`SELECT id, rule, created_by, created_at, updated_by, updated_at FROM app_rules
	  ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppRuleRow{}
	for rows.Next() {
		var r AppRuleRow
		if err := rows.Scan(&r.ID, &r.Rule, &r.CreatedBy, &r.CreatedAt, &r.UpdatedBy, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutAppRule inserts a rule, or replaces the rule of one with that id while
// keeping who made it and when.
func (d *DB) PutAppRule(id, rule, userID string, now int64) error {
	_, err := d.sql.Exec(`INSERT INTO app_rules (id, rule, created_by, created_at, updated_by, updated_at)
	  VALUES (?, ?, ?, ?, ?, ?)
	  ON CONFLICT(id) DO UPDATE SET rule = excluded.rule, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		id, rule, userID, now, userID, now)
	return err
}

// DeleteAppRule removes a rule. ErrAppRuleNotFound when there was none.
func (d *DB) DeleteAppRule(id string) error {
	res, err := d.sql.Exec(`DELETE FROM app_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAppRuleNotFound
	}
	return nil
}

// FoldAppUsage moves every stored hour of `app` into `into` (the unnamed
// row), adding to what that router and hour already hold, in one transaction.
func (d *DB) FoldAppUsage(app, into string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// `WHERE ... AND true`: SQLite reads ON CONFLICT after a SELECT's bare
	// WHERE as part of a join without it.
	if _, err := tx.Exec(`INSERT INTO app_usage (router_id, app, hour, rx_bytes, tx_bytes)
	  SELECT router_id, ?, hour, rx_bytes, tx_bytes FROM app_usage WHERE app = ? AND true
	  ON CONFLICT(router_id, app, hour) DO UPDATE SET rx_bytes = rx_bytes + excluded.rx_bytes,
	    tx_bytes = tx_bytes + excluded.tx_bytes`, into, app); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM app_usage WHERE app = ?`, app); err != nil {
		return err
	}
	return tx.Commit()
}

// AppCategories is every custom category, oldest first.
func (d *DB) AppCategories() ([]AppCategoryRow, error) {
	rows, err := d.sql.Query(`SELECT id, name, tone, created_by, created_at FROM app_categories ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppCategoryRow{}
	for rows.Next() {
		var c AppCategoryRow
		if err := rows.Scan(&c.ID, &c.Name, &c.Tone, &c.CreatedBy, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PutAppCategory inserts a category, or renames and recolours one with that
// id.
func (d *DB) PutAppCategory(id, name, tone, userID string, now int64) error {
	_, err := d.sql.Exec(`INSERT INTO app_categories (id, name, tone, created_by, created_at) VALUES (?, ?, ?, ?, ?)
	  ON CONFLICT(id) DO UPDATE SET name = excluded.name, tone = excluded.tone`, id, name, tone, userID, now)
	return err
}

// DeleteAppCategory removes a category. ErrAppRuleNotFound when there was
// none.
func (d *DB) DeleteAppCategory(id string) error {
	res, err := d.sql.Exec(`DELETE FROM app_categories WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAppRuleNotFound
	}
	return nil
}
