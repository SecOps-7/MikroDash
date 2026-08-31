// Package db reads and writes the SQLite database the Node app owns.
//
// TWO WRITERS, ONE FILE, AND ONLY ONE OWNER OF THE SCHEMA.
//
// The port is strangler-fig: this process runs alongside the Node one and both
// have the same /data mounted. SQLite supports that — the file is already in WAL
// mode, so a reader never blocks a writer and a second writer serialises rather
// than corrupting — but "supported" is doing a lot of work in that sentence, and
// two things have to be true for it to hold.
//
// FIRST: THIS SIDE NEVER MIGRATES. src/db.js carries fourteen numbered
// migrations and runs them at open. If this package ran them too, two processes
// could apply the same migration concurrently, and a migration is exactly the
// operation that cannot survive being run twice. Worse, the Go side would then
// be asserting a schema the Node side has not agreed to — and Node is still the
// product. So Open() asserts a MINIMUM schema version and refuses below it.
// Node creates tables; this appends rows to them.
//
// SECOND: A BUSY TIMEOUT, NOT A BUSY ERROR. Without one, a write landing while
// Node holds the write lock fails at once with SQLITE_BUSY. With one, it waits.
// The audit trail is the one table where a dropped row is a silent hole in a
// record that exists to be complete, so it waits.
//
// `foreign_keys` is set here because SQLite defaults it OFF and it is a
// PER-CONNECTION setting rather than a property of the file — Node setting it
// says nothing about this connection. Same note as src/db.js:622.
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	_ "modernc.org/sqlite"
)

// MinSchema is the migration that created audit_events (src/db.js, version 11).
// Below it the table does not exist and nothing here can work; at or above it
// the columns this package names are present. A floor rather than an equality on
// purpose: Node is free to migrate forward, and a port demanding an exact
// version would break every time it did.
const MinSchema = 11

// DB is a handle on the shared database.
type DB struct {
	sql  *sql.DB
	path string
	// vacuums counts entries into `Vacuum`. See `VacuumCountForTest`.
	vacuums atomic.Int64
}

// Open opens <dataDir>/mikrodash.db. It does not create it: an absent file means
// the Node app has never run here, and creating an empty one would hand back a
// database with no schema and no migrations — a failure that then surfaces later
// and further away than its cause.
func Open(dataDir string) (*DB, error) {
	path := filepath.Join(dataDir, "mikrodash.db")

	// Checked explicitly, because the driver would happily create it. An empty
	// file has no schema_version, so the check below would report "v0, needs
	// v11" — technically true and useless, since the real problem is that this
	// is the wrong /data.
	if st, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s does not exist — is this the right /data? "+
				"(the Node app creates and migrates it)", path)
		}
		return nil, err
	} else if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}

	// _txlock=immediate takes the write lock when a transaction begins rather
	// than when it first writes. Without it two transactions that read and then
	// write can deadlock, and SQLite resolves that by returning SQLITE_BUSY to
	// one of them immediately — a busy timeout does not apply to an upgrade
	// deadlock, so the timeout above would not save it.
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
	h, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	// One connection. The driver is safe for concurrent use, but a pool means
	// several writers inside THIS process competing for a lock the Node process
	// may already hold, and nothing here needs the parallelism — the audit path
	// is a single insert per user action.
	h.SetMaxOpenConns(1)

	if err := h.Ping(); err != nil {
		h.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	d := &DB{sql: h, path: path}
	v, err := d.schemaVersion()
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("read schema_version from %s: %w", path, err)
	}
	if v < MinSchema {
		h.Close()
		return nil, fmt.Errorf("%s is at schema v%d; this build needs v%d or later "+
			"(the Node app owns migrations — run it once against this /data)", path, v, MinSchema)
	}
	return d, nil
}

func (d *DB) Close() error {
	if d == nil || d.sql == nil {
		return nil
	}
	return d.sql.Close()
}

