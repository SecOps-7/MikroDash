/**
 * Settings → the four notification Test buttons.
 *
 * ── THEY SEND, AND THAT SHAPES EVERY DECISION HERE ─────────────────────────
 *
 * A press delivers one real message to the operator's real Telegram, mailbox,
 * Pushbullet or ntfy topic. So the button is disabled for the duration of the
 * request: a double-click is two messages, and unlike a duplicated render that
 * cannot be undone.
 *
 * ── THE TYPED CREDENTIALS GO WITH IT ───────────────────────────────────────
 *
 * The live comment: "Include any credentials the user has currently typed so
 * Test works without requiring a Save first." Which fields are collected depends
 * on the channel, and the collection is NOT uniform — `smtpSecure` is sent from
 * a checkbox whether or not it is ticked, while every text field is sent only
 * when non-empty. That difference is load-bearing on the server, where an absent
 * field falls back to what is stored and a present one overrides even when
 * false. See `notify.MergeForAdminTest`.
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
  channel: string;
  /** Where to post. Defaults to the notification route. */
  url?: string;
  /** What to post. Defaults to `testPayload(channel)`. */
  payload?: () => Record<string, unknown>;
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
  { btnId: 'btn-test-telegram', resultId: 'test-telegram-result', channel: 'telegram' },
  { btnId: 'btn-test-pushbullet', resultId: 'test-pushbullet-result', channel: 'pushbullet' },
  { btnId: 'btn-test-smtp', resultId: 'test-smtp-result', channel: 'smtp' },
  { btnId: 'btn-test-ntfy', resultId: 'test-ntfy-result', channel: 'ntfy' },
  {
    btnId: 'btn-test-ai', resultId: 'test-ai-result', channel: 'ai',
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
 * What to send for a channel, read off the form as it stands.
 *
 * THREE DIFFERENT RULES, and they are the live ones:
 *
 *   plain text fields   sent only when non-empty (`if (x && x.value)`)
 *   trimmed fields      host, from, to and the ntfy url are `.trim()`ed —
 *                       the tokens and passwords are NOT, because leading or
 *                       trailing space can be part of a secret
 *   smtpSecure          sent whenever the checkbox EXISTS, ticked or not
 *   smtpPort            parsed to a number, and only when non-empty
 */
export function testPayload(channel: string): Record<string, unknown> {
  const p: Record<string, unknown> = { channel };
  if (channel === 'telegram') {
    if (val('s_telegramBotToken')) p.botToken = val('s_telegramBotToken');
    if (val('s_telegramChatId')) p.chatId = val('s_telegramChatId');
  } else if (channel === 'pushbullet') {
    if (val('s_pushbulletApiKey')) p.apiKey = val('s_pushbulletApiKey');
  } else if (channel === 'smtp') {
    if (val('s_smtpHost')) p.smtpHost = val('s_smtpHost').trim();
    if (val('s_smtpPort')) p.smtpPort = parseInt(val('s_smtpPort'), 10);
    // NO `if (value)` GUARD. The live code checks the ELEMENT exists and then
    // sends `.checked` — so an unticked box sends `false`, which the server
    // treats as an explicit "no TLS" rather than falling back to the stored
    // value. Guarding on truthiness here would make it impossible to test with
    // TLS off once it had been saved on.
    const secure = el<HTMLInputElement>('s_smtpSecure');
    if (secure) p.smtpSecure = secure.checked;
    if (val('s_smtpUser')) p.smtpUser = val('s_smtpUser');
    if (val('s_smtpPass')) p.smtpPass = val('s_smtpPass');
    if (val('s_smtpFrom')) p.smtpFrom = val('s_smtpFrom').trim();
    if (val('s_smtpTo')) p.smtpTo = val('s_smtpTo').trim();
  } else if (channel === 'ntfy') {
    if (val('s_ntfyUrl')) p.ntfyUrl = val('s_ntfyUrl').trim();
    if (val('s_ntfyToken')) p.ntfyToken = val('s_ntfyToken');
  }
  return p;
}

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
    void fetch(spec.url || '/api/settings/test-notification', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify(spec.payload ? spec.payload() : testPayload(spec.channel)),
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
