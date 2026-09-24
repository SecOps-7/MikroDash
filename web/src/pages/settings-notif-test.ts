/**
 * Settings → the Test buttons.
 *
 * ── THEY REACH OUT, AND THAT SHAPES EVERY DECISION HERE ────────────────────
 *
 * A press makes one real request to the operator's real endpoint. So the button
 * is disabled for the duration: a double-click is two requests, and unlike a
 * duplicated render that cannot be undone.
 *
 * ── THE TYPED CREDENTIALS GO WITH IT ───────────────────────────────────────
 *
 * The live comment: "Include any credentials the user has currently typed so
 * Test works without requiring a Save first." A text field goes only when
 * non-empty, so an untouched box falls back to what is stored; the checkbox goes
 * whether or not it is ticked. That difference is load-bearing on the server,
 * where an absent field falls back to what is stored and a present one overrides
 * even when false. See `notify.MergeForAdminTest`, which the notification
 * channels' own test endpoint still goes through.
 *
 * ── THE RESULT LINE CLEARS ITSELF ONLY ON A REPLY ──────────────────────────
 *
 * Success and refusal both fade after five seconds; a request that failed
 * outright does NOT, because there was no answer to have read.
 */

import { el } from '../dom';

export interface TestChannelSpec {
  btnId: string;
  resultId: string;
  /**
   * Where to post, and what to post. BOTH REQUIRED, where they used to default
   * to the notification route and `testPayload(channel)`. Those defaults served
   * the four transport rows, and the transports are channels now: a default
   * with no caller is a second way to do one job, kept alive by nothing.
   */
  url: string;
  payload: () => Record<string, unknown>;
  /** The word for a success. A notification is SENT; a connection is not. */
  okText?: string;
  /** Called with every reply, after the result line is written. */
  onReply?: (d: TestReply) => void;
}

/** A test's reply. `certificate` is the AI test's refused certificate. */
export interface TestReply {
  ok?: boolean;
  error?: string;
  certificate?: CertInfo;
}

/** A certificate an endpoint presented and was refused for (see ai_test_api.go). */
export interface CertInfo {
  fingerprint: string;
  subject: string;
  issuer: string;
  names: string[];
  notAfter: string;
  selfSigned: boolean;
  /** A pin was set and this is not it. */
  mismatch: boolean;
}

/**
 * Every Test button on the Settings page.
 *
 * ── ONE BINDER, NOT TWO ────────────────────────────────────────────────────
 *
 * The AI Agent tab's button posts to a different route with a different body, so
 * it would have been easy to give it a module of its own. That would also have
 * given it a second mount point in `main.ts` to forget — and a Test button bound
 * by a module nothing mounts is precisely the shape
 * `TestInteractiveControlsAreBoundBeyondCaps` exists to catch, having shipped
 * twice already as `rtrAddBtn` and `settingsSaveBtn`.
 *
 * Extending the table instead means the AI button inherits the existing mount,
 * the disable-on-press rule, and the result line's clearing behaviour, none of
 * which is worth a second copy.
 */
export const TEST_CHANNELS: TestChannelSpec[] = [
  // ── ONLY THE AI ENDPOINT REMAINS ──────────────────────────────────────
  //
  // Telegram, Pushbullet, ntfy and the mail server all left this table when they
  // became notification channels: a channel is tested through
  // `/api/notify-channels/{id}/test`, against what is STORED, from its own card.
  // Their buttons are gone from the markup too, so leaving the rows here would
  // only mean `el()` lookups that never find anything — which is exactly what
  // `TestEveryLookupHasAProducer` caught when the mail server card went.
  //
  // The table is kept for the one row rather than inlined, because what it
  // carries is the MOUNT: `initNotifTestButtons` is already called from
  // `main.ts`, and a Test button bound by a module nothing mounts is the shape
  // `TestInteractiveControlsAreBoundBeyondCaps` exists to catch.
  {
    btnId: 'btn-test-ai', resultId: 'test-ai-result',
    url: '/api/settings/test-ai', payload: aiTestPayload,
    // NOT "Sent!". Nothing was delivered to anybody: the endpoint answered, the
    // key was accepted and the model name exists, which is a different claim.
    okText: '✓ Connected',
    onReply: showCertificate,
  },
];

/**
 * What the AI Agent tab's Test button sends.
 *
 * ── THE SAME TWO GUARDS AS THE CHANNELS ABOVE, FOR THE SAME REASON ─────────
 *
 * Text fields go only when non-empty, so an untouched box falls back to what is
 * stored and Test works before a Save. The checkbox goes whether or not it is
 * empty, because an empty `aiTlsPin` is a value an operator sets deliberately —
 * guarding it on truthiness would make it impossible to test with the pin
 * cleared without saving first. `aiConfigFor` on the server is the other
 * half of this pair.
 *
 * THE MASKED KEY IS SENT AS IT STANDS. The server's `store.IsMasked` drops it
 * and falls back to the stored key, which is the same round trip `smtpUser`
 * makes; blanking it here would instead read as "no key configured".
 */
export function aiTestPayload(): Record<string, unknown> {
  const p: Record<string, unknown> = {};
  if (val('s_aiBaseUrl')) p.aiBaseUrl = val('s_aiBaseUrl').trim();
  if (val('s_aiModel')) p.aiModel = val('s_aiModel').trim();
  if (val('s_aiApiKey')) p.aiApiKey = val('s_aiApiKey');
  if (val('s_aiHeaders')) p.aiHeaders = val('s_aiHeaders');
  if (val('s_aiTimeoutMs')) p.aiTimeoutMs = parseInt(val('s_aiTimeoutMs'), 10);
  const pin = el<HTMLInputElement>('s_aiTlsPin');
  if (pin) p.aiTlsPin = pin.value.trim();
  return p;
}

