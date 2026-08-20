/**
 * Backups page: bulk delete, and the selection that drives it.
 *
 * Restore moved out of the rows and into the header, joined by a Delete that
 * acts on a checkbox column. The rules the UI has to keep: Delete works on one
 * or many, Restore only on exactly one, and both dim rather than disappear so
 * the header does not jump and the affordance stays visible.
 *
 * Source scans rather than a live socket, matching the convention the res:* and
 * restore handlers already use here: what matters about these handlers is
 * structural — a gate is present, a router scope is applied, a payload built for
 * one socket is not broadcast to everyone.
 */

const test   = require('node:test');
const assert = require('node:assert');
const fs     = require('fs');
const path   = require('path');

const INDEX_SRC = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');
const APP_SRC   = fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');
const HTML_SRC  = fs.readFileSync(path.join(__dirname, '..', 'public', 'index.html'), 'utf8');

function handlerFor(name) {
  const at = INDEX_SRC.indexOf("socket.on('" + name + "'");
  assert.ok(at > -1, name + ' handler is missing');
  return INDEX_SRC.slice(at, INDEX_SRC.indexOf('\n  }));', at));
}

function fnBody(src, decl) {
  const at = src.indexOf(decl);
  assert.ok(at > -1, decl + ' not found');
  return src.slice(at, src.indexOf('\n  }', at));
}

// ── The delete handler ──────────────────────────────────────────────────────

