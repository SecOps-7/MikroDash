// A step-by-step dialog: a rail of numbered steps, one step's body, and Back
// and Next. Zero-touch provisioning's Add device and Onboard are its first
// users; nothing in it knows about them.
//
// ── A STEP SAYS WHY IT CANNOT GO ON ─────────────────────────────────────────
//
// `check()` returns '' when the step is complete, or the reason it is not, and
// the reason is shown beside a disabled Next. A greyed button with no reason is
// the commonest way a wizard strands someone.
//
// ── AN ACTION BELONGS TO THE STEP THAT TRIGGERS IT ──────────────────────────
//
// `leave()` runs when Next is pressed and may be async: creating the device,
// say. It returns '' to go on or a message to stay and show. Everything before
// it can be walked back and forth freely; nothing is sent until then.
//
// ── ESCAPE, THE BACKDROP AND × ALL CLOSE IT ─────────────────────────────────
//
// Handled here rather than through `CLOSABLE_MODALS`, which is a frozen list
// generated from the app this one replaced and cannot be extended.

import { esc } from './dom';
import { t } from './i18n';

export interface WizardStep {
  /** Its name on the rail. */
  title: string;
  /** The body's markup. Called on every redraw. */
  render(): string;
  /** Wire the body's controls. `redraw` re-renders this step; `recheck` only
   *  re-evaluates Next, which keeps focus in a text box being typed into. */
  bind?(body: HTMLElement, redraw: () => void, recheck: () => void): void;
  /** '' when this step may be left forward, or why not. */
  check?(): string;
  /** Run on Next: '' to go on, or a message to stay and show. */
  leave?(): Promise<string>;
  /** The Next button's label here. Defaults to "Next", and "Done" on the last. */
  next?: string;
  /** No Back from here: what came before has been acted on. */
  noBack?: boolean;
}

export interface WizardSpec {
  title: string;
  steps: WizardStep[];
  /** Called once, however the dialog was closed. */
  onClose?(): void;
}

/** The rail: done, current and still to come. Exported for the tests. */
export function railHtml(steps: WizardStep[], at: number): string {
  return '<ol class="wiz-rail" aria-label="Steps">' + steps.map((s, i) =>
    '<li class="wiz-step' + (i < at ? ' is-done' : i === at ? ' is-current' : '') + '"' +
    (i === at ? ' aria-current="step"' : '') + '><span class="wiz-dot">' + (i < at ? '&#10003;' : String(i + 1)) +
    '</span><span class="wiz-step-name">' + esc(s.title) + '</span></li>').join('') + '</ol>' +
    '<div class="wiz-rail-small">' + t('Step {n} of {total}: {title}', { n: at + 1, total: steps.length, title: esc(steps[at]?.title ?? '') }) + '</div>';
}

/** Open a wizard. Returns a function that closes it. */
export function openWizard(spec: WizardSpec): () => void {
  const bg = document.createElement('div');
  bg.className = 'rtr-modal-bg open wiz-bg';
  bg.innerHTML = '<div class="rtr-modal wiz" role="dialog" aria-modal="true" aria-label="' + esc(spec.title) + '">' +
    '<div class="rtr-modal-hdr"><span class="rtr-modal-title">' + esc(spec.title) + '</span>' +
    '<button class="rtr-modal-close" type="button" data-wiz-close aria-label="Close">&#10005;</button></div>' +
    '<div class="wiz-rail-wrap"></div><div class="rtr-modal-body wiz-body"></div>' +
    ('<div class="wiz-foot"><button class="sbtn sbtn-ghost" type="button" data-wiz-back>' + t('Back') + '</button>') +
    '<span class="wiz-why" aria-live="polite"></span>' +
    ('<button class="sbtn sbtn-primary" type="button" data-wiz-next>' + t('Next') + '</button></div></div>');
  document.body.appendChild(bg);

  const q = <T extends HTMLElement>(sel: string): T => bg.querySelector(sel) as T;
  const rail = q('.wiz-rail-wrap'), body = q('.wiz-body'), why = q('.wiz-why');
  const back = q<HTMLButtonElement>('[data-wiz-back]'), next = q<HTMLButtonElement>('[data-wiz-next]');
  let at = 0, busy = false, closed = false, error = '';

  const step = (): WizardStep => spec.steps[at] as WizardStep;
  const recheck = (): void => {
    const reason = busy ? t('Working…') : error || (step().check?.() ?? '');
    why.textContent = reason;
    why.classList.toggle('is-bad', !!error);
    next.disabled = busy || (!!reason && !error);
    back.hidden = at === 0 || !!step().noBack;
    next.textContent = step().next ?? (at === spec.steps.length - 1 ? t('Done') : t('Next'));
  };
  const redraw = (): void => {
    rail.innerHTML = railHtml(spec.steps, at);
    body.innerHTML = step().render();
    step().bind?.(body, redraw, () => { error = ''; recheck(); });
    recheck();
  };
  const close = (): void => {
    if (closed) return;
    closed = true;
    document.removeEventListener('keydown', onKey);
    bg.remove();
    spec.onClose?.();
  };
  const onKey = (e: KeyboardEvent): void => { if (e.key === 'Escape' && !busy) close(); };
  document.addEventListener('keydown', onKey);

  bg.addEventListener('click', (e) => {
    const t = e.target as HTMLElement;
    if (!busy && (t === bg || t.closest?.('[data-wiz-close]'))) close();
  });
  back.addEventListener('click', () => {
    if (busy || at === 0) return;
    at--;
    error = '';
    redraw();
  });
  next.addEventListener('click', () => {
    if (busy) return;
    const s = step();
    if (!error && s.check?.()) return;
    error = '';
    const advance = (): void => {
      if (at === spec.steps.length - 1) { close(); return; }
      at++;
      redraw();
    };
    if (!s.leave) { advance(); return; }
    busy = true;
    recheck();
    void s.leave().then((msg) => {
      busy = false;
      if (msg) { error = msg; recheck(); return; }
      advance();
    }, (e: unknown) => {
      busy = false;
      error = t('The request failed: {error}', { error: String(e) });
      recheck();
    });
  });

  redraw();
  return close;
}
