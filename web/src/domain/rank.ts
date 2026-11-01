import type { Subject } from '@/api/types'
import { compareRows } from './order'
import type { Display } from './status'

/** One rank write of POST /subjects/ranks: the rank read (`from`, 0 = unranked) and the rank wanted (`to`). */
export interface RankChange {
  id: string
  from: number
  to: number
}

/** A line of the displayed group: En cours + Ouvert rows of one group, in display order. */
export interface RankLine {
  subject: Subject
  display: Display
}

const rankOf = (s: Subject) => s.rank ?? 0
const scopeOf = (s: Subject) => s.scope ?? ''

/** Copies of the subjects with the changes applied (to 0 = unranked). */
export function applyChanges(list: readonly Subject[], changes: readonly RankChange[]): Subject[] {
  const to = new Map(changes.map((c) => [c.id, c.to]))
  return list.map((s) => {
    const r = to.get(s.id)
    if (r === undefined) return s
    const next: Subject = { ...s, rank: r || undefined }
    if (!r) delete next.rank
    return next
  })
}

/** Ranked subjects of a scope whose rank passes the test, except the given ids, each shifted +1. */
function shift(scope: readonly Subject[], sc: string, test: (r: number) => boolean, except: readonly string[]): RankChange[] {
  return scope
    .filter((x) => scopeOf(x) === sc && rankOf(x) > 0 && test(rankOf(x)) && !except.includes(x.id))
    .map((x) => ({ id: x.id, from: rankOf(x), to: rankOf(x) + 1 }))
}

/**
 * The rank writes of ⌥↑ (dir -1) / ⌥↓ (dir 1) on a line of its displayed group (D4, D11, D18, D20).
 * [] when nothing must happen: group edge, En cours | Ouvert border, neighbour of another scope,
 * two unranked lines, a ranked line moving down onto an unranked one. Two ranked lines swap their
 * values; on a tie r the line going up keeps r, the other takes r+1 and every other ranked subject
 * of the scope above r shifts +1; an unranked line moving up onto a ranked neighbour n takes n, and
 * the neighbour with every ranked subject of the scope at n or more (but itself) shifts +1.
 * A rank those writes would leave tied with the mover or its neighbour is then pushed (untie), so
 * the line moves exactly one notch whatever the tie-break.
 */
export function planMove(group: readonly RankLine[], scopeSubjects: readonly Subject[], id: string, dir: -1 | 1): RankChange[] {
  const base = baseMove(group, scopeSubjects, id, dir)
  if (!base.length) return []
  const i = group.findIndex((l) => l.subject.id === id)
  return untie(base, group[i].subject, group[i + dir].subject, group, scopeSubjects, dir)
}

/** The tail of compareRows: in a rank tie, the newest first, then the id. */
function tieBefore(a: Subject, b: Subject): boolean {
  const ta = Date.parse(a.updated_at) || 0
  const tb = Date.parse(b.updated_at) || 0
  if (ta !== tb) return ta > tb
  return a.id < b.id
}

/**
 * Makes a plan's result independent of the tie-break (verify F1): the ranked subjects of the scope
 * in their order with the mover placed next to its neighbour, walked once; a rank equal to the one
 * before it is kept only when neither line moves and the tie-break already orders them, else it
 * takes that rank + 1, and the push carries on down while it meets the next ranks.
 */
