import { useQueryClient } from '@tanstack/react-query'
import { ApiError, useApi } from '@/api/client'
import { logQuery, newestFirst } from '@/api/log'
import { useSend, useWrite } from '@/api/mutate'
import { refusalToast } from '@/api/refusals'
import type { LogEntry, Peer, Subject } from '@/api/types'
import { canToggleStandby, nextStatus } from '@/domain/standby'
import { pushToast } from '@/ui/toasts'

/** The copies of Standby / Reprendre (spec § Décidé à la spec, D9-2, D9-3, D9-10). */
export const STANDBY_COPY = {
  parked: (x: string) => `« ${x} » en standby`,
  parkedLive: (x: string, session: string) => `« ${x} » en standby · la session ${session} reste ouverte`,
  resumed: (x: string, status: string) => `« ${x} » repris · Ouvert (${status})`,
  restored: (x: string, parked: boolean) => `« ${x} » rétabli en ${parked ? 'Standby' : 'Ouvert'}`,
}

interface StatusVars {
  id: string
  status: string
}

/**
 * `h` and the sheet's switch (spec § Standby / Reprendre): an Ouvert goes parked, a Standby back to
 * the status its log names (D6). Writes the status only, never touches a session; optimistic, then
 * a toast with its undo, or the refusal with the screen put back. Done, Abandonné, a capture: nothing.
 */
export function useToggleStandby(): (id: string) => Promise<void> {
  const qc = useQueryClient()
  const get = useApi()
  const send = useSend()
  const w = useWrite<StatusVars, Subject>({
    mutationFn: ({ id, status }) => send<Subject>('POST', `/subjects/${encodeURIComponent(id)}`, { status }),
    optimistic: ({ id, status }) => {
      const prev = qc.getQueryData<Subject[]>(['subjects'])
      if (prev) qc.setQueryData(['subjects'], prev.map((s) => (s.id === id ? { ...s, status } : s)))
      return () => qc.setQueryData(['subjects'], prev)
    },
    invalidate: [['subjects'], ['peers'], ['log']],
  })
  return async (id) => {
    const s = qc.getQueryData<Subject[]>(['subjects'])?.find((x) => x.id === id)
    if (!s || !canToggleStandby(s)) return
    const title = s.short_title || s.title
    const from = s.status
    // only a Standby needs its log (D6); an Ouvert makes no other call
    let log: LogEntry[] = []
    if (from === 'parked') {
      try {
        log = newestFirst(await qc.fetchQuery(logQuery(get, id)))
      } catch (e) {
        pushToast({ text: refusalToast(asApi(e), { title, scope: s.scope, action: 'write' }) })
        return
      }
    }
    const to = nextStatus(s, log)
    try {
      await w.mutateAsync({ id, status: to })
    } catch (e) {
      pushToast({ text: refusalToast(asApi(e), { title, scope: s.scope, action: 'write' }) })
      return
    }
    const peer = (qc.getQueryData<Peer[]>(['peers']) ?? []).find((p) => p.subject_id === id && p.alive)
    const text =
      to === 'parked'
        ? peer
          ? STANDBY_COPY.parkedLive(title, peer.cell_id || peer.session_id)
          : STANDBY_COPY.parked(title)
        : STANDBY_COPY.resumed(title, to)
    pushToast({
      text,
      undo: {
        title,
        label: STANDBY_COPY.restored(title, from === 'parked'),
        run: async () => {
          await w.mutateAsync({ id, status: from })
        },
        // read at the daemon, not the cache: a CLI change since the action must win (D9-9)
        stillValid: async () => (await get<Subject>(`/subjects/${encodeURIComponent(id)}`)).status === to,
      },
    })
  }
}

function asApi(e: unknown): ApiError {
  return e instanceof ApiError ? e : new ApiError(0, String(e))
}
