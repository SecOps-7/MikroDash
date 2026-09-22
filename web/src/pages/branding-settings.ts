/**
 * Settings, Appearance, Branding: rename the install and give it an icon
 * (issue #131). See `../branding.ts` for where the result shows.
 *
 * ── NOT PART OF SAVE SETTINGS ───────────────────────────────────────────────
 *
 * Its controls have no `s_` ids, so the page's Save Settings button does not
 * collect them. Branding is stored beside settings.json rather than in it, and
 * this card saves through its own routes (internal/server/branding_api.go).
 *
 * The limits below give a friendly message before an upload. The server applies
 * the same ones and is the one that decides; `web/test/branding.test.ts` holds
 * the two sets of numbers together.
 */

import { el } from '../dom';
import { t } from '../i18n';
import { FONTS } from '../gen/appearance-tables.js';
import { applyBranding, normaliseBranding, renderWordmark, type Branding } from '../branding';

export const NAME_MAX = 40;
export const ICON_MIN_PX = 64;
export const ICON_MAX_PX = 512;
export const ICON_MAX_BYTES = 512 * 1024;

/** Why a chosen file cannot be an icon, or '' when its type and size are fine. */
/** The wordmark's own font, which its Default option stands for. */
export const WORDMARK_FONT = 'orbitron';

/**
 * The Name Font choices: every appearance font but the wordmark's own (that is
 * the Default option), under the Appearance card's labels. That card marks ITS
 * default with "(Default)", which is the interface font's, not the wordmark's,
 * so the mark is dropped here.
 */
export function wordmarkFontOptions(labels: Map<string, string>): { id: string; label: string }[] {
  return FONTS.filter((f) => f.id !== WORDMARK_FONT)
    .map((f) => ({ id: f.id, label: (labels.get(f.id) || f.id).replace(/ \(Default\)$/, '') }));
}

export function iconFileProblem(file: { size: number; type: string }): string {
  if (file.type !== 'image/png' && file.type !== 'image/jpeg') return t('The icon must be a PNG or JPEG image.');
  if (file.size > ICON_MAX_BYTES) return t('The icon is larger than 512 KB.');
  return '';
}

/** Why an image's dimensions cannot be an icon, or '' when they are fine. */
export function iconSizeProblem(width: number, height: number): string {
  if (width !== height) return 'The icon must be square; this one is ' + width + ' × ' + height + ' px.';
  if (width < ICON_MIN_PX || width > ICON_MAX_PX) {
    return 'The icon must be ' + ICON_MIN_PX + ' to ' + ICON_MAX_PX + ' px; this one is ' + width + ' px.';
  }
  return '';
}

function errorOf(j: unknown, fallback: string): string {
  if (j && typeof j === 'object') {
    const r = j as Record<string, unknown>;
    if (typeof r.error === 'string' && r.error) return r.error;
    if (typeof r.message === 'string' && r.message) return r.message;
  }
  return fallback;
}

export function initBrandingSettings(): void {
  const nameIn = el<HTMLInputElement>('brandName');
  const fontSel = el<HTMLSelectElement>('brandFont');
  const saveBtn = el<HTMLButtonElement>('brandSaveBtn');
  const fileIn = el<HTMLInputElement>('brandIconFile');
  const resetBtn = el<HTMLButtonElement>('brandIconReset');
  if (!nameIn || !fontSel || !saveBtn || !fileIn || !resetBtn) return;
  const preview = el('brandPreviewName');
  const previewIcon = el<HTMLImageElement>('brandPreviewIcon');
  const pickBtn = el<HTMLButtonElement>('brandIconPick');
  const status = el('brandStatus');

  // The fonts the Appearance card offers, under the labels it shows them with.
  // Orbitron is the wordmark's own font, which is the Default option.
  const labels = new Map<string, string>();
  el<HTMLSelectElement>('appearanceFont')?.querySelectorAll('option').forEach((o) => {
    labels.set(o.value, o.textContent || o.value);
  });
  for (const f of wordmarkFontOptions(labels)) {
    const opt = document.createElement('option');
    opt.value = f.id;
    opt.textContent = f.label;
    fontSel.appendChild(opt);
  }

  const say = (msg: string, kind: '' | 'ok' | 'error' = ''): void => {
    if (!status) return;
    status.textContent = msg;
    status.classList.toggle('is-ok', kind === 'ok');
    status.classList.toggle('is-error', kind === 'error');
  };
  const show = (b: Branding): void => {
    nameIn.value = b.name;
    fontSel.value = b.font;
    if (preview) renderWordmark(preview, b.name, b.font);
    if (previewIcon) previewIcon.src = b.icon;
    resetBtn.disabled = !b.customIcon;
  };
  const settle = (r: Response): Promise<Branding> =>
    r.json().catch(() => null).then((j: unknown) => {
      if (!r.ok) throw new Error(errorOf(j, t('The change could not be saved.')));
      const b = normaliseBranding(j);
      show(b);
      applyBranding(b);
      return b;
    });

  void fetch('/api/branding', { credentials: 'same-origin' })
    .then((r) => (r.ok ? r.json() : null))
    .then((j: unknown) => show(normaliseBranding(j)))
    .catch(() => { /* the card keeps the default it was drawn with */ });

  const refreshPreview = (): void => {
    if (preview) renderWordmark(preview, nameIn.value.trim(), fontSel.value);
  };
  nameIn.addEventListener('input', refreshPreview);
  fontSel.addEventListener('change', refreshPreview);

  saveBtn.addEventListener('click', () => {
    const name = nameIn.value.trim();
    if (Array.from(name).length > NAME_MAX) {
      say('The name is longer than ' + NAME_MAX + ' characters.', 'error');
      return;
    }
    saveBtn.disabled = true;
    void fetch('/api/branding', {
      method: 'PUT', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, font: fontSel.value }),
    })
      .then(settle)
      .then(() => say('Saved.', 'ok'))
      .catch((e: Error) => say(e.message, 'error'))
      .finally(() => { saveBtn.disabled = false; });
  });

  // The file input is hidden; this themed button opens it.
  pickBtn?.addEventListener('click', () => fileIn.click());

  fileIn.addEventListener('change', () => {
    const file = fileIn.files && fileIn.files[0];
    if (!file) return;
    const problem = iconFileProblem(file);
    if (problem) {
      say(problem, 'error');
      fileIn.value = '';
      return;
    }
    const url = URL.createObjectURL(file);
    const probe = new Image();
    probe.onload = () => {
      URL.revokeObjectURL(url);
      const sized = iconSizeProblem(probe.naturalWidth, probe.naturalHeight);
      if (sized) {
        say(sized, 'error');
        fileIn.value = '';
        return;
      }
      say(t('Uploading…'));
      void fetch('/api/branding/icon', {
        method: 'POST', credentials: 'same-origin',
        headers: { 'Content-Type': file.type }, body: file,
      })
        .then(settle)
        .then(() => say(t('Icon saved.'), 'ok'))
        .catch((e: Error) => say(e.message, 'error'))
        .finally(() => { fileIn.value = ''; });
    };
    probe.onerror = () => {
      URL.revokeObjectURL(url);
      say(t('The file could not be read as an image.'), 'error');
      fileIn.value = '';
    };
    probe.src = url;
  });

  resetBtn.addEventListener('click', () => {
    resetBtn.disabled = true;
    void fetch('/api/branding/icon', { method: 'DELETE', credentials: 'same-origin' })
      .then(settle)
      .then(() => say(t('Using the default icon.'), 'ok'))
      .catch((e: Error) => {
        say(e.message, 'error');
        resetBtn.disabled = false;
      });
  });
}
