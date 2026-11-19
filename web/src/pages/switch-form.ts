/**
 * SwOS switches (#142): the device-type chooser and the switch form.
 *
 * ── ONE WAY IN FOR BOTH KINDS ───────────────────────────────────────────────
 *
 * Settings > Devices > Add and the Devices page's Add device both open
 * `chooseDeviceType` first. A RouterOS router goes on to whatever that button
 * always opened (the router modal, or zero-touch provisioning); a SwOS switch
 * comes here. Routers and switches are saved through the same routes
 * (`POST /api/routers` with `kind: "swos"`, `PUT /api/routers/:id`), so the
 * Settings table and the Devices page refresh on the same `routers:update`.
 *
 * ── TEST BEFORE THE FIRST SAVE, AS A ROUTER'S ADD DOES ─────────────────────
 *
 * A new switch is saved only after `POST /api/switches/probe` signed in and
 * found SwOS, so a typo or a RouterOS address is caught in the form rather
 * than as a card that never comes online. An edit may save straight away: its
 * stored login already worked, and a blank password keeps it.
 */

import { el } from '../dom';

/** A switch as the Settings table holds it (credential masked). */
export interface SwitchFormRow {
  id: string;
  label?: string;
  host?: string;
  port?: number | string;
  scheme?: string;
  username?: string;
  alertsEnabled?: boolean;
  reportingEnabled?: boolean;
}

export interface SwitchFormValues {
  label: string;
  host: string;
  scheme: string;
  port: string;
  username: string;
  password: string;
  alertsEnabled: boolean;
  reportingEnabled: boolean;
}

/** What a firmware family is called on screen. */
export function familyLabel(family: string): string {
  if (family === 'lite') return 'SwOS Lite';
  if (family === 'css106') return 'SwOS (CSS106)';
  return 'SwOS';
}

/**
 * The request body for a save. A NEW switch carries `kind`; an edit must not,
 * and the server drops it either way. A blank password on an edit is left out,
 * which keeps the stored one. A blank port takes the scheme's default.
 */
export function switchBody(v: SwitchFormValues, isNew: boolean): Record<string, unknown> {
  const out: Record<string, unknown> = {
    label: v.label.trim(),
    host: v.host.trim(),
    scheme: v.scheme === 'https' ? 'https' : 'http',
    username: v.username.trim() || 'admin',
    alertsEnabled: v.alertsEnabled,
    reportingEnabled: v.reportingEnabled,
  };
  if (v.port.trim()) out.port = v.port.trim();
  if (v.password || isNew) out.password = v.password;
  if (isNew) out.kind = 'swos';
  return out;
}

function value(id: string): string {
  return el<HTMLInputElement>(id)?.value ?? '';
}

function checked(id: string): boolean {
  return !!el<HTMLInputElement>(id)?.checked;
}

function readForm(): SwitchFormValues {
  return {
    label: value('swModalLabel'), host: value('swModalHost'), scheme: value('swModalScheme'),
    port: value('swModalPort'), username: value('swModalUser'), password: value('swModalPass'),
    alertsEnabled: checked('swModalAlerts'), reportingEnabled: checked('swModalReporting'),
  };
}

function result(text: string, ok: boolean | null): void {
  const r = el('swModalResult');
  if (!r) return;
  r.textContent = text;
  r.className = 'sw-result' + (ok === true ? ' is-ok' : ok === false ? ' is-err' : '');
}

let editingId = '';
let tested = false;

function setSaveEnabled(): void {
  const save = el<HTMLButtonElement>('swModalSaveBtn');
  if (save) save.disabled = !editingId && !tested;
}