function untie(base: RankChange[], m: Subject, n: Subject, group: readonly RankLine[], scopeSubjects: readonly Subject[], dir: -1 | 1): RankChange[] {
  const sc = scopeOf(m)
  const all = new Map<string, Subject>()
  for (const x of [...scopeSubjects, ...group.map((l) => l.subject)]) if (scopeOf(x) === sc) all.set(x.id, x)
  const after = new Map(base.map((c) => [c.id, c.to]))
  const rankAfter = (x: Subject) => after.get(x.id) ?? rankOf(x)
  const seq = [...all.values()]
    .filter((x) => rankOf(x) > 0 && x.id !== m.id)
    .sort((a, b) => rankOf(a) - rankOf(b) || (tieBefore(a, b) ? -1 : 1))
  const at = seq.findIndex((x) => x.id === n.id)
  seq.splice(dir === -1 ? at : at + 1, 0, m)
  const moving = (x: Subject) => x.id === m.id || x.id === n.id
  const final = new Map<string, number>()
  let prev: Subject | null = null
  let prevRank = 0
  for (const x of seq) {
    let r = Math.max(rankAfter(x), prevRank)
    if (prev && r === prevRank && (moving(prev) || moving(x) || !tieBefore(prev, x))) r = prevRank + 1
    final.set(x.id, r)
    prev = x
    prevRank = r
  }
  const out: RankChange[] = []
  const seen = new Set<string>()
  const push = (x: Subject) => {
    const to = final.get(x.id) ?? rankAfter(x)
    seen.add(x.id)
    if (to !== rankOf(x)) out.push({ id: x.id, from: rankOf(x), to })
  }
  for (const c of base) {
    const x = all.get(c.id)
    if (x) push(x)
  }
  for (const x of seq) if (!seen.has(x.id)) push(x)
  return out
}

function baseMove(group: readonly RankLine[], scopeSubjects: readonly Subject[], id: string, dir: -1 | 1): RankChange[] {
  const i = group.findIndex((l) => l.subject.id === id)
  const j = i + dir
  if (i < 0 || j < 0 || j >= group.length) return []
  const mover = group[i]
  const nb = group[j]
  const movable = (d: Display) => d === 'cours' || d === 'ouvert'
  if (!movable(mover.display) || mover.display !== nb.display) return []
  const sc = scopeOf(mover.subject)
  if (scopeOf(nb.subject) !== sc) return []
  const m = mover.subject
  const n = nb.subject
  const rm = rankOf(m)
  const rn = rankOf(n)
  if (rm && rn && rm !== rn) {
    return [
      { id: m.id, from: rm, to: rn },
      { id: n.id, from: rn, to: rm },
    ]
  }
  if (rm && rn) {
    const up = dir === -1 ? m : n
    const down = up === m ? n : m
    return [{ id: down.id, from: rm, to: rm + 1 }, ...shift(scopeSubjects, sc, (r) => r > rm, [m.id, n.id])]
  }
  if (!rm && rn && dir === -1) {
    return [{ id: m.id, from: 0, to: rn }, ...shift(scopeSubjects, sc, (r) => r >= rn, [m.id])]
  }
  return []
}

function sortLines(lines: readonly RankLine[]): RankLine[] {
  return lines
    .map((l) => ({ l, s: l.subject, d: l.display }))
    .sort(compareRows)
    .map((x) => x.l)
}

/**
 * The rank writes of a drop of `dragId` on `overId` (any line of the group): planMove step by step,
 * one notch at a time on the already moved state, until the dragged line stands where the target was.
 * One empty step = [] (no partial write, D18, D20); else the steps merged per id (`from` = the rank
 * read, `to` = the final one), the ids back at their first rank left out.
 */
export function planDrop(group: readonly RankLine[], scopeSubjects: readonly Subject[], dragId: string, overId: string): RankChange[] {
  const from = group.findIndex((l) => l.subject.id === dragId)
  const target = group.findIndex((l) => l.subject.id === overId)
  if (from < 0 || target < 0 || from === target) return []
  const dir = target > from ? 1 : -1
  const first = new Map<string, number>()
  const last = new Map<string, number>()
  let lines = group.slice()
  let scope = scopeSubjects.slice()
  let at = from
  while (at !== target) {
    const ch = planMove(lines, scope, dragId, dir)
    if (!ch.length) return []
    for (const c of ch) {
      if (!first.has(c.id)) first.set(c.id, c.from)
      last.set(c.id, c.to)
    }
    const moved = new Map(applyChanges(lines.map((l) => l.subject), ch).map((s) => [s.id, s]))
    lines = sortLines(lines.map((l) => ({ ...l, subject: moved.get(l.subject.id) ?? l.subject })))
    scope = applyChanges(scope, ch)
    const now = lines.findIndex((l) => l.subject.id === dragId)
    if (now !== at + dir) return []
    at = now
  }
  const out: RankChange[] = []
  for (const [cid, f] of first) {
    const t = last.get(cid) as number
    if (f !== t) out.push({ id: cid, from: f, to: t })
  }
  return out
}
