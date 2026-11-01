import { useSyncExternalStore } from 'react'

/** The three forms of the session dock (D6): a side band, a floating card, a bottom sheet. */
export type DockForm = 'side' | 'card' | 'sheet'

/** D7: from 1700 px the band (the mockup's 1320 + the band's 380); under 600 px the sheet; a media query takes no var(). */
export const DOCK_SIDE_MIN = 1700
export const DOCK_CARD_MIN = 600

export function dockFormOf(width: number): DockForm {
  if (width >= DOCK_SIDE_MIN) return 'side'
  if (width >= DOCK_CARD_MIN) return 'card'
  return 'sheet'
}

function subscribe(l: () => void) {
  window.addEventListener('resize', l)
  return () => window.removeEventListener('resize', l)
}

/** The dock's form for the window's width, following a resize. */
export function useDockForm(): DockForm {
  return useSyncExternalStore(subscribe, () => dockFormOf(window.innerWidth), () => 'card')
}

export interface Pos {
  x: number
  y: number
}

/** The gap the floating card keeps from its box edges (= --sp-6, the card's resting offset). */
export const CARD_MARGIN = 16

/**
 * The card's top-left corner kept inside its box (the reader under its bar), `margin` away from every edge; a card
 * taller or wider than the box minus its margins sticks to the top-left margin, its header reachable.
 */
export function clampCard(p: Pos, card: { w: number; h: number }, box: { w: number; h: number }, margin = 0): Pos {
  const x = Math.max(margin, Math.min(p.x, box.w - card.w - margin))
  const y = Math.max(margin, Math.min(p.y, box.h - card.h - margin))
  return { x, y }
}
