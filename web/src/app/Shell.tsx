import { useEffect, useMemo, useRef, useState, useSyncExternalStore, type CSSProperties } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { installFreshness, useBacklog, type Backlog } from '@/api/queries'
import { findRow } from '@/api/subject'
import { GROUPINGS } from '@/domain/group'
import { EmptyState, Skeleton } from '@/list/EmptyStates'
import { Footer } from '@/list/Footer'
import { List } from '@/list/List'
import { digitOf, pickArea, pickGrouping } from '@/list/Menus'
import { buildModel, revealPath, type ListModel } from '@/list/model'
import { useLaunch } from '@/list/launch'
import { useDropRank, useMoveRank } from '@/list/rank'
import { useToggleStandby } from '@/list/status'
import { cn, isTyping } from '@/lib/utils'
import { registerCapture, useCapture } from '@/palette/CaptureMode'
import { Palette } from '@/palette/Palette'
import { PreviewLayer } from '@/ui/ImagePreview'
import { ReaderLayer } from '@/reader/Reader'
import { registerBuiltins, type PaletteCtx } from '@/palette/registry'
import { SubjectPanel } from '@/panel/SubjectPanel'
import { installHashSync } from '@/state/route'
import { closeLayer, getUi, openCapture, openPanel, openSessions, setUi, toggleKeys, toggleSettings, toggleSide, useUi, type UiState } from '@/state/ui'
import { liveCount, visible } from '@/domain/sessions'
import { SESS_SEARCH_ID, SessionsView, useSessionsModel } from '@/sessions/SessionsView'
import { SessionPanel } from '@/sessions/SessionPanel'
import { SessionsFooter } from '@/sessions/SessionsFooter'
import { PHONE_MAX } from '@/state/viewport'
import { resetPanelW, resetSideW, setPanelW, setSideW, useWidths, type Widths } from '@/state/widths'
import { ToastHost } from '@/ui/Toast'
import { undoLast, useToasts } from '@/ui/toasts'
import { OfflineBanner } from './OfflineBanner'
import { Settings } from './Settings'
import { Sidebar } from './Sidebar'
import { Splitter } from './Splitter'
import { SEARCH_ID, Topbar } from './Topbar'
import { clamp, panelBounds, sideBounds, tokenPx, type Bounds, type PanelMode } from './widths'

/** Under this width the sidebar floats over the list (t8). */
export const DOCK_MIN = 1120
/** Under this width the sidebar cannot be docked at all, only floated (t10 --bp-half). */
export const HALF_MIN = 800
/** Under this width the keys are hidden (t10 `.mobile kbd`). */
export { PHONE_MAX }

export type { PanelMode } from './widths'

registerBuiltins()
registerCapture()

/** Header level by the list's available width (t10 hdLevel): 0 scope pills, 1 scopes in one menu, 2 icons; tight drops the counts. */
export function headLevel(avail: number): { level: 0 | 1 | 2; tight: boolean } {
  const level = avail >= tokenPx('--bp-head-1', 1000) ? 0 : avail >= tokenPx('--bp-head-2', 920) ? 1 : 2
  return { level, tight: avail < tokenPx('--bp-head-tight', 1180) }
}

/** t10 pmode(): the page under --bp-half; beside the list when the list keeps --size-list-min, over it otherwise. */
export function panelMode(width: number, avail: number, panelW: number): PanelMode {
  if (width < tokenPx('--bp-half', HALF_MIN)) return 'page'
  return avail - panelW >= tokenPx('--size-list-min', 400) ? 'side' : 'over'
}

/** The real widths of a render (H1.1): sidebar, sheet and list, each inside its bounds; the grid, the sheet mode, the header and the separators all read them. */
export interface Layout {
  sideW: number
  /** Sidebar default (the token), the separator's double-click target. */
  sideDef: number
  side: Bounds
  avail: number
  panelW: number
  panelDef: number
  panel: Bounds
  pmode: PanelMode | null
  listW: number
}