// Path is the file this handle is open on, for the compat gate to report.
func (d *DB) Path() string { return d.path }

func (d *DB) schemaVersion() (int, error) {
	var v sql.NullInt64
	if err := d.sql.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

// SchemaVersion is the highest migration the Node app has applied.
func (d *DB) SchemaVersion() (int, error) { return d.schemaVersion() }

// ── audit_events ─────────────────────────────────────────────────────────────

// Event is one row of the trail, with the fields named as src/db.js names its
// bindings so the two read side by side.
type Event struct {
	TS         int64
	ActorID    string
	ActorName  string
	ActorIP    string
	Action     string
	Scope      string
	RouterID   string
	TargetType string
	TargetID   string
	TargetName string
	Outcome    string
	Detail     string // already-encoded JSON, or "" for NULL
}

// nul maps Go's zero string to SQL NULL, which is what `ev.actorId || null` does
// on the Node side. Every nullable column in this table is written that way.
func nul(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const insertAuditSQL = `
INSERT INTO audit_events
  (ts, actor_id, actor_name, actor_ip, action, scope, router_id,
   target_type, target_id, target_name, outcome, detail)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// InsertAuditEvent appends one row.
//
// The defaults are db.js's rather than this package's invention: an empty actor
// becomes 'system', and any scope that is not exactly "router" becomes "app".
// The second is a CHECK constraint in the schema, so clamping here turns a
// caller's typo into an app-scoped row instead of a failed insert that loses the
// event altogether — and losing the event is the one outcome this table exists
// to prevent.
func (d *DB) InsertAuditEvent(ev Event) error {
	if d == nil || d.sql == nil {
		return errors.New("db not open")
	}
	if ev.ActorName == "" {
		ev.ActorName = "system"
	}
	if ev.Scope != "router" {
		ev.Scope = "app"
	}
	if ev.Outcome == "" {
		ev.Outcome = "ok"
	}
	_, err := d.sql.Exec(insertAuditSQL,
		ev.TS, nul(ev.ActorID), ev.ActorName, nul(ev.ActorIP), ev.Action,
		ev.Scope, nul(ev.RouterID), nul(ev.TargetType), nul(ev.TargetID),
		nul(ev.TargetName), ev.Outcome, nul(ev.Detail))
	return err
}

// Row is a read-back event. The JSON tags match what the Node API sends the
// browser, because the Audit page will be ported against this shape.
type Row struct {
	ID         int64   `json:"id"`
	TS         int64   `json:"ts"`
	ActorID    *string `json:"actor_id"`
	ActorName  string  `json:"actor_name"`
	ActorIP    *string `json:"actor_ip"`
	Action     string  `json:"action"`
	Scope      string  `json:"scope"`
	RouterID   *string `json:"router_id"`
	TargetType *string `json:"target_type"`
	TargetID   *string `json:"target_id"`
	TargetName *string `json:"target_name"`
	Outcome    string  `json:"outcome"`
	// Detail is the stored JSON AS A STRING, or nil for NULL — NOT embedded
	// JSON. better-sqlite3 hands db.js a TEXT column as a JS string and
	// `res.json` then sends a string, so the page's `detailCell` does
	// `JSON.parse(raw)` on it. Sending an object here made that parse throw and
	// every detail cell render "[object Object]"; the DOM comparison against the
	// lifted live renderer is what caught it, after a comment right here had
	// asserted the opposite.
	Detail *string `json:"detail"`
}

// Query is the filter set queryAuditEvents accepts.
type Query struct {
	// RouterIDs is the concrete list of routers the caller may see, and
	// IncludeApp whether app-scope rows are permitted. Both are decided by the
	// caller from RBAC; this type does not know about sessions.
	RouterIDs  []string
	IncludeApp bool

	From, To int64
	RouterID string
	Actor    string
	Outcome  string
	Action   string // prefix match
	Search   string

	Limit, Offset int
}

// Page is one page of the trail plus the unpaged total.
type Page struct {
	Rows   []Row `json:"rows"`
	Total  int   `json:"total"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
}

// QueryAuditEvents reads the trail, filtered and paged.
//
// VISIBILITY IS BUILT FIRST SO NO LATER FILTER CAN WIDEN IT, and the empty case
// yields NOTHING rather than everything. That is the shape of the whole function
// and the reason it is not a generic query builder: "no routers and no app
// scope" describes a caller with no permissions, and the bug class where an
// empty allow-list means unrestricted is precisely what the Node version was
// written to avoid. Reproduced deliberately, early return included.
func (d *DB) QueryAuditEvents(q Query) (Page, error) {
	empty := Page{Rows: []Row{}}
	if d == nil || d.sql == nil {
		return empty, errors.New("db not open")
	}

	var where []string
	var args []any

	switch {
	case q.IncludeApp && len(q.RouterIDs) > 0:
		where = append(where, "(scope = 'app' OR router_id IN ("+placeholders(len(q.RouterIDs))+"))")
		for _, id := range q.RouterIDs {
			args = append(args, id)
		}
	case q.IncludeApp:
		where = append(where, "scope = 'app'")
	case len(q.RouterIDs) > 0:
		where = append(where, "(scope = 'router' AND router_id IN ("+placeholders(len(q.RouterIDs))+"))")
		for _, id := range q.RouterIDs {
			args = append(args, id)
		}
	default:
		return empty, nil
	}

	if q.From != 0 {
		where = append(where, "ts >= ?")
		args = append(args, q.From)
	}
	if q.To != 0 {
		where = append(where, "ts <= ?")
		args = append(args, q.To)
	}
	if q.RouterID != "" {
		where = append(where, "router_id = ?")
		args = append(args, q.RouterID)
	}
	if q.Actor != "" {
		where = append(where, "actor_name = ?")
		args = append(args, q.Actor)
	}
	if q.Outcome != "" {
		where = append(where, "outcome = ?")
		args = append(args, q.Outcome)
	}
	// Prefix match, so 'router' selects router.create/update/delete without the
	// caller needing to know the verb list.
	if q.Action != "" {
		where = append(where, "action LIKE ?")
		args = append(args, q.Action+"%")
	}
	if q.Search != "" {
		where = append(where, "(action LIKE ? OR target_name LIKE ? OR detail LIKE ? OR actor_name LIKE ?)")
		s := "%" + q.Search + "%"
		args = append(args, s, s, s, s)
	}

	tail := " FROM audit_events WHERE " + strings.Join(where, " AND ")

	var total int
	if err := d.sql.QueryRow("SELECT COUNT(*) AS n"+tail, args...).Scan(&total); err != nil {
		return empty, err
	}

	limit := clamp(q.Limit, 200, 1, 1000)
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}

	rows, err := d.sql.Query(
		"SELECT id, ts, actor_id, actor_name, actor_ip, action, scope, router_id, "+
			"target_type, target_id, target_name, outcome, detail"+tail+
			" ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?",
		append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return empty, err
	}
	defer rows.Close()

	out := []Row{}
	for rows.Next() {
		var r Row
		var detail sql.NullString
		if err := rows.Scan(&r.ID, &r.TS, &r.ActorID, &r.ActorName, &r.ActorIP, &r.Action,
			&r.Scope, &r.RouterID, &r.TargetType, &r.TargetID, &r.TargetName,
			&r.Outcome, &detail); err != nil {
			return empty, err
		}
		// A NULL column marshals as null, which is what the Node row does too.
		if detail.Valid {
			d := detail.String
			r.Detail = &d
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return empty, err
	}
	return Page{Rows: out, Total: total, Limit: limit, Offset: offset}, nil
}

// Facets are the distinct actors and actions, for the filter dropdowns.
type Facets struct {
	Actors  []string `json:"actors"`
	Actions []string `json:"actions"`
}

func (d *DB) AuditFacets() (Facets, error) {
	f := Facets{Actors: []string{}, Actions: []string{}}
	if d == nil || d.sql == nil {
		return f, errors.New("db not open")
	}
	var err error
	if f.Actors, err = d.distinct(`SELECT DISTINCT actor_name FROM audit_events ORDER BY actor_name`); err != nil {
		return f, err
	}
	if f.Actions, err = d.distinct(`SELECT DISTINCT action FROM audit_events ORDER BY action`); err != nil {
		return f, err
	}
	return f, nil
}

func (d *DB) distinct(q string) ([]string, error) {
	rows, err := d.sql.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if s.Valid {
			out = append(out, s.String)
		}
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// clamp reproduces `Math.min(Math.max(parseInt(v) || def, lo), hi)`. The `|| def`
// matters: a zero limit is falsy in JavaScript and becomes the default rather
// than a page of nothing.
func clamp(v, def, lo, hi int) int {
	if v == 0 {
		v = def
	}
	return max(lo, min(hi, v))
}

// ── the grant graph ──────────────────────────────────────────────────────────
//
// Read, never written. Node owns principals, roles and grants; this side only
// needs to ANSWER "may this user write this page on this router", and answering
// it here is what closes the over-permissive gap documented in auth.go.

// Grant is one row of the grant graph as rbac.js's viewFor consumes it.
type Grant struct {
	RoleID    string
	ScopeType string // "global" | "site" | "router"
	ScopeID   string
}

// GrantsForUser returns every grant reaching a user, DIRECTLY OR THROUGH A
// GROUP. The group half is not optional: a principal whose access comes only
// from a group membership would otherwise resolve to no access at all, which
// fails closed but locks a legitimate user out of every page.
//
// Byte-for-byte the query in db.js:grantsForUser, so the two cannot drift into
// disagreeing about what a grant is.
func (d *DB) GrantsForUser(userID string) ([]Grant, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	rows, err := d.sql.Query(`
		SELECT role_id, scope_type, scope_id FROM grants
		WHERE (principal_type = 'user'  AND principal_id = ?)
		   OR (principal_type = 'group' AND principal_id IN
		         (SELECT group_id FROM group_members WHERE user_id = ?))`,
		userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		var g Grant
		var scopeID sql.NullString
		if err := rows.Scan(&g.RoleID, &g.ScopeType, &scopeID); err != nil {
			return nil, err
		}
		g.ScopeID = scopeID.String
		out = append(out, g)
	}
	return out, rows.Err()
}

// RolePage is one page grant a role confers.
type RolePage struct {
	Page   string
	Access string // "read" | "write"
}

// Role is what canPage needs to know about a role: whether it is builtin, and
// which pages it names.
//
// BUILTIN IS STRUCTURAL, NOT TABLE-DRIVEN. Administrator confers every page at
// write without a single role_pages row, so a port that only read the table
// would deny an administrator everything — silently, and only on installs that
// have one. rbac.js says the same thing at more length: "a page added in a later
// release is covered with no data migration".
type Role struct {
	ID      string
	Builtin bool
	Pages   []RolePage
}

// RoleByID reads one role and its pages. Returns nil (and no error) when the
// role does not exist: a grant naming a deleted role confers nothing, which is
// what rbac.js's _roleDef does when getRole misses.
func (d *DB) RoleByID(id string) (*Role, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	var builtin int
	err := d.sql.QueryRow(`SELECT builtin FROM roles WHERE id = ?`, id).Scan(&builtin)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r := &Role{ID: id, Builtin: builtin != 0, Pages: []RolePage{}}
	if r.Builtin {
		return r, nil // pages are structural; the caller supplies the key set
	}
	rows, err := d.sql.Query(`SELECT page, access FROM role_pages WHERE role_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p RolePage
		if err := rows.Scan(&p.Page, &p.Access); err != nil {
			return nil, err
		}
		r.Pages = append(r.Pages, p)
	}
	return r, rows.Err()
}
