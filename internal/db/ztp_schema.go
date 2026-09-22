package db

// Zero-touch provisioning's tables: the devices it knows (pre-provisioned or
// calling home unannounced) and the batches of generic scripts. Port-added,
// migration 21. One constant for the fresh schema and the migration, as
// cfgTablesDDL is, so the two cannot describe different tables.
//
// ── NO SCRIPT SECRET IS KEPT ────────────────────────────────────────────────
//
// A bootstrap script holds a device's tunnel key (remote) or a batch's shared
// enrolment key (generic), and a token. MikroDash shows the script once and
// keeps only the token's SHA-256 and the PUBLIC key its peer needs. Downloading
// a script again means regenerating it, which rotates both, so a copy left in a
// mailbox stops working. The one secret that must be kept is a device's API
// password, until the device is onboarded into routers.json (where passwords
// live, encrypted): `secret` holds it SEALED by the server with the store's
// envelope, and is cleared on onboarding.
//
// No state vocabulary in CHECK constraints, for the reason cfgTablesDDL gives:
// the states are Go constants (ztp.go).
const ztpTablesDDL = `
CREATE TABLE IF NOT EXISTS ztp_batches (
          id          TEXT    PRIMARY KEY,
          name        TEXT    NOT NULL DEFAULT '',
          token_hash  TEXT    NOT NULL UNIQUE,
          public_key  TEXT    NOT NULL,
          expires_at  INTEGER,
          revoked_at  INTEGER,
          created_by  TEXT    NOT NULL,
          created_at  INTEGER NOT NULL
        );

CREATE TABLE IF NOT EXISTS ztp_devices (
          id          TEXT    PRIMARY KEY,
          mode        TEXT    NOT NULL,
          state       TEXT    NOT NULL,
          label       TEXT    NOT NULL DEFAULT '',
          serial      TEXT    NOT NULL DEFAULT '',
          token_hash  TEXT    UNIQUE,
          batch_id    TEXT    REFERENCES ztp_batches(id) ON DELETE SET NULL,
          expires_at  INTEGER,
          tunnel_ip   TEXT    UNIQUE,
          peer_key    TEXT    UNIQUE,
          lan_from    TEXT,
          secret      TEXT,
          template_id TEXT,
          values_json TEXT    NOT NULL DEFAULT '{}',
          acked_json  TEXT    NOT NULL DEFAULT '[]',
          site_ids    TEXT    NOT NULL DEFAULT '[]',
          router_id   TEXT,
          facts_json  TEXT    NOT NULL DEFAULT '{}',
          run_id      TEXT,
          created_by  TEXT    NOT NULL,
          created_at  INTEGER NOT NULL,
          first_seen  INTEGER,
          last_seen   INTEGER,
          error       TEXT
        );
CREATE INDEX IF NOT EXISTS idx_ztp_devices_state ON ztp_devices(state);
`
