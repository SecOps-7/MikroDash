/**
 * The notification channels card, and the dialog that edits one.
 *
 * ── WHAT REPLACED WHAT ──────────────────────────────────────────────────────
 *
 * Four hand-written transport boxes, each with its own credentials, its own
 * toggle and its own Send Test button. Adding a provider meant writing another
 * box and another set of settings keys. A channel is a record instead, so the
 * page is a list and the providers are a URL scheme.
 *
 * ── THE CREDENTIALS NEVER COME BACK ────────────────────────────────────────
 *
 * A webhook URL carries its token in the path, so the server sends a COUNT and
 * never the URLs. That has a visible consequence this file has to handle
 * honestly: editing a channel shows an empty URL box, and leaving it empty keeps
 * what is stored. The placeholder says so, because a box that looks empty and
 * silently keeps a value is how an operator deletes their own configuration.
 */

import { el, esc } from '../dom';

interface ChannelView {
  id: string;
  owner: string;
  name: string;
  kind: string;
  enabled: boolean;
  events: string[];
  routers: string[];
  urlCount: number;
  smtpHost?: string;
  smtpFrom?: string;
  smtpTo?: string;
  hasSecret: boolean;
  mine: boolean;
}

interface EventRow {
  key: string;
  label: string;
  desc: string;
  raised: boolean;
  backup: boolean;
}

let channels: ChannelView[] = [];
let events: EventRow[] = [];
let schemes: string[] = [];
let defaults: string[] = [];
let routers: Array<{ id: string; label: string }> = [];
/** The channel being edited, or null for a new one. */
let editing: ChannelView | null = null;

const INSTALL = '_install';

function show(open: boolean): void {
  const m = el('notifChanModal');
  if (m) m.classList.toggle('open', open);
}

/** The card for one channel. */
function card(c: ChannelView): string {
  const where = c.owner === INSTALL ? 'Install' : 'Mine';
  const dest = c.kind === 'smtp'
    ? 'SMTP: ' + esc(c.smtpTo || c.smtpHost || 'not configured')
    : 'Webhook: ' + c.urlCount + (c.urlCount === 1 ? ' URL' : ' URLs');
  const evs = c.events.length === 0
    ? 'no events'
    : c.events.length + (c.events.length === 1 ? ' event' : ' events');
  const scope = c.routers.length === 0
    ? 'all routers'
    : c.routers.length + (c.routers.length === 1 ? ' router' : ' routers');
  return '<div class="apps-card' + (c.enabled ? ' st-running' : '') + '" data-nchan-card="'
    + esc(c.id) + '">'
    + '<div class="apps-card-head"><div class="apps-card-titles">'
    + '<strong>' + esc(c.name) + '</strong>'
    + '<span class="apps-card-sub">' + esc(where) + ' &middot; ' + esc(c.kind) + '</span>'
    + '</div>'
    + '<span class="vpn-hs-badge ' + (c.enabled ? 'hs-ok' : 'hs-stale') + '">'
    + (c.enabled ? 'ON' : 'OFF') + '</span></div>'
    + '<div class="apps-card-desc">' + dest + '</div>'
    + '<div class="apps-card-meta">' + esc(evs) + ' &middot; ' + esc(scope) + '</div>'
    + '<div class="apps-card-actions">'
    + '<button class="apps-btn" data-nchan-act="test" data-nchan="' + esc(c.id) + '">Test</button>'
    + '<button class="apps-btn" data-nchan-act="edit" data-nchan="' + esc(c.id) + '">Edit</button>'
    + '<button class="apps-btn is-danger" data-nchan-act="delete" data-nchan="'
    + esc(c.id) + '">Delete</button>'
    + '</div></div>';
}

function render(): void {
  const grid = el('nchanGrid');
  if (grid) {
    grid.innerHTML = channels.length
      ? channels.map(card).join('')
      : '<p class="nchan-help">No channels yet. Add one to start receiving alerts.</p>';
  }
  const count = el('nchanCount');
  if (count) {
    count.textContent = String(channels.length);
    count.className = 'card-badge' + (channels.length > 0 ? ' active-blue' : '');
  }
}

async function load(): Promise<void> {
  try {
    const r = await fetch('/api/notify-channels', { credentials: 'same-origin' });
    const j = await r.json();
    channels = (j && j.channels) || [];
  } catch { channels = []; }
  render();
}

async function loadEvents(): Promise<void> {
  try {
    const r = await fetch('/api/notify-channels/events', { credentials: 'same-origin' });
    const j = await r.json();
    events = (j && j.events) || [];
    schemes = (j && j.schemes) || [];
    defaults = (j && j.defaults) || [];
  } catch { /* the modal renders an empty list and says so */ }
  try {
    const r = await fetch('/api/routers', { credentials: 'same-origin' });
    const j = await r.json();
    routers = ((j && j.routers) || []).map((x: { id: string; label: string }) =>
      ({ id: x.id, label: x.label }));
  } catch { routers = []; }
}

