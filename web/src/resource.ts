// The resource edit form — a port of the resource engine at the foot of app.js,
// rendering into the #resModal markup lifted verbatim from the live page.
//
// The schema is FETCHED, never restated. `res:schema` serves the Go registry
// over the socket, so the field list, its types and its conditional visibility
// have exactly one home. That is the registry-over-the-wire pattern the evidence
// pass singled out as the fix for the client/server mirrors, applied here rather
// than growing a fourteenth one.
//
// Over the SOCKET rather than over HTTP, and that is not a style choice. The
// reply carries `permitted` — may THIS viewer write THIS router — which depends
// on the selected router, and an HTTP request does not have one.
//
// Nothing this form decides is trusted. `readOnly` arrives from the server and
// is re-derived there against a fresh read; the values arrive from a fresh read
// for the same reason. The form's job is to be the shape of the row, not the
// authority on it.

import { esc, el } from './dom';
import type { Socket } from './socket';

interface SchemaField {
  name: string;
  label: string;
  type: string;
  input: string;
  required: boolean;
  options: string[] | null;
  placeholder: string;
  help: string;
  showIf: { field: string; in: string[] } | null;
  min: number | null;
  max: number | null;
}

interface HistState {
  canUndo: boolean; canRedo: boolean; undoLabel: string; redoLabel: string;
}

interface Schema {
  key: string;
  label: string;
  title: string;
  identity: string;
  fields: SchemaField[];
  actions: Array<{ key: string; label: string }>;
  // Answered by the SOCKET, not the registry: whether THIS viewer may write
  // THIS router. The collector payload is shared by every viewer of the router,
  // so it can never answer this.
  permitted: boolean;
  unsupported: boolean;
  ordered: boolean;
}

const schemas = new Map<string, Schema>();
// Callers waiting on a schema that has been asked for and not yet answered, so
// a burst of pages mounting at once produces one request per resource.
const waiting = new Map<string, Array<(s: Schema) => void>>();
let sock: Socket | null = null;
// The undo/redo state per resource, as the server last reported it.
const hist = new Map<string, HistState>();
let current: { key: string; id: string | null; identity: string | null } | null = null;
// The submission to repeat once a warning is acknowledged. A guard answers by
// refusing the write and describing what it saw, so the retry is the same
// request plus the fingerprint of the warning that was read.
let retry: ((ack: string) => void) | null = null;
let wired = false;
// Both delegations live on `document`, and four pages mount them. Without these
// a second page mount would add a second listener and one click would open the
// form twice.
let addsWired = false;
let rowsWired = false;

/**
 * Ask for a schema once, then serve it from memory.
 *
 * OVER THE SOCKET, not over HTTP, and the reason is `permitted`: it depends on
 * the selected router, and an HTTP request does not have one. The schema was
 * served from `/next/api/resources/<key>` until that mattered.
 *
 * A schema that never arrives leaves its caller pending for ever. That is the
 * live behaviour — `need()` has no timeout either — and it is the right one
 * here: the two ways it can fail are a refused read, which the page already
 * reports through res:error, and a disconnect, which re-asks on reconnect.
 */
function schemaFor(key: string): Promise<Schema> {
  const cached = schemas.get(key);
  if (cached) return Promise.resolve(cached);
  return new Promise<Schema>((ok) => {
    const q = waiting.get(key);
    if (q) { q.push(ok); return; }        // already asked; just join the queue
    waiting.set(key, [ok]);
    sock?.emit('res:schema', { resource: key });
  });
}

// One builder for both option paths — a declared select and a router-supplied
// list render the same control, and two copies would drift.
function selectHtml(f: SchemaField, id: string, value: unknown, list: string[]): string {
  const opts = list.slice();
  const cur = (value === undefined || value === null) ? '' : String(value);
  if (cur && opts.indexOf(cur) === -1) opts.unshift(cur);
  return '<select class="sform-input" id="' + id + '">' +
    (f.required ? '' : '<option value=""></option>') +
    opts.map((o) =>
      '<option value="' + esc(o) + '"' + (String(value) === o ? ' selected' : '') + '>' +
      esc(o) + '</option>').join('') + '</select>';
}