/** t1 layout(): the stored widths (or the tokens), bounded, decide the sheet mode; the mode in turn bounds the sheet. */
export function layoutOf(width: number, docked: boolean, bpWide: boolean, sheet: boolean, w: Widths): Layout {
  const sideDef = tokenPx('--size-sidebar', 240)
  const sideW = clamp(w.side ?? sideDef, tokenPx('--size-sidebar-min', 200), tokenPx('--size-sidebar-max', 360))
  const avail = width - (docked ? sideW : 0)
  const panelDef = bpWide ? tokenPx('--size-panel-xl', 640) : tokenPx('--size-panel', 560)
  const raw = w.panel ?? panelDef
  // the mode the asked width gives, then the width bounded for that mode; bounding can only bring a sheet over the list back beside it
  const first = panelBounds(avail, panelDef, panelMode(width, avail, raw))
  const panelW = clamp(raw, first.min, first.max)
  const mode = panelMode(width, avail, panelW)
  const pmode = sheet ? mode : null
  const listW = avail - (pmode === 'side' ? panelW : 0)
  // in `side` the list keeps its minimum by construction, so sideW is already inside sideBounds
  return { sideW, sideDef, side: sideBounds(width, panelW, pmode), avail, panelW, panelDef, panel: panelBounds(avail, panelDef, mode), pmode, listW }
}

/** Goes to a subject from the palette: clears the filters that hide it, unfolds its path, selects it and opens its sheet. */
export function goSubject(data: Pick<Backlog, 'subjects' | 'themes' | 'spaces' | 'peers' | 'captures'>, id: string) {
  const ui = getUi()
  let model = buildModel(data, ui)
  const patch: Partial<UiState> = {}
  if (!model.visible.some((r) => r.s.id === id)) {
    Object.assign(patch, { query: '', areaF: null, projectF: null, statusF: null })
    model = buildModel(data, { ...ui, ...patch })
  }
  const path = revealPath(model.groups, id)
  setUi((s) => ({
    ...patch,
    open: { ...s.open, ...path.open },
    more: { ...s.more, ...path.more },
    sel: id,
    panel: true,
    view: 'backlog',
    sessionPanel: false,
    palette: false,
    focusTick: s.focusTick + 1,
  }))
}

function subscribeWidth(l: () => void) {
  window.addEventListener('resize', l)
  return () => window.removeEventListener('resize', l)
}

function useWidth(): number {
  return useSyncExternalStore(subscribeWidth, () => window.innerWidth, () => DOCK_MIN)
}

/** True when the key goes to a control of its own (a button, a link): ⏎ then acts on it, not on the selection. */
function isControl(t: EventTarget | null): boolean {
  return t instanceof Element && !!t.closest('button, a, [role="menuitemradio"], [role="option"]')
}

const LONE_MODIFIERS: ReadonlySet<string> = new Set(['Shift', 'CapsLock', 'Meta', 'Alt', 'Control'])

function isSeparator(t: EventTarget | null): boolean {
  return t instanceof Element && t.getAttribute('role') === 'separator'
}

function move(model: ListModel, by: number, focus = true) {
  const { order } = model
  if (!order.length) return
  const cur = getUi().sel
  const i = cur ? order.indexOf(cur) : -1
  const next = i < 0 ? 0 : Math.min(order.length - 1, Math.max(0, i + by))
  setUi((s) => ({ sel: order[next], focusTick: focus ? s.focusTick + 1 : s.focusTick }))
}

function fold(model: ListModel, open: boolean) {
  const a = document.activeElement
  const key = a instanceof HTMLElement && a.dataset.g ? a.dataset.g : null
  const sel = getUi().sel
  const target = key ?? (sel ? model.groupOf.get(sel) : undefined)
  if (!target) return
  setUi((s) => ({ open: { ...s.open, [target]: open } }))
  // a folded row leaves the screen: its group header takes the focus
  if (!key && !open) document.querySelector<HTMLElement>(`[data-g="${CSS.escape(target)}"]`)?.focus()
}

/** The actions the keys trigger on the selected line, built in the Shell from the write hooks. */
export interface KeyActions {
  moveRank: (id: string, dir: -1 | 1) => void
  toggleStandby: (id: string) => void
  /** Optional: callers built before Lancer (lot 3) launch nothing. */
  launch?: (id: string) => void
}

const NO_ACTIONS: KeyActions = { moveRank: () => {}, toggleStandby: () => {}, launch: () => {} }

