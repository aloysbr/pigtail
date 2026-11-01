import { useEffect, useRef } from 'react'
import { kindLabel, shownOutputs, useOutputGroups, useSubjectOutputs } from '@/api/outputs'
import type { OutputGroup, Subject } from '@/api/types'
import { getUi, openPreview, openReader } from '@/state/ui'
import { isImage, webUrl } from './images'
import { LinkRow, type LinkItem } from './LinkRow'
import { LINK_KEYS, linkDigit, openLink } from './linkKeys'

const SAFE = new Set(['http:', 'https:', 'obsidian:'])

/** The URL itself when its scheme is http, https or obsidian; null for anything else (javascript:, data:, file:, a relative path). */
export function safeHref(raw: string | undefined): string | null {
  if (!raw) return null
  let u: URL
  try {
    // no base: a relative path is not a link the sheet may follow
    u = new URL(raw)
  } catch {
    return null
  }
  return SAFE.has(u.protocol) ? raw.trim() : null
}

/** The « Note » link: obsidian://open?path= + the vault path, encoded (same target as the board, handlers/board.go). */
export function noteHref(vaultPath: string): string {
  return `obsidian://open?path=${encodeURIComponent(vaultPath)}`
}

function basename(p: string): string {
  const parts = p.split('/').filter(Boolean)
  return parts[parts.length - 1] ?? p
}

/**
 * Whether a deliverable of this kind opens in the reader (D4) rather than a tab: html only, so mockups, reports and pages
 * do, a pdf opens its tab (pending D10; the one test to change if it moves).
 */
export function opensInReader(kind: string): boolean {
  return kind === 'mockup' || kind === 'report' || kind === 'html'
}

const MONTHS = ['janv.', 'févr.', 'mars', 'avr.', 'mai', 'juin', 'juil.', 'août', 'sept.', 'oct.', 'nov.', 'déc.']

function dayKey(d: Date): string {
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`
}

/** When a round was made: « auj. 18:04 », « hier 15:11 », « 12 oct. 09:30 », « 12 oct. 2025 » another year (local time). */
export function tourWhen(ts: number, now: Date = new Date()): string {
  const d = new Date(ts)
  const hm = `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  if (dayKey(d) === dayKey(now)) return `auj. ${hm}`
  const y = new Date(now)
  y.setDate(y.getDate() - 1)
  if (dayKey(d) === dayKey(y)) return `hier ${hm}`
  const day = `${d.getDate()} ${MONTHS[d.getMonth()]}`
  return d.getFullYear() === now.getFullYear() ? `${day} ${hm}` : `${day} ${d.getFullYear()}`
}

/** « 21 tours · t23, hier 15:11 », « 1 tour · auj. 18:04 » outside a round, « · 1 introuvable » when rounds left the disk. */
export function groupCount(g: OutputGroup, now: Date = new Date()): string {
  const n = g.tours.length
  const last = g.tours[n - 1]
  const when = last ? tourWhen(last.ts, now) : ''
  const latest = last && last.n > 0 ? `${last.label}, ${when}` : when
  const gone = g.tours.filter((t) => !t.exists).length
  return `${n} ${n === 1 ? 'tour' : 'tours'}${latest ? ` · ${latest}` : ''}${gone ? ` · ${gone} ${gone === 1 ? 'introuvable' : 'introuvables'}` : ''}`
}

/** Numbers the safe links and the deliverable lines 1-9 in order; a line with no safe href is skipped. */
function numbered(items: LinkItem[]): LinkItem[] {
  let n = 0
  return items.map((it) => ((it.href || it.group !== undefined) && n < LINK_KEYS ? { ...it, digit: ++n } : it))
}

/** The sheet's digit keys, while it is open and no layer above it owns the keys. */
function useLinkKeys(items: LinkItem[], onImage: (index: number) => void, onGroup: (key: string) => void) {
  const ref = useRef({ items, onImage, onGroup })
  ref.current = { items, onImage, onGroup }
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // a digit handleKey already used (g / v / f menus) is not a link key
      if (e.defaultPrevented) return
      const ui = getUi()
      if (!ui.panel || ui.menu || ui.palette || ui.settings || ui.preview || ui.reader) return
      const d = linkDigit(e)
      if (d === null) return
      const it = ref.current.items.find((x) => x.digit === d)
      if (!it) return
      if (it.group !== undefined) {
        e.preventDefault()
        ref.current.onGroup(it.group)
        return
      }
      if (!it.href) return
      e.preventDefault()
      if (it.image !== undefined) ref.current.onImage(it.image)
      else openLink(it.href)
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [])
}

/**
 * « Liens »: the vault note, then one line per deliverable group (D2, in order of first registration), then the other
 * outputs with their type (the grouped ones left out); t10's empty sentence when there is none.
 */
export function Links({ subject }: { subject: Subject }) {
  const q = useSubjectOutputs(subject.id)
  // an older daemon (no route) or a failed read: no group, the flat list as before
  const gq = useOutputGroups(subject.id)
  const groups = (gq.data ?? []).filter((g) => g.tours.length > 0 && opensInReader(g.kind)).sort((a, b) => a.first_seq - b.first_seq)
  const grouped = new Set(groups.flatMap((g) => g.tours.map((t) => t.output_id)))
  const raw: LinkItem[] = []
  if (subject.vault_path) {
    raw.push({ key: 'note', chip: 'Note', text: basename(subject.vault_path), href: noteHref(subject.vault_path), title: subject.vault_path })
  }
  const now = new Date()
  for (const g of groups) {
    raw.push({ key: `g:${g.key}`, chip: kindLabel(g.kind), text: g.name, href: null, title: g.key, group: g.key, count: groupCount(g, now) })
  }
  let img = 0
  for (const o of shownOutputs(q.data ?? [])) {
    if (grouped.has(o.id)) continue
    const web = webUrl(o)
    const target = web || o.path || o.key
    // the same test and order as imageLinks: the k-th image link is the preview's k-th image
    const image = isImage(o) ? img++ : undefined
    raw.push({ key: o.id, chip: kindLabel(o.kind), text: o.title || target, href: safeHref(web), title: target, image })
  }
  const items = numbered(raw)
  const n = items.filter((it) => it.digit).length
  const onImage = (index: number) => openPreview(subject.id, index)
  const onGroup = (key: string) => openReader(subject.id, key)
  useLinkKeys(items, onImage, onGroup)
  return (
    <section className="lp-sec" aria-labelledby="ln-h">
      <h3 className="lp-h3" id="ln-h">
        Liens
        {n ? (
          <span className="lp-hint" aria-hidden="true">
            <kbd>1</kbd>
            {n > 1 ? (
              <>
                –<kbd>{n}</kbd>
              </>
            ) : null}{' '}
            ouvre
          </span>
        ) : null}
      </h3>
      {items.length ? (
        <ul className="links">
          {items.map((it) => (
            <LinkRow key={it.key} it={it} onImage={onImage} onGroup={onGroup} />
          ))}
        </ul>
      ) : q.isPending ? (
        <p className="muted">Chargement…</p>
      ) : (
        <p className="muted">Aucun lien : la PR et les notes du vault apparaîtront ici.</p>
      )}
    </section>
  )
}
