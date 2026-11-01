import type { Backlog } from '@/api/queries'
import type { Capture, Theme } from '@/api/types'
import { buildGroups, enrich, NO_AREA, liveSet, scopesOf, type Group, type Grouping, type Row } from '@/domain/group'
import { fold, matches } from '@/domain/search'
import type { Display } from '@/domain/status'
import type { UiState } from '@/state/ui'

/** Rows per project (En cours + Ouvert) before « Afficher les N autres » (spec § Ligne). */
export const PROJECT_CAP = 6
/** Rows per status group in the Statut grouping (t10: 12 En cours, 8 others). */
export const STATUS_CAP: Record<Display, number> = { cours: 12, ouvert: 8, standby: 8, done: 8, dropped: 8 }
const FOLDED: readonly Display[] = ['standby', 'done', 'dropped']

/** One area of the sidebar and of the filter menu. */
export interface AreaItem {
  scope: string
  name: string
  count: number
  /** The projects of the area that have a subject shown (not dropped), for the sidebar's project rows (D16). */
  projects: ProjectItem[]
}

/** One project row of the sidebar: its pie (done of total) and its open count. */
export interface ProjectItem {
  /** The group key `th:<theme id>`, also what `projectF` holds. */
  key: string
  name: string
  done: number
  total: number
  open: number
}

/** The projects of one area of one scope, from the subjects the list counts. */
function projectsOf(base: readonly Row[], scope: string, area: string): ProjectItem[] {
  const m = new Map<string, ProjectItem>()
  for (const r of base) {
    if (r.scope !== scope || r.area !== area) continue
    const p = m.get(r.projectKey) ?? { key: r.projectKey, name: r.projectName, done: 0, total: 0, open: 0 }
    p.total += 1
    if (r.d === 'done') p.done += 1
    else p.open += 1
    m.set(r.projectKey, p)
  }
  return [...m.values()].sort((a, b) => (fold(a.name) < fold(b.name) ? -1 : fold(a.name) > fold(b.name) ? 1 : 0))
}

/** Everything the shell draws, computed once per read and per UI change. */
export interface ListModel {
  /** Every subject read, dropped and captured included (the sheet and the route find theirs here). */
  rows: Row[]
  /** The subjects of the active scopes the list counts (not dropped nor captured), before search and filters: the palette searches them. */
  base: Row[]
  scopes: string[]
  active: string[]
  scopeCounts: Record<string, number>
  total: number
  dropped: number
  areas: AreaItem[]
  visible: Row[]
  groups: Group[]
  grouping: Grouping
  filtering: boolean
  order: string[]
  groupOf: Map<string, string>
  /** The open, unpromoted captures of each project shown (key `th:<theme id>`, newest first); Aire and Projet groupings only (D14). */
  captures: Record<string, Capture[]>
  /** The ids of those captures: a selection among them is a capture, not a subject (no h, l, ⌥, ⏎). */
  capIds: Set<string>
}

/** The fold key of a project's « Captures · n » line. */
export function captureKey(themeId: string): string {
  return `cap:${themeId}`
}

/** A part of a group body: shown rows (with how many are hidden), a folded status line, or the project's Captures line. */
export type Part =
  | { type: 'rows'; key: string; rows: Row[]; hidden: number }
  | { type: 'line'; key: string; d: Display; count: number; open: boolean; part: Part | null }
  | { type: 'caps'; key: string; caps: Capture[]; open: boolean }

/** Whether a group is open, from the UI state or its default (t10: level 1 open except projects, level 2 open while filtering). */
export function isOpen(ui: Pick<UiState, 'open'>, g: Group, grouping: Grouping, filtering: boolean): boolean {
  const v = ui.open[g.key]
  if (v !== undefined) return v
  if (g.kind === 'status') return g.key === 'st:cours' || g.key === 'st:ouvert'
  if (g.level === 1) return grouping !== 'projet' || filtering
  return filtering
}

function capped(key: string, rows: Row[], cap: number, more: Record<string, boolean>): Part {
  const lim = more[key] ? rows.length : cap
  return { type: 'rows', key, rows: rows.slice(0, lim), hidden: Math.max(0, rows.length - lim) }
}

/** The body of a leaf group: En cours + Ouvert (capped), then one folded line per Standby, Done, Abandonné, then its captures' line. */
export function bodyOf(g: Group, ui: Pick<UiState, 'open' | 'more'>, caps?: readonly Capture[]): Part[] {
  if (g.kind === 'status') return [capped(g.key, g.rows, STATUS_CAP[g.key.slice(3) as Display], ui.more)]
  const act = g.rows.filter((r) => r.d === 'cours' || r.d === 'ouvert')
  const parts: Part[] = act.length ? [capped(g.key, act, PROJECT_CAP, ui.more)] : []
  for (const d of FOLDED) {
    const rows = g.rows.filter((r) => r.d === d)
    if (!rows.length) continue
    const key = `${g.key}:${d}`
    const open = ui.open[key] ?? false
    parts.push({ type: 'line', key, d, count: rows.length, open, part: open ? capped(key, rows, PROJECT_CAP, ui.more) : null })
  }
  if (g.kind === 'project' && caps?.length) {
    const key = captureKey(g.key.slice(3))
    parts.push({ type: 'caps', key, caps: caps.slice(), open: ui.open[key] ?? false })
  }
  return parts
}

