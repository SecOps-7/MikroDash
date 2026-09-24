package db

// The notification channels table.
//
// ── ONE CONSTANT, TWO PATHS ────────────────────────────────────────────────
//
// Shared by `freshSchemaDDL` and `portMigrations[24]`, the rule `cfgTablesDDL`
// and `ztpTablesDDL` follow: a new database and a migrated one must not be able
// to describe different tables, and the only way to guarantee that is for there
// to be one description.
//
// ── WHY A TABLE AND NOT MORE SETTINGS KEYS ─────────────────────────────────
//
// Notifications were four fixed transports, each a handful of flat keys in
// settings.json (`telegramBotToken`, `smtpHost`, …). A channel is a record an
// operator creates, names and deletes, and there may be any number of them, so
// it is a list — and the settings file holds exactly one list-shaped value in
// the whole app (`hiddenAreas`), as a special case, with no way to seal a secret
// nested inside an element. Every list this port has added is a SQLite table.
//
// ── OWNER IS A USER ID OR THE INSTALL ──────────────────────────────────────
//
// `_install` or a user ID, matching the recipient ids `alertdispatch` already
// fans out to. That is deliberate: it is what lets the per-user "My Alerts"
// channels become rows in this same table rather than a second mechanism, and it
// means the dispatcher's recipient loop and this table's owner column share one
// vocabulary rather than being two that must be kept in step.
//
// `owner` and `created_by` BOTH hold a user ID, never a username — see
// `internal/verify/identity_test.go`, and CLAUDE.md on why a round-trip test
// cannot catch the other choice.
//
// ── WHAT IS IN config, AND WHAT IS NOT ─────────────────────────────────────
//
// `config` is JSON whose secret-bearing fields are sealed with the settings
// envelope before they get here; `internal/db` never encrypts anything itself.
// For a webhook channel the secret is the WHOLE URL — `tgram://<token>/<chat>`
// carries the bot token in its path — so the URL list is sealed entire rather
// than picked over for a password field.
//
// No CHECK constraint on `kind`, for the reason `cfgTablesDDL` gives: the
// vocabulary lives in Go constants, and a CHECK would make adding a channel type
// a table rebuild.
const notifyTablesDDL = `
CREATE TABLE IF NOT EXISTS notify_channels (
  id         TEXT PRIMARY KEY,
  owner      TEXT NOT NULL,
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL,
  enabled    INTEGER NOT NULL DEFAULT 1,
  config     TEXT NOT NULL,
  events     TEXT NOT NULL,
  routers    TEXT NOT NULL,
  -- WHICH INTERFACE TYPES the Interface Up/Down event covers, as a JSON array
  -- of ether/wlan/bridge/vlan/other. EMPTY MEANS ALL, exactly as the routers
  -- column does, so a channel nobody has narrowed covers everything -- which is
  -- what every channel did before the question existed.
  --
  -- A PLAIN COLUMN, not part of the sealed config: it is not a secret, and
  -- putting it there would mean decrypting a channel to find out whether it
  -- wants an alert.
  -- (No backticks in this comment: it sits inside a Go raw string.)
  iface_types TEXT NOT NULL DEFAULT '[]',
  -- HOW LOUD THIS CHANNEL IS: the CPU and ping-loss percentages it wants to be
  -- told about, and how long it stays quiet about a subject it has just
  -- mentioned. JSON, and EMPTY MEANS THE DEFAULTS -- see notify.ChannelSpec.
  --
  -- ONE COLUMN FOR THREE NUMBERS, because they are one idea and they arrive and
  -- leave together. Three columns would be three migrations the next time the
  -- set changes, for values nothing joins or filters on.
  -- (No backticks in this comment: it sits inside a Go raw string.)
  tuning TEXT NOT NULL DEFAULT '{}',
  created_by TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_notify_channels_owner ON notify_channels(owner);
`
