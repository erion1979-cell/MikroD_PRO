// MikroDash's own small dialogs: ask for a line of text, ask to confirm, or say
// something. They replace the browser's window.prompt / confirm / alert, which
// look like the browser speaking ("192.168.20.26:3081 says") rather than the app.
//
// ── BUILT IN SCRIPT, ON THE APP'S MODAL STYLES ──────────────────────────────
//
// One dialog element, made on first use and reused, with the same classes as
// every page's dialogs (rtr-modal-bg, rtr-modal, the footer buttons), so it
// looks like the rest of the app in both themes. It answers with a promise:
// the text or null, true or false. Enter confirms, Escape and the backdrop
// cancel; it stacks above other dialogs, so it can be asked from inside one.

import { esc } from './dom';

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

/** Ask for one line of text: the text, or null when cancelled. */
export function askText(o: { title: string; label: string; value?: string; okLabel?: string; maxLength?: number }): Promise<string | null> {
  const s = show(o.title, o.okLabel || 'OK', 'Cancel', false);
  s.body.innerHTML = '<label class="sform-label"></label><input class="sform-input" type="text" autocomplete="off">';
  s.body.querySelector('label')!.textContent = o.label;
  const input = s.body.querySelector('input')!;
  input.value = o.value || '';
  if (o.maxLength) input.maxLength = o.maxLength;
  const p = settle<string | null>(s, () => input.value, null, input);
  input.select();
  return p;
}

/** Ask to confirm: true for OK. `danger` paints OK red, for a deletion. */
export function askConfirm(o: { title: string; message: string; okLabel?: string; danger?: boolean }): Promise<boolean> {
  const s = show(o.title, o.okLabel || 'OK', 'Cancel', !!o.danger);
  s.body.innerHTML = '<p class="app-dialog-msg"></p>';
  s.body.querySelector('p')!.textContent = o.message;
  return settle(s, () => true, false, s.cancel);
}

/** Say something, with one OK button. */
export function tell(o: { title: string; message: string }): Promise<void> {
  const s = show(o.title, 'OK', null, false);
  s.body.innerHTML = '<p class="app-dialog-msg"></p>';
  s.body.querySelector('p')!.textContent = o.message;
  return settle(s, () => undefined, undefined, s.ok);
}