function fieldHtml(f: SchemaField, value: unknown, choices?: string[]): string {
  const id = 'resf_' + f.name;

  if (f.input === 'checkbox') {
    // No <label for> on a toggle: the .stoggle markup wraps its own input.
    return '<label class="stoggle" style="margin-top:.7rem" data-res-field="' + esc(f.name) + '">' +
      '<span class="stoggle-label">' + esc(f.label) + '</span>' +
      '<span class="stoggle-switch"><input type="checkbox" id="' + id + '"' +
      (value ? ' checked' : '') + '><span class="stoggle-track"></span>' +
      '<span class="stoggle-thumb"></span></span></label>';
  }

  const lbl = '<label class="sform-label" for="' + id + '">' + esc(f.label) +
    (f.required ? ' <span style="color:var(--accent-err)">*</span>' : '') + '</label>';
  const help = f.help ? '<div style="font-size:.66rem;color:var(--text-muted);margin-top:.15rem">' +
    esc(f.help) + '</div>' : '';
  let body: string;

  if (choices && choices.length) {
    // THE ROUTER TOLD US WHAT THIS FIELD MAY BE, so offer that rather than a
    // blank box — even for a field whose declared type is free text. That is
    // what the live form does, and without it the VLAN parent and both
    // bridge-port fields were text boxes here and pickers there.
    //
    // The current value is kept as an option even when the router's list does
    // not name it, for the same reason the declared-select branch below does
    // it: a required control with nothing matching falls back to option zero,
    // and Save then rewrites the record as whatever that happens to be.
    body = selectHtml(f, id, value, choices);
  } else if (f.input === 'select') {
    // The router's value is kept as an option even when our list does not name
    // it. A required select emits no blank option, so without this nothing
    // matches, the browser falls back to selectedIndex 0, and Save rewrites the
    // record as whatever option zero happens to be — silently, and with no sign
    // on screen that the form was not showing the record. A list declared here
    // is no more exhaustive than one the router sent.
    body = selectHtml(f, id, value, f.options || []);
  } else {
    let attrs = ' type="' + esc(f.input) + '"';
    if (f.min !== null && f.min !== undefined) attrs += ' min="' + esc(f.min) + '"';
    if (f.max !== null && f.max !== undefined && f.input === 'number') attrs += ' max="' + esc(f.max) + '"';
    // A secret is never filled in: an empty box means "leave it unchanged".
    const v = f.type === 'secret' ? '' : (value === undefined || value === null ? '' : value);
    body = '<input class="sform-input" id="' + id + '"' + attrs +
      ' value="' + esc(v) + '" placeholder="' + esc(f.placeholder || '') + '"' +
      ' autocomplete="off">';
  }

  return '<div style="margin-top:.6rem" data-res-field="' + esc(f.name) + '">' + lbl + body + help + '</div>';
}

function applyShowIf(schema: Schema): void {
  const host = el('res_fields');
  if (!host) return;
  for (const f of schema.fields) {
    if (!f.showIf) continue;
    const ctl = el<HTMLInputElement | HTMLSelectElement>('resf_' + f.showIf.field);
    const wrap = host.querySelector<HTMLElement>('[data-res-field="' + f.name + '"]');
    if (!ctl || !wrap) continue;
    wrap.style.display = f.showIf.in.indexOf(String(ctl.value)) !== -1 ? '' : 'none';
  }
}

function buildForm(schema: Schema, values: Record<string, unknown> | null,
                   options?: Record<string, string[]>): void {
  const host = el('res_fields');
  if (!host) return;
  host.innerHTML = schema.fields
    .map((f) => fieldHtml(f, values ? values[f.name] : undefined, options ? options[f.name] : undefined))
    .join('');
  applyShowIf(schema);
  // A field that controls another's visibility redraws it as it changes — the
  // DNS type picker is the only one today, but the rule is general.
  const seen = new Set<string>();
  for (const f of schema.fields) {
    if (!f.showIf || seen.has(f.showIf.field)) continue;
    seen.add(f.showIf.field);
    el('resf_' + f.showIf.field)?.addEventListener('change', () => applyShowIf(schema));
  }
}

function readValues(schema: Schema): Record<string, string> {
  const out: Record<string, string> = {};
  for (const f of schema.fields) {
    const node = el<HTMLInputElement | HTMLSelectElement>('resf_' + f.name);
    if (!node) continue;
    out[f.name] = f.input === 'checkbox'
      ? String((node as HTMLInputElement).checked)
      : node.value;
  }
  return out;
}

function setError(msg: string): void {
  const box = el('res_error');
  if (!box) return;
  box.textContent = msg;
  box.style.display = msg ? '' : 'none';
}

function close(): void {
  el('resModal')?.classList.remove('open');
  const warn = el('res_warn');
  if (warn) warn.style.display = 'none';
  current = null;
  retry = null;
}

/**
 * The self-cutoff prompt.
 *
 * Rendered into #res_warn — inside the dialog rather than a window.confirm — so
 * it can NAME the interface and the address the router sees us from. A warning
 * that says only "this may be dangerous" is one people learn to click through.
 *
 * The acknowledgement is the guard's FINGERPRINT, not a boolean: it binds the
 * answer to the exact inputs it was asked about, so it cannot be carried to
 * another row or replayed against a later write. A `stale-warning` means the
 * ground moved between the prompt and the answer.
 */