type Walk = { order: string[]; groupOf: Map<string, string>; captures: Record<string, Capture[]> }

function walk(gs: Group[], ui: UiState, grouping: Grouping, filtering: boolean, out: Walk) {
  for (const g of gs) {
    if (!isOpen(ui, g, grouping, filtering)) continue
    if (g.children.length) {
      walk(g.children, ui, grouping, filtering, out)
      continue
    }
    for (const p of bodyOf(g, ui, out.captures[g.key])) {
      if (p.type === 'caps') {
        if (!p.open) continue
        for (const c of p.caps) {
          out.order.push(c.id)
          out.groupOf.set(c.id, p.key)
        }
        continue
      }
      const rp = p.type === 'rows' ? p : p.part
      if (!rp || rp.type !== 'rows') continue
      for (const r of rp.rows) {
        out.order.push(r.s.id)
        out.groupOf.set(r.s.id, p.key)
      }
    }
  }
}

const newestFirst = (a: Capture, b: Capture) => (a.created_at < b.created_at ? 1 : a.created_at > b.created_at ? -1 : 0)

/** The open, unpromoted captures of the active scopes that the filters keep, by project key (D14; no status, so none under a status filter). */
function capturesBy(caps: readonly Capture[], themes: readonly Theme[], active: readonly string[], ui: UiState): Record<string, Capture[]> {
  const out: Record<string, Capture[]> = {}
  if (ui.group !== 'aire' && ui.group !== 'projet') return out
  if (ui.statusF) return out
  const byId = new Map(themes.map((t) => [t.id, t]))
  const q = fold(ui.query.trim())
  for (const c of caps) {
    if (c.promoted_subject_id || c.closed_at) continue
    const t = byId.get(c.theme_id)
    if (!t) continue
    const scope = c.scope || t.scope || ''
    if (!active.includes(scope)) continue
    if (ui.areaF && (ui.areaF.scope !== scope || (t.space || NO_AREA) !== ui.areaF.name)) continue
    if (ui.projectF && ui.projectF !== `th:${t.id}`) continue
    if (q && !fold(c.text).includes(q)) continue
    ;(out[`th:${t.id}`] ??= []).push(c)
  }
  for (const k of Object.keys(out)) out[k].sort(newestFirst)
  return out
}

function emptyGroup(kind: 'area' | 'project', level: 1 | 2, key: string, label: string, sub: string, scope: string): Group {
  return { key, kind, level, label, sub, scope, rows: [], children: [], count: 0, summary: {} }
}

function findGroup(gs: readonly Group[], key: string): Group | undefined {
  for (const g of gs) {
    if (g.key === key) return g
    const c = findGroup(g.children, key)
    if (c) return c
  }
  return undefined
}

/** Adds an empty project group (and its area in Aire › Projet) for each project that has captures but no subject shown, at the end. */
function addCaptureProjects(groups: Group[], caps: Record<string, Capture[]>, themes: readonly Theme[], grouping: Grouping): Group[] {
  const out = groups.slice()
  for (const t of themes) {
    const key = `th:${t.id}`
    if (!caps[key] || findGroup(out, key)) continue
    const scope = t.scope || ''
    const sub = t.kind === 'ongoing' ? 'projet · continu' : 'projet'
    if (grouping === 'projet') {
      out.push(emptyGroup('project', 1, key, t.name, sub, scope))
      continue
    }
    const area = t.space || ''
    const aKey = `sp:${scope}/${area}`
    let a = out.find((g) => g.key === aKey)
    if (!a) {
      a = emptyGroup('area', 1, aKey, area || NO_AREA, `aire · ${scope}`, scope)
      out.push(a)
    } else {
      // a fresh object: the group read from buildGroups is not mutated
      a = { ...a, children: a.children.slice() }
      out[out.findIndex((g) => g.key === aKey)] = a
    }
    a.children.push(emptyGroup('project', 2, key, t.name, sub, scope))
  }
  return out
}

