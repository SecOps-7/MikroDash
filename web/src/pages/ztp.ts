// Zero-touch provisioning in the browser: the Devices page's Provisioning
// section and its wizards, Settings → Provisioning, and the Dashboard's notice.
// The markup is `ztp-views.ts`; this wires it.
//
// ── ONE PAYLOAD, EVERY PAGE ─────────────────────────────────────────────────
//
// `ztp:state` goes to the `ztp` room, which the server joins only for a global
// administrator. So a viewer who is not one never receives it, and every part
// here stays hidden for them without a check of its own. The room is joined
// on every connect, not per page: membership is per connection, and a
// reconnect starts in no rooms.

import { el, esc } from '../dom';
import { t } from '../i18n';
import type { Socket } from '../socket';
import type { ZTPDeviceView, ZTPPayload } from '../gen/payloads';
import { onZtpState, setZtpState, ztpState } from '../ztp-state';
import { openWizard, type WizardStep } from '../wizard';
import { newSecret } from './config-management-deploy';
import type { VarDef } from './config-management-editor';
import * as V from './ztp-views';

async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch(path, {
    method, credentials: 'same-origin',
    ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
  });
  const j = (await r.json().catch(() => ({}))) as T & { ok?: boolean; error?: string };
  if (!r.ok || j.ok === false) throw new Error(j.error || 'The request failed (' + r.status + ')');
  return j;
}
const errText = (e: unknown): string => (e instanceof Error ? e.message : String(e));

// ── what the wizards pick from ──────────────────────────────────────────────

async function loadTemplates(): Promise<V.TemplateOpt[]> {
  const r = await api<{ templates: { id: string; name: string; description: string; kind: string }[];
    canned: { id: string; name: string; description: string }[] }>('GET', '/api/config/templates');
  return [
    ...r.canned.map((c) => ({ id: 'canned:' + c.id, name: c.name, description: c.description, canned: true })),
    ...r.templates.filter((t) => t.kind === 'fragment')
      .map((t) => ({ id: t.id, name: t.name, description: t.description, canned: false })),
  ];
}

async function openTemplate(id: string): Promise<V.OpenTemplate> {
  const r = await api<{ template: { id: string; variables: string; findings: V.Finding[] } }>('GET',
    '/api/config/templates/' + encodeURIComponent(id));
  let defs: VarDef[] = [];
  try { defs = JSON.parse(r.template.variables || '[]') as VarDef[]; } catch { /* no settings */ }
  return { id, defs, findings: r.template.findings ?? [] };
}

async function loadSites(): Promise<V.SiteOpt[]> {
  try {
    const r = await api<{ sites: V.SiteOpt[] }>('GET', '/api/sites');
    return (r.sites ?? []).map((s) => ({ id: s.id, name: s.name }));
  } catch {
    return [];
  }
}

// ── the configuration step, shared by Add device and Onboard ────────────────

interface ConfigState {
  templates: V.TemplateOpt[]; chosen: string; tpl: V.OpenTemplate | null;
  values: Record<string, string>; acked: Set<string>; reveal: boolean;
}

const newConfig = (): ConfigState =>
  ({ templates: [], chosen: '', tpl: null, values: {}, acked: new Set(), reveal: false });

