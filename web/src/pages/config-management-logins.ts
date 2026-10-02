/**
 * The Credentials tab's MikroDash login profiles (#143): the account MikroDash
 * ITSELF signs in with - user `MikroDash` in group `MikroDash` - one password
 * for every linked device.
 *
 * ── LINKING WRITES TO THE DEVICE, AND PROVES IT FIRST ───────────────────────
 *
 * The server creates the account on each device, signs in afresh with it, and
 * only then switches the device over; a failure puts the device back as it was
 * (`internal/server/loginprof_api.go`). So linking is a job that runs for a
 * while, and its progress is polled from the list rather than awaited.
 *
 * ── THE PASSWORD IS WRITE-ONLY ───────────────────────────────────────────────
 *
 * The server never sends it. On edit the box is empty and blank keeps it; a
 * value there changes the password on EVERY linked device, all or nothing.
 */

import { el, esc } from '../dom';

export interface LoginOpResult { state: string; message: string }

export interface LoginOp {
  kind: string;
  running: boolean;
  started: number;
  results: Record<string, LoginOpResult>;
  summary: string;
}

/** One profile, as GET /api/credentials/logins lists it. */
export interface LoginProfileView {
  id: string;
  name: string;
  username: string;
  group: string;
  hasSecret: boolean;
  devices: string[];
  updatedAt: number;
  op: LoginOp | null;
}

interface Device { id: string; label: string; siteIds: string[]; loginProfileId: string }

/** The status cell: the job's state while it runs, then its summary. */
export function opStatus(op: LoginOp | null): string {
  if (!op) return '<span class="cp-pill cp-none">idle</span>';
  const vals = Object.values(op.results);
  const failed = vals.filter((r) => r.state === 'failed').length;
  if (op.running) {
    const done = vals.filter((r) => r.state === 'done' || r.state === 'failed').length;
    const what = op.kind === 'password' ? 'Changing password' : 'Linking';
    return `<span class="cp-pill cp-wait">${what} ${done}/${vals.length}</span>`;
  }
  const kind = failed ? 'warn' : 'ok';
  return `<span class="cp-pill cp-${kind}" title="${esc(op.summary)}">${esc(op.summary || 'done')}</span>`;
}

/** One table row. */
export function loginRow(p: LoginProfileView): string {
  const busy = !!(p.op && p.op.running);
  return '<tr>'
    + `<td><strong>${esc(p.name)}</strong></td>`
    + `<td><span class="cp-user">${esc(p.username)}</span> <span class="cfg-meta">in ${esc(p.group)}</span></td>`
    + `<td>${p.devices.length}</td>`
    + `<td>${opStatus(p.op)}</td>`
    + '<td class="text-end cp-actions">'
    + `<button class="cfg-btn" type="button" data-lp-edit="${esc(p.id)}"${busy ? ' disabled' : ''}>Edit</button> `
    + `<button class="cfg-btn" type="button" data-lp-devices="${esc(p.id)}">Devices</button> `
    + `<button class="cfg-btn cfg-btn-danger" type="button" data-lp-del="${esc(p.id)}"`
    + (p.devices.length ? ' disabled title="Switch its devices to their own login first"' : '')
    + '>Delete</button>'
    + '</td></tr>';
}

/**
 * The devices Link would act on: every device ticked directly or through a
 * site, minus those already on this profile. A site is a shortcut that ticks
 * its devices, not a lasting rule - see the dialog's note.
 */
export function linkTargets(sites: ReadonlySet<string>, picked: ReadonlySet<string>,
  devices: readonly Device[], profileId: string): string[] {
  return devices.filter((d) => d.loginProfileId !== profileId
    && (picked.has(d.id) || d.siteIds.some((s) => sites.has(s)))).map((d) => d.id);
}

