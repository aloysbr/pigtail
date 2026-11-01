import { useLayoutEffect, useRef, useState, type MouseEvent, type ReactNode } from 'react'
import { LABEL } from '@/domain/status'
import { FilterMenu, GroupMenu } from '@/list/Menus'
import type { ListModel } from '@/list/model'
import { ScopeMenu, ScopePills } from '@/list/ScopePills'
import { Icon, Kbd, nf } from '@/list/StatusIcon'
import { cn } from '@/lib/utils'
import { clearFilters, openCapture, setUi, toggleSide, useUi } from '@/state/ui'
import { useFitChips } from './fitChips'

/** The search field's id, focused by « / ». */
export const SEARCH_ID = 'pigtail-search'

/**
 * A press on a header button keeps the focus where it is. Without it, a finger's compatibility mousedown (after pointerup) blurs the
 * search, which shrinks and slides the chips, and the click lands on the header instead of the button.
 */
function keepFocus(e: MouseEvent<HTMLElement>) {
  const t = e.target as Element
  if (e.currentTarget.contains(t) && t.closest('button')) e.preventDefault()
}

function Chip({ label, value, onRemove, className }: { label: string; value: ReactNode; onRemove: () => void; className?: string }) {
  return (
    <span className={cn('fchip', className)}>
      <span className="muted">{label}</span>
      <b>{value}</b>
      <button type="button" className="fx" aria-label={`Retirer le filtre ${label}`} onClick={onRemove}>
        <Icon name="x" />
      </button>
    </span>
  )
}

/**
 * Levels 0 and 1: one header line (navigation, scopes, search, filters, count, Abandonnés, grouping). Level 2 (D7, Q3 A): two fixed
 * lines, navigation · title · scopes · tools, then the search over the rest of the line · Filtre · the chips folding into « +n ».
 * The daemon banner spans the bottom of either (G3).
 */
export function Topbar({
  model,
  docked,
  wide,
  level = 0,
  tight = false,
  banner = null,
  phone = false,
}: {
  model: ListModel | null
  docked: boolean
  wide: boolean
  level?: 0 | 1 | 2
  tight?: boolean
  banner?: ReactNode
  /** The phone: « + » (Capturer) closes the header, the capture opens as a bottom sheet (t10). */
  phone?: boolean
}) {
  const nav = docked ? null : (
    <button
      type="button"
      className="ghost sm"
      aria-label="Navigation"
      aria-keyshortcuts="Meta+B"
      title="Navigation (⌘B)"
      onClick={() => toggleSide(false, wide)}
    >
      <Icon name="menu" />
      <Kbd k="⌘B" />
    </button>
  )
  if (level === 2) return <TwoLines model={model} nav={nav} tight={tight} banner={banner} />
  return (
    <header className={cn('d-head', `hd-${level}`, tight && 'tight')} onMouseDown={keepFocus}>
      {nav}
      <h1>Backlog</h1>
      {model ? (
        <Controls
          model={model}
          level={level}
          // the phone's « + » closes the header's first row, right after the scopes, never alone on a row of its own
          plus={
            phone ? (
              <button type="button" className="ib cap-add" aria-label="Capturer" title="Capturer" onClick={openCapture}>
                <Icon name="plus" />
              </button>
            ) : null
          }
        />
      ) : null}
      {banner}
    </header>
  )
}

/** Level 2: two lines of fixed height, the loading header included (no count, no chip while the first read is pending). */
function TwoLines({ model, nav, tight, banner }: { model: ListModel | null; nav: ReactNode; tight: boolean; banner: ReactNode }) {
  const l2 = useRef<HTMLDivElement>(null)
  useFitChips(l2)
  return (
    <header className={cn('d-head', 'hd-2', 'h2l', tight && 'tight')} onMouseDown={keepFocus}>
      <div className="h-l1">
        {nav}
        <h1>Backlog</h1>
        {model ? <ScopeMenu model={model} /> : null}
        <span className="c-sp" />
        {model ? <Tools model={model} level={2} /> : null}
      </div>
      <div className="h-l2" ref={l2}>
        {model ? (
          <>
            <SearchField grow />
            <FilterMenu model={model} />
            <Chips model={model} fold />
          </>
        ) : null}
      </div>
      {banner}
    </header>
  )
}

/**
 * The search field. It rests narrow and widens on focus (siblings reflow, t10). It shrinks on a real blur only: a press on a header
 * button never blurs it (see Topbar's onMouseDown), so no reflow can slide a button or chip x from under the pointer between
 * pointerdown and click, whatever the event order (mouse, or a finger whose compatibility mousedown comes after pointerup).
 */
/**
 * Level 2 lays the header out as another tree, so a level change remounts the field. A focused field hands its focus and caret over
 * to the next one, mounted in the same commit (unmount cleanup runs while the old input is still in the document), so typing goes on
 * in the field, never in the global shortcuts.
 */
let handover: { start: number | null; end: number | null; dir: 'forward' | 'backward' | 'none' | null } | null = null

