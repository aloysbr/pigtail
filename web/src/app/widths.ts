// Pure width arithmetic of the web front (D2, D5): the bounds of the sidebar and of the sheet. No React, no storage.

/** Where an open sheet goes (t10 pmode): beside the list, over it, or the whole page. */
export type PanelMode = 'side' | 'over' | 'page'

/** A px token of the stylesheet (tokens.css), with the same fallback as t10's tok(); jsdom reads the fallback, so it must equal the token. */
export function tokenPx(name: string, fallback: number): number {
  try {
    return parseFloat(getComputedStyle(document.documentElement).getPropertyValue(name)) || fallback
  } catch {
    return fallback
  }
}

/** Keeps a value inside [min, max]. */
export function clamp(v: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, v))
}

export interface Bounds {
  min: number
  max: number
}

/** The sidebar's range: 200-360, and never so wide that the list (beside a sheet at its side) drops under --size-list-min. */
export function sideBounds(width: number, panelW: number, pmode: PanelMode | null): Bounds {
  const min = tokenPx('--size-sidebar-min', 200)
  const room = width - (pmode === 'side' ? panelW : 0) - tokenPx('--size-list-min', 400)
  return { min, max: Math.max(min, Math.min(tokenPx('--size-sidebar-max', 360), room)) }
}

/** The sheet's range in what the sidebar leaves (avail): the list keeps --size-list-min; a sheet over the list may still reach its default. */
export function panelBounds(avail: number, def: number, pmode: PanelMode | null): Bounds {
  const min = tokenPx('--size-panel-min', 400)
  const room = avail - tokenPx('--size-list-min', 400)
  return { min, max: pmode === 'over' ? Math.max(min, room, def) : Math.max(min, room) }
}