function showWarning(d: {
  code: string;
  fingerprint?: string;
  warning?: { interface?: string; address?: string; action?: string };
}): void {
  const box = el('res_warn');
  if (!box) return;
  const w = d.warning || {};
  const why = 'The router sees MikroDash at <code>' + esc(w.address || '?') +
    '</code>, which arrives on <code>' + esc(w.interface || '?') + '</code> — the interface this ' +
    'change ' + (w.action === 'delete' ? 'removes' : 'alters') + '.';
  box.innerHTML =
    '<strong>This may cut MikroDash off from this router.</strong><br>' + why +
    (d.code === 'stale-warning'
      ? '<br><em>The values changed since you were asked, so please confirm again.</em>' : '') +
    '<div style="display:flex;gap:.4rem;justify-content:flex-end;margin-top:.5rem">' +
    '<button class="sbtn sbtn-outline" id="res_warnCancel" style="padding:.3rem .7rem;font-size:.72rem">Cancel</button>' +
    '<button class="sbtn sbtn-danger" id="res_warnGo" style="padding:.3rem .7rem;font-size:.72rem">Do it anyway</button></div>';
  box.style.display = '';

  const ack = d.fingerprint || '';
  const again = retry;
  el('res_warnCancel')?.addEventListener('click', () => {
    box.style.display = 'none';
    retry = null;
  });
  el('res_warnGo')?.addEventListener('click', () => {
    box.style.display = 'none';
    again?.(ack);
  });
}

/**
 * `actions` are the verbs a read-only row still offers.
 *
 * A dynamic DHCP lease is the case this exists for: it cannot be edited, because
 * it belongs to the server rather than to us, and the only useful thing to do
 * with it is make it static. Refusing to open the form would make that verb
 * unreachable — which is why the bar is drawn even when Save is hidden.
 */
function actionBar(schema: Schema, avail: string[]): void {
  const host = el('res_actions');
  if (!host) return;
  host.innerHTML = (avail || []).map((k) => {
    const a = (schema.actions || []).find((x) => x.key === k);
    return a ? '<button class="sbtn sbtn-primary" data-res-actionbtn="' + esc(a.key) + '" ' +
      'style="padding:.3rem .7rem;font-size:.72rem">' + esc(a.label) + '</button>' : '';
  }).join(' ');
}

function show(schema: Schema, values: Record<string, unknown> | null,
              row: { id: string; identity: string } | null, readOnly: boolean,
              options?: Record<string, string[]>, actions?: string[]): void {
  current = { key: schema.key, id: row ? row.id : null, identity: row ? row.identity : null };
  const title = el('res_title');
  if (title) title.textContent = (readOnly ? '' : row ? 'Edit ' : 'Add ') + schema.title;
  buildForm(schema, values, options);
  actionBar(schema, actions || []);
  if (readOnly) {
    // Shown, not hidden: the operator clicked the row to see it, and the values
    // are the answer to that even when they cannot be changed.
    el('res_fields')?.querySelectorAll<HTMLInputElement>('input,select')
      .forEach((n) => { n.disabled = true; });
  }
  setError('');
  const warn = el('res_warn'); if (warn) warn.style.display = 'none';
  const prev = el('res_preview'); if (prev) prev.style.display = 'none';
  const del = el('res_delete'); if (del) del.style.display = (row && !readOnly) ? '' : 'none';
  const save = el('res_save');
  if (save) {
    save.style.display = readOnly ? 'none' : '';
    save.textContent = row ? 'Save' : 'Add ' + schema.label;
  }
  // THE PREVIEW BUTTON, shown on a writable form and hidden on a read-only one.
  //
  // This was `display = 'none'` unconditionally until 2026-08-25 — which is the
  // live app's READ-ONLY branch (app.js:14716) applied to every form; almost
  // certainly the wrong one of its two lines was ported. The writable branch is
  // `app.js:14657`, `ro ? 'none' : ''`. A user-visible control was missing from
  // every resource page as a result, found by `inbound-audit` noticing that the
  // live app answers `res:preview` and ws.go did not.
  const pbtn = el('res_previewBtn'); if (pbtn) pbtn.style.display = readOnly ? 'none' : '';
  el('resModal')?.classList.add('open');
}

/**
 * Fill every `[data-res-add]` slot on the page with its Add button(s).
 *
 * ONE implementation, because three pages had grown three: the labels had
 * drifted to "Add Bridge"/"Add Port"/"Add VLAN" where the live app renders
 * "+ Add Bridge"/"+ Add Bridge Port"/"+ Add VLAN", and the padding with them.
 * The label is taken from the fetched schema rather than written here, so the
 * text has the same single home the field list does and cannot drift again.
 *
 * A slot names one or more resources — routing's is `route,route6` — and
 * renders one button each, in the order the slot lists them.
 *
 * KNOWN GAP, deliberately not papered over: the live slot also gates each
 * button on `schema.permitted` and precedes them with the undo/redo pair from
 * `res:history`. This port serves schemas over HTTP, where the selected router
 * — which `permitted` depends on — is not known, and has not ported
 * `res:history` at all. So a viewer with read-only access sees Add buttons that
 * the server then refuses, and nobody sees undo/redo. Tracked during the port;
 * it is the same on every ported page and is not introduced here.
 */