function SearchField({ grow = false }: { grow?: boolean }) {
  const query = useUi((s) => s.query)
  const [wide, setWide] = useState(false)
  const ref = useRef<HTMLInputElement>(null)
  useLayoutEffect(() => {
    const el = ref.current
    if (el && handover) {
      const h = handover
      handover = null
      el.focus()
      if (h.start !== null && h.end !== null) el.setSelectionRange(h.start, h.end, h.dir ?? undefined)
    }
    return () => {
      if (el && document.activeElement === el) {
        handover = { start: el.selectionStart, end: el.selectionEnd, dir: el.selectionDirection }
        // only the field mounted in the same commit takes it: an unmount for good hands nothing to a later mount
        queueMicrotask(() => {
          handover = null
        })
      }
    }
  }, [])
  return (
    // grow (level 2): the field takes the rest of its line, so its focus never widens it
    <label className={cn('fsearch', wide && !grow && 'wide')}>
      <Icon name="search" />
      <input
        ref={ref}
        id={SEARCH_ID}
        type="search"
        placeholder="Rechercher…"
        aria-label="Rechercher dans le backlog"
        value={query}
        onChange={(e) => setUi({ query: e.target.value })}
        onFocus={() => setWide(true)}
        onBlur={() => setWide(false)}
        // Esc never clears a search (spec): stop the browser's own clearing of a search field
        onKeyDown={(e) => {
          if (e.key === 'Escape') e.preventDefault()
        }}
      />
      <Kbd k="/" />
    </label>
  )
}

/**
 * The filter chips, then « Effacer tous les filtres ». Folding (level 2), they sit in `.h-chips`, each a `.h-ch`, with « +n »
 * (`.h-more`, hidden until useFitChips folds one); no chip, no group.
 */
function Chips({ model, fold = false }: { model: ListModel; fold?: boolean }) {
  const query = useUi((s) => s.query)
  const areaF = useUi((s) => s.areaF)
  const projectF = useUi((s) => s.projectF)
  const projectName = projectF ? model.areas.flatMap((a) => a.projects).find((p) => p.key === projectF)?.name ?? '' : ''
  const statusF = useUi((s) => s.statusF)
  const ch = fold ? 'h-ch' : undefined
  const items = (
    <>
      {query.trim() ? <Chip className={ch} label="Recherche" value={`« ${query.trim()} »`} onRemove={() => setUi({ query: '' })} /> : null}
      {areaF ? <Chip className={ch} label="Aire" value={areaF.name} onRemove={() => setUi({ areaF: null })} /> : null}
      {projectF ? <Chip className={ch} label="Projet" value={projectName} onRemove={() => setUi({ projectF: null })} /> : null}
      {statusF ? <Chip className={ch} label="Statut" value={LABEL[statusF]} onRemove={() => setUi({ statusF: null })} /> : null}
      {model.filtering ? (
        <button type="button" className={cn('ghost sm', ch)} onClick={clearFilters}>
          Effacer tous les filtres
        </button>
      ) : null}
    </>
  )
  if (!fold) return items
  if (!query.trim() && !areaF && !projectF && !statusF && !model.filtering) return null
  return (
    <div className="h-chips">
      {items}
      <span className="h-more" hidden />
    </div>
  )
}

/** The header's controls on one line: scopes, search, filters, count, Abandonnés, grouping (absent while the first read is pending). */
function Controls({ model, level, plus = null }: { model: ListModel; level: 0 | 1; plus?: ReactNode }) {
  return (
    <>
      {level === 0 ? <ScopePills model={model} /> : <ScopeMenu model={model} />}
      {plus}
      <SearchField />
      <FilterMenu model={model} />
      <Chips model={model} />
      <span className="c-sp" />
      <Tools model={model} level={level} />
    </>
  )
}

/** Count, Abandonnés, grouping; level 2 folds the last two to icons and hides « sujets » visually. */
function Tools({ model, level }: { model: ListModel; level: 0 | 1 | 2 }) {
  const showDropped = useUi((s) => s.showDropped)
  const n = model.visible.length
  return (
    <div className="d-tools">
      <span className="fcount">
        {nf(n)}
        {level === 2 ? <span className="sr"> {n > 1 ? 'sujets' : 'sujet'}</span> : <> {n > 1 ? 'sujets' : 'sujet'}</>}
      </span>
      <button type="button" className="ghost sm" aria-pressed={showDropped}
        aria-label={level === 2 ? `${showDropped ? 'Masquer' : 'Voir'} les abandonnés · ${nf(model.dropped)}` : undefined}
        title={level === 2 ? `${showDropped ? 'Masquer' : 'Voir'} les abandonnés` : undefined}
        onClick={() => setUi({ showDropped: !showDropped })}>
        {level === 2 ? <Icon name="eyeoff" /> : showDropped ? 'Masquer les abandonnés' : 'Abandonnés'} · {nf(model.dropped)}
      </button>
      <GroupMenu icon={level === 2} />
    </div>
  )
}
