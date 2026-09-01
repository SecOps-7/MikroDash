// GENERATED from testdata/modal-list.json — do not edit.
// Rebuild with `node tools/modal-list-ts.js` from the committed JSON.
// The JSON it reads is a FROZEN artefact: the generator that produced it read the
// Node app and was deleted with the port-parity harness on 2026-09-01. This
// transform still runs, so the .ts can be rebuilt from the committed JSON --
// but the JSON itself can only change by hand, or from `v0.7.40` in git history.

/**
 * Every dialog that closes on Escape and on a backdrop click.
 *
 * The live name for this is `_PRINCIPAL_MODALS`, which is a leftover: it began
 * as the Settings principals dialogs and has not been that for a long time. An
 * earlier version of this port read the name, believed it, and skipped the
 * behaviour on the grounds that none of the list was ported. Four of the ten
 * are. Generated so the port cannot hold an opinion about the contents.
 */
export const CLOSABLE_MODALS: readonly string[] = [
  "userFormWrap",
  "groupFormWrap",
  "siteFormWrap",
  "roleFormWrap",
  "accountModal",
  "faModal",
  "ruUserFormWrap",
  "ruGroupFormWrap",
  "qFormWrap",
  "wanWarnWrap"
];