/** Checks the form before anything is sent. "" when it is fine. */
export function formError(name: string, pass: string, pass2: string, creating: boolean): string {
  if (!name.trim()) return 'A name is required.';
  if (creating && !pass) return 'A password is required.';
  if (pass && pass.length < 12) return 'The password needs at least 12 characters.';
  if (pass !== pass2) return 'The two passwords differ.';
  return '';
}

async function lpApi<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await fetch('/api/credentials/logins' + path, { credentials: 'same-origin', ...init });
  const body = (await r.json().catch(() => ({}))) as T & { error?: string };
  if (!r.ok) throw new Error(body.error || 'The request failed (' + r.status + ')');
  return body;
}

const send = (method: string, v: unknown): RequestInit => ({
  method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(v),
});

export function mountLoginProfiles(): { load: () => Promise<void> } {
  let list: LoginProfileView[] = [];
  let devices: Device[] = [];
  let sites: { id: string; name: string }[] = [];
  let editing: LoginProfileView | null = null;
  let devFor = '';
  let ownFor = '';
  const wantSites = new Set<string>();
  const wantDevices = new Set<string>();
  let poll: ReturnType<typeof setTimeout> | null = null;

  const open = (id: string, on: boolean): void => { el(id)?.classList.toggle('open', on); };
  const showErr = (id: string, msg: string): void => {
    const n = el(id);
    if (!n) return;
    n.textContent = msg;
    n.style.display = msg ? '' : 'none';
  };
  const nameOf = (id: string): string => devices.find((d) => d.id === id)?.label ?? id;

  async function loadDevices(): Promise<void> {
    try {
      const r = await fetch('/api/routers', { credentials: 'same-origin' });
      const b = (await r.json()) as { routers?: { id: string; label?: string; host?: string; disabled?: boolean;
        siteIds?: string[]; loginProfileId?: string }[] };
      devices = (b.routers ?? []).filter((x) => !x.disabled).map((x) => ({
        id: x.id, label: x.label || x.host || x.id, siteIds: x.siteIds ?? [],
        loginProfileId: x.loginProfileId ?? '',
      }));
    } catch {
      devices = [];
    }
  }

  async function load(): Promise<void> {
    try {
      list = (await lpApi<{ logins: LoginProfileView[] }>('')).logins;
    } catch {
      list = [];
    }
    const body = el('lpBody');
    const empty = el('lpEmpty');
    if (body) body.innerHTML = list.map(loginRow).join('');
    if (empty) empty.hidden = list.length > 0;
    if (devFor) {
      await loadDevices();
      drawDevices();
    }
    // POLLED WHILE A JOB RUNS, and only then.
    if (poll) clearTimeout(poll);
    poll = list.some((p) => p.op && p.op.running) ? setTimeout(() => void load(), 2000) : null;
  }

  // ── the profile form ────────────────────────────────────────────────────
  function openForm(p: LoginProfileView | null): void {
    editing = p;
    showErr('lpError', '');
    const t = el('lpModalTitle');
    if (t) t.textContent = p ? 'Edit login profile' : 'New login profile';
    const set = (id: string, v: string): void => { const n = el<HTMLInputElement>(id); if (n) n.value = v; };
    set('lpName', p?.name ?? '');
    set('lpPass', '');
    set('lpPass2', '');
    const lbl = el('lpPassLabel');
    if (lbl) lbl.textContent = p ? 'New password (blank keeps it)' : 'Password';
    const note = el('lpPassNote');
    if (note) {
      note.textContent = p && p.devices.length
        ? `A new password is changed on all ${p.devices.length} linked device(s) at once, or on none: if any `
          + 'device is offline or refuses it, nothing changes.'
        : 'At least 12 characters. It signs in to every linked device with every permission, so treat it '
          + "like the devices' admin password.";
    }
    open('lpModal', true);
  }

  async function save(): Promise<void> {
    const name = el<HTMLInputElement>('lpName')?.value ?? '';
    const pass = el<HTMLInputElement>('lpPass')?.value ?? '';
    const pass2 = el<HTMLInputElement>('lpPass2')?.value ?? '';
    const bad = formError(name, pass, pass2, !editing);
    if (bad) { showErr('lpError', bad); return; }
    try {
      if (editing) {
        await lpApi('/' + editing.id, send('PUT', { name, password: pass }));
      } else {
        await lpApi('', send('POST', { name, password: pass }));
      }
      open('lpModal', false);
      await load();
    } catch (e) {
      showErr('lpError', e instanceof Error ? e.message : 'The profile could not be saved');
    }
  }

  async function remove(id: string): Promise<void> {
    const p = list.find((x) => x.id === id);
    if (!p || !window.confirm(`Delete the login profile "${p.name}"?`)) return;
    try {
      await lpApi('/' + id, { method: 'DELETE' });
      await load();
    } catch (e) {
      window.alert(e instanceof Error ? e.message : 'The profile could not be deleted');
    }
  }

  // ── the devices dialog ──────────────────────────────────────────────────
  function drawDevices(): void {
    const p = list.find((x) => x.id === devFor);
    const t = el('lpDevTitle');
    if (t && p) t.textContent = 'Devices signing in with ' + p.name;
    const sitesHost = el('lpDevSites');
    if (sitesHost) {
      sitesHost.innerHTML = sites.length ? sites.map((s) => `<label class="cfg-pick-item${wantSites.has(s.id) ? ' is-on' : ''}">`
        + `<input type="checkbox" data-lp-site="${esc(s.id)}"${wantSites.has(s.id) ? ' checked' : ''}>`
        + `<span class="cfg-pick-name">${esc(s.name)}</span></label>`).join('')
        : '<div class="cfg-meta">No sites.</div>';
    }
    const host = el('lpDevList');
    const results = p?.op?.results ?? {};
    if (host) {
      host.innerHTML = devices.map((d) => {
        const linked = d.loginProfileId === devFor;
        const other = !linked && d.loginProfileId !== '';
        const viaSite = d.siteIds.some((s) => wantSites.has(s));
        const on = linked || viaSite || wantDevices.has(d.id);
        const r = results[d.id];
        const pill = linked ? '<span class="cp-pill cp-ok">linked</span>'
          : other ? '<span class="cp-pill cp-wait" title="Signs in with another login profile; linking moves it">other profile</span>'
          : '';
        const res = r && !linked ? `<span class="cp-pill cp-${r.state === 'failed' ? 'bad' : r.state === 'done' ? 'ok' : 'wait'}"`
          + ` title="${esc(r.message)}">${esc(r.state)}</span>` : '';
        return `<label class="cfg-pick-item${on ? ' is-on' : ''}">`
          + `<input type="checkbox" data-lp-device="${esc(d.id)}"${on ? ' checked' : ''}${linked || viaSite ? ' disabled' : ''}>`
          + `<span class="cfg-pick-name">${esc(d.label)}</span>${pill}${res}`
          + (r && r.state === 'failed' && r.message ? `<span class="cfg-meta lp-err">${esc(r.message)}</span>` : '')
          + (linked ? `<button class="cfg-btn" type="button" data-lp-own="${esc(d.id)}">Use own login</button>` : '')
          + '</label>';
      }).join('') || '<div class="cfg-meta">No devices.</div>';
    }
    const n = linkTargets(wantSites, wantDevices, devices, devFor).length;
    const btn = el<HTMLButtonElement>('lpDevApply');
    const running = !!(p && p.op && p.op.running);
    if (btn) {
      btn.disabled = n === 0 || running;
      btn.textContent = running ? 'Working…' : n ? `Link ${n} device${n === 1 ? '' : 's'}` : 'Link';
    }
  }

  async function openDevices(id: string): Promise<void> {
    devFor = id;
    wantSites.clear();
    wantDevices.clear();
    showErr('lpDevError', '');
    await loadDevices();
    try {
      const r = await fetch('/api/sites', { credentials: 'same-origin' });
      sites = ((await r.json()) as { sites?: { id: string; name: string }[] }).sites ?? [];
    } catch {
      sites = [];
    }
    drawDevices();
    open('lpDevModal', true);
  }

  async function link(): Promise<void> {
    const ids = linkTargets(wantSites, wantDevices, devices, devFor);
    const p = list.find((x) => x.id === devFor);
    if (!ids.length || !p) return;
    const msg = `Link ${ids.length} device(s) to "${p.name}"?\n\nOn each one this creates user MikroDash in group `
      + 'MikroDash (every permission) with the profile\'s password, checks it signs in, and switches MikroDash '
      + 'over. A device where that fails is put back as it was.';
    if (!window.confirm(msg)) return;
    try {
      await lpApi('/' + devFor + '/devices', send('POST', { routerIds: ids }));
      wantSites.clear();
      wantDevices.clear();
      await load();
    } catch (e) {
      showErr('lpDevError', e instanceof Error ? e.message : 'Linking could not start');
    }
  }

  // ── back to an own login ────────────────────────────────────────────────
  function openOwn(routerId: string): void {
    ownFor = routerId;
    showErr('lpOwnError', '');
    const d = el('lpOwnDevice');
    if (d) d.textContent = nameOf(routerId);
    for (const id of ['lpOwnUser', 'lpOwnPass']) { const n = el<HTMLInputElement>(id); if (n) n.value = ''; }
    open('lpOwnModal', true);
  }

  async function saveOwn(): Promise<void> {
    const username = el<HTMLInputElement>('lpOwnUser')?.value.trim() ?? '';
    const password = el<HTMLInputElement>('lpOwnPass')?.value ?? '';
    if (!username || !password) { showErr('lpOwnError', 'A username and password are required.'); return; }
    try {
      await lpApi('/' + devFor + '/devices/' + encodeURIComponent(ownFor) + '/own',
        send('POST', { username, password }));
      open('lpOwnModal', false);
      await load();
    } catch (e) {
      showErr('lpOwnError', e instanceof Error ? e.message : 'The device could not be switched');
    }
  }

  // ── wiring ──────────────────────────────────────────────────────────────
  el('lpNew')?.addEventListener('click', () => openForm(null));
  el('lpCancel')?.addEventListener('click', () => open('lpModal', false));
  el('lpSave')?.addEventListener('click', () => void save());
  el('lpBody')?.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    const b = t?.closest?.('button') as HTMLElement | null;
    if (!b) return;
    if (b.dataset.lpEdit) openForm(list.find((p) => p.id === b.dataset.lpEdit) ?? null);
    if (b.dataset.lpDevices) void openDevices(b.dataset.lpDevices);
    if (b.dataset.lpDel) void remove(b.dataset.lpDel);
  });
  el('lpDevClose')?.addEventListener('click', () => { devFor = ''; open('lpDevModal', false); });
  el('lpDevApply')?.addEventListener('click', () => void link());
  for (const id of ['lpDevSites', 'lpDevList']) {
    el(id)?.addEventListener('change', (e) => {
      const t = e.target as HTMLInputElement | null;
      if (!t) return;
      const set = t.dataset.lpSite ? wantSites : wantDevices;
      const key = t.dataset.lpSite || t.dataset.lpDevice || '';
      if (!key) return;
      if (t.checked) set.add(key); else set.delete(key);
      drawDevices();
    });
  }
  el('lpDevList')?.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement | null)?.closest?.('[data-lp-own]') as HTMLElement | null;
    if (!b) return;
    e.preventDefault();
    openOwn(b.dataset.lpOwn || '');
  });
  el('lpOwnCancel')?.addEventListener('click', () => open('lpOwnModal', false));
  el('lpOwnSave')?.addEventListener('click', () => void saveOwn());

  return { load };
}