/** One event toggle. `raised` false means the install does not raise it at all. */
function eventRow(e: EventRow, on: boolean): string {
  return '<label class="stoggle stoggle-bare nchan-event' + (e.raised ? '' : ' is-gated') + '">'
    + '<span class="stoggle-label"><strong>' + esc(e.label) + '</strong>'
    + '<span class="nchan-event-desc">' + esc(e.desc)
    + (e.raised ? '' : ' &mdash; not raised install-wide') + '</span></span>'
    + '<span class="stoggle-switch"><input type="checkbox" data-nchan-event="' + esc(e.key) + '"'
    + (on ? ' checked' : '') + '>'
    + '<span class="stoggle-track"></span><span class="stoggle-thumb"></span></span></label>';
}

function fillModal(c: ChannelView | null): void {
  editing = c;
  const t = el('nchanTitle');
  if (t) t.textContent = c ? 'Edit notification channel' : 'Add notification channel';

  setValue('nchanName', c ? c.name : '');
  setValue('nchanKind', c ? c.kind : 'webhook');
  setChecked('nchanEnabled', c ? c.enabled : true);
  setValue('nchanUrls', '');
  const urls = el<HTMLTextAreaElement>('nchanUrls');
  if (urls) {
    // THE PLACEHOLDER CARRIES THE PROMISE. An empty box that silently keeps the
    // stored URLs is how an operator deletes their own configuration.
    urls.placeholder = c && c.urlCount > 0
      ? 'Leave blank to keep the ' + c.urlCount + ' stored URL'
        + (c.urlCount === 1 ? '' : 's') + ', or type new ones to replace them'
      : 'tgram://bot_token/chat_id\ndiscord://webhook_id/webhook_token\nntfys://ntfy.sh/my-topic';
  }
  setValue('nchanSmtpHost', (c && c.smtpHost) || '');
  setValue('nchanSmtpPort', '');
  setChecked('nchanSmtpSecure', false);
  setValue('nchanSmtpUser', '');
  setValue('nchanSmtpPass', '');
  setValue('nchanSmtpFrom', (c && c.smtpFrom) || '');
  setValue('nchanSmtpTo', (c && c.smtpTo) || '');

  const schemeHelp = el('nchanSchemes');
  if (schemeHelp) schemeHelp.textContent = schemes.length ? 'Schemes: ' + schemes.join(', ') : '';

  const chosen = new Set(c ? c.events : defaults);
  const evHost = el('nchanEvents');
  if (evHost) {
    evHost.innerHTML = events.length
      ? events.map((e) => eventRow(e, chosen.has(e.key))).join('')
      : '<p class="nchan-help">The event list could not be loaded.</p>';
  }
  const note = el('nchanEventNote');
  if (note) {
    note.textContent = 'Which of the alerts MikroDash raises this channel receives. '
      + 'An event switched off in Alert Types is never raised, so no channel can receive it.';
  }

  const scope = new Set(c ? c.routers : []);
  const rHost = el('nchanRouters');
  if (rHost) {
    rHost.innerHTML = routers.length
      ? routers.map((rt) =>
        '<label class="nchan-router"><input type="checkbox" data-nchan-router="' + esc(rt.id) + '"'
        + (scope.has(rt.id) ? ' checked' : '') + '> ' + esc(rt.label) + '</label>').join('')
      : '<p class="nchan-help">No routers configured.</p>';
  }

  applyKind();
  selectTab('channel');
  setError('');
  setTestResult('');
}

function applyKind(): void {
  const kind = getValue('nchanKind');
  const wh = el('nchanWebhookBody');
  const smtp = el('nchanSmtpBody');
  if (wh) wh.hidden = kind !== 'webhook';
  if (smtp) smtp.hidden = kind !== 'smtp';
}

function selectTab(which: string): void {
  document.querySelectorAll('[data-nchantab]').forEach((b) => {
    b.classList.toggle('active', (b as HTMLElement).getAttribute('data-nchantab') === which);
  });
  document.querySelectorAll('[data-nchanpanel]').forEach((p) => {
    p.classList.toggle('active', (p as HTMLElement).getAttribute('data-nchanpanel') === which);
  });
}

function getValue(id: string): string {
  const e = el<HTMLInputElement>(id);
  return e ? e.value : '';
}

function setValue(id: string, v: string): void {
  const e = el<HTMLInputElement>(id);
  if (e) e.value = v;
}

function isChecked(id: string): boolean {
  const e = el<HTMLInputElement>(id);
  return !!e && e.checked;
}

function setChecked(id: string, on: boolean): void {
  const e = el<HTMLInputElement>(id);
  if (e) e.checked = on;
}

function setError(msg: string): void {
  const e = el('nchanError');
  if (!e) return;
  e.textContent = msg;
  e.style.display = msg ? 'block' : 'none';
}

function setTestResult(msg: string, bad?: boolean): void {
  const e = el('nchanTestResult');
  if (!e) return;
  e.textContent = msg;
  e.style.display = msg ? 'inline' : 'none';
  e.style.color = bad ? 'var(--accent-err)' : 'var(--accent-tx)';
}