function configStep(c: ConfigState, label: () => string, next: string,
  leave: () => Promise<string>): WizardStep {
  return {
    title: t('Configuration'),
    next,
    render: () => V.configStep(c.templates, c.chosen, c.tpl, label(), c.values, c.acked, c.reveal),
    check: () => V.configProblem(c.tpl, c.chosen, c.values, c.acked),
    leave,
    bind(body, redraw, recheck) {
      body.querySelector<HTMLSelectElement>('#ztpTpl')?.addEventListener('change', (e) => {
        c.chosen = (e.target as HTMLSelectElement).value;
        c.tpl = null;
        c.values = {};
        c.acked = new Set();
        redraw();
        if (!c.chosen) return;
        const want = c.chosen;
        void openTemplate(want).then((t) => {
          if (c.chosen !== want) return;
          c.tpl = t;
          // Defaults filled in, and every password a fresh one, as the Deploy
          // tab fills them: a secret has no default on purpose.
          for (const d of t.defs) c.values[d.name] = d.type === 'secret' ? newSecret() : d.default ?? '';
          redraw();
        }, () => {
          c.chosen = '';
          redraw();
        });
      });
      body.querySelectorAll<HTMLInputElement>('[data-dep-val]').forEach((i) => i.addEventListener('input', () => {
        c.values[i.dataset.depVar ?? ''] = i.value;
        recheck();
      }));
      body.querySelector<HTMLInputElement>('[data-dep-reveal]')?.addEventListener('change', (e) => {
        c.reveal = (e.target as HTMLInputElement).checked;
        redraw();
      });
      body.querySelectorAll<HTMLInputElement>('[data-ztp-ack]').forEach((i) => i.addEventListener('change', () => {
        if (i.checked) c.acked.add(i.dataset.ztpAck ?? '');
        else c.acked.delete(i.dataset.ztpAck ?? '');
        recheck();
      }));
    },
  };
}

// ── the script, shown once ──────────────────────────────────────────────────

function scriptStep(get: () => V.ScriptResult | null, what: string): WizardStep {
  return {
    title: t('Script'),
    noBack: true,
    render: () => { const r = get(); return r ? V.scriptPanel(r, Date.now(), what) : ''; },
    bind(body) {
      const r = get();
      if (!r) return;
      const copy = body.querySelector<HTMLButtonElement>('[data-ztp-copy]');
      copy?.addEventListener('click', () => {
        void navigator.clipboard?.writeText(r.script).then(() => { copy.textContent = t('Copied'); },
          () => { copy.textContent = t('Select the text and copy it'); });
      });
      body.querySelector('[data-ztp-download]')?.addEventListener('click', () => {
        const a = document.createElement('a');
        a.href = URL.createObjectURL(new Blob([r.script], { type: 'text/plain' }));
        a.download = r.filename;
        document.body.appendChild(a);
        a.click();
        a.remove();
        setTimeout(() => URL.revokeObjectURL(a.href), 1000);
      });
    },
  };
}

function showScript(title: string, r: V.ScriptResult, what: string): void {
  openWizard({ title, steps: [scriptStep(() => r, what)] });
}

// ── Add device ──────────────────────────────────────────────────────────────

function openAddDevice(): void {
  const p = ztpState();
  if (!p) return;
  let mode = V.remoteBlocked(p.status) ? 'local' : 'remote';
  const form: V.DeviceForm = { label: '', serial: '', siteIds: [], days: 7, lanUrl: p.status.lanUrl || V.lanSuggestion(location.origin, location.hostname) };
  const c = newConfig();
  let sites: V.SiteOpt[] = [];
  let result: V.ScriptResult | null = null;
  void loadTemplates().then((t) => { c.templates = t; }, () => { /* the picker offers nothing but None */ });
  void loadSites().then((s) => { sites = s; });

  openWizard({
    title: t('Add a device'),
    steps: [
      {
        title: t('Where'),
        render: () => V.whereStep(mode, ztpState()?.status ?? p.status),
        check: () => (mode ? '' : t('Choose where it will be')),
        bind(body, redraw) {
          body.querySelectorAll<HTMLInputElement>('input[name="ztpMode"]').forEach((i) => i.addEventListener('change', () => {
            mode = i.value;
            redraw();
          }));
        },
      },
      deviceFormStep(form, () => mode, () => sites),
      configStep(c, () => form.label, t('Make the script'), async () => {
        try {
          result = await api<V.ScriptResult>('POST', '/api/ztp/devices', {
            mode, label: form.label.trim(), serial: form.serial.trim(), siteIds: form.siteIds, days: form.days,
            lanUrl: mode === 'local' ? form.lanUrl.trim().replace(/\/$/, '') : '',
            templateId: c.chosen, values: c.chosen ? c.values : {}, acked: [...c.acked],
          });
          return '';
        } catch (e) {
          return errText(e);
        }
      }),
      scriptStep(() => result, 'Run it on ' + (form.label.trim() || 'the router') + '.'),
    ],
  });
}