/** True when the selection is a capture row: read only, no subject action applies to it (D14). */
function onCapture(model: ListModel | null, sel: string | null): boolean {
  return !!model && !!sel && model.capIds.has(sel)
}

/** True when the pointer is a finger (no drag and drop there, spec § Téléphone). */
function coarsePointer(): boolean {
  try {
    return typeof window.matchMedia === 'function' && window.matchMedia('(pointer: coarse)').matches
  } catch {
    return false
  }
}

/** The Sessions view's own keys: ↑/↓ j/k walk the sessions, ⏎ opens the selected one's panel, / the filter; every backlog key is inert there. */
function handleSessionsKey(e: KeyboardEvent, order: readonly string[]) {
  if (e.metaKey || e.ctrlKey || e.altKey || isTyping(e.target) || isSeparator(e.target)) return
  const ui = getUi()
  switch (e.key) {
    case 'j':
    case 'ArrowDown':
    case 'k':
    case 'ArrowUp': {
      e.preventDefault()
      if (!order.length) return
      const by = e.key === 'j' || e.key === 'ArrowDown' ? 1 : -1
      const i = ui.sessionSel ? order.indexOf(ui.sessionSel) : -1
      const next = i < 0 ? 0 : Math.min(order.length - 1, Math.max(0, i + by))
      setUi((s) => ({ sessionSel: order[next], focusTick: s.focusTick + 1 }))
      return
    }
    case 'Enter':
      if (isControl(e.target) || !ui.sessionSel || !order.includes(ui.sessionSel)) return
      e.preventDefault()
      setUi({ sessionPanel: true, panel: false })
      return
    case '/':
      e.preventDefault()
      document.getElementById(SESS_SEARCH_ID)?.focus()
      return
    default:
  }
}

