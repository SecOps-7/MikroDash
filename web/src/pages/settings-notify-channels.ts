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
  backup: boolean;
}

let channels: ChannelView[] = [];
let events: EventRow[] = [];
let schemes: string[] = [];
let defaults: string[] = [];
let routers: Array<{ id: string; label: string }> = [];
/** The channel being edited, or null for a new one. */
let editing: ChannelView | null = null;
/** Whether this viewer may own install-wide channels. Told by the server; a
 *  non-administrator makes channels for themselves instead. */
let canManageInstall = false;

/**
 * Which SMTP channel scheduled reports go out through.
 *
 * "" IS NOT "NONE". It means the first enabled install-owned SMTP channel, which
 * is what `smtpConfig` on the server falls back to and what `reportsChannelID`
 * below ticks. That is why an upgraded install needs no migration: the seeded
 * "Email" channel is found on its own.
 */
let reportChannelId = '';
/**
 * The stored URLs, once the detail read has returned them.
 *
 * `null` means "not loaded" — the read failed or was refused — and in that case
 * an empty box still means "keep what is stored", as it always did. Once they
 * ARE loaded the box is authoritative: what you see is what gets saved, and
 * clearing it is refused rather than silently ignored.
 */
let loadedURLs: string[] | null = null;

const INSTALL = '_install';

/** The dimmed examples a new channel shows, one per line, as other dashboards do.
 *  A placeholder rather than prefilled text: it must not be saved by accident. */
const URL_EXAMPLES = [
  'tgram://bot_token/chat_id',
  'discord://webhook_id/webhook_token',
  'slack://token_a/token_b/token_c',
  'ntfy://ntfy.sh/my-topic',
  'ntfys://host/topic?token=tk_abc',
  'gotify://host/app-token',
  'pover://user_key/api_token',
  'pbul://access_token',
  'jsons://hooks.example.com/path',
  'apprise://host/config-key',
].join('\n');

function show(open: boolean): void {
  const m = el('notifChanModal');
  if (m) m.classList.toggle('open', open);
}

/**
 * The channel the reports tick belongs on, resolved the way the server resolves
 * it.
 *
 * ONLY ONE CAN BE TICKED BECAUSE THERE IS ONLY ONE VALUE. The state is the
 * single `reportChannelId` setting, not a flag per channel, so exclusivity is
 * structural — there is no handler untick­ing the others and therefore no way for
 * two to drift into being ticked at once.
 *
 * A stored id naming a channel that has since been DELETED falls through to the
 * first, matching the server's second pass. Without that the page would show no
 * tick at all while reports carried on being sent, or worse, agree with a
 * settings value that stops them.
 */
function reportsChannelID(): string {
  const smtp = channels.filter((c) => c.kind === 'smtp' && c.owner === INSTALL);
  if (reportChannelId && smtp.some((c) => c.id === reportChannelId)) return reportChannelId;
  const first = smtp.find((c) => c.enabled);
  return first ? first.id : '';
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
  // No `data-nchan-card`: the actions carry `data-nchan` and the delegated
  // handler reads that. An id on the card too would be an attribute nothing
  // queries, which `TestRenderedAttributesAreRead` exists to catch.
  return '<div class="apps-card' + (c.enabled ? ' st-running' : '') + '">'
    + '<div class="apps-card-head"><div class="apps-card-titles">'
    + '<strong>' + esc(c.name) + '</strong>'
    + '<span class="apps-card-sub">' + esc(where) + ' &middot; ' + esc(c.kind) + '</span>'
    + '</div>'
    + '<span class="vpn-hs-badge ' + (c.enabled ? 'hs-ok' : 'hs-stale') + '">'
    + (c.enabled ? 'ON' : 'OFF') + '</span></div>'
    + '<div class="apps-card-desc">' + dest + '</div>'
    + '<div class="apps-card-meta">' + esc(evs) + ' &middot; ' + esc(scope) + '</div>'
    + reportsTick(c)
    + '<div class="apps-card-actions">'
    + '<button class="apps-btn" data-nchan-act="test" data-nchan="' + esc(c.id) + '">Test</button>'
    + '<button class="apps-btn" data-nchan-act="edit" data-nchan="' + esc(c.id) + '">Edit</button>'
    + '<button class="apps-btn is-danger" data-nchan-act="delete" data-nchan="'
    + esc(c.id) + '">Delete</button>'
    + '</div></div>';
}

