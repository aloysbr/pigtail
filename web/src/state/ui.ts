import { useSyncExternalStore } from 'react'
import type { Grouping } from '@/domain/group'
import type { Display } from '@/domain/status'

// UI state lives outside the query cache: a re-read never resets selection, folds or menus (D4).

/** Everything the list remembers between two reads. */
export interface UiState {
  group: Grouping
  scopesOff: string[]
  query: string
  areaF: { scope: string; name: string } | null
  /** The project the sidebar filters on (its group key `th:<id>`), single choice like the area (D16). */
  projectF: string | null
  statusF: Display | null
  showDropped: boolean
  open: Record<string, boolean>
  more: Record<string, boolean>
  sel: string | null
  focusTick: number
  /** 'go' is not a menu: it is the wait after `g`, for a digit that picks an area (the sidebar shows the digits). */
  menu: 'group' | 'filter' | 'scopes' | 'go' | null
  sideClosed: boolean
  /** Docked on purpose (« Ancrer »): the sidebar then stays docked from 800 px, not only from 1 120 px (t10 sideP). */
  sideDocked: boolean
  floatOpen: boolean
  kbHidden: boolean
  /** The ⌘K palette: closed, open on its commands (true), or in its « Capture › » mode (a bottom sheet on the phone). */
  palette: boolean | 'capture'
  /** The subject sheet is open on `sel` (lot 3). */
  panel: boolean
  /** The Réglages popover (theme, appearance), opened by its sidebar button or ⌘, (T8). */
  settings: boolean
  /** The image preview (Quick Look, T13): the subject whose « Liens » it walks, and the image shown; over every other layer. */
  preview: { subjectId: string; index: number } | null
  /** The full-page reader (D4): a subject's deliverable group (its key) and the round shown; tour null = the latest version. */
  reader: { subjectId: string; groupKey: string; tour: number | null } | null
  /** The main view: the backlog, or the Sessions view (`s`, the sidebar entry, #/sessions; T15). */
  view: 'backlog' | 'sessions'
  /** The selected session of the Sessions view (its session_id). */
  sessionSel: string | null
  /** The Sessions view's text filter (« / »), apart from the backlog's search. */
  sessQuery: string
  /** The session panel is open on `sessionSel` (T16); one panel at a time with the subject sheet. */
  sessionPanel: boolean
}

const K_SCOPES = 'pigtail-scopes'
const K_SIDE = 'pigtail-side'
const K_KB = 'pigtail-kb'

/** Reads a per-browser choice; a blocked storage reads as nothing kept. */
export function lsGet(k: string): string | null {
  try {
    return globalThis.localStorage.getItem(k)
  } catch {
    return null
  }
}

/** Writes a per-browser choice; a blocked storage simply does not keep it. */
export function lsSet(k: string, v: string) {
  try {
    globalThis.localStorage.setItem(k, v)
  } catch {
    // private window or blocked site data: the choice is not kept
  }
}

function readScopesOff(): string[] {
  try {
    const v: unknown = JSON.parse(lsGet(K_SCOPES) ?? '[]')
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
  } catch {
    return []
  }
}

function initial(): UiState {
  return {
    group: 'aire',
    scopesOff: readScopesOff(),
    query: '',
    areaF: null,
    projectF: null,
    statusF: null,
    showDropped: false,
    open: {},
    more: {},
    sel: null,
    focusTick: 0,
    menu: null,
    sideClosed: lsGet(K_SIDE) === 'closed',
    sideDocked: lsGet(K_SIDE) === 'docked',
    floatOpen: false,
    kbHidden: lsGet(K_KB) === 'hidden',
    palette: false,
    panel: false,
    settings: false,
    preview: null,
    reader: null,
    view: 'backlog',
    sessionSel: null,
    sessQuery: '',
    sessionPanel: false,
  }
}

let state: UiState = initial()
const listeners = new Set<() => void>()

/** The current state (tests, event handlers). */
export function getUi(): UiState {
  return state
}

/** Merges a patch (or the result of a function of the state) and notifies. */
export function setUi(patch: Partial<UiState> | ((s: UiState) => Partial<UiState>)) {
  const p = typeof patch === 'function' ? patch(state) : patch
  state = { ...state, ...p }
  for (const l of listeners) l()
}

/** Re-reads the stored choices, as a page load would (tests). */
export function resetUi() {
  state = initial()
  for (const l of listeners) l()
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => listeners.delete(l)
}

/** Listens to every change of the state (the hash route); returns the unsubscribe. */
export function subscribeUi(l: () => void): () => void {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}

/** Reads a slice of the state; the selector must return a stable value (a field, not a new object). */
export function useUi<T>(sel: (s: UiState) => T): T {
  return useSyncExternalStore(subscribe, () => sel(state), () => sel(state))
}