function histButton(kind: 'undo' | 'redo', key: string, on: boolean, label: string): string {
  const glyph = kind === 'undo' ? '↶' : '↷';
  const title = on
    ? (kind === 'undo' ? 'Undo ' : 'Redo ') + label
    : 'Nothing to ' + kind;
  return '<button class="sbtn ' + (on ? 'sbtn-primary' : 'sbtn-ghost') + ' res-hist"' +
    ' data-res-hist="' + kind + '" data-res-histkey="' + esc(key) + '"' +
    (on ? '' : ' disabled') +
    ' title="' + esc(title) + '">' + glyph + '</button>';
}

/**
 * One undo/redo pair per SLOT, not per resource.
 *
 * A slot naming several resources — `route,route6` — gets one pair, pointed at
 * whichever of them has something to undo. Falls back to the first, so the
 * buttons are present and grey rather than absent: a control that appears only
 * once it is usable teaches nobody it exists.
 */
function histTarget(ready: Schema[]): string {
  for (const s of ready) {
    const h = hist.get(s.key);
    if (h && (h.canUndo || h.canRedo)) return s.key;
  }
  return ready[0]?.key ?? '';
}

/**
 * The row a reorder is about, held between the click and any acknowledgement.
 *
 * A move is a WRITE, so a guard can refuse it and ask for confirmation — and the
 * answer has to reach the SAME request rather than a new one. That is what
 * `retry` is for, and it is why the request is held here rather than rebuilt
 * from the DOM: by the time an operator answers the prompt the table may have
 * re-rendered underneath them.
 */
let pendingMove:
  { resource: string; id: string; expectedIdentity?: string; direction?: string; anchor?: string }
  | null = null;

function doMove(ack: string): void {
  if (!pendingMove) return;
  retry = doMove;
  sock?.emit('res:move', ack ? { ...pendingMove, ack } : { ...pendingMove });
}

// ── Drag to reorder ─────────────────────────────────────────────────────────
//
// POINTER EVENTS, not HTML5 drag-and-drop — the choice the live app made, and
// the one that works on touch.
//
// The dragged row is moved in the DOM as the pointer travels, so the table shows
// the OUTCOME rather than a floating ghost. That also makes the answer trivial to
// read at the end: whatever row now follows it is the anchor. If the server
// refuses, the next payload re-renders the truth.

interface DragState {
  host: HTMLElement;
  row: HTMLElement;
  key: string;
  raf: number;
  x: number;
  y: number;
  marker: HTMLElement | null;
}
let drag: DragState | null = null;

/**
 * Where the pointer is, in ONE lookup.
 *
 * Measuring every row per pointermove is thirty `getBoundingClientRect` calls on
 * a table of thirty rules, each forced to flush a layout the previous
 * `insertBefore` had just invalidated. `elementFromPoint` asks the browser once.
 *
 * The origin gap counts as a target: without it, hovering the hole is a dead
 * spot and putting a rule back where it came from means aiming at its
 * neighbours.
 */
function rowUnder(host: HTMLElement, x: number, y: number): HTMLElement | null {
  const hit = document.elementFromPoint(x, y) as HTMLElement | null;
  const row = hit?.closest?.('tr[data-id], tr.res-drag-origin') as HTMLElement | null;
  return row && host.contains(row) ? row : null;
}

/**
 * A stand-in left in the slot the row is leaving.
 *
 * Created on the FIRST MOVE, not at pointerdown: inserting it up front pushes
 * everything below down a row before the drag has gone anywhere. Made on the
 * first move, the row vacates its slot as the marker fills it and nothing jumps.
 *
 * The height is measured off the row rather than guessed in CSS, because rules
 * wrap to different heights and a gap that is not the size of the hole shifts
 * every row beneath it.
 */
function makeOriginMarker(row: HTMLElement): HTMLElement {
  const tr = document.createElement('tr');
  tr.className = 'res-drag-origin';
  const td = document.createElement('td');
  td.colSpan = row.children.length || 1;
  td.style.height = row.getBoundingClientRect().height + 'px';
  tr.appendChild(td);
  return tr;
}

/**
 * Show the gap only while the row is actually elsewhere.
 *
 * A row cannot be moved INSIDE its own marker — the DOM offers only before and
 * after — so dragging back leaves the rule beside the gap with the slot still
 * looking empty, and it never reads as "dropped back where it was". Collapsing
 * the marker whenever the row immediately follows it makes the table look
 * exactly as it did before the drag started.
 */
function syncOriginMarker(): void {
  if (!drag || !drag.marker) return;
  drag.marker.classList.toggle('is-home', drag.marker.nextElementSibling === drag.row);
}