/**
 * The "send scheduled reports through this one" tick, on SMTP cards only.
 *
 * ── WHY ONLY INSTALL-OWNED SMTP ────────────────────────────────────────────
 *
 * A webhook cannot carry a PDF attachment, so a report needs a real mail server.
 * And the owner test is the server's: a user's personal email channel must not
 * become the server every scheduled report in the install goes out through, so
 * it is not offered as one.
 *
 * ── IT DOES NOT UNTICK ─────────────────────────────────────────────────────
 *
 * Clicking the ticked one is a no-op. Reports have to leave through something,
 * and an untick would store an id of "" — which the server reads as "the first
 * enabled install SMTP channel", i.e. this one again. A control whose off state
 * is indistinguishable from its on state is worse than one that does not turn
 * off, so it is `disabled` when ticked and says why.
 */
function reportsTick(c: ChannelView): string {
  if (c.kind !== 'smtp' || c.owner !== INSTALL) return '';
  const on = c.id === reportsChannelID();
  const locked = on || !canManageInstall;
  const why = on
    ? 'Reports already go out through this channel. Tick another SMTP channel to move them.'
    : (canManageInstall
      ? 'Send scheduled reports through this mail server'
      : 'Only a global administrator can move the reports channel');
  return '<label class="nchan-reports' + (on ? ' is-on' : '') + '" title="' + esc(why) + '">'
    + '<input type="checkbox" data-nchan-report="' + esc(c.id) + '"'
    + (on ? ' checked' : '') + (locked ? ' disabled' : '') + '>'
    + '<span>Scheduled reports</span></label>';
}

function render(): void {
  const grid = el('nchanGrid');
  if (grid) {
    grid.innerHTML = channels.length
      ? channels.map(card).join('')
      : '<p class="nchan-help">No channels yet. Add one to start receiving alerts.</p>';
  }
}

/**
 * Store which SMTP channel the scheduled reports leave through.
 *
 * ── A ONE-KEY POST TO /api/settings ────────────────────────────────────────
 *
 * `reportChannelId` is an ordinary setting, so this is the route that already
 * validates and audits it — the same partial-body POST the Polling tab's Save
 * Custom Profile button makes. Inventing an endpoint for it would be a second
 * way to write one settings key.
 *
 * ── THE LOCAL VALUE MOVES ONLY ON `ok` ─────────────────────────────────────
 *
 * A refusal — a non-admin, a validation failure — must leave the tick where it
 * was, so the state is set from the reply and the grid repainted from that. An
 * optimistic update would show the reports moving to a channel the server is
 * still sending nothing through.
 */
async function setReportsChannel(id: string): Promise<void> {
  if (!id || id === reportsChannelID()) return;
  try {
    const r = await fetch('/api/settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ reportChannelId: id }),
    });
    const j = await r.json();
    if (j && j.ok) reportChannelId = id;
  } catch { /* the tick stays where it was, which is where the reports are */ }
  render();
}

async function load(): Promise<void> {
  try {
    const r = await fetch('/api/notify-channels', { credentials: 'same-origin' });
    const j = await r.json();
    channels = (j && j.channels) || [];
    canManageInstall = !!(j && j.canManageInstall);
    // FROM THIS REPLY, not from `/api/settings`. The grid used to paint before
    // that separate load answered, so the tick arrived a beat later on whichever
    // card the fallback would have picked anyway.
    reportChannelId = (j && typeof j.reportChannelId === 'string') ? j.reportChannelId : '';
  } catch { channels = []; }
  render();
}