/** Switches a scope on or off, never the last one on; remembered in localStorage. `all` differs by caller (the Sessions view adds its sessions' scopes): a scope off that it does not list stays off. */
export function toggleScope(scope: string, all: readonly string[]) {
  const off = state.scopesOff
  let next: string[]
  if (off.includes(scope)) next = off.filter((x) => x !== scope)
  else {
    if (all.filter((x) => !off.includes(x)).length <= 1) return
    next = [...off, scope]
  }
  lsSet(K_SCOPES, JSON.stringify(next))
  setUi({ scopesOff: next, areaF: state.areaF && next.includes(state.areaF.scope) ? null : state.areaF, projectF: null })
}

/** ⌘B: docked → put away (remembered); wide but put away → docked again; narrow → the floating sidebar opens or closes. */
export function toggleSide(docked: boolean, wide = false) {
  if (docked) {
    lsSet(K_SIDE, 'closed')
    setUi({ sideClosed: true, sideDocked: false, floatOpen: false, menu: null })
  } else if (wide) dockSide()
  else setUi((s) => ({ floatOpen: !s.floatOpen, menu: null }))
}

/** Docks the sidebar again (remembered). */
export function dockSide() {
  lsSet(K_SIDE, 'docked')
  setUi({ sideClosed: false, sideDocked: true, floatOpen: false })
}

/** ? : hides or shows the shortcut footer (remembered). */
export function toggleKeys() {
  const h = !state.kbHidden
  lsSet(K_KB, h ? 'hidden' : 'shown')
  setUi({ kbHidden: h })
}

/** Clears search and filters. */
export function clearFilters() {
  setUi({ query: '', areaF: null, projectF: null, statusF: null })
}

/** c, « Capturer… » and the phone's « + »: the palette in its « Capture › » mode (menus and the floating sidebar closed). */
export function openCapture() {
  setUi({ palette: 'capture', menu: null, floatOpen: false })
}

/** ⌘, and the sidebar's « Réglages » button: opens or closes the Réglages popover. */
export function toggleSettings() {
  // the floating sidebar stays: the popover sits over the button that opened it, Esc peels Réglages then the drawer
  setUi((s) => ({ settings: !s.settings, menu: null }))
}

/** Opens Réglages (idempotent, unlike the toggle): the palette's « Open Settings » over an already open popover leaves it open. */
export function openSettings() {
  setUi({ settings: true, menu: null })
}

/** Opens the subject sheet on a subject (⏎, a click, the palette, the route); it lives in the backlog and closes a session panel. */
export function openPanel(id: string) {
  setUi({ sel: id, panel: true, view: 'backlog', sessionPanel: false })
}

/** `s` and the sidebar entry: the Sessions view (menus and the floating sidebar closed, the subject sheet with them). */
export function openSessions() {
  setUi({ view: 'sessions', panel: false, menu: null, floatOpen: false })
}

/** The sidebar's « Backlog »: back to the list (its filters cleared), the session panel closed. */
export function openBacklog() {
  setUi({ view: 'backlog', sessionPanel: false, areaF: null, projectF: null, floatOpen: false })
}

/** Closes the sheet; the selection stays and its row takes the focus back. */
export function closePanel() {
  setUi((s) => ({ panel: false, focusTick: s.focusTick + 1 }))
}

/** Closes the session panel; the session stays selected and its row takes the focus back. */
export function closeSessionPanel() {
  setUi((s) => ({ sessionPanel: false, focusTick: s.focusTick + 1 }))
}

/** Opens the image preview on the index-th image of a subject's links. */
export function openPreview(subjectId: string, index: number) {
  setUi({ preview: { subjectId, index } })
}

/** Opens the reader on a subject's deliverable group, at round `tour` (the latest version when omitted). */
export function openReader(subjectId: string, groupKey: string, tour: number | null = null) {
  setUi({ reader: { subjectId, groupKey, tour } })
}

/** Esc: closes the topmost layer only, in the order they stack on screen: image preview > reader > palette > menu > Réglages > floating sidebar > sheet or session panel (never a search or a filter); returns false when nothing was open. The reader gives the focus back to its line itself. */
export function closeLayer(): boolean {
  const s = state
  if (s.preview) setUi({ preview: null })
  else if (s.reader) setUi({ reader: null })
  else if (s.palette) setUi({ palette: false, focusTick: s.focusTick + 1 })
  else if (s.menu) setUi({ menu: null })
  else if (s.settings) setUi({ settings: false, focusTick: s.focusTick + 1 })
  else if (s.floatOpen) setUi({ floatOpen: false })
  else if (s.panel) closePanel()
  else if (s.sessionPanel && s.view === 'sessions') closeSessionPanel()
  else return false
  return true
}