/**
 * Move the dragged row to wherever the pointer now is.
 *
 * It swaps on the FIRST OVERLAP with a neighbour rather than waiting for the
 * pointer to cross that row's midpoint — waiting means travelling a whole
 * row-height before anything happens, which reads as lag.
 *
 * That cannot oscillate: after the swap the dragged row is under the cursor
 * again, so the next lookup finds the dragged row and does nothing until the
 * pointer moves on.
 */
export function dragTo(x: number, y: number): void {
  if (!drag) return;
  // The row was re-rendered out from under us — a tab switch, a router switch,
  // anything that rebuilds the table. Re-inserting the detached node would put a
  // SECOND copy of it beside its replacement, so the drag ends here instead.
  if (!drag.host.contains(drag.row)) { endDrag(); return; }
  const over = rowUnder(drag.host, x, y);
  if (!over || over === drag.row) return;

  // Mark the slot being left, once, before the row actually moves out of it.
  if (!drag.marker) {
    drag.marker = makeOriginMarker(drag.row);
    drag.row.parentNode?.insertBefore(drag.marker, drag.row);
  }

  if (over === drag.marker) {
    // Back over the gap: immediately after it IS the original slot once the gap
    // collapses. Settles rather than oscillating — once home the marker hides,
    // so the next lookup finds the row itself.
    drag.host.insertBefore(drag.row, drag.marker.nextSibling);
  } else {
    const above = drag.row.compareDocumentPosition(over) & Node.DOCUMENT_POSITION_PRECEDING;
    drag.host.insertBefore(drag.row, above ? over : over.nextSibling);
  }
  syncOriginMarker();
}

export function endDrag(): DragState | null {
  if (!drag) return null;
  const d = drag;
  drag = null;
  if (d.raf) cancelAnimationFrame(d.raf);
  // The gap closes however the drag ended — dropped elsewhere, dropped back, or
  // cancelled. Removed BEFORE the caller walks siblings for the anchor, so it can
  // never be mistaken for a neighbour.
  d.marker?.parentNode?.removeChild(d.marker);
  d.marker = null;
  d.row.classList.remove('res-dragging');
  document.body.classList.remove('res-dragging-body');
  return d;
}

/** The row a drop lands before, or '' for the end of the table. */
export function anchorAfter(row: HTMLElement): string {
  let next = row.nextElementSibling;
  while (next && !next.getAttribute('data-id')) next = next.nextElementSibling;
  // '' rather than undefined for the end of the table: the server tells the two
  // apart by whether the key is PRESENT, not by whether it is empty. See
  // HasAnchor in internal/server/resource.go.
  return next ? (next.getAttribute('data-id') || '') : '';
}

function wireDrag(): void {
  document.addEventListener('pointerdown', (e) => {
    const t = e.target as HTMLElement;
    if (!t?.closest) return;
    const h = t.closest('[data-res-drag]') as HTMLElement | null;
    if (!h) return;
    const row = h.closest('[data-id]') as HTMLElement | null;
    const host = h.closest('[data-res-rows]') as HTMLElement | null;
    if (!row || !host) return;
    const key = row.getAttribute('data-res') || host.getAttribute('data-res-rows') || '';
    const schema = schemas.get(key);
    if (!schema || !schema.permitted) return;

    e.preventDefault();
    drag = { host, row, key, raf: 0, x: e.clientX, y: e.clientY, marker: null };
    row.classList.add('res-dragging');
    // Stops the pointer painting a text selection across the table as it travels.
    document.body.classList.add('res-dragging-body');
    try { h.setPointerCapture(e.pointerId); } catch { /* not fatal */ }
  });

  document.addEventListener('pointermove', (e) => {
    if (!drag) return;
    drag.x = e.clientX;
    drag.y = e.clientY;
    // Pointer events fire faster than the screen redraws, and every one of them
    // would otherwise reorder the DOM. Coalesced to one update per frame.
    if (drag.raf) return;
    drag.raf = requestAnimationFrame(() => {
      if (!drag) return;
      drag.raf = 0;
      dragTo(drag.x, drag.y);
    });
  });

  document.addEventListener('pointercancel', () => { endDrag(); });

  document.addEventListener('pointerup', () => {
    if (!drag) return;
    // One last placement at the exact release point, in case the pointer moved
    // after the previous frame. It can end the drag, if the row went away.
    dragTo(drag.x, drag.y);
    const d = endDrag();
    if (!d || !d.host.contains(d.row)) return;
    pendingMove = {
      resource: d.key,
      id: d.row.getAttribute('data-id') || '',
      expectedIdentity: d.row.getAttribute('data-identity') || undefined,
      anchor: anchorAfter(d.row),
    };
    doMove('');
  });
}

