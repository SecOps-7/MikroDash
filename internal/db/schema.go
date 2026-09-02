package db

import (
	"database/sql"
	"fmt"
	"os"
	"time"
)

// schemaVersion is the version a database created by `schemaDDL` is stamped at.
//
// It is the version the Node app's fifteen migrations END at, because the DDL is
// their final shape rather than a replay of them. Stamping anything lower would
// make `Open` refuse the database it had just written; stamping higher would
// claim migrations that never ran.
const schemaVersion = 15

// createSchema builds a new database at `path`.
//
// ── EVERY VERSION IS STAMPED, NOT JUST THE LAST ────────────────────────────
//
// `schema_version` holds one row per applied migration — live's runner inserts
// as it goes, and `MAX(version)` is what reads it back. A fresh database has run
// none of them, but it HAS arrived at the state they produce, so recording only
// v15 would let a future migration runner believe 1..14 are outstanding and
// replay them against tables that already exist.
//
// ── AND IT IS ALL ONE TRANSACTION ──────────────────────────────────────────
//
// A half-written database is worse than none: `Open` would find a file, skip
// this path for ever, and then fail on whichever table did not get made. On any
// error the file is removed, so the next start tries again from nothing.
func createSchema(path string) error {
	h, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	defer h.Close()

	if err := createSchemaIn(h); err != nil {
		// Leave nothing half-built behind. The error that matters is the first
		// one; a failure to clean up is worth neither reporting nor hiding.
		_ = h.Close()
		_ = os.Remove(path)
		return fmt.Errorf("creating the schema in %s: %w", path, err)
	}
	return nil
}

func createSchemaIn(h *sql.DB) error {
	tx, err := h.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed

	if _, err := tx.Exec(freshSchemaDDL); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for v := 1; v <= schemaVersion; v++ {
		if _, err := tx.Exec(
			`INSERT INTO schema_version (version, applied_at) VALUES (?, ?)`, v, now); err != nil {
			return err
		}
	}
	if err := seedRoles(tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

// builtinRoles is what migration 7 inserted, verbatim.
//
// THE SCHEMA ALONE IS NOT ENOUGH, and a fresh install proved it: `grants.role_id`
// REFERENCES `roles(id)`, so with an empty roles table `grantFirstAdmin` failed
// with "FOREIGN KEY constraint failed" and the first administrator held no
// grants — the same symptom as having no database at all. Tables without their
// seed are a schema, not an install.
var builtinRoles = []struct {
	id, name, desc string
	builtin        int
}{
	{"administrator", "Administrator",
		"Full access to everything, including users, groups, roles and sites.", 1},
	{"operator", "Operator", "Acknowledge alerts, read reports and run diagnostics.", 0},
	{"readonly", "Read Only", "View live data only. No reports, no settings.", 0},
}

// roleReadPages is live's READ_ONLY_PAGES, **with today's page keys**.
//
// The originals were `topology`, `wireless` and `routers`; those keys were
// renamed on 2026-09-01 and `pages.Renamed` carries the mapping for grants
// already stored. Seeding the old names and letting that ledger correct them on
// the next start would work and would be too clever: a new install would hold
// keys that name nothing for the length of one startup.
//
// `TestTheSeededRolePagesNameRealPages` fails if a later rename orphans one.
var roleReadPages = []string{
	"dashboard", "network-topology", "wifi-clients", "interfaces", "dhcp",
	"vpn", "connections", "routing", "bandwidth", "firewall",
	"logs", "devices",
}

// operatorWritePages: the two pages whose actions the role holds — dashboard
// (acknowledge) and firewall (diagnose). Everything else it sees, it reads.
var operatorWritePages = map[string]bool{"dashboard": true, "firewall": true}

// seedRoles inserts the three builtin roles and the two page matrices.
//
// ── READ ONLY GETS NO `reports` ROW, AND THAT IS THE LIVE COMMENT'S POINT ───
//
// "Read Only has NO reports row: today's viewer holds router:read and nothing
// else, and a reports row confers router:history, which would hand every
// existing viewer historical reports and exports they do not have." Operator
// gets it; neither gets settings. Reproducing the matrix exactly matters more
// than making it tidy — a generous approximation here silently widens every
// install's least-privileged role.
//
// Administrator gets NO role_pages rows at all: its reach is structural, so a
// page added in a later release is covered with no data change.
func seedRoles(tx *sql.Tx, now int64) error {
	for _, r := range builtinRoles {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO roles (id, name, description, builtin, created_at)
			 VALUES (?, ?, ?, ?, ?)`, r.id, r.name, r.desc, r.builtin, now); err != nil {
			return fmt.Errorf("seeding role %s: %w", r.id, err)
		}
	}

	page := func(role, p, access string) error {
		_, err := tx.Exec(
			`INSERT OR IGNORE INTO role_pages (role_id, page, access) VALUES (?, ?, ?)`,
			role, p, access)
		return err
	}
	for _, p := range roleReadPages {
		if err := page("readonly", p, "read"); err != nil {
			return fmt.Errorf("seeding readonly/%s: %w", p, err)
		}
	}
	for _, p := range append(append([]string{}, roleReadPages...), "reports") {
		access := "read"
		if operatorWritePages[p] {
			access = "write"
		}
		if err := page("operator", p, access); err != nil {
			return fmt.Errorf("seeding operator/%s: %w", p, err)
		}
	}
	return nil
}
