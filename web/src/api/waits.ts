import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useApi } from './client'
import { useSend, useWrite } from './mutate'
import { REFETCH_MS } from './queries'
import type { Wait, WaitAnswer, WaitAnswerRequest } from './types'

/** The re-read of the waits while a session is followed live (the dock open): a new wait, an answer, an end show within 3 s. */
export const WAITS_LIVE_MS = 3_000

/** Every waits query lives under this key: an answer's optimistic change and its invalidation cover them all. */
export const WAITS_KEY = ['waits'] as const

/** GET /waits of a subject: its open waits, then up to `closed` closed ones, most recent first (docs/wire-protocol.md § Waits). */
export function subjectWaitsRoute(subjectId: string, closed: number): string {
  return `/waits?subject=${encodeURIComponent(subjectId)}&closed=${closed}`
}

/** GET /waits of one session, whatever its subject: the dock never mixes two sessions (D8). */
export function sessionWaitsRoute(sessionId: string, closed: number): string {
  return `/waits?session=${encodeURIComponent(sessionId)}&closed=${closed}`
}

/** A subject's waits; every 3 s when `live`, else with the backlog (REFETCH_MS). Never waits for /peers (R33). */
export function useSubjectWaits(subjectId: string, opts: { closed?: number } = {}, live = false) {
  const get = useApi()
  const closed = opts.closed ?? 2
  return useQuery({
    queryKey: [...WAITS_KEY, 'subject', subjectId, closed],
    queryFn: () => get<Wait[]>(subjectWaitsRoute(subjectId, closed)),
    enabled: !!subjectId,
    refetchInterval: live ? WAITS_LIVE_MS : REFETCH_MS,
  })
}

/** One session's waits (a session picked in the dock's list, maybe of another subject); same cadence. */
export function useSessionWaits(sessionId: string, opts: { closed?: number } = {}, live = false) {
  const get = useApi()
  const closed = opts.closed ?? 2
  return useQuery({
    queryKey: [...WAITS_KEY, 'session', sessionId, closed],
    queryFn: () => get<Wait[]>(sessionWaitsRoute(sessionId, closed)),
    enabled: !!sessionId,
    refetchInterval: live ? WAITS_LIVE_MS : REFETCH_MS,
  })
}

/** An answer in words, as the session reads it: « B · Fenêtre — note », the text, « Vu ». */
export function answerText(w: Pick<Wait, 'details'>, a: Pick<WaitAnswer, 'choice' | 'text' | 'seen' | 'reason' | 'items'>): string {
  let head = ''
  if (a.choice) {
    const o = w.details?.options?.find((x) => x.id === a.choice)
    head = o ? `${o.id} · ${o.label}` : a.choice
  } else if (a.text) head = a.text
  else if (a.seen) head = 'Vu'
  else if (a.items?.length) head = a.items.map((v) => `${v.n} ${v.ok ? 'ok' : 'à revoir'}`).join(', ')
  return a.reason ? `${head} — ${a.reason}` : head
}

/** The variables of an answer: the wait and the body sent. */
export interface AnswerVars {
  id: string
  req: WaitAnswerRequest
}

/**
 * Answers a wait (POST /waits/{id}/answer, cookie allowlist): optimistic (R19) — every cached copy of the wait reads as
 * answered at once, with the answer — rolled back on a refusal; the waits and the sessions re-read once it settles.
 */
export function useAnswerWait() {
  const qc = useQueryClient()
  const send = useSend()
  return useWrite<AnswerVars, unknown>({
    mutationFn: ({ id, req }) => send('POST', `/waits/${encodeURIComponent(id)}/answer`, req),
    optimistic: ({ id, req }) => {
      const before = qc.getQueriesData<Wait[]>({ queryKey: WAITS_KEY })
      const answer: WaitAnswer = { ...req, at: new Date().toISOString() }
      qc.setQueriesData<Wait[]>({ queryKey: WAITS_KEY }, (list) =>
        list?.map((w) => (w.id === id ? { ...w, state: 'answered', closed_at: answer.at, answer } : w)),
      )
      return () => {
        for (const [key, data] of before) qc.setQueryData(key, data)
      }
    },
    invalidate: [WAITS_KEY, ['peers']],
  })
}