function doHist(kind: string, key: string, ack: string): void {
  if (!kind || !key) return;
  // An undo is a write, so a guard can refuse it and ask for an acknowledgement
  // — the same retry hook the form uses, because the answer has to reach the
  // same request rather than a new one.
  retry = (a: string) => doHist(kind, key, a);
  sock?.emit('res:' + kind, ack ? { resource: key, ack } : { resource: key });
}

function mountAddSlots(): void {
  document.querySelectorAll('[data-res-add]').forEach((host) => {
    const keys = (host.getAttribute('data-res-add') || '')
      .split(',').map((k) => k.trim()).filter(Boolean);
    // Drawn from what has ARRIVED, never from a promise: this runs again on
    // every res:schema, so a slot naming two resources fills in as each answers
    // instead of waiting for both. An empty slot is laid out away entirely by
    // CSS, so a viewer who may not write gets back exactly the header they had.
    const ready = keys.map((k) => schemas.get(k))
      .filter((s): s is Schema => !!s && s.permitted);
    const target = ready.length ? histTarget(ready) : '';
    const h = (target && hist.get(target)) || null;
    host.innerHTML = (target
      ? histButton('undo', target, !!h?.canUndo, h?.undoLabel || '') +
        histButton('redo', target, !!h?.canRedo, h?.redoLabel || '')
      : '') + ready.map((s) =>
      '<button class="sbtn sbtn-primary" data-res-addbtn="' + esc(s.key) + '"' +
      ' style="padding:.28rem .65rem;font-size:.72rem">' + esc('+ Add ' + s.label) +
      '</button>').join('');
  });
}

export function mountAdds(socket: Socket): void {
  sock = socket;
  wire(socket);
  document.querySelectorAll('[data-res-add]').forEach((host) => {
    (host.getAttribute('data-res-add') || '').split(',')
      .forEach((k) => { const key = k.trim(); if (key) schemaFor(key); });
  });
  mountAddSlots();
  if (addsWired) return;
  addsWired = true;
  document.addEventListener('click', (ev) => {
    const target = ev.target as HTMLElement;
    if (!target.closest) return;
    const hb = target.closest('[data-res-hist]');
    if (hb) {
      if ((hb as HTMLButtonElement).disabled) return;
      doHist(hb.getAttribute('data-res-hist') || '',
             hb.getAttribute('data-res-histkey') || '', '');
      return;
    }
    const act = target.closest('[data-res-actionbtn]');
    if (act && current) {
      const key = act.getAttribute('data-res-actionbtn') || '';
      const send = (ack: string): void => {
        socket.emit('res:action', {
          resource: current!.key, action: key,
          id: current!.id || undefined,
          expectedIdentity: current!.identity || undefined,
          ack: ack || undefined,
        });
      };
      // A guard can refuse an action exactly as it refuses a save, so the retry
      // hook is the same one the form uses.
      retry = send;
      send('');
      return;
    }
    const btn = target.closest('[data-res-addbtn]');
    if (!btn) return;
    openResource(socket, btn.getAttribute('data-res-addbtn') || '', null);
  });
}

/**
 * Open the edit form when a row inside a `[data-res-rows]` table is clicked.
 *
 * A ROW-LEVEL `data-res` WINS over the table's. The Routes table holds both
 * address families in one tbody declared `data-res-rows="route"`, and a v6 row
 * overrides itself to `route6` — the family is what decides which RouterOS menu
 * the edit goes to, and editing an IPv6 route through /ip/route would fail at
 * the router with nothing on screen explaining why.
 *
 * The identity is round-tripped from `data-identity` rather than looked up in
 * the payload: the server refuses the write if the row no longer carries it,
 * and a `.id` survives a rename, so it addresses a row without identifying it.
 *
 * The earlier ported pages each grew their own version of this that looks the
 * row up in the page payload instead; they still work and are covered by the
 * DOM gate, but they are three copies of one thing. Consolidating them is a
 * queue item, not a change to make in passing.
 */
