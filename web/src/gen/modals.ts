// GENERATED from testdata/modal-list.json — do not edit.
// Regenerate with `node tools/modal-list.js` then `node tools/modal-list-ts.js`.

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
