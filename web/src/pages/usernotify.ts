/**
 * The My Alerts tab — a user's own notification channels.
 *
 * The install-wide channels belong to an administrator; these belong to the
 * person signed in, and the tab does not exist unless the install has switched
 * the feature on. The server enforces that; this only draws it.
 */

import { el } from '../dom';

/** The three field groups, and they are treated differently on purpose. */
export const UN_BOOLS = ['telegramEnabled', 'pushbulletEnabled', 'ntfyEnabled', 'emailEnabled'];
export const UN_STRS = ['telegramChatId', 'ntfyUrl', 'emailTo'];

/**
 * The credential fields the FORM has.
 *
 * The live client's list also carries `smtpUser` and `smtpPass`, and both are
 * dead: no `un_smtpUser` or `un_smtpPass` element exists in the markup, so the
 * loop's `if (el)` guard skips them, and the server's allowlist would drop them
 * anyway. Left out here rather than copied — carrying them across would mean a
 * field that LOOKS wired, and if anyone later added the inputs the browser would
 * send values the server silently discards.
 */
export const UN_CREDS = ['telegramBotToken', 'pushbulletApiKey', 'ntfyToken'];

/**
 * Each field's element id, WRITTEN OUT rather than built as `'un_' + k`.
 *
 * `wiring-audit` scans this port's TypeScript for the ids the live app writes,
 * so an id that only ever exists as a concatenation reads as unwired — and, more
 * usefully, an id that stopped being written would read as wired for exactly as
 * long as the prefix survived. Being greppable is the point; the loops below
 * still do the work.
 */
export const UN_IDS: Record<string, string> = {
  telegramEnabled: 'un_telegramEnabled',
  pushbulletEnabled: 'un_pushbulletEnabled',
  ntfyEnabled: 'un_ntfyEnabled',
  emailEnabled: 'un_emailEnabled',
  telegramChatId: 'un_telegramChatId',
  ntfyUrl: 'un_ntfyUrl',
  emailTo: 'un_emailTo',
  telegramBotToken: 'un_telegramBotToken',
  pushbulletApiKey: 'un_pushbulletApiKey',
  ntfyToken: 'un_ntfyToken',
};

/**
 * What each credential input should show once the stored config arrives.
 *
 * A STORED CREDENTIAL GOES IN THE PLACEHOLDER, NEVER THE VALUE. Two things
 * depend on that and neither is obvious:
 *
 *   - the user would otherwise have to clear eight bullet characters before
 *     typing a new token;
 *   - an untouched form would post them back as a literal password. The server
 *     treats the mask as "unchanged", so it would survive — but only because of
 *     a second guard. Not putting it in the value is the first.
 *
 * Absence of the key, not an empty string, is what tells the server to keep what
 * it has.
 */
export function credentialPlaceholder(stored: unknown): string {
  return stored ? 'leave blank to keep current' : 'not set';
}

export interface UserNotifyConfig {
  [k: string]: unknown;
}

/** A form field, as much of one as this module needs. */
export interface Field {
  value?: string;
  checked?: boolean;
  placeholder?: string;
}

/** Fill the form from the server's masked config. */
export function populateWith(
  data: UserNotifyConfig,
  read: (id: string) => Field | null,
): void {
  for (const k of UN_BOOLS) {
    const node = read(UN_IDS[k]!);
    if (node) node.checked = !!data[k];
  }
  for (const k of UN_STRS) {
    const node = read(UN_IDS[k]!);
    if (node) node.value = (data[k] as string) || '';
  }
  for (const k of UN_CREDS) {
    const node = read(UN_IDS[k]!);
    if (!node) continue;
    node.value = '';
    node.placeholder = credentialPlaceholder(data[k]);
  }
}

/**
 * Read the form for a SAVE.
 *
 * Credentials are included ONLY when the user actually typed one. Key absence
 * means "keep what is stored", which is what lets an unrelated edit — ticking a
 * box, changing an address — leave a token untouched rather than re-sending and
 * re-encrypting it.
 */
export function collectForm(read: (id: string) => Field | null): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const k of UN_BOOLS) {
    const node = read(UN_IDS[k]!);
    if (node) out[k] = !!node.checked;
  }
  for (const k of UN_STRS) {
    const node = read(UN_IDS[k]!);
    if (node) out[k] = (node.value || '').trim();
  }
  for (const k of UN_CREDS) {
    const node = read(UN_IDS[k]!);
    if (node && node.value) out[k] = node.value;
  }
  return out;
}

/**
 * Read the form for a TEST.
 *
 * Different from a save in two ways, and both follow from what a test is for:
 * the toggles are not sent (the server force-enables the channel under test, so
 * testing before ticking the box reports the truth rather than "not
 * configured"), and only NON-EMPTY fields go — a blank field means "use what is
 * stored", where on a save it means "clear it".
 */