export function mountRows(socket: Socket): void {
  sock = socket;
  wire(socket);
  if (rowsWired) return;
  rowsWired = true;
  wireDrag();
  document.addEventListener('click', (ev) => {
    const target = ev.target as HTMLElement;
    if (!target.closest) return;
    // Reordering, and it lives HERE rather than beside the other buttons.
    //
    // The live app has ONE delegated click handler, so its move branch simply
    // returns before the row-open code below it. This port has TWO listeners —
    // `mountAdds` owns the history and action buttons, `mountRows` owns opening
    // a row — and a `return` in one does not stop the other from running. An
    // arrow sits inside `[data-id]`, so putting this branch in the other
    // listener emitted the move AND opened the edit dialog on the same click.
    //
    // Same ordering as the original, in the listener where the ordering is what
    // does the work.
    //
    // Rendered by firewall.ts and capsman.ts, which name the attribute
    // `data-res-move` rather than anything page-specific precisely so this
    // engine owns the behaviour. Until now nothing here read it, so both pages
    // drew working-looking arrows that did nothing — on Firewall, where rule
    // ORDER decides what the rule does.
    const mv = target.closest('[data-res-move]');
    if (mv) {
      if ((mv as HTMLButtonElement).disabled) return;
      const mrow = target.closest('[data-id]');
      const mhost = target.closest('[data-res-rows]');
      if (!mrow || !mhost) return;
      // A ROW-LEVEL `data-res` WINS over the host's, because one table can hold
      // two families — Routes carries v4 and v6 — and the family decides which
      // RouterOS menu the move goes to.
      const mkey = mrow.getAttribute('data-res') || mhost.getAttribute('data-res-rows') || '';
      const mschema = schemas.get(mkey);
      // A viewer's arrows are already disabled by the page; this is the second
      // check, on the schema the server sent, so a stale render cannot send a
      // write the caller may not make.
      if (!mschema || !mschema.permitted) return;
      pendingMove = {
        resource: mkey,
        id: mrow.getAttribute('data-id') || '',
        expectedIdentity: mrow.getAttribute('data-identity') || undefined,
        direction: mv.getAttribute('data-res-move') || '',
      };
      doMove('');
      return;
    }

    const host = target.closest('[data-res-rows]');
    if (!host) return;
    const row = target.closest('[data-id]');
    if (!row || !host.contains(row)) return;
    const key = row.getAttribute('data-res') || host.getAttribute('data-res-rows') || '';
    if (!key) return;
    openResource(socket, key, {
      id: row.getAttribute('data-id') || '',
      name: row.getAttribute('data-identity') || '',
    });
  });
}

/**
 * Open the form for a row, or for a new one.
 *
 * An existing row is NOT rendered from the list payload. The collector reads
 * with a proplist narrow enough for the page it feeds, so a form filled from it
 * would blank every property the page does not display — on dnsStatic that is
 * match-subdomain, cname, forward-to and text. The server is asked for a fresh
 * read instead, and answers on `res:row`.
 */
export function openResource(socket: Socket, key: string,
                             row: { id: string; name?: string } | null): void {
  sock = socket;
  wire(socket);
  schemaFor(key).then((schema) => {
    // No dialog for a viewer who cannot write — the server would refuse it, and
    // a form that always fails is worse than no form.
    //
    // app.js asks this in each of its CALLERS instead. One check here covers
    // them all, which matters because `dns`, `bridges` and `vlans` still open
    // rows through their own per-page handlers; putting it in the delegation
    // would have left those three uncovered until they migrate.
    if (!schema.permitted) return;
    if (!row) {
      // The pickers are read when the form opens rather than cached with the
      // schema: a bridge added a minute ago should be in the list. Until this
      // asked, every Add form on a page with a picker rendered a text box while
      // the Edit form beside it rendered the router's own list.
      current = null;
      socket.emit('res:new', { resource: key });
      return;
    }
    current = { key, id: row.id, identity: row.name ?? null };
    // `|| undefined`, NOT `?? ''`. An absent identity is OMITTED from the
    // payload, exactly as the original's `getAttribute('data-identity') ||
    // undefined` omits it — JSON.stringify drops an undefined value and keeps an
    // empty string.
    //
    // Harmless today, because `find` in resource.go guards on `expected != ""`
    // and so treats the two the same. Aligned anyway: the payload contract is
    // one of the things this port is not supposed to move, and a server that
    // later distinguished "expect an empty name" from "do not check" would find
    // this side asserting something it never meant to.
    socket.emit('res:row', { resource: key, id: row.id, expectedIdentity: row.name || undefined });
  }).catch((e) => {
    console.error(e);
    setError(String(e.message || e));
  });
}