function deviceFormStep(form: V.DeviceForm, mode: () => string, sites: () => V.SiteOpt[],
  facts?: ZTPDeviceView): WizardStep {
  return {
    title: t('Device'),
    render: () => V.deviceStep(form, mode(), sites(), facts),
    check: () => V.deviceProblem(form, mode()),
    bind(body, _redraw, recheck) {
      const on = (i: HTMLInputElement | HTMLSelectElement | null, fn: (v: string) => void): void => {
        i?.addEventListener('input', () => { fn(i.value); recheck(); });
        i?.addEventListener('change', () => { fn(i.value); recheck(); });
      };
      on(body.querySelector<HTMLInputElement>('#ztpLabel'), (v) => { form.label = v; });
      on(body.querySelector<HTMLInputElement>('#ztpSerial'), (v) => { form.serial = v; });
      on(body.querySelector<HTMLInputElement>('#ztpLanUrl'), (v) => { form.lanUrl = v; });
      on(body.querySelector<HTMLSelectElement>('#ztpDays'), (v) => { form.days = Number(v) || 7; });
      body.querySelectorAll<HTMLInputElement>('[data-ztp-site]').forEach((i) => i.addEventListener('change', () => {
        const id = i.dataset.ztpSite ?? '';
        form.siteIds = i.checked ? [...form.siteIds.filter((s) => s !== id), id] : form.siteIds.filter((s) => s !== id);
      }));
      body.querySelector<HTMLInputElement>('#ztpLabel')?.focus();
    },
  };
}

// ── Onboard ─────────────────────────────────────────────────────────────────

function openOnboard(d: ZTPDeviceView): void {
  const form: V.DeviceForm = { label: d.identity || d.label || d.serial, serial: d.serial, siteIds: [], days: 7,
    lanUrl: '' };
  const c = newConfig();
  let sites: V.SiteOpt[] = [];
  void loadTemplates().then((t) => { c.templates = t; }, () => { /* None only */ });
  void loadSites().then((s) => { sites = s; });
  openWizard({
    title: t('Onboard a device'),
    steps: [
      deviceFormStep(form, () => 'generic', () => sites, d),
      configStep(c, () => form.label, t('Onboard'), async () => {
        try {
          await api('POST', '/api/ztp/devices/' + encodeURIComponent(d.id) + '/approve', {
            label: form.label.trim(), siteIds: form.siteIds, templateId: c.chosen,
            values: c.chosen ? c.values : {}, acked: [...c.acked],
          });
          return '';
        } catch (e) {
          return errText(e);
        }
      }),
      {
        title: t('Done'),
        noBack: true,
        render: () => ('<p class="ztp-lead">' + t('Onboarding') + ' <strong>') + esc(form.label) + '</strong>.</p>' +
          '<p class="ztp-help">MikroDash is logging in through its tunnel. It joins the Devices grid as soon as it ' +
          'answers' + (c.chosen ? t(', and its template is then previewed on it and applied') : '') + '. Its progress shows on its card in the meantime.</p>',
      },
    ],
  });
}

// ── the Devices page ────────────────────────────────────────────────────────

let message = '';

function renderSection(): void {
  const host = el('ztpSection');
  const p = ztpState();
  if (!host || !p) return;
  const html = V.sectionHtml(p, Date.now());
  host.innerHTML = (message ? '<div class="cfg-banner is-bad ztp-msg">' + esc(message) + '</div>' : '') + html;
  host.hidden = !html && !message;
  const add = el('ztpAddBtn');
  if (add) add.hidden = false;
}

async function act(action: string, d: ZTPDeviceView): Promise<void> {
  const name = d.label || d.identity || d.serial || 'this device';
  const base = '/api/ztp/devices/' + encodeURIComponent(d.id);
  message = '';
  try {
    switch (action) {
      case 'onboard':
        openOnboard(d);
        return;
      case 'reject':
        if (!confirm('Reject ' + name + '?\n\nIts tunnel is closed and it can no longer reach this MikroDash.')) return;
        await api('POST', base + '/reject');
        return;
      case 'retry':
        await api('POST', base + '/retry');
        return;
      case 'delete':
        if (!confirm('Remove ' + name + ' from provisioning?' + (d.state === 'awaiting'
          ? t('\n\nIts script stops working.') : ''))) return;
        await api('DELETE', base);
        return;
      case 'regenerate': {
        if (!confirm('Make a new script for ' + name + '?\n\nThe old one stops working.')) return;
        const r = await api<V.ScriptResult>('POST', base + '/regenerate', {});
        showScript('New script for ' + name, r, 'Run it on ' + name + '.');
        return;
      }
    }
  } catch (e) {
    message = errText(e);
    renderSection();
  }
}

