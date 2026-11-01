import { useQuery } from '@tanstack/react-query'
import { useApi, type Get } from './client'
import { subjectRoute } from './subject'
import type { LogEntry } from './types'

/** Newest first, whatever order the daemon sent (it sends newest first; a tie keeps its order). */
export function newestFirst(entries: readonly LogEntry[]): LogEntry[] {
  const t = (e: LogEntry) => {
    const v = Date.parse(e.ts)
    return Number.isNaN(v) ? 0 : v
  }
  return entries
    .map((e, i) => [e, i] as const)
    .sort(([a, i], [b, j]) => t(b) - t(a) || i - j)
    .map(([e]) => e)
}

/** The pigtail log of the open subject (GET /subjects/{id}/log), re-read with the backlog. */
export function useSubjectLog(id: string) {
  const get = useApi()
  return useQuery(logQuery(get, id))
}

/** The query of a subject's log, shared by the sheet and `h` (which fetches it to resume a Standby). */
export function logQuery(get: Get, id: string) {
  return { queryKey: ['log', id], queryFn: () => get<LogEntry[]>(subjectRoute(id, 'log')) }
}
