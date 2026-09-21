package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

// schemaVersion is the version a database created by `schemaDDL` is stamped at.
//
// ── 1..15 ARE THE NODE APP'S, 16 ONWARD ARE THIS PORT'S ─────────────────────
//
// Fifteen is where the Node app's migrations end, and the DDL is their final
// shape rather than a replay of them. Everything above it is a migration this
// port owns and applies itself, from `portMigrations` — the Node app is gone, so
// "Node owns migrations" stopped being a reason and became a reason nothing
// could ever be added.
//
// Stamping anything lower would make `Open` refuse the database it had just
// written; stamping higher than the migrations listed would claim ones that
// never ran.
const schemaVersion = 20

// portMigrations are the schema steps this port owns, keyed by the version they
// take a database TO.
//
// ── EVERY STATEMENT MUST BE SAFE TO RUN TWICE ───────────────────────────────
//
// The version row is what normally stops a replay, but a database that was
// created by `freshSchemaDDL` already HAS everything here and is stamped at the
// current version, so the two paths must agree. `IF NOT EXISTS` makes them
// agree without the two lists having to be compared.
//
// ── AND THEY RUN FROM cmd/mikrodash, NOT FROM Open ──────────────────────────
//
// `cmd/compat` opens a production /data through a READ-ONLY mount to prove this
// build can still read it. A migration inside `Open` would fail on the one tool
// whose job is to touch nothing. Same placement, and the same reason, as
// `RenamePageGrants`.
var portMigrations = map[int][]string{
	// 16: the operator's own documents — declared uplinks, pinned cabling, the
	// Wi-Fi site plan. See internal/db/routerdocs.go.
	16: {`CREATE TABLE IF NOT EXISTS router_docs (
          router_id  TEXT NOT NULL,
          kind       TEXT NOT NULL,
          data       TEXT NOT NULL,
          updated_at INTEGER NOT NULL,
          PRIMARY KEY (router_id, kind)
        )`},
	// 17: the AI Agent page, for the two seeded roles that already existed.
	//
	// ── seedRoles DOES NOT REACH AN INSTALL THAT IS ALREADY BUILT ────────────
	//
	// It runs inside `createSchema` and nowhere else, so editing `roleReadPages`
	// changes what a NEW database is born with and nothing about an existing
	// one. Without this, the decision to give Operator and Read Only the AI
	// Agent page would apply only to installs created after it — which is the
	// quietest possible way for a permission change to half-happen.
	//
	// `RenamePageGrants` is the precedent for touching `role_pages` outside
	// creation. This is additive rather than corrective, and it is bounded to
	// the two BUILTIN roles: a custom role somebody wrote is theirs, and an
	// upgrade must not decide what it confers.
	//
	// Safe to run twice, as every statement here must be: the primary key makes
	// the second insert a no-op rather than a duplicate row.
	//
	// FROM roles, NOT VALUES: both roles are deletable, and OR IGNORE does not
	// cover a foreign key. A VALUES insert naming a deleted role failed this
	// migration on every boot and held 18 behind it.
	17: {
		`INSERT OR IGNORE INTO role_pages (role_id, page, access)
		 SELECT id, 'ai-agent', 'read' FROM roles WHERE id IN ('readonly', 'operator')`,
	},
	// 18: the assistant's conversation history, per person per router.
	//
	// Scoped to BOTH because a thread that followed a router switch would hand
	// the model one device's discussion while it answered about another, and it
	// would do so confidently. Scoped to the person because a transcript is
	// theirs: it holds the questions they asked about their network.
	// 19: the WireGuard page's grants, carried across from the VPN page's.
	//
	// ── A SPLIT IS NOT A RENAME, AND `pages.Renamed` CANNOT EXPRESS IT ────
	//
	// The WireGuard peers used to live on the VPN page, so a `vpn` grant is what
	// conferred managing them. They are on their own page now and BOTH keys stay
	// live — `vpn` is the cross-protocol overview — so this is not a rename:
	// `pages.Renamed` would move the grant rather than copy it, and
	// `TestRenamedNamesNoLivePage` refuses an entry whose key is a current page.
	//
	// Without this, every install's operators keep a VPN page that no longer has
	// peers on it and silently lose the ability to manage them. NOTHING FAILS
	// when that happens, which is the exact shape of the 2026-09-01 incident
	// where readonly and operator quietly lost pages.
	//
	// ── EVERY ROLE, NOT JUST THE BUILTIN TWO ──────────────────────────────
	//
	// Migration 17 bounded itself to the builtin roles because it GRANTED
	// something new, and an upgrade must not decide what a custom role confers.
	// This one PRESERVES reach that already existed: a role that could manage
	// WireGuard peers yesterday can manage them today, at the same access level,
	// and a role that could only read still only reads.
	//
	// It is not perfectly conservative and the difference is worth stating: the
	// new page also manages WireGuard INTERFACES, which the VPN page never did,
	// so a role with `vpn` write gains creating and removing them. Carried
	// deliberately on the operator's decision of 2026-09-20 — the alternative
	// silently breaks the workflows people already have.
	//
	// Safe to run twice: the primary key makes the second insert a no-op.
	// FROM role_pages rather than VALUES, so a deleted role cannot fail it on
	// the foreign key — the trap migration 17 records at length.
	19: {
		`INSERT OR IGNORE INTO role_pages (role_id, page, access)
		 SELECT role_id, 'wireguard', access FROM role_pages WHERE page = 'vpn'`,
	},
	18: {`CREATE TABLE IF NOT EXISTS ai_messages (
          id        INTEGER PRIMARY KEY AUTOINCREMENT,
          ts        INTEGER NOT NULL,
          user_id   TEXT    NOT NULL,
          router_id TEXT    NOT NULL,
          role      TEXT    NOT NULL CHECK (role IN ('user','assistant')),
          text      TEXT    NOT NULL
        )`,
		`CREATE INDEX IF NOT EXISTS idx_ai_messages_thread
         ON ai_messages(user_id, router_id, ts)`},
	// 20: Config Management — templates, deploy runs, their targets, and drift
	// baselines. One constant for this and the fresh schema: cfg_schema.go.
	20: {cfgTablesDDL},
}

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