/** Opens the form: `null` adds a switch, a row edits it. */
export function openSwitchForm(row: SwitchFormRow | null): void {
  editingId = row ? row.id : '';
  tested = false;
  const set = (id: string, v: string) => { const i = el<HTMLInputElement>(id); if (i) i.value = v; };
  const tick = (id: string, v: boolean) => { const i = el<HTMLInputElement>(id); if (i) i.checked = v; };
  set('swModalId', editingId);
  set('swModalLabel', row?.label ?? '');
  set('swModalHost', row?.host ?? '');
  set('swModalScheme', row?.scheme === 'https' ? 'https' : 'http');
  set('swModalPort', row?.port ? String(row.port) : '');
  set('swModalUser', row?.username ?? 'admin');
  set('swModalPass', '');
  tick('swModalAlerts', !!row?.alertsEnabled);
  tick('swModalReporting', !!row?.reportingEnabled);
  const title = el('swModalTitle');
  if (title) title.textContent = row ? 'Edit SwOS switch' : 'Add SwOS switch';
  const help = el('swModalPassHelp');
  if (help) help.textContent = row ? 'Leave blank to keep the current password.' : '';
  result(row ? '' : 'Test the connection before saving.', null);
  setSaveEnabled();
  el('swModalBg')?.classList.add('open');
}

function close(): void {
  el('swModalBg')?.classList.remove('open');
}

async function test(): Promise<void> {
  const v = readForm();
  const body: Record<string, unknown> = { host: v.host.trim(), scheme: v.scheme, username: v.username, password: v.password };
  if (v.port.trim()) body.port = v.port.trim();
  if (editingId) body.id = editingId;
  result('Signing in to the switch...', null);
  try {
    const res = await fetch('/api/switches/probe', {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    });
    const j = await res.json() as { ok?: boolean; error?: string; model?: string; version?: string; family?: string; identity?: string };
    if (j.ok) {
      tested = true;
      result('Found ' + (j.model || 'a switch') + ', ' + familyLabel(j.family || '') + ' ' + (j.version || '') +
        (j.identity ? ' ("' + j.identity + '")' : '') + '.', true);
      const label = el<HTMLInputElement>('swModalLabel');
      if (label && !label.value.trim() && j.identity) label.value = j.identity;
    } else {
      tested = false;
      result(j.error || 'The switch could not be read.', false);
    }
  } catch {
    tested = false;
    result('Network error.', false);
  }
  setSaveEnabled();
}

async function save(): Promise<void> {
  const isNew = !editingId;
  const body = switchBody(readForm(), isNew);
  try {
    const res = await fetch(isNew ? '/api/routers' : '/api/routers/' + encodeURIComponent(editingId), {
      method: isNew ? 'POST' : 'PUT', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    });
    const j = await res.json() as { ok?: boolean; error?: string };
    if (!j.ok) {
      result(j.error || 'The switch was not saved.', false);
      return;
    }
    close();
  } catch {
    result('Network error.', false);
  }
}

let onRouterChoice: (() => void) | null = null;

/** Asks which kind of device is being added. */
export function chooseDeviceType(onRouter: () => void): void {
  onRouterChoice = onRouter;
  el('devTypeBg')?.classList.add('open');
}

// The chooser element the controls were last bound to: once per element, so
// the page binds once and a test mounting fresh markup binds its own.
let boundTo: unknown = null;

/** Binds the two dialogs' controls, once per markup. */
export function initSwitchForm(): void {
  const typeBg = el('devTypeBg');
  if (boundTo === typeBg) return;
  boundTo = typeBg;
  el('swModalCloseBtn')?.addEventListener('click', close);
  el('swModalCancelBtn')?.addEventListener('click', close);
  el('swModalTestBtn')?.addEventListener('click', () => { void test(); });
  el('swModalSaveBtn')?.addEventListener('click', () => { void save(); });
  // A changed address or login needs a new test before a new switch is saved.
  for (const id of ['swModalHost', 'swModalScheme', 'swModalPort', 'swModalUser', 'swModalPass']) {
    el(id)?.addEventListener('input', () => { tested = false; setSaveEnabled(); });
  }
  el('devTypeCloseBtn')?.addEventListener('click', () => typeBg?.classList.remove('open'));
  typeBg?.addEventListener('click', (e) => {
    const opt = (e.target as HTMLElement | null)?.closest?.('[data-devtype]') as HTMLElement | null;
    if (!opt) return;
    typeBg.classList.remove('open');
    if (opt.dataset.devtype === 'swos') openSwitchForm(null);
    else onRouterChoice?.();
  });
}
