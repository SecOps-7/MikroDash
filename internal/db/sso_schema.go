package db

// The SSO / OIDC tables: configured identity providers, how their claims map to
// roles, and which local account each external identity is bound to.
//
// ── ONE CONSTANT, TWO PATHS ────────────────────────────────────────────────
//
// Shared by `freshSchemaDDL` and `portMigrations[29]`, the rule `cfgTablesDDL`,
// `ztpTablesDDL` and `notifyTablesDDL` follow: a new database and a migrated one
// must not be able to describe different tables, and the only way to guarantee
// that is for there to be one description.
//
// ── WHY THREE TABLES AND NOT ONE ───────────────────────────────────────────
//
// They have three different lifetimes. A provider is configuration an operator
// edits. A role mapping is a set of rows that changes when the identity
// provider's groups change. An identity is a fact minted the first time a person
// signs in, and it outlives any particular mapping.
//
// ── WHY sso_identities EXISTS AT ALL ───────────────────────────────────────
//
// `users.json` is a bare JSON array with a fixed field order that `cmd/compat`
// proves this build can still read, and CLAUDE.md records the bare array as a
// security property rather than a preference. Adding a "provider" column to a
// user would change that file's shape for every install.
//
// So the binding lives here instead, and "is this account an SSO account" is a
// question answered by a row in this table rather than by a field over there.
// That also makes the binding SUBJECT-first: the identity provider's `sub` is
// the stable identifier, and a person who changes their username or email at the
// provider keeps the same account here.
//
// ── user_id IS A USER ID, NEVER A USERNAME ─────────────────────────────────
//
// Same rule, and the same reason, as `notify_channels.owner` and
// `grants.principal_id`: see `internal/verify/identity_test.go` and CLAUDE.md on
// why a round-trip test cannot catch the other choice. `userIDFor(username)` is
// the only bridge between the two vocabularies and it lives in one place.
//
// ── enabled DEFAULTS TO 0, UNLIKE notify_channels ──────────────────────────
//
// A provider is half-configured for as long as it takes to paste a client
// secret and add the redirect URI at the identity provider's end. Defaulting it
// on would put a button on the login page that cannot work yet, which reads as a
// broken app rather than an unfinished setting. The operator turns it on when
// the other end is ready, which is what the example modal's unticked "Enable
// this OIDC provider" box says too.
//
// ── client_secret ARRIVES SEALED ───────────────────────────────────────────
//
// It is sealed with the settings envelope before it gets here; `internal/db`
// never encrypts anything itself. Same split as `notify_channels.config`.
//
// ── role_id IS RESTRICT, AND THAT IS THE POINT ─────────────────────────────
//
// Deleting a role that a mapping still names FAILS, rather than cascading the
// mapping away. A cascade would be silent and its effect would arrive later, at
// somebody else's next sign-in, as "no role matched" -- a lockout with no
// obvious cause. Refusing the delete puts the problem in front of the person who
// caused it, while they still know what they were doing. `grants.role_id` is
// RESTRICT for the same reason.
//
// No CHECK constraint on the claim columns, for the reason `cfgTablesDDL` gives:
// the vocabulary lives in Go, and a CHECK would make adding a claim a table
// rebuild.
const ssoTablesDDL = `
CREATE TABLE IF NOT EXISTS sso_providers (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  enabled       INTEGER NOT NULL DEFAULT 0,
  -- The issuer as the provider states it, and the one an id token must match
  -- EXACTLY. It is also what the discovery document is fetched from and what
  -- that document's own issuer field is checked against.
  -- (No backticks in this comment: it sits inside a Go raw string.)
  issuer        TEXT NOT NULL,
  client_id     TEXT NOT NULL,
  -- Sealed with the settings envelope before it reaches this table.
  client_secret TEXT NOT NULL,
  scopes        TEXT NOT NULL DEFAULT 'openid profile email',
  -- WHICH CLAIM CARRIES WHAT. Defaults are the ones Entra, Okta, Auth0 and
  -- Keycloak all populate; claim_roles is the one operators most often change,
  -- because Entra app roles arrive in "roles" rather than "groups".
  -- (No backticks in this comment: it sits inside a Go raw string.)
  claim_username TEXT NOT NULL DEFAULT 'preferred_username',
  claim_email    TEXT NOT NULL DEFAULT 'email',
  claim_name     TEXT NOT NULL DEFAULT 'name',
  claim_roles    TEXT NOT NULL DEFAULT 'groups',
  -- Unix ms of the last icon upload, or 0 for none. It is a cache buster in the
  -- icon URL, exactly as branding's icon version is.
  -- (No backticks in this comment: it sits inside a Go raw string.)
  icon_version  INTEGER NOT NULL DEFAULT 0,
  created_by    TEXT,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sso_role_map (
  provider_id TEXT NOT NULL REFERENCES sso_providers(id) ON DELETE CASCADE,
  -- One value as it appears in the roles claim: a group name, a group object
  -- id, or an app role. Compared literally, because a provider that emits
  -- object ids and one that emits names are both ordinary.
  -- (No backticks in this comment: it sits inside a Go raw string.)
  claim_value TEXT NOT NULL,
  role_id     TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
  PRIMARY KEY (provider_id, claim_value)
);

CREATE TABLE IF NOT EXISTS sso_identities (
  provider_id  TEXT NOT NULL REFERENCES sso_providers(id) ON DELETE CASCADE,
  -- The provider's own subject identifier. Stable across a rename at the
  -- provider, which is why the binding is on this rather than on a username.
  -- (No backticks in this comment: it sits inside a Go raw string.)
  subject      TEXT NOT NULL,
  -- A store.User.ID. See the header.
  user_id      TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  PRIMARY KEY (provider_id, subject)
);
CREATE INDEX IF NOT EXISTS idx_sso_identities_user ON sso_identities(user_id);
`