/** Global keys (spec § Clavier); letters are ignored while typing in a field. `sessOrder` is the Sessions view's session order (j/k there). */
export function handleKey(
  e: KeyboardEvent,
  model: ListModel | null,
  docked: boolean,
  wide = false,
  actions: KeyActions = NO_ACTIONS,
  sessOrder: readonly string[] = [],
) {
  const mod = e.metaKey || e.ctrlKey
  // the image preview over everything: only Esc here; Space, ← → and z belong to the preview (ImagePreview), every other key waits
  if (getUi().preview) {
    if (e.key === 'Escape' && closeLayer()) e.preventDefault()
    return
  }
  // the reader (D4) over the app: only Esc here; ← → and Tab belong to the reader, every other key waits
  if (getUi().reader) {
    if (e.key === 'Escape' && !e.defaultPrevented && closeLayer()) e.preventDefault()
    return
  }
  // ⌘, opens and closes the Réglages, even from a field (a modifier is never typing)
  if (mod && e.key === ',') {
    e.preventDefault()
    toggleSettings()
    return
  }
  // ⌘K opens the palette (z 10) over the Réglages popover (z 9); Esc then peels palette > menu > Réglages
  if (mod && (e.code === 'KeyK' || e.key === 'k')) {
    e.preventDefault()
    setUi((s) => ({ palette: !s.palette, menu: null, focusTick: s.palette ? s.focusTick + 1 : s.focusTick }))
    return
  }
  // Réglages open: a layer of its own, only Esc, ⌘, and ⌘K answer; the list keys and ⌘B wait for it to close
  if (getUi().settings) {
    if (e.key === 'Escape' && closeLayer()) e.preventDefault()
    return
  }
  if (mod && (e.code === 'KeyB' || e.key === 'b')) {
    e.preventDefault()
    toggleSide(docked, wide)
    return
  }
  if (e.key === 'Escape') {
    if (closeLayer()) e.preventDefault()
    return
  }
  // a modifier pressed alone (Shift for an AZERTY digit) is not the next key: the wait stays open
  if (LONE_MODIFIERS.has(e.key)) return
  // the Sessions view: its own keys, no backlog key (l, h, f, v, ?, ,, ⌥↑↓, ← →, c, z, g) reaches the list behind
  if (getUi().view === 'sessions') {
    handleSessionsKey(e, sessOrder)
    return
  }
  // ⌥↑ / ⌥↓ move the selected line's rank; every other ⌥ key stays ignored (a focused separator keeps its own arrows)
  if (e.altKey && !mod && !e.shiftKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown') && !isTyping(e.target) && !isSeparator(e.target) && model) {
    const sel = getUi().sel
    if (!sel) return
    e.preventDefault()
    if (onCapture(model, sel)) return
    actions.moveRank(sel, e.key === 'ArrowUp' ? -1 : 1)
    return
  }
  // a focused separator owns its arrows (Splitter): the list never folds under it
  if (mod || e.altKey || isTyping(e.target) || isSeparator(e.target) || !model) return
  const ui: UiState = getUi()
  const digit = digitOf(e.code)
  if (ui.menu && digit !== null) {
    e.preventDefault()
    if (ui.menu === 'group') {
      const g = GROUPINGS[digit - 1]
      if (g) pickGrouping(g)
    } else if (ui.menu === 'filter' || ui.menu === 'go') {
      // 'filter' (f) and 'go' (g) both pick the digit-th area of the sidebar; another menu (Scopes) takes no digit
      const a = model.areas[digit - 1]
      if (a) pickArea(a)
      else if (ui.menu === 'go') setUi({ menu: null })
    }
    return
  }
  // g waits for one digit only: any other key ends the wait, then does its own job
  if (ui.menu === 'go') setUi({ menu: null })
  switch (e.key) {
    case 'Enter':
      if (isControl(e.target) || !ui.sel || onCapture(model, ui.sel)) return
      e.preventDefault()
      openPanel(ui.sel)
      return
    case 'j':
    case 'ArrowDown':
      e.preventDefault()
      move(model, 1)
      return
    case 'k':
    case 'ArrowUp':
      e.preventDefault()
      move(model, -1)
      return
    case 'ArrowRight':
      e.preventDefault()
      fold(model, true)
      return
    case 'ArrowLeft':
      e.preventDefault()
      fold(model, false)
      return
    case '/':
      e.preventDefault()
      setUi({ menu: null })
      document.getElementById(SEARCH_ID)?.focus()
      return
    case 'f':
      e.preventDefault()
      setUi({ menu: ui.menu === 'filter' ? null : 'filter' })
      return
    case 'v':
      e.preventDefault()
      setUi({ menu: ui.menu === 'group' ? null : 'group' })
      return
    case 'g':
      e.preventDefault()
      setUi({ menu: ui.menu === 'go' ? null : 'go' })
      return
    case ',':
      e.preventDefault()
      toggleKeys()
      return
    case 'c':
      e.preventDefault()
      openCapture()
      return
    case 's':
      e.preventDefault()
      openSessions()
      return
    case 'l':
      // a capture is read only: no call (D14)
      if (!ui.sel || onCapture(model, ui.sel)) return
      e.preventDefault()
      actions.launch?.(ui.sel)
      return
    case 'h':
      if (!ui.sel || onCapture(model, ui.sel)) return
      e.preventDefault()
      actions.toggleStandby(ui.sel)
      return
    case 'z':
      // read by e.key: AZERTY's z (code KeyW) undoes, its w (code KeyZ) does not
      e.preventDefault()
      void undoLast()
      return
    default:
  }
}