function checkedValues(attr: string): string[] {
  const out: string[] = [];
  document.querySelectorAll('[' + attr + ']').forEach((b) => {
    const box = b as HTMLInputElement;
    if (box.checked) out.push(box.getAttribute(attr) || '');
  });
  return out.filter((v) => v !== '');
}

/**
 * The request body. `config` is omitted when nothing secret was typed, which is
 * what makes "leave blank to keep" true on the server as well as in the text.
 */
function bodyFor(): Record<string, unknown> {
  const kind = getValue('nchanKind');
  const body: Record<string, unknown> = {
    name: getValue('nchanName').trim(),
    kind,
    enabled: isChecked('nchanEnabled'),
    events: checkedValues('data-nchan-event'),
    routers: checkedValues('data-nchan-router'),
  };
  if (!editing) body.owner = 'install';

  if (kind === 'webhook') {
    const raw = getValue('nchanUrls').split('\n').map((s) => s.trim()).filter((s) => s !== '');
    if (raw.length > 0) body.config = { urls: raw };
  } else {
    const host = getValue('nchanSmtpHost').trim();
    const pass = getValue('nchanSmtpPass');
    if (host !== '' || pass !== '' || getValue('nchanSmtpTo').trim() !== '') {
      body.config = {
        host,
        port: Number(getValue('nchanSmtpPort')) || 0,
        secure: isChecked('nchanSmtpSecure'),
        user: getValue('nchanSmtpUser'),
        pass,
        from: getValue('nchanSmtpFrom').trim(),
        to: getValue('nchanSmtpTo').trim(),
      };
    }
  }
  return body;
}

async function save(): Promise<void> {
  const btn = el<HTMLButtonElement>('nchanSaveBtn');
  if (btn) btn.disabled = true;
  setError('');
  try {
    const url = editing ? '/api/notify-channels/' + encodeURIComponent(editing.id)
      : '/api/notify-channels';
    const r = await fetch(url, {
      method: editing ? 'PUT' : 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(bodyFor()),
    });
    const j = await r.json();
    if (!r.ok || !j || j.ok === false) {
      setError((j && (j.error || j.message)) || 'The channel could not be saved.');
      return;
    }
    show(false);
    await load();
  } catch {
    setError('The channel could not be saved.');
  } finally {
    if (btn) btn.disabled = false;
  }
}

async function test(id: string, btn?: HTMLButtonElement): Promise<void> {
  if (btn) btn.disabled = true;
  setTestResult('Sending…');
  try {
    const r = await fetch('/api/notify-channels/' + encodeURIComponent(id) + '/test',
      { method: 'POST', credentials: 'same-origin' });
    const j = await r.json();
    if (!r.ok || !j || j.ok === false) {
      setTestResult((j && (j.error || j.message)) || 'Test failed', true);
      return;
    }
    setTestResult('Sent');
  } catch {
    setTestResult('Test failed', true);
  } finally {
    if (btn) btn.disabled = false;
  }
}

async function remove(c: ChannelView): Promise<void> {
  if (!window.confirm('Delete the channel "' + c.name + '"?')) return;
  try {
    await fetch('/api/notify-channels/' + encodeURIComponent(c.id),
      { method: 'DELETE', credentials: 'same-origin' });
  } catch { /* the reload below shows whether it went */ }
  await load();
}

function byID(id: string): ChannelView | null {
  return channels.find((c) => c.id === id) || null;
}

export function initNotifyChannels(): void {
  const add = el('nchanAdd');
  if (add) {
    add.addEventListener('click', () => {
      void (async () => {
        await loadEvents();
        fillModal(null);
        show(true);
      })();
    });
  }

  const grid = el('nchanGrid');
  if (grid) {
    grid.addEventListener('click', (ev) => {
      const t = (ev.target as HTMLElement).closest('[data-nchan-act]') as HTMLElement | null;
      if (!t) return;
      const c = byID(t.getAttribute('data-nchan') || '');
      if (!c) return;
      const act = t.getAttribute('data-nchan-act');
      void (async () => {
        if (act === 'edit') {
          await loadEvents();
          fillModal(c);
          show(true);
        } else if (act === 'delete') {
          await remove(c);
        } else if (act === 'test') {
          await test(c.id, t as HTMLButtonElement);
        }
      })();
    });
  }

  document.querySelectorAll('[data-nchantab]').forEach((b) => {
    b.addEventListener('click', () =>
      selectTab((b as HTMLElement).getAttribute('data-nchantab') || 'channel'));
  });

  const kind = el('nchanKind');
  if (kind) kind.addEventListener('change', applyKind);

  const saveBtn = el('nchanSaveBtn');
  if (saveBtn) saveBtn.addEventListener('click', () => { void save(); });

  const testBtn = el<HTMLButtonElement>('nchanTestBtn');
  if (testBtn) {
    testBtn.addEventListener('click', () => {
      // A CHANNEL IS TESTED AS STORED, so an unsaved one has nothing to test.
      if (!editing) {
        setTestResult('Save the channel first, then test it.', true);
        return;
      }
      void test(editing.id, testBtn);
    });
  }

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'settings') void load();
  });
  void load();
}
