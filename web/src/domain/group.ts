import type { Space, Subject, Theme } from '@/api/types'
import { compareRows } from './order'
import { fold } from './search'
import { displayStatus, LABEL, ORDER, type Display } from './status'
import { longTitle, shortTitle } from './title'

/** The four groupings of t10 (menu g then 1-4). */
export type Grouping = 'scope' | 'aire' | 'projet' | 'statut'
export const GROUPINGS: readonly Grouping[] = ['scope', 'aire', 'projet', 'statut']
/** The header label of each grouping (« Grouper : … »). */
export const GROUP_LABEL: Record<Grouping, string> = { scope: 'Scope › Aire', aire: 'Aire › Projet', projet: 'Projet', statut: 'Statut' }
/** The menu label of each grouping. */
export const GROUP_MENU: Record<Grouping, string> = { scope: 'Scope', aire: 'Aire', projet: 'Projet', statut: 'Statut' }

export const NO_AREA = 'Sans aire'
export const NO_PROJECT = 'Sans projet'

/** A subject with everything the list draws, computed once per read. */
export interface Row {
  s: Subject
  d: Display
  short: string
  long: string
  scope: string
  area: string
  theme: Theme | undefined
  projectKey: string
  projectName: string
  ongoing: boolean
}

/** A list group: level 1 (scope, area, project or status) or level 2 (area or project). */
export interface Group {
  key: string
  kind: 'scope' | 'area' | 'project' | 'status'
  level: 1 | 2
  label: string
  sub: string
  scope: string
  rows: Row[]
  children: Group[]
  count: number
  summary: Partial<Record<Display, number>>
  /** A project group's pie: done of total over the project's counted subjects (not dropped), before search and filters, as the sidebar's. */
  pie?: { done: number; total: number }
}

/** Joins each subject to its theme and display status. Scope = Subject.scope (never deduced from the area). */
export function enrich(subjects: readonly Subject[], themes: readonly Theme[], live: ReadonlySet<string>): Row[] {
  const byId = new Map(themes.map((t) => [t.id, t]))
  return subjects.map((s) => {
    const theme = s.theme_id ? byId.get(s.theme_id) : undefined
    const scope = s.scope || theme?.scope || ''
    return {
      s,
      d: displayStatus(s, live),
      short: shortTitle(s),
      long: longTitle(s),
      scope,
      area: theme?.space || '',
      theme,
      projectKey: theme ? `th:${theme.id}` : `th:none:${scope}`,
      projectName: theme ? theme.name : NO_PROJECT,
      ongoing: theme?.kind === 'ongoing',
    }
  })
}

/** The live peers' subject ids. */
export function liveSet(peers: readonly { subject_id?: string; alive?: boolean }[]): Set<string> {
  return new Set(peers.filter((p) => p.alive !== false && p.subject_id).map((p) => p.subject_id as string))
}

/** The scopes in display order: first seen in /spaces, then /themes, then the subjects. */
export function scopesOf(spaces: readonly Space[], themes: readonly Theme[], subjects: readonly Subject[]): string[] {
  const out: string[] = []
  for (const x of [...spaces.map((s) => s.scope), ...themes.map((t) => t.scope), ...subjects.map((s) => s.scope)]) {
    if (x && !out.includes(x)) out.push(x)
  }
  return out
}

/** Context the ordering needs: scope order and the /spaces folder order. */
export interface GroupCtx {
  scopes: readonly string[]
  spaces: readonly Space[]
}

function summarize(rows: readonly Row[]): Partial<Record<Display, number>> {
  const c: Partial<Record<Display, number>> = {}
  for (const r of rows) c[r.d] = (c[r.d] ?? 0) + 1
  return c
}

function bucket<K>(rows: readonly Row[], key: (r: Row) => K): Map<K, Row[]> {
  const m = new Map<K, Row[]>()
  for (const r of rows) {
    const k = key(r)
    const l = m.get(k)
    if (l) l.push(r)
    else m.set(k, [r])
  }
  return m
}

function sorted(rows: Row[]): Row[] {
  return rows.slice().sort(compareRows)
}

function make(kind: Group['kind'], level: 1 | 2, key: string, label: string, sub: string, scope: string, rows: Row[], children: Group[] = []): Group {
  return { key, kind, level, label, sub, scope, rows: children.length ? [] : sorted(rows), children, count: rows.length, summary: summarize(rows) }
}

function areaRank(ctx: GroupCtx, scope: string, area: string): [number, number, string] {
  const si = ctx.scopes.indexOf(scope)
  const s = si < 0 ? ctx.scopes.length : si
  if (!area) return [s, Number.MAX_SAFE_INTEGER, '']
  const i = ctx.spaces.findIndex((x) => x.name === area && x.scope === scope)
  return [s, i < 0 ? ctx.spaces.length : i, fold(area)]
}

function cmpTuple(a: [number, number, string], b: [number, number, string]): number {
  return a[0] - b[0] || a[1] - b[1] || (a[2] < b[2] ? -1 : a[2] > b[2] ? 1 : 0)
}

function areaGroups(rows: Row[], ctx: GroupCtx, level: 1 | 2, inner: (rows: Row[]) => Group[]): Group[] {
  const m = bucket(rows, (r) => `${r.scope}\u0000${r.area}`)
  return [...m.entries()]
    .sort(([a], [b]) => cmpTuple(areaRank(ctx, ...(a.split('\u0000') as [string, string])), areaRank(ctx, ...(b.split('\u0000') as [string, string]))))
    .map(([k, l]) => {
      const [scope, area] = k.split('\u0000')
      const label = area || NO_AREA
      return make('area', level, `sp:${scope}/${area}`, label, level === 1 ? `aire · ${scope}` : 'aire', scope, l, inner(l))
    })
}

function projectGroups(rows: Row[], level: 1 | 2): Group[] {
  const m = bucket(rows, (r) => r.projectKey)
  const best = (l: Row[]) => sorted(l)[0]
  return [...m.values()]
    .sort((a, b) => compareRows(best(a), best(b)) || (fold(a[0].projectName) < fold(b[0].projectName) ? -1 : 1))
    .map((l) => {
      const r = l[0]
      return make('project', level, r.projectKey, r.projectName, r.ongoing ? 'projet · continu' : 'projet', r.scope, l)
    })
}

/** Groups the rows of the list for a grouping; every row lands exactly once. */
export function buildGroups(rows: readonly Row[], grouping: Grouping, ctx: GroupCtx): Group[] {
  const all = rows.slice()
  if (grouping === 'statut') {
    const m = bucket(all, (r) => r.d)
    return ORDER.filter((d) => m.has(d)).map((d) => make('status', 1, `st:${d}`, LABEL[d], '', '', m.get(d) as Row[]))
  }
  if (grouping === 'projet') return projectGroups(all, 1)
  if (grouping === 'aire') return areaGroups(all, ctx, 1, (l) => projectGroups(l, 2))
  const m = bucket(all, (r) => r.scope)
  const order = (s: string) => {
    const i = ctx.scopes.indexOf(s)
    return i < 0 ? ctx.scopes.length : i
  }
  return [...m.entries()]
    .sort(([a], [b]) => order(a) - order(b) || (a < b ? -1 : 1))
    .map(([scope, l]) => make('scope', 1, `sc:${scope}`, scope || '—', 'scope', scope, l, areaGroups(l, ctx, 2, () => [])))
}
