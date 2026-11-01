import { useEffect, useSyncExternalStore } from 'react'
import { lsGet, lsSet } from '@/state/ui'

/** The theme (D5) and the appearance (D10): remembered per browser, set on the root before the first paint by
 *  boot.js (the same rules, plain ES5), then kept live here. System follows prefers-color-scheme without a reload. */
export type PaletteName = 'terra' | 'ocean'
export type Appearance = 'light' | 'dark' | 'system'
export interface Choice {
  palette: PaletteName
  appearance: Appearance
}

const K_PALETTE = 'pigtail-palette'
const K_APPEARANCE = 'pigtail-appearance'
const QUERY = '(prefers-color-scheme: dark)'
export const DEFAULT_CHOICE: Choice = { palette: 'terra', appearance: 'system' }

/** The saved choice; an unknown value or a blocked storage reads as the default. */
export function readChoice(): Choice {
  const p = lsGet(K_PALETTE)
  const a = lsGet(K_APPEARANCE)
  return {
    palette: p === 'terra' || p === 'ocean' ? p : DEFAULT_CHOICE.palette,
    appearance: a === 'light' || a === 'dark' || a === 'system' ? a : DEFAULT_CHOICE.appearance,
  }
}

export function saveChoice(c: Choice) {
  lsSet(K_PALETTE, c.palette)
  lsSet(K_APPEARANCE, c.appearance)
}

function systemDark(): boolean {
  try {
    return typeof globalThis.matchMedia === 'function' && globalThis.matchMedia(QUERY).matches
  } catch {
    return false
  }
}

/** light or dark, System resolved by the Mac's setting. */
export function resolve(a: Appearance): 'light' | 'dark' {
  return a === 'system' ? (systemDark() ? 'dark' : 'light') : a
}

/** Sets data-theme-palette and data-theme (resolved) on the root; idempotent. */
export function applyChoice(c: Choice) {
  const root = document.documentElement
  root.setAttribute('data-theme-palette', c.palette)
  root.setAttribute('data-theme', resolve(c.appearance))
}

const listeners = new Set<() => void>()
let snap: { choice: Choice; resolved: 'light' | 'dark' } | null = null
function snapshot() {
  const choice = readChoice()
  const resolved = resolve(choice.appearance)
  if (!snap || snap.resolved !== resolved || snap.choice.palette !== choice.palette || snap.choice.appearance !== choice.appearance)
    snap = { choice, resolved }
  return snap
}
function notify() {
  for (const f of listeners) f()
}

/** Saves, applies and re-renders every reader: the one path of a change of choice (Réglages, ⌘K). */
export function commitChoice(c: Choice) {
  saveChoice(c)
  applyChoice(c)
  notify()
}

/** Re-applies the saved choice when the Mac switches light / dark: only System moves. Returns the unsubscribe. */
export function followSystem(): () => void {
  if (typeof globalThis.matchMedia !== 'function') return () => {}
  let mql: MediaQueryList
  try {
    mql = globalThis.matchMedia(QUERY)
  } catch {
    return () => {}
  }
  const on = () => {
    const c = readChoice()
    if (c.appearance === 'system') applyChoice(c)
    notify()
  }
  mql.addEventListener('change', on)
  return () => mql.removeEventListener('change', on)
}

function subscribe(f: () => void) {
  listeners.add(f)
  return () => listeners.delete(f)
}

/** The current choice and its resolved mode; set() saves, applies and re-renders every reader (tasks 8-9). */
export function useAppearance() {
  const s = useSyncExternalStore(subscribe, snapshot, snapshot)
  useEffect(() => followSystem(), [])
  return {
    choice: s.choice,
    resolved: s.resolved,
    set: commitChoice,
  }
}