function wire(socket: Socket): void {
  if (wired) return;
  wired = true;

  socket.on('res:row', (d: any) => {
    const schema = schemas.get(d.resource);
    if (!schema) return;
    show(schema, d.values || {}, { id: d.id, identity: d.identity }, !!d.readOnly,
         d.options || {}, d.actions || []);
  });

  socket.on('res:history', (d: any) => {
    if (!d || !d.resource) return;
    hist.set(d.resource, d as HistState);
    mountAddSlots();
  });

  socket.on('res:schema', (d: any) => {
    if (!d || !d.key) return;
    schemas.set(d.key, d as Schema);
    mountAddSlots();
    const q = waiting.get(d.key) || [];
    waiting.delete(d.key);
    q.forEach((cb) => cb(d as Schema));
  });

  // Schemas are PER-ROUTER: switching routers can change whether the viewer may
  // write, so they are dropped and re-asked rather than carried across.
  // `router:switched` and not `router:active` — the permissions that matter are
  // the ones for the router we have arrived at.
  const refreshAll = (): void => {
    schemas.clear();
    waiting.clear();
    mountAddSlots();                       // clear the last router's buttons
    document.querySelectorAll('[data-res-add]').forEach((host) => {
      (host.getAttribute('data-res-add') || '').split(',')
        .forEach((k) => { const key = k.trim(); if (key) schemaFor(key); });
    });
  };
  socket.on('connect', refreshAll);
  socket.on('router:switched', refreshAll);

  // ── A CARD WHOSE ADD SLOT CHANGES RESOURCE SAYS SO ────────────────────────
  //
  // The Firewall card's four tables share one header, and its Add button belongs
  // to whichever tab is showing; the WiFi and CAPsMAN cards do the same. Those
  // three pages rewrite `data-res-add` and then announce `mikrodash:resmount`
  // (`firewall.ts`, `wifi.ts`, `capsman.ts`), and the live app listens for it
  // here — `../MikroDash/public/app.js:15186`.
  //
  // This port announced it three times and listened nowhere, so the Add button
  // on a swapped tab kept the PREVIOUS tab's resource: pressing Add on the NAT
  // table opened the filter-rule form. Found by `tools/announcement-audit.js`,
  // which recorded it as one-sided until now.
  //
  // Schemas are NOT cleared, unlike `refreshAll`. The router has not changed —
  // only which resource this slot names — so what is already known stays known
  // and only the newly named keys are asked for.
  document.addEventListener('mikrodash:resmount', () => {
    document.querySelectorAll('[data-res-add]').forEach((host) => {
      (host.getAttribute('data-res-add') || '').split(',')
        .forEach((k) => { const key = k.trim(); if (key) schemaFor(key); });
    });
    mountAddSlots();
  });

  socket.on('res:new', (d: any) => {
    const schema = schemas.get(d.resource);
    if (!schema) return;
    show(schema, null, null, false, d.options || {}, []);
  });

  socket.on('res:ok', () => close());

  socket.on('res:error', (d: any) => {
    if (d && (d.code === 'self-cutoff' || d.code === 'stale-warning')) {
      showWarning(d);
      return;
    }
    const codes: Record<string, string> = {
      denied: 'You may not change this.',
      unavailable: 'The router is not reachable.',
      'stale-row': 'That row changed on the router. Close and reopen it.',
      'read-only-row': 'This entry cannot be edited here.',
      'router-denied': 'The router refused the change: the API user lacks permission.',
      'write-failed': 'The router refused the change.',
      'bad-request': 'That request was incomplete.',
      'guard-not-ported': 'This change needs a safety check that is not available yet, so it was refused.',
    };
    if (d && d.code === 'invalid' && Array.isArray(d.errors)) {
      setError(d.errors.map((e: { message: string }) => e.message).join('; '));
      return;
    }
    setError((d && codes[d.code]) || (d && d.message) || 'The change was refused.');
  });

  // ── the preview ───────────────────────────────────────────────────────────
  //
  // The command is rendered by the SERVER, never assembled here: it is built
  // from the same validated values a save would use, and its secret masking
  // lives beside the code that knows which fields are secret. A browser-side
  // preview would be a second implementation of both, and the one that leaks.
  el('res_previewBtn')?.addEventListener('click', () => {
    if (!current) return;
    const schema = schemas.get(current.key);
    if (!schema) return;
    socket.emit('res:preview', {
      resource: current.key,
      id: current.id || '',
      values: readValues(schema),
    });
  });

  socket.on('res:preview', (d: { resource?: string; command?: string }) => {
    // Guarded on `resource`, as the original is: a reply for a form the operator
    // has since closed and reopened elsewhere must not paint into this one.
    if (!d || !current || d.resource !== current.key) return;
    const p = el('res_preview');
    if (!p) return;
    p.textContent = d.command || '';
    p.style.display = '';
  });

  el('res_save')?.addEventListener('click', () => {
    if (!current) return;
    const schema = schemas.get(current.key);
    if (!schema) return;
    setError('');
    // Captured, so an acknowledged retry sends the SAME values rather than
    // re-reading a form the operator may have touched while reading the warning.
    const body = {
      resource: current.key,
      id: current.id || '',
      expectedIdentity: current.identity || '',
      values: readValues(schema),
    };
    retry = (ack: string) => socket.emit('res:save', { ...body, ack });
    socket.emit('res:save', body);
  });

  el('res_delete')?.addEventListener('click', () => {
    if (!current || !current.id) return;
    const body = {
      resource: current.key,
      id: current.id,
      expectedIdentity: current.identity || '',
    };
    retry = (ack: string) => socket.emit('res:remove', { ...body, ack });
    socket.emit('res:remove', body);
  });

  document.querySelectorAll('[data-modal-close="resModal"]').forEach((b) =>
    b.addEventListener('click', close));
  el('resModal')?.addEventListener('click', (e) => {
    if (e.target === el('resModal')) close();
  });
}