export function collectTestPayload(
  channel: string,
  read: (id: string) => Field | null,
): Record<string, unknown> {
  const out: Record<string, unknown> = { channel };
  for (const k of [...UN_STRS, ...UN_CREDS]) {
    const node = read(UN_IDS[k]!);
    if (node && node.value) out[k] = node.value;
  }
  return out;
}

/** The outcome line under a button. */
export interface Outcome {
  text: string;
  colour: string;
  clearAfterMs: number;
}

const GREEN = 'var(--accent-green, #4ade80)';
const RED = 'var(--accent-red, #f87171)';
const MUTED = 'var(--text-muted)';

export const pending = (word: string): Outcome => ({ text: word, colour: MUTED, clearAfterMs: 0 });

/**
 * The save result. Cleared after four seconds; the test's after five.
 *
 * The two differ in the live app and are kept apart rather than unified: a test
 * result is the thing the user is waiting for and deserves the longer read.
 */
export function saveOutcome(ok: boolean, error?: string): Outcome {
  return ok
    ? { text: '✓ Saved', colour: GREEN, clearAfterMs: 4000 }
    : { text: '✗ ' + (error || 'failed'), colour: RED, clearAfterMs: 4000 };
}

export function testOutcome(ok: boolean, error?: string): Outcome {
  return ok
    ? { text: '✓ Sent!', colour: GREEN, clearAfterMs: 5000 }
    : { text: '✗ ' + (error || 'failed'), colour: RED, clearAfterMs: 5000 };
}

/** The four test buttons, and which channel each drives. */
export const TEST_BUTTONS: Array<[string, string, string]> = [
  ['btn-un-test-telegram', 'un-test-telegram-result', 'telegram'],
  ['btn-un-test-pushbullet', 'un-test-pushbullet-result', 'pushbullet'],
  ['btn-un-test-email', 'un-test-email-result', 'email'],
  ['btn-un-test-ntfy', 'un-test-ntfy-result', 'ntfy'],
];

// ── wiring ──────────────────────────────────────────────────────────────────

const domRead = (id: string): Field | null => el(id) as HTMLInputElement | null;

export function populate(data: UserNotifyConfig): void {
  populateWith(data, domRead);
}

function show(node: HTMLElement | null, o: Outcome): void {
  if (!node) return;
  node.textContent = o.text;
  node.style.color = o.colour;
  if (o.clearAfterMs > 0) {
    setTimeout(() => { node.textContent = ''; }, o.clearAfterMs);
  }
}

/** Fetch the stored config and fill the form. */
export function loadUserNotify(): void {
  void fetch('/api/user-notify')
    .then((r) => (r.ok ? r.json() : null))
    .then((d) => { if (d) populate(d as UserNotifyConfig); })
    // The tab is hidden unless the feature is on, so a refusal here is the
    // normal state on most installs rather than something to report.
    .catch(() => { /* ignore */ });
}

export function initUserNotify(): void {
  const saveBtn = el('saveUserNotifyBtn') as HTMLButtonElement | null;
  const saveResult = el('userNotifySaveResult');

  if (saveBtn) {
    saveBtn.addEventListener('click', () => {
      saveBtn.disabled = true;
      show(saveResult, pending('Saving…'));
      void fetch('/api/user-notify', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(collectForm(domRead)),
      })
        .then((r) => r.json())
        .then((data: { ok?: boolean; error?: string; config?: UserNotifyConfig }) => {
          saveBtn.disabled = false;
          show(saveResult, saveOutcome(!!data.ok, data.error));
          // REPOPULATE on success: the server's answer is the truth about what
          // is stored, and it comes back masked — so a credential just typed is
          // replaced by its placeholder rather than left on screen.
          if (data.ok && data.config) populate(data.config);
        })
        .catch((e: unknown) => {
          saveBtn.disabled = false;
          show(saveResult, saveOutcome(false, String(e)));
        });
    });
  }

  for (const [btnId, resultId, channel] of TEST_BUTTONS) {
    const btn = el(btnId) as HTMLButtonElement | null;
    const result = el(resultId);
    if (!btn) continue;
    btn.addEventListener('click', () => {
      btn.disabled = true;
      show(result, pending('Sending…'));
      void fetch('/api/user-notify/test-notification', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(collectTestPayload(channel, domRead)),
      })
        .then((r) => r.json())
        .then((data: { ok?: boolean; error?: string }) => {
          btn.disabled = false;
          show(result, testOutcome(!!data.ok, data.error));
        })
        .catch((e: unknown) => {
          btn.disabled = false;
          show(result, testOutcome(false, String(e)));
        });
    });
  }
}