/** Builds the model of the list from the reads and the UI state. */
export function buildModel(data: Pick<Backlog, 'subjects' | 'themes' | 'spaces' | 'peers'> & { captures?: readonly Capture[] }, ui: UiState): ListModel {
  const rows = enrich(data.subjects, data.themes, liveSet(data.peers))
  const scopes = scopesOf(data.spaces, data.themes, data.subjects)
  let active = scopes.filter((x) => !ui.scopesOff.includes(x))
  if (!active.length) active = scopes
  const scopeCounts: Record<string, number> = {}
  for (const r of rows) if (r.d !== 'dropped') scopeCounts[r.scope] = (scopeCounts[r.scope] ?? 0) + 1
  const inScope = rows.filter((r) => active.includes(r.scope))
  const base = inScope.filter((r) => r.d !== 'dropped')
  // scope by scope, as the sidebar lists them, so the filter menu's digits match the sidebar order
  const areas = active
    .flatMap((sc): AreaItem[] => {
      const own = data.spaces
        .filter((sp) => sp.scope === sc)
        .map((sp) => ({ scope: sc, name: sp.name, count: base.filter((r) => r.scope === sc && r.area === sp.name).length, projects: projectsOf(base, sc, sp.name) }))
      // subjects whose theme has no space (or no theme) get their own entry, so the areas add up to the scope's total
      const none = base.filter((r) => r.scope === sc && !r.area).length
      return none ? [...own, { scope: sc, name: NO_AREA, count: none, projects: projectsOf(base, sc, '') }] : own
    })
  const filtering = !!(ui.query.trim() || ui.areaF || ui.projectF || ui.statusF)
  const visible = inScope.filter(
    (r) =>
      (ui.showDropped || r.d !== 'dropped') &&
      (!ui.areaF || (r.scope === ui.areaF.scope && (r.area || NO_AREA) === ui.areaF.name)) &&
      (!ui.projectF || r.projectKey === ui.projectF) &&
      (!ui.statusF || r.d === ui.statusF) &&
      matches(r.s, ui.query),
  )
  const captures = capturesBy(data.captures ?? [], data.themes, active, ui)
  const groups = addCaptureProjects(buildGroups(visible, ui.group, { scopes, spaces: data.spaces }), captures, data.themes, ui.group)
  setPies(groups, base)
  const out: Walk = { order: [], groupOf: new Map<string, string>(), captures }
  walk(groups, ui, ui.group, filtering, out)
  return {
    rows,
    base,
    scopes,
    active,
    scopeCounts,
    total: base.length,
    dropped: inScope.length - base.length,
    areas,
    visible,
    groups,
    grouping: ui.group,
    filtering,
    order: out.order,
    groupOf: out.groupOf,
    captures,
    capIds: new Set(Object.values(captures).flatMap((l) => l.map((c) => c.id))),
  }
}

/** Gives each project group the pie of its base rows (the sidebar's figure), whatever the filters show. */
function setPies(groups: readonly Group[], base: readonly Row[]) {
  for (const g of groups) {
    if (g.kind === 'project') {
      const mine = base.filter((r) => r.projectKey === g.key)
      g.pie = { done: mine.filter((r) => r.d === 'done').length, total: mine.length }
    }
    setPies(g.children, base)
  }
}

function holds(g: Group, id: string): boolean {
  return g.children.length ? g.children.some((c) => holds(c, id)) : g.rows.some((r) => r.s.id === id)
}

/** The folds and « more » to open so a subject's row shows: its group chain, its folded status line, past-the-cap rows (palette). */
export function revealPath(groups: readonly Group[], id: string): { open: Record<string, boolean>; more: Record<string, boolean> } {
  const open: Record<string, boolean> = {}
  const more: Record<string, boolean> = {}
  let level = groups
  for (;;) {
    const g = level.find((x) => holds(x, id))
    if (!g) break
    open[g.key] = true
    if (g.children.length) {
      level = g.children
      continue
    }
    const row = g.rows.find((r) => r.s.id === id) as Row
    if (g.kind === 'status') {
      if (g.rows.indexOf(row) >= STATUS_CAP[row.d]) more[g.key] = true
    } else if (row.d === 'cours' || row.d === 'ouvert') {
      if (g.rows.filter((r) => r.d === 'cours' || r.d === 'ouvert').indexOf(row) >= PROJECT_CAP) more[g.key] = true
    } else {
      const key = `${g.key}:${row.d}`
      open[key] = true
      if (g.rows.filter((r) => r.d === row.d).indexOf(row) >= PROJECT_CAP) more[key] = true
    }
    break
  }
  return { open, more }
}

/** The folds to open so a project's « Captures · n » line shows unfolded: its group chain, then the line itself. */
export function revealCapture(groups: readonly Group[], themeId: string): Record<string, boolean> {
  const key = `th:${themeId}`
  const open: Record<string, boolean> = {}
  const path = (gs: readonly Group[]): boolean => {
    for (const g of gs) {
      if (g.key === key || path(g.children)) {
        open[g.key] = true
        return true
      }
    }
    return false
  }
  if (path(groups)) open[captureKey(themeId)] = true
  return open
}
