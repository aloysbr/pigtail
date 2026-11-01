import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import { ApiError, useApi } from '@/api/client'
import { useSend } from '@/api/mutate'
import { ROUTES } from '@/api/queries'
import type { SdkAnswer, SdkEvent, SdkList, SdkSession, SdkState } from '@/api/types'

/** The journal is re-read every second while the block is visible (R33, plan T14). */
export const SDK_EVENTS_MS = 1_000

/** A session nothing writes any more: its journal stops being read. `idle` is not one: it still waits for a reply. */
export function isTerminal(state: SdkState): boolean {
  return state === 'ended' || state === 'failed' || state === 'lost'
}

const sessionsKey = (subject: string) => ['sdk-sessions', subject] as const

/** What the sheet reads of a subject's SDK sessions. `off`: the option is not wired (503 SDK_OFF), the sheet shows nothing. `error`: a 503 that is not SDK_OFF (REGISTRY_UNREADABLE…), the block says so; any other failure still reads as « no session ». */
export interface SdkSessions {
  /** The first read has answered (data, off, or a failure). */
  ready: boolean
  off: boolean
  error: boolean
  sessions: SdkSession[]
  /** `-sdk-launch` is on: « Lancer ici » may show. */
  launchEnabled: boolean
}

/** GET /sdk-sessions?subject=<id>. Only the code SDK_OFF reads as `off`; any other 503 (REGISTRY_UNREADABLE…) is `error`, shown by the block; any other failure reads as « no session », never a toast (the block is optional). */
export function useSdkSessions(subject: string): SdkSessions {
  const get = useApi()
  const q = useQuery({
    queryKey: sessionsKey(subject),
    queryFn: async (): Promise<SdkList | 'off' | 'unreadable'> => {
      try {
        return await get<SdkList>(`${ROUTES.sdkSessions}?subject=${encodeURIComponent(subject)}`)
      } catch (e) {
        if (e instanceof ApiError && e.status === 503) return e.code === 'SDK_OFF' ? 'off' : 'unreadable'
        throw e
      }
    },
  })
  const d = q.data
  const off = d === 'off'
  const list = d && d !== 'off' && d !== 'unreadable' ? d : null
  return {
    ready: q.isSuccess || q.isError,
    off,
    error: d === 'unreadable',
    sessions: list?.sessions ?? [],
    launchEnabled: !!list?.launch_enabled,
  }
}

interface Journal {
  events: SdkEvent[]
  last_seq: number
}

/** The kinds that change what the block shows besides the thread: the session itself is re-read when one arrives. */
const MOVES = new Set(['state', 'request_opened', 'request_answered'])

/**
 * The journal of one session: the first read takes everything (`after=0`), each next one only `?after=<last_seq>` every
 * second, and a `seq` already held is never added twice. It stops once the session is ended, failed or lost (not idle).
 * A state or request event re-reads the subject's session list so the card and the state label follow.
 */
export function useSdkEvents(id: string, state: SdkState): { events: SdkEvent[]; ready: boolean } {
  const get = useApi()
  const qc = useQueryClient()
  const key = ['sdk-events', id] as const
  const q = useQuery({
    queryKey: key,
    refetchInterval: isTerminal(state) ? false : SDK_EVENTS_MS,
    queryFn: async (): Promise<Journal> => {
      const prev = qc.getQueryData<Journal>(key)
      const after = prev?.last_seq ?? 0
      const r = await get<{ events: SdkEvent[]; last_seq: number }>(`${ROUTES.sdkSessions}/${encodeURIComponent(id)}/events?after=${after}`)
      const held = new Set((prev?.events ?? []).map((e) => e.seq))
      const fresh = (r.events ?? []).filter((e) => !held.has(e.seq))
      return { events: [...(prev?.events ?? []), ...fresh], last_seq: Math.max(after, r.last_seq ?? after) }
    },
  })
  const last = q.data?.last_seq ?? 0
  const moved = q.data?.events.some((e) => e.seq === last && MOVES.has(e.kind)) ?? false
  useEffect(() => {
    if (moved) void qc.invalidateQueries({ queryKey: ['sdk-sessions'] })
  }, [last, moved, qc])
  return { events: q.data?.events ?? [], ready: q.isSuccess }
}

/** The writes of the block (cookie only, docs/wire-protocol.md § SDK sessions); each re-reads the sessions once settled. */
export function useSdkActions() {
  const send = useSend()
  const qc = useQueryClient()
  const settle = <T,>(p: Promise<T>): Promise<T> => {
    const done = () => void qc.invalidateQueries({ queryKey: ['sdk-sessions'] })
    p.then(done, done)
    return p
  }
  const base = (id: string) => `${ROUTES.sdkSessions}/${encodeURIComponent(id)}`
  return {
    launch: (subject: string) => settle(send<{ id: string; state: SdkState }>('POST', `/subjects/${encodeURIComponent(subject)}/sdk-sessions`)),
    answer: (id: string, rid: string, body: SdkAnswer) => settle(send<{ answered: boolean }>('POST', `${base(id)}/requests/${encodeURIComponent(rid)}/answer`, body)),
    say: (id: string, text: string) => settle(send<{ sent: boolean }>('POST', `${base(id)}/messages`, { text })),
    end: (id: string) => settle(send<{ ending: boolean }>('POST', `${base(id)}/end`)),
  }
}