/** The web front: sidebar, header (with the daemon banner), the grouped list, the shortcut footer, the subject sheet and the palette. */
export function Shell() {
  const { ready, subjects, themes, spaces, peers, peersReady, captures, error, nextRetryAt, fetching } = useBacklog()
  const qc = useQueryClient()
  const retryNow = () => void qc.refetchQueries({ predicate: (q) => q.state.status === 'error' })
  const ui = useUi((s) => s)
  const widths = useWidths()
  const [resizing, setResizing] = useState(false)
  const width = useWidth()
  const model = useMemo(
    () => (ready ? buildModel({ subjects, themes, spaces, peers, captures }, ui) : null),
    [ready, subjects, themes, spaces, peers, captures, ui],
  )
  const docked = width >= HALF_MIN && (ui.sideDocked || (width >= DOCK_MIN && !ui.sideClosed))
  const wide = width >= DOCK_MIN
  const phone = width < PHONE_MAX
  const bpWide = width >= tokenPx('--bp-wide', 1536)
  const row = ui.panel && model ? findRow(model.rows, ui.sel) : undefined
  const onSessions = ui.view === 'sessions'
  // one GET /peers?all=1, read only while the Sessions view is on screen
  const sess = useSessionsModel(subjects, themes, onSessions)
  const sessOrder = sess.model?.order ?? []
  // the session panel takes the sheet's place (one panel at a time), on a session /peers?all=1 still shows
  const sessPeer = onSessions && ui.sessionPanel && ui.sessionSel ? sess.all?.find((p) => p.session_id === ui.sessionSel && visible(p)) : undefined
  const lay = layoutOf(width, docked, bpWide, !!row || !!sessPeer, widths)
  const { sideW, panelW, pmode } = lay
  // the phone keeps its own layout; elsewhere the header folds by what the list really has (window minus the docked sidebar and a sheet beside it)
  const head = phone ? { level: 0 as const, tight: false } : headLevel(lay.listW)
  const sizes = { '--size-sidebar': `${sideW}px`, '--size-panel': `${panelW}px`, '--size-panel-xl': `${panelW}px` } as CSSProperties
  const hasToast = useToasts().length > 0
  const moveRank = useMoveRank()
  const dropRank = useDropRank()
  const toggleStandby = useToggleStandby()
  const launch = useLaunch()
  const actions: KeyActions = { moveRank, toggleStandby: (id) => void toggleStandby(id), launch }
  // « N vivantes »: GET /peers (always mounted), the one source of the sidebar and of the view's header
  const live = peersReady ? liveCount(peers) : null
  const ctx = useRef({ model, docked, wide, actions, sessOrder })
  ctx.current = { model, docked, wide, actions, sessOrder }
  const data = useRef({ subjects, themes, spaces, peers, captures })
  data.current = { subjects, themes, spaces, peers, captures }
  const palCtx: PaletteCtx = { docked, wide, phone, areas: model?.areas ?? [] }
  const saveCapture = useCapture()
  // the current project of a capture: the selected subject's or the selected capture's
  const curTheme = ui.sel ? (subjects.find((s) => s.id === ui.sel)?.theme_id ?? captures.find((c) => c.id === ui.sel)?.theme_id) : undefined

  useEffect(() => installFreshness(), [])
  useEffect(() => installHashSync(), [])

  // a route naming a subject that the read does not hold: close the sheet so the address goes back to #/ (not while loading or failing)
  const gone = !!model && !error && ui.panel && !!ui.sel && !row
  useEffect(() => {
    if (gone) setUi({ panel: false })
  }, [gone])
  // the same for a session route: a session the read no longer holds (ended, or a stale link) closes its panel
  const sessGone = onSessions && ui.sessionPanel && !!sess.all && !sessPeer
  useEffect(() => {
    if (sessGone) setUi({ sessionPanel: false })
  }, [sessGone])
  const sessIdx = sessPeer ? sessOrder.indexOf(sessPeer.session_id) : -1

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => handleKey(e, ctx.current.model, ctx.current.docked, ctx.current.wide, ctx.current.actions, ctx.current.sessOrder)
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [])

  // keyed on focusTick alone: a step that must not take the focus (‹ › of the sheet) changes sel only
  const selNow = useRef({ sel: ui.sel, view: ui.view, sess: ui.sessionSel })
  selNow.current = { sel: ui.sel, view: ui.view, sess: ui.sessionSel }
  useEffect(() => {
    const { sel, view, sess: sid } = selNow.current
    if (!ui.focusTick) return
    if (view === 'sessions') {
      if (sid) document.querySelector<HTMLElement>(`li.srow[data-sid="${CSS.escape(sid)}"]`)?.focus()
      return
    }
    if (!sel) return
    const id = CSS.escape(sel)
    document.querySelector<HTMLElement>(`li.lr6[data-id="${id}"], li.lr6[data-cap="${id}"]`)?.focus()
  }, [ui.focusTick])

  useEffect(() => {
    if (!ui.menu) return
    const onDown = (e: PointerEvent) => {
      if (e.target instanceof Element && e.target.closest('.gmenu-w')) return
      setUi({ menu: null })
    }
    document.addEventListener('pointerdown', onDown)
    return () => document.removeEventListener('pointerdown', onDown)
  }, [ui.menu])

  const now = Date.now()
  const showSide = docked || ui.floatOpen
  const banner = model && error && nextRetryAt !== null ? <OfflineBanner nextRetryAt={nextRetryAt} fetching={fetching} onRetry={retryNow} /> : null
  return (
    <div className={cn('app', !docked && 'nodock', phone && 'phone', bpWide && 'bp-wide', pmode && `pm-${pmode}`, resizing && 'resizing')} style={sizes}>
      {showSide && !docked ? <div className="side-scrim" onClick={() => setUi({ floatOpen: false })} /> : null}
      {showSide ? <Sidebar model={model} float={!docked} canDock={width >= HALF_MIN} live={live} /> : null}
      <main className={cn('lmain', hasToast && 'has-toast')}>
        <div className="lscroll" data-scroll="list">
        {onSessions ? (
          <SessionsView data={sess} baseScopes={model?.scopes ?? []} live={live} docked={docked} wide={wide} now={now} banner={banner} />
        ) : (
        <>
        <Topbar model={model} docked={docked} wide={wide} level={head.level} tight={head.tight} banner={banner} phone={phone} />
        {model ? (
          model.visible.length ? (
            <List model={model} now={now} drag={phone || coarsePointer() ? null : dropRank} />
          ) : (
            <div className="ll6" role="region" aria-label="Sujets">
              <EmptyState />
            </div>
          )
        ) : error ? (
          <div className="empty6" role="alert">
            <h2>pigtaild ne répond pas</h2>
            <p>{error.message}</p>
          </div>
        ) : (
          <Skeleton />
        )}
        </>
        )}
        </div>
        {/* the reader covers the page and mounts the toast host itself (live over it, never inert) */}
        {pmode === 'page' || ui.reader ? null : <ToastHost />}
        {onSessions ? <SessionsFooter /> : <Footer model={model} narrow={!phone && lay.listW <= PHONE_MAX} />}
      </main>
      {row ? (
        <div className="pwrap" id="sheet">
          <SubjectPanel
            row={row}
            page={pmode === 'page'}
            onStep={(by) => model && move(model, by, false)}
            canPrev={!!model && model.order.indexOf(row.s.id) > 0}
            canNext={!!model && model.order.indexOf(row.s.id) < model.order.length - 1}
          />
        </div>
      ) : sessPeer ? (
        <div className="pwrap" id="sheet">
          <SessionPanel
            p={sessPeer}
            subjects={subjects}
            themes={themes}
            now={now}
            page={pmode === 'page'}
            onStep={(by) => {
              const next = sessOrder[Math.min(sessOrder.length - 1, Math.max(0, sessIdx + by))]
              if (next) setUi({ sessionSel: next })
            }}
            stepper={sessIdx >= 0}
            canPrev={sessIdx > 0}
            canNext={sessIdx < sessOrder.length - 1}
            onOpenSubject={(id) => goSubject(data.current, id)}
          />
        </div>
      ) : null}
      {docked && !phone ? (
        <Splitter
          className="sp-side"
          label="Largeur de la barre latérale"
          value={sideW}
          min={lay.side.min}
          max={lay.side.max}
          dir={1}
          onChange={setSideW}
          onReset={resetSideW}
          onResizing={setResizing}
          valueText={`Barre latérale ${sideW} px`}
        />
      ) : null}
      {pmode === 'side' || pmode === 'over' ? (
        <Splitter
          className="sp-panel"
          label="Largeur de la fiche"
          controls="sheet"
          value={panelW}
          min={lay.panel.min}
          max={lay.panel.max}
          dir={-1}
          onChange={setPanelW}
          onReset={resetPanelW}
          onResizing={setResizing}
          valueText={`Fiche ${panelW} px, liste ${lay.avail - panelW} px`}
        />
      ) : null}
      {/* a sheet in page mode hides the list (pm-page), never the toast: then it sits on the page */}
      {pmode === 'page' && !ui.reader ? <ToastHost /> : null}
      {ui.settings ? <Settings /> : null}
      {ui.preview ? <PreviewLayer /> : null}
      {ui.palette && model ? <Palette
          model={model}
          ctx={palCtx}
          onPick={(id) => goSubject(data.current, id)}
          capture={{ themes, current: curTheme || undefined, onSave: saveCapture }}
          actions={actions}
        /> : null}
      {ui.reader ? <ReaderLayer /> : null}
    </div>
  )
}
