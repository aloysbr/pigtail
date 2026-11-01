import { useSyncExternalStore } from 'react'
import { clamp, tokenPx } from '@/app/widths'
import { lsGet, lsSet } from './ui'

// The widths of the sidebar and the sheet (D2) live outside UiState: a drag must not invalidate the list model memoised on `ui`.

/** A width chosen by hand, in px; null = the token's default. */
export interface Widths {
  side: number | null
  panel: number | null
}

const K_SIDE_W = 'pigtail-side-w'
const K_PANEL_W = 'pigtail-panel-w'

function lsDel(k: string) {
  try {
    globalThis.localStorage.removeItem(k)
  } catch {
    // blocked storage: there is nothing kept to remove
  }
}

function readPx(k: string): number | null {
  const v = Number.parseFloat(lsGet(k) ?? '')
  return Number.isFinite(v) ? Math.round(v) : null
}

function initial(): Widths {
  const side = readPx(K_SIDE_W)
  return {
    side: side === null ? null : clamp(side, tokenPx('--size-sidebar-min', 200), tokenPx('--size-sidebar-max', 360)),
    // the sheet's range depends on the window: Shell bounds it at render
    panel: readPx(K_PANEL_W),
  }
}

let state: Widths = initial()
const listeners = new Set<() => void>()

function set(patch: Partial<Widths>) {
  state = { ...state, ...patch }
  for (const l of listeners) l()
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}

/** The current widths (tests, event handlers). */
export function getWidths(): Widths {
  return state
}

/** The widths, re-rendering on change (one stable object per change). */
export function useWidths(): Widths {
  return useSyncExternalStore(subscribe, () => state, () => state)
}

/** Sets the sidebar's width; `persist` keeps it (the end of a drag, a key), a move during a drag does not. */
export function setSideW(px: number, persist: boolean) {
  const v = Math.round(px)
  if (persist) lsSet(K_SIDE_W, String(v))
  if (state.side !== v) set({ side: v })
}

/** Sets the sheet's width; `persist` keeps it. */
export function setPanelW(px: number, persist: boolean) {
  const v = Math.round(px)
  if (persist) lsSet(K_PANEL_W, String(v))
  if (state.panel !== v) set({ panel: v })
}

/** Back to the sidebar's default width (double-click), forgotten. */
export function resetSideW() {
  lsDel(K_SIDE_W)
  set({ side: null })
}

/** Back to the sheet's default width (double-click), forgotten. */
export function resetPanelW() {
  lsDel(K_PANEL_W)
  set({ panel: null })
}

/** Re-reads the stored widths, as a page load would (tests). */
export function resetWidths() {
  state = initial()
  for (const l of listeners) l()
}