// Migrate applies every port-owned migration this database has not run.
//
// Returns how many it applied, so a start that changes nothing says nothing.
// NOT FATAL at the call site: a /data this cannot write is still perfectly able
// to serve every page — the features the new tables back are what stop working,
// and they say so in the UI rather than silently.
func (d *DB) Migrate() (int, error) {
	if d == nil || d.sql == nil {
		return 0, errors.New("db not open")
	}
	have, err := d.schemaVersion()
	if err != nil {
		return 0, err
	}
	applied := 0
	// ORDERED, because a later step may depend on an earlier one. A map alone
	// would apply them in whatever order Go felt like.
	versions := make([]int, 0, len(portMigrations))
	for v := range portMigrations {
		versions = append(versions, v)
	}
	sort.Ints(versions)

	for _, v := range versions {
		if v <= have {
			continue
		}
		tx, terr := d.sql.Begin()
		if terr != nil {
			return applied, terr
		}
		for _, stmt := range portMigrations[v] {
			if _, eerr := tx.Exec(stmt); eerr != nil {
				_ = tx.Rollback()
				return applied, fmt.Errorf("migration v%d: %w", v, eerr)
			}
		}
		if _, eerr := tx.Exec(
			`INSERT INTO schema_version (version, applied_at) VALUES (?, ?)`,
			v, time.Now().UnixMilli()); eerr != nil {
			_ = tx.Rollback()
			return applied, fmt.Errorf("migration v%d: recording it: %w", v, eerr)
		}
		if cerr := tx.Commit(); cerr != nil {
			return applied, cerr
		}
		applied++
	}
	return applied, nil
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
	// `wireguard` sits beside `vpn` rather than replacing it: the VPN page is
	// the cross-protocol overview and the WireGuard page is where the peers
	// are, so a new install's read roles need both. Migration 19 is what does
	// the same for installs that already exist.
	"vpn", "wireguard", "connections", "routing", "bandwidth", "firewall",
	"logs", "devices",
	// ── ai-agent IS A DELIBERATE WIDENING, DECIDED RATHER THAN INHERITED ──
	//
	// It is not in live's READ_ONLY_PAGES because live had no such page. Adding
	// it gives both seeded roles an assistant over everything they can already
	// read — which is the point, and is also why it is not silent: the page
	// itself stays invisible until an administrator enables and configures the
	// agent, so this grants reach to a feature that is off by default.
	//
	// It confers NOTHING a role could not already see. The context assembler is
	// handed the same per-page permission check the nav is, and every tool is
	// filtered by it before being advertised, so a viewer denied the Firewall
	// page gets an assistant that has never seen firewall data.
	"ai-agent",
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
