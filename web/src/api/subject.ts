import type { Row } from '@/domain/group'

// The sheet's subject comes from the backlog already read (D6: computed client-side): no extra GET /subjects/{id}.

/** The path of a subject's sub-route; the id is escaped (pigtail ids never need it, but a hash route could carry anything). */
export function subjectRoute(id: string, sub: 'log' | 'outputs'): string {
  return `/subjects/${encodeURIComponent(id)}/${sub}`
}

/** The enriched row of a subject among every subject read (dropped and captured included), undefined when gone. */
export function findRow(rows: readonly Row[], id: string | null): Row | undefined {
  if (!id) return undefined
  return rows.find((r) => r.s.id === id)
}
