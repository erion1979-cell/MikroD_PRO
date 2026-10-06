// MikroDash's own dialogs: confirm, ask for a line of text, or tell. They
// replace the browser's window.confirm / prompt / alert everywhere in the app,
// which look like the browser speaking ("192.168.20.26:3081 says") rather than
// MikroDash, and cannot be styled.
//
// ── BUILT IN SCRIPT, ON THE APP'S MODAL STYLES ──────────────────────────────
//
// One dialog element, made on first use and reused, with the same classes as
// every page's dialogs (rtr-modal-bg, rtr-modal, the footer buttons), so it
// looks like the rest of the app in both themes. It answers with a promise:
// true or false, the text or null. Enter confirms, Escape and the backdrop
// cancel, and it stacks above other dialogs, so it can be asked from inside one.
// A message keeps its line breaks.
//
// ── NOT BLOCKING, SO EVERY CALLER AWAITS ────────────────────────────────────
//
// The browser's dialogs stopped the page until answered; these do not. A caller
// awaits the answer, and must act on what it captured BEFORE asking, since the
// page may have changed while the dialog was open.
//
// ── TESTS ANSWER THROUGH ONE SEAM ───────────────────────────────────────────
//
// The web tests drive pages on a fake DOM that cannot draw a dialog. They set
// `globalThis.mikrodashTestDialogs` to answer in its place, as they used to
// stub window.confirm; nothing in the app sets it.

import { esc } from './dom';

/** How the tests answer (see above). */
interface TestDialogs {
  confirm?: (message: string) => boolean;
  prompt?: (message: string, value: string) => string | null;
  alert?: (message: string) => void;
}
const testDialogs = (): TestDialogs | undefined =>
  (globalThis as { mikrodashTestDialogs?: TestDialogs }).mikrodashTestDialogs;

interface Shown { body: HTMLElement; ok: HTMLButtonElement; cancel: HTMLButtonElement }

let wrap: HTMLElement | null = null;

function show(title: string, okLabel: string, cancelLabel: string | null, danger: boolean): Shown {
  if (!wrap) {
    wrap = document.createElement('div');
    wrap.className = 'rtr-modal-bg app-dialog-bg';
    wrap.setAttribute('role', 'dialog');
    wrap.setAttribute('aria-modal', 'true');
    document.body.appendChild(wrap);
  }
  wrap.innerHTML =
    '<div class="rtr-modal app-dialog"><div class="rtr-modal-hdr"><span class="rtr-modal-title"></span></div>' +
    '<div class="rtr-modal-body"></div><div class="rtr-modal-footer">' +
    (cancelLabel === null ? '' : '<button class="sbtn sbtn-outline" type="button" data-act="cancel">' + esc(cancelLabel) + '</button>') +
    '<button class="sbtn ' + (danger ? 'sbtn-danger' : 'sbtn-primary') + '" type="button" data-act="ok">' + esc(okLabel) +
    '</button></div></div>';
  wrap.querySelector('.rtr-modal-title')!.textContent = title;
  wrap.classList.add('open');
  const ok = wrap.querySelector<HTMLButtonElement>('[data-act="ok"]')!;
  return {
    body: wrap.querySelector<HTMLElement>('.rtr-modal-body')!,
    ok,
    // With no Cancel button, closing any other way is the same as OK.
    cancel: wrap.querySelector<HTMLButtonElement>('[data-act="cancel"]') || ok,
  };
}

/** Wait for OK or a cancel (button, Escape, backdrop); `answer` is read on OK. */
function settle<T>(s: Shown, answer: () => T, none: T, focus: HTMLElement): Promise<T> {
  return new Promise((resolve) => {
    const w = wrap!;
    const done = (v: T): void => {
      w.classList.remove('open');
      w.removeEventListener('keydown', onKey, true);
      w.removeEventListener('click', onBackdrop);
      w.innerHTML = '';
      resolve(v);
    };
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); done(none); }
      if (e.key === 'Enter' && !(e.target instanceof HTMLButtonElement)) { e.preventDefault(); done(answer()); }
    };
    const onBackdrop = (e: MouseEvent): void => { if (e.target === w) done(none); };
    s.ok.addEventListener('click', () => done(answer()));
    if (s.cancel !== s.ok) s.cancel.addEventListener('click', () => done(none));
    w.addEventListener('keydown', onKey, true);
    w.addEventListener('click', onBackdrop);
    focus.focus();
  });
}

function message(body: HTMLElement, text: string): void {
  body.innerHTML = '<p class="app-dialog-msg"></p>';
  body.querySelector('p')!.textContent = text;
}

/**
 * Ask to confirm: true for OK. `danger` paints OK red, for something that
 * deletes or cannot be undone.
 */
export function askConfirm(
  text: string, o: { title?: string; okLabel?: string; danger?: boolean } = {},
): Promise<boolean> {
  const t = testDialogs();
  if (t) return Promise.resolve(t.confirm ? t.confirm(text) : true);
  const s = show(o.title || 'Please confirm', o.okLabel || 'OK', 'Cancel', !!o.danger);
  message(s.body, text);
  return settle(s, () => true, false, s.cancel);
}

/**
 * Ask for one line of text: the text, or null when cancelled. `label` is the
 * question, which may run to several lines.
 */
export function askText(
  label: string, o: { title?: string; value?: string; okLabel?: string; maxLength?: number; danger?: boolean } = {},
): Promise<string | null> {
  const t = testDialogs();
  if (t) return Promise.resolve(t.prompt ? t.prompt(label, o.value || '') : null);
  const s = show(o.title || 'MikroDash', o.okLabel || 'OK', 'Cancel', !!o.danger);
  s.body.innerHTML = '<label class="app-dialog-msg app-dialog-q"></label><input class="sform-input" type="text" autocomplete="off">';
  s.body.querySelector('label')!.textContent = label;
  const input = s.body.querySelector('input')!;
  input.value = o.value || '';
  if (o.maxLength) input.maxLength = o.maxLength;
  const p = settle<string | null>(s, () => input.value, null, input);
  input.select();
  return p;
}

/** Say something, with one OK button. */
export function tell(text: string, o: { title?: string } = {}): Promise<void> {
  const t = testDialogs();
  if (t) { t.alert?.(text); return Promise.resolve(); }
  const s = show(o.title || 'MikroDash', 'OK', null, false);
  message(s.body, text);
  return settle(s, () => undefined, undefined, s.ok);
}
