package db

// The credential profile tables: RouterOS accounts MikroDash provisions onto
// many devices, and one row per device saying whether that account is really
// there yet.
//
// ── ONE CONSTANT, TWO PATHS ────────────────────────────────────────────────
//
// Shared by `freshSchemaDDL` and `portMigrations[31]`, the rule `cfgTablesDDL`,
// `ztpTablesDDL`, `notifyTablesDDL` and `ssoTablesDDL` follow: a new database
// and a migrated one must not be able to describe different tables, and the
// only way to guarantee that is for there to be one description.
//
// ── WHAT THIS IS NOT ───────────────────────────────────────────────────────
//
// It is NOT where MikroDash's own login is kept. That stays in `routers.json`,
// and `internal/guard/selfguard.go` refuses any write that could disturb it.
// A profile provisions accounts for PEOPLE; MikroDash signs in as itself
// throughout, and a profile that named MikroDash's own account or group is
// refused twice over - once when it is saved, once per router by the guard.
//
// ── TWO TABLES, BECAUSE ONE IS INTENT AND THE OTHER IS FACT ────────────────
//
// `cred_profiles` is what the operator asked for. `cred_profile_links` is what
// each router has actually been told, which is a different thing and lags: a
// device can be off, unreachable, or refusing. Folding the two together would
// leave "the password is now X" indistinguishable from "the password is now X
// on the eleven routers that answered".
//
// That also makes the link table the DESIRED-STATE ledger a background
// reconciler reads. `applied_revision` below `cred_profiles.revision` is the
// whole of "this device owes an update", so an offline router converges when it
// comes back with nobody pressing anything.
//
// ── secret ARRIVES SEALED ──────────────────────────────────────────────────
//
// Sealed with the settings envelope before it gets here; `internal/db` never
// encrypts anything itself. Same split as `notify_channels.config`,
// `sso_providers.client_secret` and `ztp_devices.secret`.
//
// ONE PASSWORD PER PROFILE, not one per device - the operator's choice, and it
// has a cost worth writing down where the column is: /data now holds a
// credential valid on every linked router at that profile's privilege. That is
// recorded in SECURITY.md, because a column comment is not where anyone looks
// for a threat model.
//
// ── created_by AND linked_by ARE USER IDS, NEVER USERNAMES ─────────────────
//
// Same rule, and the same reason, as `grants.principal_id` and
// `sso_identities.user_id`: a round-trip test cannot catch the other choice,
// because one implementation agrees with itself whatever it wrote. They are in
// the ledger at `internal/verify/identity_test.go`.
//
// ── ON DELETE RESTRICT, NOT CASCADE ────────────────────────────────────────
//
// A cascade would let deleting a profile silently forget accounts that are
// still sitting on routers - MikroDash losing track of a login it created,
// which is the worst outcome this feature can produce. The profile row goes
// only once every link has been resolved, and the one way to drop them anyway
// is the explicit Forget action, which audits every device it abandons.
//
// ── NO CHECK CONSTRAINT ON state ───────────────────────────────────────────
//
// The vocabulary lives in Go, for the reason `cfgTablesDDL` gives: a CHECK
// makes adding a state a table rebuild, and the states here will change as the
// applier learns what routers actually do.
const credProfTablesDDL = `
CREATE TABLE IF NOT EXISTS cred_profiles (
  id            TEXT PRIMARY KEY,
  -- What the operator calls it. UNIQUE so two profiles cannot be told apart
  -- only by their id in a list.
  -- (No backticks in this comment: it sits inside a Go raw string.)
  name          TEXT NOT NULL UNIQUE,
  description   TEXT NOT NULL DEFAULT '',
  -- The RouterOS account name this profile creates. UNIQUE because two
  -- profiles claiming one username on one router has no good resolution: one
  -- would silently win, per device, depending on apply order.
  -- (No backticks in this comment: it sits inside a Go raw string.)
  ros_username  TEXT NOT NULL UNIQUE,
  -- ── EVERY PROFILE OWNS ITS GROUP. NONE SHARE ONE. ───────────────────────
  --
  -- There is no "use RouterOS's read group" option, and its absence is the
  -- design. A profile that joined a built-in group could collide with the one
  -- MikroDash signs in with (the lockout guard then refuses it), would share
  -- its permissions with every other profile that joined it, and would change
  -- accounts this feature never made when the policies were edited.
  --
  -- NOT editable after creation: renaming a group across every linked device is
  -- a migration, not an edit.
  -- (No backticks in this comment: it sits inside a Go raw string.)
  group_name    TEXT NOT NULL,
  -- A JSON array of names from resource.UserPolicies. The WHOLE vocabulary is
  -- available: what a profile may grant is not a function of what MikroDash
  -- itself holds. Measured on RouterOS 7.24.4 - a user holding policy but not
  -- sniff created a group granting sniff, and the router stored it.
  -- (No backticks in this comment: it sits inside a Go raw string.)
  policy_json   TEXT NOT NULL DEFAULT '[]',
  -- Sealed with the settings envelope before it reaches this table.
  secret        TEXT NOT NULL,
  -- Bumped by any change a router would have to be told about: the username,
  -- the password, the permissions. NOT by a description edit, which no device
  -- can observe - a bump there would re-write every linked router for nothing.
  -- (No backticks in this comment: it sits inside a Go raw string.)
  revision      INTEGER NOT NULL DEFAULT 1,
  -- Set when Delete was pressed while routers still held the account. The
  -- accounts are taken off first and the profile goes when the last link has
  -- CONFIRMED its removal, so a router that is switched off holds the delete
  -- open rather than stranding an account nobody knows about.
  pending_delete INTEGER NOT NULL DEFAULT 0,
  -- A store.User.ID. See the header.
  created_by    TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS cred_profile_links (
  profile_id       TEXT NOT NULL REFERENCES cred_profiles(id) ON DELETE RESTRICT,
  router_id        TEXT NOT NULL,
  -- pending applying applied refused conflict failed unreachable removing
  -- orphaned unknown. Vocabulary in Go; see internal/credprof.
  -- (No backticks in this comment: it sits inside a Go raw string.)
  state            TEXT NOT NULL DEFAULT 'pending',
  -- A short machine-readable reason, so the page can explain a refusal without
  -- parsing prose: protected-group-value, last-full-user, conflict, and so on.
  -- (No backticks in this comment: it sits inside a Go raw string.)
  code             TEXT NOT NULL DEFAULT '',
  error            TEXT NOT NULL DEFAULT '',
  -- The cred_profiles.revision this device has actually been told. Below the
  -- profile's own revision means it owes an update; 0 means it has never had
  -- one applied.
  -- (No backticks in this comment: it sits inside a Go raw string.)
  applied_revision INTEGER NOT NULL DEFAULT 0,
  attempts         INTEGER NOT NULL DEFAULT 0,
  next_attempt_at  INTEGER NOT NULL DEFAULT 0,
  last_attempt_at  INTEGER NOT NULL DEFAULT 0,
  applied_at       INTEGER NOT NULL DEFAULT 0,
  -- HOW this router came to be linked: 'direct' means somebody picked it,
  -- 'site' means it is in a site the profile is linked to.
  --
  -- The difference decides what happens when a router LEAVES a site. A 'site'
  -- link is membership-derived, so it goes with the membership. A 'direct' one
  -- was an explicit choice and survives. A router that is both stays 'direct',
  -- because the explicit choice is the stronger claim and losing it to a site
  -- edit would be the surprise.
  -- (No backticks in this comment: it sits inside a Go raw string.)
  via              TEXT NOT NULL DEFAULT 'direct',
  -- A store.User.ID. See the header.
  linked_by        TEXT NOT NULL,
  linked_at        INTEGER NOT NULL,
  PRIMARY KEY (profile_id, router_id)
);
-- ── SITES: MEMBERSHIP IS THE GRANT ─────────────────────────────────────────
--
-- A profile linked to a site applies to every router in that site, and STOPS
-- applying to a router that leaves it. That is the operator's choice, and it
-- has a consequence worth stating where the table is: editing a site's
-- membership now adds and removes LOGINS on devices. Every one is audited per
-- device, and a link the operator made by hand is via=direct and survives.
-- (No backticks in this comment: it sits inside a Go raw string.)
--
-- The site id is NOT a foreign key to the sites table. That one is written by a
-- different subsystem and a deleted site must not fail this delete; the
-- reconciler treats a site it cannot resolve as holding no routers, which
-- removes the accounts - the same answer as the site being emptied.
CREATE TABLE IF NOT EXISTS cred_profile_sites (
  profile_id TEXT NOT NULL REFERENCES cred_profiles(id) ON DELETE CASCADE,
  site_id    TEXT NOT NULL,
  linked_by  TEXT NOT NULL,
  linked_at  INTEGER NOT NULL,
  PRIMARY KEY (profile_id, site_id)
);

CREATE INDEX IF NOT EXISTS idx_cred_links_router ON cred_profile_links(router_id);
-- The reconciler's own query: everything owing work, soonest first.
CREATE INDEX IF NOT EXISTS idx_cred_links_due ON cred_profile_links(state, next_attempt_at);
`
