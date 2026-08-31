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
}

/** The four buttons, in the order the live app registers them. */
export const TEST_CHANNELS: TestChannelSpec[] = [
  { btnId: 'btn-test-telegram', resultId: 'test-telegram-result', channel: 'telegram' },
  { btnId: 'btn-test-pushbullet', resultId: 'test-pushbullet-result', channel: 'pushbullet' },
  { btnId: 'btn-test-smtp', resultId: 'test-smtp-result', channel: 'smtp' },
  { btnId: 'btn-test-ntfy', resultId: 'test-ntfy-result', channel: 'ntfy' },
];

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
 * NICER and is a different app: `tools/notif-test-check.js` drives a null reply
 * through both and compares, and the guard was what it caught.
 *
 * Recorded rather than silently matched, because the temptation to re-add the
 * guard on sight is obvious. If the live app ever guards it, the gate fails and
 * this note goes with it.
 */
export function resultText(d: { ok?: boolean; error?: string }): string {
  return d.ok ? '✓ Sent!' : '✗ ' + (d.error || 'failed');
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
    void fetch('/api/settings/test-notification', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify(testPayload(spec.channel)),
    })
      .then((r) => r.json())
      .then((d) => {
        btn.disabled = false;
        if (!result) return;
        // `resultText` FIRST, so a null body throws before anything is written
        // — exactly as the live code does, where the throw happens on `data.ok`
        // in the same expression. Reading `d.ok` for the colour first would
        // write a colour and then throw, leaving a coloured empty line.
        const text = resultText(d);
        result.textContent = text;
        result.style.color = resultColour(!!d.ok);
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

export function initNotifTestButtons(): void {
  for (const spec of TEST_CHANNELS) wire(spec);
}
