package db

// Sites — the last tier of the Routers map's location chain.
//
// Persistence only, matching the original: validation of names, lengths and
// coordinate ranges lives above this, in internal/geoplace.
//
// The table arrived in migration 4 and its `place_*` columns in migration 10,
// both below this package's MinSchema of 11 — so every column named here is
// present and no defensive existence check is warranted.

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Site is one row of the sites table.
//
// ── Lat AND Lon ARE POINTERS BECAUSE THE COLUMNS ARE NULLABLE ──────────────
//
// The live schema says why, in the migration that created them: "Nullable: most
// installs will never set them, and an unset location must not read as
// coordinates 0,0 in the Gulf of Guinea." Two float64s would place every site
// that has never been located in the Atlantic, and the map would draw it
// confidently.
//
// ── THE TEXT COLUMNS ARE POINTERS, AND WERE PLAIN STRINGS UNTIL 2026-08-28 ──
//
// They were `string`, on the argument that "NULL and empty are the same thing to
// geoplace.NormalizePlace, which trims and then tests, so nothing distinguishes
// them downstream". True of the CONSUMER and irrelevant to the PAYLOAD: this
// struct is marshalled straight onto the wire, so a NULL column reached the
// browser as `""` where the live app sends `null`.
//
// Found by running both servers against the same /data with the same cookie and
// diffing `/api/sites` — not by any test, because a round trip through one
// implementation agrees with itself whatever it wrote.
//
// It was also the ONLY nullable description in this package still doing that:
// `principals.go`, `rolewrite.go` and `groupwrite.go` all use `*string` and each
// carries a comment saying the column is nullable. Three out of four, which is
// the same shape as the JSON escaping found two days earlier.
//
// `web/src/pages/settings-sites.ts` already declared them `string | null`.
type Site struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description *string  `json:"description"`
	Lat         *float64 `json:"lat"`
	Lon         *float64 `json:"lon"`
	PlaceName   *string  `json:"place_name"`
	PlaceRegion *string  `json:"place_region"`
	PlaceCC     *string  `json:"place_cc"`
	CreatedAt   int64    `json:"created_at"`
}

