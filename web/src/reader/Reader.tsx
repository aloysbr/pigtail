import { useCallback, useEffect, useRef, useState } from 'react'
import { groupLabel, kindLabel, useOutputGroups } from '@/api/outputs'
import type { OutputGroup, OutputTour } from '@/api/types'
import { isTyping } from '@/lib/utils'
import { tourWhen } from '@/panel/Links'
import { closeLayer, getUi, setUi, useUi } from '@/state/ui'
import { ToastHost } from '@/ui/Toast'
import { SessionDock } from './SessionDock'
import { ToursMenu } from './ToursMenu'

/** What stays live in the reader: the session dock of the subject (lot 4, mounted inside the layer, so never inert); the toasts are mounted inside the layer too. */
export const READER_KEEP = '[data-session-dock]'

function Icon({ d, className = 'ql-ic' }: { d: string; className?: string }) {
  return (
    <svg className={className} viewBox="0 0 16 16" aria-hidden="true">
      <path d={d} fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

const I_X = 'M4.5 4.5l7 7M11.5 4.5l-7 7'
const I_LEFT = 'M10 3.5 5.5 8l4.5 4.5'
const I_RIGHT = 'M6 3.5 10.5 8 6 12.5'
const I_CHEV = 'M4.5 6.5 8 10l3.5-3.5'
const I_EXT = 'M9.5 2.5h4v4M13.5 2.5 8 8M12 9.5v3a1 1 0 0 1-1 1H3.5a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1h3'

/** The line of the group in « Liens », to give it the focus back. */
function lineOf(key: string): HTMLElement | null {
  return document.querySelector<HTMLElement>(`[data-group="${CSS.escape(key)}"]`)
}

/** The index of round `n` in the group (the last of that number), the latest when null or gone. */
export function tourIndex(g: OutputGroup, n: number | null): number {
  const last = g.tours.length - 1
  if (n === null) return last
  for (let i = last; i >= 0; i -= 1) if (g.tours[i].n === n) return i
  return last
}

function go(g: OutputGroup, index: number) {
  const r = getUi().reader
  if (!r || index < 0 || index >= g.tours.length) return
  setUi({ reader: { ...r, tour: index === g.tours.length - 1 ? null : g.tours[index].n } })
}

/** « hier 15:11 » stays, a date takes « le » (« le 12 oct. 09:30 »). */
function onDay(when: string): string {
  return /^\d/.test(when) ? `le ${when}` : when
}

/**
 * Marks inert every element outside the dialog and the kept ones (toasts, session dock): for each ancestor of a kept
 * element, its other children. Returns the undo.
 */
function inertAround(win: HTMLElement): () => void {
  const keep = [win, ...Array.from(document.querySelectorAll<HTMLElement>(READER_KEEP))]
  const path = new Set<Element>()
  for (const k of keep) for (let n: Element | null = k; n && n !== document.body; n = n.parentElement) path.add(n)
  const set: Element[] = []
  for (const n of [document.body, ...path]) {
    if (keep.includes(n as HTMLElement)) continue
    for (const c of Array.from(n.children)) {
      if (path.has(c) || c.hasAttribute('inert')) continue
      c.setAttribute('inert', '')
      set.push(c)
    }
  }
  return () => set.forEach((e) => e.removeAttribute('inert'))
}

/** The stage of a round that cannot show: gone from the disk (t1), or not served (the outputs server is off). */
function Missing({ t, latest, onLatest }: { t: OutputTour; latest: OutputTour | null; onLatest: () => void }) {
  return (
    <div className="ql-miss">
      <div>
        {!t.exists ? (
          <>
            <h3>{t.label} n’est plus sur le disque</h3>
            <p>
              Ce tour a été enregistré {onDay(tourWhen(t.ts))}, mais son fichier <code>{t.path}</code> a été déplacé ou supprimé.
            </p>
            <p>Choisis un autre tour dans la liste, ou remets le fichier à sa place : pigtail le montrera de nouveau, sans rien d’autre à faire.</p>
          </>
        ) : (
          <>
            <h3>{t.label} ne peut pas être affiché</h3>
            <p>
              Son fichier <code>{t.path}</code> est sur le disque, mais pigtaild ne sert pas les livrables en ce moment (son second port est éteint ou pris).
            </p>
            <p>Relance pigtaild, puis rouvre ce tour.</p>
          </>
        )}
        {latest ? (
          <button type="button" className="btnl" onClick={onLatest}>
            Voir la dernière version ({latest.label})
          </button>
        ) : null}
      </div>
    </div>
  )
}

/**
 * The full-page reader (D4, t2 « .rd »): a modal layer over the app, its 44 px bar (×, name, « Maquette · N tours »,
 * the round's title and date, the key hints, ‹ tN▾ ›, « Ouvrir dans un onglet »), the old-version band, and the round
 * in a sandboxed iframe at its served_url. Esc (handleKey) or × close it and give the focus back to its line; ← → walk
 * the rounds outside a field. Everything behind is inert but the session dock; the toasts live inside the layer.
 */
export function Reader({ subjectId, group, tour }: { subjectId: string; group: OutputGroup; tour: number | null }) {
  const win = useRef<HTMLDivElement>(null)
  const [menu, setMenu] = useState(false)
  const key = useRef(group.key)
  key.current = group.key
  const count = group.tours.length
  const i = tourIndex(group, tour)
  const t = group.tours[i]
  const lastI = count - 1
  const isLast = i === lastI
  const multi = count > 1
  const kind = kindLabel(group.kind)
  const shown = t.exists && t.served_url ? t.served_url : null

  // modal: everything behind inert, the focus into the layer on open; on close the inert goes first, then the focus
  // returns to the group's line, found again by its data-group (a re-read may have re-rendered it). A browser refuses
  // the focus to an element under [inert]: two separate effects ran the focus before the inert was lifted.
  useEffect(() => {
    const w = win.current
    const undo = w ? inertAround(w) : () => {}
    w?.focus()
    return () => {
      undo()
      lineOf(key.current)?.focus()
    }
  }, [])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Tab cycles inside the layer
      if (e.key === 'Tab' && !e.metaKey && !e.ctrlKey && !e.altKey) {
        const w = win.current
        if (!w) return
        const f = Array.from(w.querySelectorAll<HTMLElement>('button, a[href]')).filter((x) => x.tabIndex >= 0)
        if (!f.length) return
        const at = document.activeElement
        const first = f[0]
        const last = f[f.length - 1]
        if (!w.contains(at) || at === w) {
          e.preventDefault()
          ;(e.shiftKey ? last : first).focus()
        } else if (e.shiftKey && at === first) {
          e.preventDefault()
          last.focus()
        } else if (!e.shiftKey && at === last) {
          e.preventDefault()
          first.focus()
        }
        return
      }
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey || e.shiftKey || isTyping(e.target)) return
      if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
        e.preventDefault()
        const r = getUi().reader
        if (r) go(group, tourIndex(group, r.tour) + (e.key === 'ArrowRight' ? 1 : -1))
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [group])

  const tourBtn = useRef<HTMLButtonElement>(null)
  const closeMenu = useCallback((refocus: boolean) => {
    setMenu(false)
    if (refocus) tourBtn.current?.focus()
  }, [])
  const toLatest = () => go(group, lastI)
  const step = (dir: -1 | 1) => {
    const off = dir < 0 ? i === 0 : isLast
    return (
      <button
        type="button"
        className="ql-z ico"
        aria-label={dir < 0 ? 'Tour précédent (←)' : 'Tour suivant (→)'}
        aria-disabled={off ? 'true' : undefined}
        onClick={() => {
          if (!off) go(group, i + dir)
        }}
      >
        <Icon d={dir < 0 ? I_LEFT : I_RIGHT} />
      </button>
    )
  }
  const before = lastI - i

  return (
    <div ref={win} className="rd" role="dialog" aria-modal="true" aria-label={`${kind} ${group.name}`} tabIndex={-1}>
      <div className="rd-h">
        <button type="button" className="ib" aria-label="Fermer (Esc)" onClick={() => closeLayer()}>
          <Icon d={I_X} />
        </button>
        <span className="rd-n">
          <b className="rd-nm" title={group.name}>
            {group.name}
          </b>
          <span className="rd-sub">
            <span className="ql-i">{groupLabel(group)}</span>
            <span className="dsep" aria-hidden="true">
              ·
            </span>
            <span className="ql-meta">
              <b>{t.title || t.label}</b> · {tourWhen(t.ts)}
              {multi && isLast ? ' · dernière version' : ''}
            </span>
          </span>
        </span>
        <span className="c-sp" />
        {multi ? (
          <span className="ql-hint">
            <kbd>←</kbd>
            <kbd>→</kbd> tour
          </span>
        ) : null}
        <span className="ql-hint">
          <kbd>Esc</kbd> fermer
        </span>
        <div className="rd-tools">
          {multi ? (
            <>
              {step(-1)}
              <button ref={tourBtn} type="button" className="ql-z tour" aria-haspopup="menu" aria-expanded={menu} onClick={() => setMenu((m) => !m)}>
                {t.label}
                <Icon d={I_CHEV} className="chv" />
              </button>
              {step(1)}
              {menu ? (
                <ToursMenu
                  tours={group.tours}
                  current={i}
                  onPick={(n) => {
                    closeMenu(true)
                    go(group, n)
                  }}
                  onClose={closeMenu}
                />
              ) : null}
              <span className="rd-gap" />
            </>
          ) : null}
          {/* under 480 px the text goes (reader.css), the aria-label keeps the name */}
          {shown ? (
            <a className="ql-z rd-ext" href={shown} target="_blank" rel="noopener noreferrer" aria-label="Ouvrir dans un onglet">
              <Icon d={I_EXT} />
              <span className="rd-ext-t">Ouvrir dans un onglet</span>
            </a>
          ) : (
            <a className="ql-z rd-ext" aria-disabled="true" aria-label="Ouvrir dans un onglet">
              <Icon d={I_EXT} />
              <span className="rd-ext-t">Ouvrir dans un onglet</span>
            </a>
          )}
        </div>
      </div>
      {!isLast ? (
        <div className="rd-old">
          <span className="rd-old-t">
            Version ancienne · <b>{t.label}</b>, {before} {before === 1 ? 'tour' : 'tours'} avant la dernière.
          </span>
          <button type="button" className="ql-z" onClick={toLatest}>
            Revenir à la dernière
          </button>
        </div>
      ) : null}
      {/* the round, and beside or over it the session dock of the subject (lot 4: band, card or sheet) */}
      <div className="rd-body">
      <div className="rd-st">
        {shown ? (
          <iframe
            key={t.output_id}
            src={shown}
            sandbox="allow-scripts"
            tabIndex={-1}
            title={`${kind} ${group.name}, ${t.n > 0 ? `tour ${t.label}` : 'tour unique'}`}
          />
        ) : (
          <Missing t={t} latest={isLast ? null : group.tours[lastI]} onLatest={toLatest} />
        )}
      </div>
      <SessionDock subjectId={subjectId} />
      </div>
      {/* the toasts over the layer, live (the shell mounts none while the reader is open) */}
      <ToastHost />
    </div>
  )
}

/** The reader of the open subject's group, mounted by the shell while `ui.reader` is set; a group gone closes it. */
export function ReaderLayer() {
  const r = useUi((s) => s.reader)
  const q = useOutputGroups(r?.subjectId ?? '')
  const group = r ? (q.data ?? []).find((g) => g.key === r.groupKey && g.tours.length > 0) : undefined
  const gone = !!r && !q.isPending && !group
  // nothing to show: drop the reader, or handleKey would swallow the keys behind an empty layer
  useEffect(() => {
    if (gone) setUi({ reader: null })
  }, [gone])
  if (!r || !group) return null
  return <Reader subjectId={r.subjectId} group={group} tour={r.tour} />
}