// ── Settings → Provisioning ─────────────────────────────────────────────────

function renderSettings(p: ZTPPayload): void {
  const s = p.status;
  const status = el('ztpStatusLine');
  if (status) status.innerHTML = V.statusLine(s);
  const inst = el('ztpInstanceId');
  if (inst) inst.textContent = s.instanceId || t('Made when provisioning is first switched on');
  const key = el('ztpPublicKey');
  if (key) key.textContent = s.publicKey || '—';
  const body = el('ztpBatchBody');
  if (body) body.innerHTML = V.batchRows(p.batches, Date.now());
  const lan = el<HTMLInputElement>('s_ztpLanUrl');
  const suggest = V.lanSuggestion(location.origin, location.hostname);
  if (lan && suggest) lan.placeholder = suggest;
}

function mountSettings(): void {
  el('ztpBatchBtn')?.addEventListener('click', () => {
    const nameIn = el<HTMLInputElement>('ztpBatchName');
    const days = Number(el<HTMLSelectElement>('ztpBatchDays')?.value) || 7;
    const name = nameIn?.value.trim() ?? '';
    const msg = el('ztpBatchMsg');
    if (msg) msg.textContent = '';
    if (!name) { if (msg) msg.textContent = t('Name the script first, for example the rollout it is for.'); return; }
    void api<V.ScriptResult>('POST', '/api/ztp/batches', { name, days }).then((r) => {
      if (nameIn) nameIn.value = '';
      showScript('Generic script: ' + name, r, 'Run it on any number of routers; each waits on the Devices page ' +
        'to be onboarded.');
    }, (e) => { if (msg) msg.textContent = errText(e); });
  });
  el('ztpBatchBody')?.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement).closest?.('[data-ztp-revoke]') as HTMLElement | null;
    if (!b) return;
    if (!confirm(t('Revoke this generic script?\n\nRouters that have not called home yet can no longer use it. Devices it already brought in are not affected.'))) return;
    void api('POST', '/api/ztp/batches/' + encodeURIComponent(b.dataset.ztpRevoke ?? '') + '/revoke')
      .catch((err) => { const m = el('ztpBatchMsg'); if (m) m.textContent = errText(err); });
  });
}

// ── the Dashboard's notice ──────────────────────────────────────────────────

function renderNotice(p: ZTPPayload): void {
  const n = el('ztpNotice');
  if (!n) return;
  const pending = p.devices.filter((d) => d.state === 'pending').length;
  n.hidden = !pending;
  const text = el('ztpNoticeText');
  if (text) text.textContent = pending + (pending === 1 ? ' device has' : ' devices have') +
    ' called home and ' + (pending === 1 ? 'is' : 'are') + ' waiting to be onboarded.';
}

export function mountZtp(socket: Socket): void {
  socket.on('ztp:state', (p) => setZtpState(p));
  socket.on('connect', () => socket.emit('ztp:watch', {}));
  if (socket.isOpen()) socket.emit('ztp:watch', {});

  onZtpState((p) => {
    renderSection();
    renderSettings(p);
    renderNotice(p);
  });
  // Relative times ("expires in 3 days") move while nothing else does.
  setInterval(renderSection, 60e3);

  el('ztpAddBtn')?.addEventListener('click', openAddDevice);
  el('ztpSection')?.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement).closest?.('[data-ztp-act]') as HTMLElement | null;
    const d = b && ztpState()?.devices.find((x) => x.id === b.dataset.ztpId);
    if (b && d) void act(b.dataset.ztpAct ?? '', d);
  });
  el('ztpNoticeBtn')?.addEventListener('click', () => {
    (document.querySelector('.nav-item[data-page="devices"]') as HTMLElement | null)?.click();
  });
  mountSettings();
}