// Coord returns a coordinate as `any`, so an unset one is a TRUE nil.
//
// ── THIS EXISTS BECAUSE OF A GO TRAP, NOT FOR TIDINESS ─────────────────────
//
// geoplace.SiteRow takes its coordinates as `any`, because it reproduces
// JavaScript's absent-versus-zero rule. Assigning a nil *float64 straight into
// an `any` does NOT produce a nil interface — it produces a non-nil interface
// holding a nil pointer, which `case nil:` does not match. The coordinate would
// then fall through to the default and be rejected, so a site WITH a location
// would be silently dropped instead of drawn... and worse, the same trap in the
// other direction is what puts an unset site at 0,0.
//
// One conversion, used by every caller, rather than the same three lines
// repeated at each of them with one of them wrong.
func Coord(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// GetSite is one site, or (nil, nil) when there is none with that id.
//
// A MISSING SITE IS NOT AN ERROR HERE, and the distinction is load-bearing: the
// caller answers 404 for nil and 500 for err, and collapsing the two would turn
// a database fault into "No such site" — after which the membership route would
// look like it had been given a bad id rather than a broken store.
func (d *DB) GetSite(id string) (*Site, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	var st Site
	// SIX OF THESE NINE COLUMNS ARE NULLABLE, and `description` is null for every
	// site created without one — which is most of them. Scanning it straight into
	// a `string` fails with "converting NULL to string is unsupported", so the
	// lookup would 500 on an ordinary site. `ListSites` above already does this;
	// the first version of GetSite did not, and its test DDL declared the columns
	// `NOT NULL DEFAULT ''` — so the fixture could not produce the row that breaks
	// it. The DDL now matches the real schema.
	//
	// `lat`/`lon` need no NullFloat64: they are already `*float64`, and
	// database/sql sets a pointer destination to nil on NULL.
	var desc, pn, pr, pc sql.NullString
	err := d.sql.QueryRow(`SELECT id, name, description, lat, lon,
	    place_name, place_region, place_cc, created_at
	  FROM sites WHERE id = ?`, id).Scan(&st.ID, &st.Name, &desc,
		&st.Lat, &st.Lon, &pn, &pr, &pc, &st.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st.Description, st.PlaceName, st.PlaceRegion, st.PlaceCC =
		nullStr(desc), nullStr(pn), nullStr(pr), nullStr(pc)
	return &st, nil
}

// ListSites is every site, ordered as the original orders them.
//
// ── THE EXPLICIT `COLLATE NOCASE` IS REDUNDANT, AND KEPT ANYWAY ────────────
//
// The COLUMN is declared `TEXT NOT NULL UNIQUE COLLATE NOCASE`, so SQLite
// applies that collation to a bare `ORDER BY name` already. Measured, not
// assumed: dropping the clause from this query leaves the order identical, and
// the ordering test does not fail. What the test DOES catch is a wrong
// collation — `COLLATE BINARY` puts every capitalised name first and fails
// loudly.
//
// The clause stays because it is what the original writes, and because the
// alternative is a query whose correctness depends silently on a declaration in
// a table this package does not own and must never migrate.
func (d *DB) ListSites() ([]Site, error) {
	if d == nil || d.sql == nil {
		return []Site{}, errors.New("db not open")
	}
	rows, err := d.sql.Query(`SELECT id, name, description, lat, lon,
                       place_name, place_region, place_cc, created_at
                FROM sites ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return []Site{}, err
	}
	defer rows.Close()

	out := []Site{}
	for rows.Next() {
		var s Site
		var desc, pn, pr, pc sql.NullString
		var lat, lon sql.NullFloat64
		if err := rows.Scan(&s.ID, &s.Name, &desc, &lat, &lon,
			&pn, &pr, &pc, &s.CreatedAt); err != nil {
			return []Site{}, err
		}
		s.Description, s.PlaceName, s.PlaceRegion, s.PlaceCC =
			nullStr(desc), nullStr(pn), nullStr(pr), nullStr(pc)
		if lat.Valid {
			v := lat.Float64
			s.Lat = &v
		}
		if lon.Valid {
			v := lon.Float64
			s.Lon = &v
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// -- writes ------------------------------------------------------------------

// siteWritableColumns is what `updateSite` may set, in the live order.
//
// A WHITELIST, NOT A CONVENIENCE. The column name goes into SQL TEXT - there is
// no way to parameterise an identifier - so a name reaching it from a request
// body would be an injection. `sites.Patch.Columns()` already produces only
// these seven; this list is what makes that a property of the WRITER rather than
// a property of one caller.
var siteWritableColumns = []string{
	"name", "description", "lat", "lon", "place_name", "place_region", "place_cc",
}

// CreateSite inserts a site and returns it as stored.
//
// `id` is a v4 UUID and `created_at` is now, both minted HERE exactly as the live
// function mints them - neither is accepted from a caller, which is why
// `sites.ParseSiteBody` dropping unknown keys is defence in depth rather than the
// only defence.
func (d *DB) CreateSite(cols map[string]any) (*Site, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	name, _ := cols["name"].(string)
	if name == "" {
		return nil, errors.New("db: a site needs a name")
	}
	id, err := newSiteID()
	if err != nil {
		return nil, err
	}
	// The six optional columns default to NULL, matching the live signature's
	// `description = null, lat = null, ...`. An ABSENT key is NULL on a create;
	// the absent-versus-null distinction only means something on an UPDATE, where
	// there is an existing value to leave alone.
	if _, err := d.sql.Exec(`INSERT INTO sites
	    (id, name, description, lat, lon, place_name, place_region, place_cc, created_at)
	  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, name, cols["description"], cols["lat"], cols["lon"],
		cols["place_name"], cols["place_region"], cols["place_cc"],
		time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	return d.GetSite(id)
}

// UpdateSite writes only the columns actually supplied.
//
// AN EMPTY MAP IS NOT AN ERROR and not a nil: it returns the site unchanged, the
// way the live function does. A body whose every field was absent is a legitimate
// request - it just has nothing to say.
func (d *DB) UpdateSite(id string, cols map[string]any) (*Site, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	sets := make([]string, 0, len(siteWritableColumns))
	params := make([]any, 0, len(siteWritableColumns)+1)
	for _, col := range siteWritableColumns {
		if v, ok := cols[col]; ok {
			sets = append(sets, col+" = ?")
			params = append(params, v)
		}
	}
	if len(sets) == 0 {
		return d.GetSite(id)
	}
	params = append(params, id)
	if _, err := d.sql.Exec(
		`UPDATE sites SET `+strings.Join(sets, ", ")+` WHERE id = ?`, params...); err != nil {
		return nil, err
	}
	return d.GetSite(id)
}

// DeleteSite removes a site and reports whether a row went.
//
// DETACHING THE DEVICES IS THE CALLER'S JOB. Devices live in `routers.json`, so
// SQLite cannot cascade into them, and a device pointing at a site that no longer
// exists renders a blank chip and is unreachable to a site-scoped grant. The live
// comment says the same thing in the same place.
func (d *DB) DeleteSite(id string) (bool, error) {
	if d == nil || d.sql == nil {
		return false, errors.New("db not open")
	}
	res, err := d.sql.Exec(`DELETE FROM sites WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// IsDuplicateSiteName reports whether an error is the UNIQUE index refusing a
// name that already exists, case-insensitively.
//
// THE INDEX IS THE ENFORCEMENT, not a pre-check. The live route says so where it
// catches this: "a duplicate surfaces here rather than from a pre-check that would
// race anyway". Two administrators creating "Depot" at the same moment both pass a
// SELECT and one still has to lose.
func IsDuplicateSiteName(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// newSiteID is a v4 UUID, matching `crypto.randomUUID()`.
func newSiteID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// SiteColumns is one site as its COLUMNS, with NULLs preserved as true nils.
//
// ── WHY `GetSite` WILL NOT DO ───────────────────────────────────────────────
//
// `Site` collapses a NULL text column to "" — deliberately, because
// `geoplace.NormalizePlace` trims and then tests, so nothing downstream can tell
// them apart. The AUDIT TRAIL can: `audit.Diff` walks the after keys and compares
// each against before, and the after side is `sites.Patch.Columns()`, which
// writes a true nil for a cleared field. Comparing that against "" reports a
// change on every save of a site that never had a description — noise in the one
// record that exists to make real changes visible.
//
// The live route has this for free: `_before` is the raw row, where a NULL is a
// null. This is that row.
func (d *DB) SiteColumns(id string) (map[string]any, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	var name string
	var desc, pn, pr, pc sql.NullString
	var lat, lon *float64
	err := d.sql.QueryRow(`SELECT name, description, lat, lon,
	    place_name, place_region, place_cc
	  FROM sites WHERE id = ?`, id).Scan(&name, &desc, &lat, &lon, &pn, &pr, &pc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// `Coord` rather than a bare assignment: a nil *float64 in an `any` is a
	// NON-NIL interface holding a nil pointer, which compares unequal to a real
	// nil and would report a coordinate change on every save.
	return map[string]any{
		"name":         name,
		"description":  nullText(desc),
		"lat":          Coord(lat),
		"lon":          Coord(lon),
		"place_name":   nullText(pn),
		"place_region": nullText(pr),
		"place_cc":     nullText(pc),
	}, nil
}

// nullText keeps a SQL NULL as a true nil, where `Site` would give "".
func nullText(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}

// nullStr keeps a NULL column distinct from an empty one, all the way to the
// wire.
//
// `sql.NullString.String` is "" for both, which is exactly the collapse this
// package spent a release not noticing: `{"description": ""}` where the live app
// sends `{"description": null}`. A site whose description was never set and one
// set to the empty string are different rows, and the payload says so.
func nullStr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}