const val = (id: string): string => el<HTMLInputElement>(id)?.value ?? '';

/**
 * The line under the button, after a reply.
 *
 * ── NOT NULL-GUARDED, AND THAT IS DELIBERATE ───────────────────────────────
 *
 * The live code is `data.ok ? … : …` with no guard, so a reply body of literal
 * `null` throws a TypeError and lands in the request's `.catch` — which prints
 * the TypeError as the result line. A guarded version showing "✗ failed" is
 * NICER and is a different app: the notif-test check drives a null reply
 * through both and compares, and the guard was what it caught.
 *
 * Recorded rather than silently matched, because the temptation to re-add the
 * guard on sight is obvious. If the live app ever guards it, the gate fails and
 * this note goes with it.
 */
export function resultText(d: { ok?: boolean; error?: string }, okText?: string): string {
  return d.ok ? (okText || '✓ Sent!') : '✗ ' + (d.error || 'failed');
}

export function resultColour(ok: boolean): string {
  return ok ? 'var(--accent-green, #4ade80)' : 'var(--accent-red, #f87171)';
}

function wire(spec: TestChannelSpec): void {
  const btn = el<HTMLButtonElement>(spec.btnId);
  const result = el(spec.resultId);
  if (!btn) return;

  btn.addEventListener('click', () => {
    // DISABLED FIRST, before anything can throw. A press that failed to build
    // its payload would otherwise leave the button live and the operator with no
    // sign the press did anything.
    btn.disabled = true;
    if (result) {
      result.textContent = 'Sending…';
      result.style.color = 'var(--text-muted)';
    }
    void fetch(spec.url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify(spec.payload()),
    })
      .then((r) => r.json())
      .then((d) => {
        btn.disabled = false;
        if (!result) return;
        // `resultText` FIRST, so a null body throws before anything is written
        // — exactly as the live code does, where the throw happens on `data.ok`
        // in the same expression. Reading `d.ok` for the colour first would
        // write a colour and then throw, leaving a coloured empty line.
        const text = resultText(d, spec.okText);
        result.textContent = text;
        result.style.color = resultColour(!!d.ok);
        spec.onReply?.(d);
        // FIVE SECONDS, on success AND on refusal. The live app clears both,
        // because the line is a transient acknowledgement rather than a record.
        setTimeout(() => { result.textContent = ''; }, 5000);
      })
      .catch((e) => {
        btn.disabled = false;
        if (!result) return;
        // NO TIMER on this path, matching the live code. A request that never
        // got an answer leaves its message up: there is nothing the operator can
        // have read and dismissed, and clearing it would look like it worked.
        result.textContent = '✗ ' + e;
        result.style.color = resultColour(false);
      });
  });
}

/** The fingerprint as a person compares it: colon-separated upper-case pairs. */
export function formatFingerprint(hex: string): string {
  return (hex.toUpperCase().match(/.{2}/g) || []).join(':');
}

/** What the certificate box says: a headline, then label and value rows. */
export function certSummary(c: CertInfo): { head: string; rows: [string, string][] } {
  const rows: [string, string][] = [['SHA-256', formatFingerprint(c.fingerprint)],
    ['Subject', c.subject || '(none)']];
  if (c.names.length) rows.push(['Names', c.names.join(', ')]);
  rows.push(['Issuer', c.issuer || '(none)'], ['Expires', c.notAfter]);
  return {
    head: c.mismatch
      ? 'The endpoint presented a DIFFERENT certificate from the one you trusted. Trust it only if you replaced it yourself.'
      : 'The endpoint\'s certificate is not trusted' + (c.selfSigned ? ' (it is self-signed).' : '.'),
    rows,
  };
}

/**
 * The AI test's refused certificate, with a Trust button.
 *
 * ── IT STAYS, UNLIKE THE RESULT LINE ───────────────────────────────────────
 *
 * The line under the button clears after five seconds; a fingerprint is read
 * and compared, which takes longer. Trust fills the pin field and nothing
 * else: only Save stores it. Every value is set as text.
 */
export function showCertificate(d: TestReply): void {
  const box = el('aiCertTrust');
  if (!box) return;
  const c = d.certificate;
  box.textContent = '';
  box.hidden = !c;
  if (!c) return;
  const summary = certSummary(c);
  const head = document.createElement('div');
  head.style.fontWeight = '600';
  head.textContent = summary.head;
  box.appendChild(head);
  for (const [label, value] of summary.rows) {
    const row = document.createElement('div');
    const b = document.createElement('strong');
    b.textContent = label + ' ';
    row.appendChild(b);
    row.appendChild(document.createTextNode(value));
    box.appendChild(row);
  }
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'sbtn sbtn-ghost';
  btn.style.marginTop = '.4rem';
  btn.textContent = 'Trust this certificate';
  btn.addEventListener('click', () => {
    const pin = el<HTMLInputElement>('s_aiTlsPin');
    if (pin) pin.value = formatFingerprint(c.fingerprint);
    box.textContent = 'Trusted in the form. Save the settings to keep it, then Test Connection again.';
  });
  box.appendChild(btn);
}

export function initNotifTestButtons(): void {
  for (const spec of TEST_CHANNELS) wire(spec);
}
