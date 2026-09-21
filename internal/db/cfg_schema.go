package db

// Config Management's tables: templates, deploy runs and their targets, and
// the drift baselines. Port-added, migration 20.
//
// ── ONE TEXT FOR BOTH PATHS ─────────────────────────────────────────────────
//
// A fresh database gets these from `freshSchemaDDL` and an upgrading one from
// `portMigrations[20]`. Both are this constant, so the two cannot describe
// different tables; IF NOT EXISTS is what lets the migration run on a database
// the fresh path already built.
//
// ── WHAT IS DELIBERATELY NOT HERE ───────────────────────────────────────────
//
//   - No secret VALUE, anywhere. `cfg_runs.values_json` holds the values a
//     run was given with every secret variable left out; they live in memory
//     for the run's life only, which is also why an interrupted run never
//     resumes on its own (InterruptCfgRuns).
//   - No state vocabulary in CHECK constraints. The states are Go constants
//     (cfg.go); a CHECK would make adding one a table rebuild, which SQLite
//     does by copying the table.
//
// ── THE LEDGER OUTLIVES WHAT IT DESCRIBES ───────────────────────────────────
//
// `cfg_runs.template_id` is SET NULL on delete, and the run keeps the
// template's name, revision and a masked copy of the body it sent. Deleting a
// template must not delete the record of what it did to which routers.
// `cfg_run_targets` is never purged with a router, for the reason
// `routerPurgeExcluded` gives.
//
// `cfg_templates.backup_id` REFERENCES config_backups with no ON DELETE: the
// binary a full-binary template is made of cannot be deleted from under it.
// Retention skips it (`PinnedBackupIDs`), and the operator's delete refuses it
// before touching its files.
const cfgTablesDDL = `
CREATE TABLE IF NOT EXISTS cfg_templates (
          id                TEXT    PRIMARY KEY,
          name              TEXT    NOT NULL UNIQUE,
          description       TEXT    NOT NULL DEFAULT '',
          kind              TEXT    NOT NULL,
          scope             TEXT    NOT NULL DEFAULT '[]',
          body              TEXT    NOT NULL,
          sealed            INTEGER NOT NULL DEFAULT 0,
          variables         TEXT    NOT NULL DEFAULT '[]',
          fingerprint       TEXT    NOT NULL,
          revision          INTEGER NOT NULL DEFAULT 1,
          source_router_id  TEXT,
          source_model      TEXT,
          source_os_version TEXT,
          backup_id         INTEGER REFERENCES config_backups(id),
          baseline          TEXT,
          created_by        TEXT    NOT NULL,
          created_at        INTEGER NOT NULL,
          updated_at        INTEGER NOT NULL
        );

CREATE TABLE IF NOT EXISTS cfg_runs (
          id                   TEXT    PRIMARY KEY,
          template_id          TEXT    REFERENCES cfg_templates(id) ON DELETE SET NULL,
          template_name        TEXT    NOT NULL,
          revision             INTEGER NOT NULL,
          method               TEXT    NOT NULL,
          body_masked          TEXT    NOT NULL,
          fingerprint          TEXT    NOT NULL,
          values_json          TEXT    NOT NULL DEFAULT '{}',
          state                TEXT    NOT NULL,
          canary_router_id     TEXT,
          allow_model_mismatch INTEGER NOT NULL DEFAULT 0,
          created_by           TEXT    NOT NULL,
          created_at           INTEGER NOT NULL,
          updated_at           INTEGER NOT NULL,
          finished_at          INTEGER,
          error                TEXT
        );
CREATE INDEX IF NOT EXISTS idx_cfg_runs_created ON cfg_runs(created_at DESC);

CREATE TABLE IF NOT EXISTS cfg_run_targets (
          run_id               TEXT    NOT NULL REFERENCES cfg_runs(id) ON DELETE CASCADE,
          router_id            TEXT    NOT NULL,
          position             INTEGER NOT NULL,
          state                TEXT    NOT NULL,
          step                 TEXT,
          model                TEXT,
          serial               TEXT,
          os_version           TEXT,
          model_override       INTEGER NOT NULL DEFAULT 0,
          rendered_fingerprint TEXT,
          backup_id            INTEGER,
          dry_run_output       TEXT,
          import_output        TEXT,
          applied              TEXT,
          failed_line          INTEGER,
          reconnect_ms         INTEGER,
          warning              TEXT,
          error                TEXT,
          started_at           INTEGER,
          finished_at          INTEGER,
          PRIMARY KEY (run_id, router_id)
        );
CREATE INDEX IF NOT EXISTS idx_cfg_run_targets_router ON cfg_run_targets(router_id);

CREATE TABLE IF NOT EXISTS cfg_baselines (
          template_id TEXT    NOT NULL REFERENCES cfg_templates(id) ON DELETE CASCADE,
          router_id   TEXT    NOT NULL,
          run_id      TEXT,
          body        TEXT    NOT NULL,
          fingerprint TEXT    NOT NULL,
          taken_at    INTEGER NOT NULL,
          PRIMARY KEY (template_id, router_id)
        );
`