/** Read one channel's stored configuration, which the list deliberately omits. */
async function loadDetail(id: string): Promise<void> {
  loadedURLs = null;
  try {
    const r = await fetch('/api/notify-channels/' + encodeURIComponent(id),
      { credentials: 'same-origin' });
    if (!r.ok) return;
    const j = await r.json();
    if (j && Array.isArray(j.urls)) loadedURLs = j.urls;
    if (j && j.smtp) {
      setValue('nchanSmtpHost', String(j.smtp.host || ''));
      setValue('nchanSmtpPort', String(j.smtp.port || ''));
      setChecked('nchanSmtpSecure', j.smtp.secure === true);
      setValue('nchanSmtpUser', String(j.smtp.user || ''));
      setValue('nchanSmtpPass', String(j.smtp.pass || ''));
      setValue('nchanSmtpFrom', String(j.smtp.from || ''));
      setValue('nchanSmtpTo', String(j.smtp.to || ''));
    }
  } catch { /* the box stays empty and keep-on-blank still applies */ }
  if (loadedURLs) setValue('nchanUrls', loadedURLs.join('\n'));
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

/**
 * One event toggle.
 *
 * NO "not raised install-wide" ANY MORE. The endpoint carried a `raised` flag
 * saying whether the install raised this event at all, and the row appended that
 * phrase when it was false. The install-wide gates are gone — every alert is
 * recorded and this toggle is the only thing deciding delivery — so the flag
 * went with them. Reading a field that no longer exists made EVERY event read
 * "not raised install-wide", which is exactly backwards; found by opening the
 * tab, not by a test.
 */
function eventRow(e: EventRow, on: boolean): string {
  return '<label class="stoggle stoggle-bare nchan-event">'
    + '<span class="stoggle-label"><strong>' + esc(e.label) + '</strong>'
    + '<span class="nchan-event-desc">' + esc(e.desc) + '</span></span>'
    + '<span class="stoggle-switch"><input type="checkbox" data-nchan-event="' + esc(e.key) + '"'
    + (on ? ' checked' : '') + '>'
    + '<span class="stoggle-track"></span><span class="stoggle-thumb"></span></span></label>';
}

function fillModal(c: ChannelView | null): void {
  editing = c;
  loadedURLs = null;
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
    // Only reached before the detail read returns, or when it could not.
    urls.placeholder = c && c.urlCount > 0
      ? 'Leave blank to keep the ' + c.urlCount + ' stored URL'
        + (c.urlCount === 1 ? '' : 's') + ', or type new ones to replace them'
      : URL_EXAMPLES;
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
    note.textContent = 'MikroDash records every alert. Choose which of them this channel '
      + 'delivers; the rest are still visible on the Alerts page and in the bell.';
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
  // OWNERSHIP IS ASKED FOR, NOT ASSUMED. Sending "install" unconditionally is
  // what made Add Channel answer 403 for everybody who is not an administrator.
  // The server still decides — this only says which of the two to ask for.
  if (!editing) body.owner = canManageInstall ? 'install' : 'mine';

  if (kind === 'webhook') {
    const raw = getValue('nchanUrls').split('\n').map((s) => s.trim()).filter((s) => s !== '');
    // ONCE THE STORED URLS ARE ON SCREEN, THE BOX IS THE TRUTH. Sending nothing
    // would make clearing it a silent no-op, and an operator who deleted a URL
    // would be told it saved while the old one kept delivering. Before the read
    // returns — or if it was refused — blank still means "keep".
    if (loadedURLs !== null || raw.length > 0) body.config = { urls: raw };
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
          await loadDetail(c.id);
        } else if (act === 'delete') {
          await remove(c);
        } else if (act === 'test') {
          await test(c.id, t as HTMLButtonElement);
        }
      })();
    });

    // ── MOVING THE REPORTS CHANNEL ────────────────────────────────────────
    //
    // A `change` listener on the grid rather than one per box, for the same
    // reason the actions above are delegated: `render()` replaces the whole
    // grid's innerHTML on every load, so anything bound to a box is thrown away
    // with it and silently stops working after the first repaint.
    grid.addEventListener('change', (ev) => {
      const box = (ev.target as HTMLElement).closest('[data-nchan-report]') as
        HTMLInputElement | null;
      if (!box) return;
      void setReportsChannel(box.getAttribute('data-nchan-report') || '');
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