test('deleting a backup removes the files AND the row that listed them', () => {
  // Pressing Delete means "I do not want this listed". A row left behind reading
  // "pruned" is retention's answer to a question the operator did not ask here.
  const h = handlerFor('backups:delete');
  assert.ok(/db\.deleteBackup\(/.test(h), 'the row goes');
  assert.ok(/removePair/.test(h), 'and so do the files');
  assert.ok(!/markBackupPruned/.test(h),
    'markBackupPruned is retention\'s half — a manual delete leaves no tombstone');
});

test('files are removed before the row', () => {
  // The row is the only thing pointing at the pair. Dropping it first and then
  // failing to unlink would orphan several MB on disk with nothing to find it by.
  const h = handlerFor('backups:delete');
  assert.ok(h.indexOf('removePair') < h.indexOf('db.deleteBackup('),
    'unlink first, then forget where it was');
});

test('retention still keeps its rows', () => {
  // Different act, different treatment: an aged-out pair is something MikroDash
  // did on its own, so the History table has to be able to explain it.
  const prune = fs.readFileSync(path.join(__dirname, '..', 'src', 'backups', 'index.js'), 'utf8');
  assert.ok(/markBackupPruned/.test(prune), 'retention marks rather than deletes');
  assert.ok(!/deleteBackup/.test(prune), 'and must not start deleting');
});

test('the audit entry is the surviving record', () => {
  // Removing the row is fine precisely because audit_events holds both the
  // backup.run that created it and the backup.delete that removed it.
  const h = handlerFor('backups:delete');
  assert.ok(/action: 'backup\.delete'/.test(h));
  assert.ok(/targetId: removed\.join/.test(h), 'naming which ids went');
});

test('a delete may only reach the router the socket is on', () => {
  const h = handlerFor('backups:delete');
  assert.ok(/_bkRow\(id, rid\)/.test(h),
    'ids must resolve router-scoped, or a guessed id reaches another device');
  assert.ok(/_bkMayWrite\(rid\)/.test(h), 'and the page write permission still applies');
  assert.ok(/audit\.fromSocket\(socket\)\.denied/.test(h), 'a refusal is audited');
  assert.ok(/audit\.fromSocket\(socket\)\.record/.test(h), 'and so is a deletion');
});

test('a delete request is bounded and de-duplicated', () => {
  const h = handlerFor('backups:delete');
  assert.ok(/new Set\(/.test(h), 'duplicate ids must not double the work');
  assert.ok(/\.slice\(0, \d+\)/.test(h), 'one message must not ask for unbounded filesystem work');
  assert.ok(/Number\.isInteger/.test(h), 'and non-numeric ids are dropped');
});

test('the delete broadcast does not leak the caller permission', () => {
  // _bkPayload carries `permitted`, computed for the CALLING socket. Sending
  // that to the room would tell a viewer they may write.
  const h = handlerFor('backups:delete');
  assert.ok(/socket\.emit\('backups:state'/.test(h), 'the caller gets the fresh state');
  assert.ok(!/\bio\.to\([^)]*\)\.emit\('backups:state'/.test(h),
    'the room must not be sent a payload built for someone else');
  assert.ok(/socket\.to\([^)]*\)\.emit\('backups:ran'/.test(h),
    'it is nudged to re-request its own instead');
});

test('delete is serialised with the other router writes', () => {
  const h = handlerFor('backups:delete');
  assert.ok(/_routerWriteQueue\(socket\.routerId/.test(h),
    'a delete racing a running backup would remove a pair mid-write');
});

// ── The header buttons ──────────────────────────────────────────────────────

test('Restore moved to the header and is no longer a per-row button', () => {
  assert.ok(!/data-bk-restore/.test(APP_SRC), 'the row-level Restore button is gone');
  assert.ok(/id="bkRestore"/.test(APP_SRC), 'and lives in the header');
  assert.ok(/id="bkDelete"/.test(APP_SRC), 'beside a Delete button');
  const run = APP_SRC.indexOf('id="bkRun"');
  const del = APP_SRC.indexOf('id="bkDelete"');
  const rst = APP_SRC.indexOf('id="bkRestore"');
  assert.ok(rst < del && del < run,
    'header order is Restore, Delete, Back Up Now — the two selection-driven ' +
    'actions lead, and the one needing no selection anchors the end');
});

test('Restore is purple and Delete keeps the danger styling', () => {
  assert.ok(/\.sbtn-purple\{/.test(HTML_SRC), 'the purple variant is defined');
  assert.ok(/sbtn sbtn-purple[^>]*id="bkRestore"/.test(APP_SRC), 'and Restore uses it');
  assert.ok(/sbtn sbtn-danger[^>]*id="bkDelete"/.test(APP_SRC),
    'Delete keeps danger red — restore and deletion are different kinds of destructive');
});

test('both bulk buttons start disabled and dim rather than vanish', () => {
  assert.ok(/id="bkDelete" disabled/.test(APP_SRC), 'nothing is selected on first render');
  assert.ok(/id="bkRestore" disabled/.test(APP_SRC));
  assert.ok(/\.sbtn-purple:disabled,\.sbtn-danger:disabled\{opacity:/.test(HTML_SRC),
    'a button that disappears makes the header jump and loses the affordance');
});

test('Restore needs exactly one selection, Delete needs at least one', () => {
  const body = fnBody(APP_SRC, 'function _syncBulk');
  assert.ok(/del\.disabled = n === 0/.test(body), 'Delete is available for one OR many');
  assert.ok(/n === 1/.test(body) && /rst\.disabled = !canRestore/.test(body),
    'Restore replaces a whole configuration, so several is not an answer it can act on');
});

test('Restore also needs the selected row to still have files', () => {
  // Delete now works on any row, so "one selected" no longer implies "one that
  // can be restored from" — an unchanged run has a row and no backup behind it.
  const body = fnBody(APP_SRC, 'function _syncBulk');
  assert.ok(/_restorable\.has\(only\)/.test(body), 'the single selection must be restorable');
  assert.ok(/no stored backup to restore from/.test(body), 'and say so when it is not');
});

test('a viewer gets no bulk buttons at all', () => {
  // Same call the page already makes for Save: there is nothing for them to
  // try, so offering it and refusing is just noise.
  assert.ok(/st\.permitted[\s\S]{0,400}id="bkDelete"/.test(APP_SRC),
    'the header buttons are built only when permitted');
});

// ── The selection column ────────────────────────────────────────────────────

test('the selection survives a re-render but not a router switch', () => {
  assert.ok(/_picked\.has\(r\.id\)/.test(APP_SRC),
    'keyed by id, so a list re-rendered under a scheduled run keeps the same backups selected');
  assert.ok(/_prunePicked/.test(APP_SRC), 'and drops ids retention pruned under us');
  // Several modules listen for router:switched; anchor on the backups one by its
  // own comment rather than taking the first match, which is the nav dropdown.
  const at = APP_SRC.indexOf('A router switch makes every row on screen belong to the wrong device');
  assert.ok(at > -1, "the backups router:switched handler moved");
  const body = APP_SRC.slice(at, APP_SRC.indexOf('});', at));
  assert.ok(/_picked\.clear\(\)/.test(body),
    'every selected id belongs to the device we just left');
});

test('every row is selectable, because Delete now clears the row itself', () => {
  // A run that stored nothing, or one whose pair retention already took, has no
  // files — but the row is still the operator's to remove. Refusing those would
  // leave rows in the History table that nothing can ever clear.
  assert.ok(/var pickable   = !!st\.permitted;/.test(APP_SRC),
    'selectability is about permission, not about files');
  assert.ok(/var restorable = !!\(r\.stem && !r\.pruned\)/.test(APP_SRC),
    'the files question moved to where it still matters: Restore');
  assert.ok(/pickable \? '' : ' disabled'/.test(APP_SRC),
    'a viewer still gets a disabled box rather than none, so rows do not jump about');
});

test('the select-all box reflects a partial selection', () => {
  const body = fnBody(APP_SRC, 'function _syncBulk');
  assert.ok(/indeterminate/.test(body), 'half-selected must not read as none selected');
  assert.ok(/id="bkPickAll"/.test(HTML_SRC), 'and the header box exists');
});

test('selection is driven by change, not click', () => {
  // A checkbox toggled with the keyboard fires change and not click, so a click
  // listener would leave keyboard users unable to select anything.
  assert.ok(/addEventListener\('change', function \(ev\) \{[\s\S]{0,400}bkPickAll/.test(APP_SRC));
});

test('the table gained columns for the boxes and the id', () => {
  // table-layout is fixed, so a colgroup out of step with the header row silently
  // misaligns every column after the mismatch rather than failing visibly.
  const at = HTML_SRC.indexOf('id="bkHistoryCard"');
  const card = HTML_SRC.slice(at, HTML_SRC.indexOf('</table>', at));
  const cols = (card.match(/<col /g) || []).length;
  const ths  = (card.match(/<th[ >]/g) || []).length;
  assert.equal(cols, ths, 'colgroup and header row must stay the same width');
  assert.equal(cols, 8, 'six data columns, plus selection and ID');
});

test('the id is shown as its own pill, immediately after the checkbox', () => {
  const at = HTML_SRC.indexOf('id="bkHistoryCard"');
  const card = HTML_SRC.slice(at, HTML_SRC.indexOf('</table>', at));
  assert.ok(/bkPickAll[\s\S]{0,200}<th>ID<\/th>/.test(card),
    'the ID header sits directly right of the select-all box');
  assert.ok(/\.bk-id-pill\{/.test(HTML_SRC), 'and has its own pill style');
  // Distinct from the Result column's Tabler badges, or it reads as a status.
  assert.ok(!/bk-id-pill[^}]*bg-\w+-lt/.test(HTML_SRC));
  assert.ok(/<td><span class="bk-id-pill">' \+ esc\(String\(r\.id\)\)/.test(APP_SRC),
    'rendered from the row id, escaped like everything else');
});

test('deleting says what goes, and where the record survives', () => {
  const body = fnBody(APP_SRC, 'function deleteSelected');
  assert.ok(/window\.confirm/.test(body), 'destructive and irreversible, so it asks');
  assert.ok(/history rows are removed/.test(body), 'the row goes too, so say so');
  assert.ok(/Audit/.test(body),
    'and point at what IS kept, rather than implying nothing is');
  assert.ok(!/marked as pruned/.test(body), 'the old promise no longer holds');
});
