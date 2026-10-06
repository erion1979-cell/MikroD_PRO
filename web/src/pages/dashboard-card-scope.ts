// Where a Dashboard card finds its elements, so one card's code can draw more
// than one copy of it (docs/dashboards/PLAN.md, phase 2).
//
// ── ONE CARD, TWO KINDS OF COPY ─────────────────────────────────────────────
//
// The Dashboard's own copy of a card is the markup in page-dashboard.html, and
// its elements are found by id across the page, as they always were. A copy on
// a named dashboard follows a device of its own: it is that markup cloned, and
// every id inside the clone is moved to `data-cid`, so the page still has one
// element per id and the original card's lookups cannot land in a copy.
//
// `chrome` is whether the copy may touch things outside its own box: the
// top-bar uptime chip, the update notice. Only the Dashboard's own copy does;
// a copy for another router must never claim to describe the selected one.

import { el } from '../dom';

export interface CardScope {
  /** The card's element that the markup gives this id. */
  q<T extends HTMLElement = HTMLElement>(id: string): T | null;
  /** Give an element the card creates an id it can be found by again. */
  mark(node: HTMLElement, id: string): void;
  /** Whether this copy may change the page outside its own box. */
  chrome: boolean;
}

/** The Dashboard's own copy: ids across the whole page. */
export const pageScope: CardScope = {
  q: <T extends HTMLElement>(id: string) => el<T>(id),
  mark: (node, id) => { node.id = id; },
  chrome: true,
};

/** A copy's scope: only inside `root`, by `data-cid`. */
export function cloneScope(root: HTMLElement): CardScope {
  return {
    q: <T extends HTMLElement>(id: string) => root.querySelector<T>('[data-cid="' + id + '"]'),
    mark: (node, id) => { node.setAttribute('data-cid', id); },
    chrome: false,
  };
}

/**
 * A copy of a card's markup with the element id `uid`: every id inside moves
 * to `data-cid`, and a `for` that pointed at one of them is dropped rather
 * than left naming the original.
 */
export function cloneCard(template: HTMLElement, uid: string): HTMLElement {
  const copy = template.cloneNode(true) as HTMLElement;
  copy.id = uid;
  copy.style.display = '';
  copy.querySelectorAll<HTMLElement>('[id]').forEach((n) => {
    n.setAttribute('data-cid', n.id);
    n.removeAttribute('id');
  });
  copy.querySelectorAll('label[for]').forEach((n) => n.removeAttribute('for'));
  return copy;
}
