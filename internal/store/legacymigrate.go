package store

import (
	"encoding/json"
	"fmt"
	"os"

	"mikrodash/internal/collection"
)

// THE #105 MIGRATION'S CALLER — live's `_migrateCollectionMode` IIFE.
//
//	(function _migrateCollectionMode() {
//	  const cfg = Settings.load();
//	  if (cfg.collectionMigrated) return;
//	  const legacy = Settings.readRetired(Object.keys(LEGACY_STREAM_KEYS));
//	  const plan   = planMigration(legacy, Routers.loadAll());
//	  for (const { id, collection } of plan) Routers.update(id, { collection });
//	  Settings.save({ collectionMigrated: true });
//	})();
//
// ── THE KEYS ARE READ OFF DISK, NOT THROUGH A DEFAULTS-MERGED VIEW ────────
//
// That is what `readRetired` is for, and the live comment says why: "The legacy
// stream* keys are no longer in DEFAULTS, so load() filters them out. They have
// to be read straight off disk or this sees nothing at all."
//
// This port has the same hazard from the other direction: `Store.Settings()`
// returns the raw file and merges nothing, so it ALREADY sees retired keys. The
// equivalent mistake here would be reading through `Defaults()`-merged settings,
// and nothing here does — so the raw read is both correct and the only option,
// which is worth saying rather than leaving to look like luck.
//
// ── THE FLAG IS SET EVEN WHEN THE PLAN IS EMPTY ───────────────────────────
//
// Live sets `collectionMigrated: true` after planning, not only when it wrote
// something. A migration that ran and found nothing to do HAS run; leaving the
// flag clear would make every subsequent start re-read and re-plan, and would
// leave the install permanently one settings edit away from a migration it has
// already passed.
// ── IT IS EXPORTED AND CALLED AT STARTUP, NOT FROM `Open` ─────────────────
//
// The first version ran this inside `Open`, next to the legacy router seed. That
// conflated two call sites live keeps apart: the seed lives inside `routers.js`
// DATA ACCESS (`loadAll`), the migration is a startup IIFE in `index.js`.
//
// The difference is not cosmetic. This function SAVES SETTINGS, and live's
// `save()` writes `{...load(), ...updates}` — where `load()` is the merged view —
// so any save materialises every default into settings.json. Doing that from
// `Open` made every store construction rewrite the file: three unrelated tests
// broke, one of which exists precisely to check the payload built from a SPARSE
// settings.json and could no longer find a sparse one to build from.
//
// Faithful behaviour, wrong seam. `Open` stays a constructor.
func (s *Store) MigrateCollectionMode() error {
	cfg, err := s.Settings()
	if err != nil {
		// No settings file is a fresh install: nothing to migrate, and nothing to
		// record either — a fresh install has never had the retired keys.
		return nil
	}
	if b, ok := cfg["collectionMigrated"].(bool); ok && b {
		return nil
	}

	all, problems := s.Routers()
	if len(problems) > 0 {
		// PLANNING FROM A PARTIAL FLEET WOULD BE WORSE THAN NOT PLANNING. A
		// router this read could not decode would silently miss its migration and
		// then be marked migrated by the flag below, permanently. Deferred to a
		// later start, by which time the record may be readable.
		return fmt.Errorf("routers.json did not decode cleanly (%d problem(s)); "+
			"the collection migration is deferred", len(problems))
	}

	fleet := make([]collection.MigrationRouter, 0, len(all))
	for _, r := range all {
		fleet = append(fleet, collection.MigrationRouter{
			ID: r.ID, Collection: collection.ParseRouter(r.Collection),
		})
	}

	for _, p := range collection.PlanMigration(cfg, fleet) {
		block := map[string]any{"mode": p.Mode}
		if len(p.Overrides) > 0 {
			ovr := make(map[string]any, len(p.Overrides))
			for k, v := range p.Overrides {
				ovr[k] = v
			}
			block["overrides"] = ovr
		}
		raw, err := json.Marshal(block)
		if err != nil {
			return fmt.Errorf("encoding the collection block for %s: %w", p.ID, err)
		}
		// THROUGH `UpdateRouter`, which is live's `Routers.update` — so the write
		// goes through the same coercions, the same password-preserving read and
		// the same file rewrite as every other router edit. A direct file write
		// here would be a second persistence path for one record shape, which is
		// the `stripWanIP` mistake in another costume.
		if err := s.UpdateRouter(p.ID, map[string]any{"collection": json.RawMessage(raw)}); err != nil {
			return fmt.Errorf("writing the collection block for %s: %w", p.ID, err)
		}
	}

	// ── THROUGH `Merge`, FOR THE `Kept` — NOT AN EMPTY ONE ────────────────
	//
	// `SaveSettings` rewrites the whole file, and `Kept` is how a sealed value
	// this process could NOT decrypt survives that rewrite. Passing `Kept{}`
	// drops it: a settings.json holding a credential encrypted under a different
	// key comes back with that field blank, and the operator's Telegram token or
	// SMTP password is gone because an unrelated migration ran at startup.
	//
	// Caught by `TestAnUnreadableCredentialSurvivesAnUnrelatedSave`, which
	// already existed for the settings ROUTE and started failing the moment this
	// became a second writer of the same file. `routers_api.go:449` is the
	// pattern being followed here.
	merged, kept := Merge(cfg, os.LookupEnv, s)
	merged["collectionMigrated"] = true
	return SaveSettings(s.Dir, merged, Settings{"collectionMigrated": true}, kept, s)
}
