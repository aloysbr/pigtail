import type { Subject } from '@/api/types'
import { displayStatus, ORDER, type Display } from './status'

const IDX = Object.fromEntries(ORDER.map((k, i) => [k, i])) as Record<Display, number>

/** Compares two subjects already given their displayed status: status order, ranked first by rank, then newest. */
export function compareRows(a: { s: Subject; d: Display }, b: { s: Subject; d: Display }): number {
  const d = IDX[a.d] - IDX[b.d]
  if (d) return d
  const ra = a.s.rank ?? 0
  const rb = b.s.rank ?? 0
  if (!!ra !== !!rb) return ra ? -1 : 1
  if (ra !== rb) return ra - rb
  const ta = Date.parse(a.s.updated_at) || 0
  const tb = Date.parse(b.s.updated_at) || 0
  if (ta !== tb) return tb - ta
  return a.s.id < b.s.id ? -1 : a.s.id > b.s.id ? 1 : 0
}

/** A sorted copy: En cours, Ouvert, Standby, Done; ranked before unranked, rank ascending; newest next. */
export function sortSubjects(list: readonly Subject[], live: ReadonlySet<string>): Subject[] {
  return list
    .map((s) => ({ s, d: displayStatus(s, live) }))
    .sort(compareRows)
    .map((x) => x.s)
}
