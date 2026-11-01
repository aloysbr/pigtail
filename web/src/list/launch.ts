import { useQueryClient } from '@tanstack/react-query'
import { useSyncExternalStore } from 'react'
import { ApiError, useApi } from '@/api/client'
import { useSend } from '@/api/mutate'
import { refusalToast } from '@/api/refusals'
import type { Peer, SessionLaunch, Subject } from '@/api/types'
import { liveSet } from '@/domain/group'
import { displayStatus, type Display } from '@/domain/status'
import { pushToast } from '@/ui/toasts'

/** GET /launches/{id} is read every 2 s while a launch is pending (spec § Lancer). */
export const LAUNCH_POLL_MS = 2_000
/** « Lancement… » is bounded: past 60 s with no outcome, the button comes back (D13). */
export const LAUNCH_TIMEOUT_MS = 60_000
/** The launch copies, t10's word for word (spec § Copies). */
export const LAUNCH_COPY = {
  opening: (cell: string) => `Ouverture d'une session dans la ${cell}`,
  opened: (cell: string, tab: string) => `Session ouverte dans cmux · ${cell}${tab ? ` · onglet « ${tab} »` : ''}`,
  failed: (x: string, cause: string) => `Le lancement de « ${x} » a échoué : ${cause.replace(/[.\s]+$/, '')}.`,
  timeout: 'Pas de réponse : vérifie cmux',
  live: (cell: string) => `Déjà en cours dans la ${cell}.`,
}

/** Lancer is offered on an Ouvert or a Standby subject only: never En cours (no « Rejoindre », D2), Done, Abandonné. */
export function canLaunch(_s: Subject, d: Display): boolean {
  return d === 'ouvert' || d === 'standby'
}

/* The launches in flight, shared by the row, the sheet and `l` (one « Lancement… » per subject, wherever it shows). */
/** Per launch: the next poll of its follow-up, and the one 60 s bound armed at the press (D13). */
interface Flight {
  poll: ReturnType<typeof setTimeout> | null
  deadline: ReturnType<typeof setTimeout> | null
}
const pending = new Map<string, Flight>()
const listeners = new Set<() => void>()

function emit() {
  for (const l of listeners) l()
}

function stop(f: Flight | undefined) {
  if (f?.poll) clearTimeout(f.poll)
  if (f?.deadline) clearTimeout(f.deadline)
}

/** Ends a launch's wait (outcome, refusal or the 60 s bound): both its timers cancelled, the button back. */
function release(id: string) {
  stop(pending.get(id))
  pending.delete(id)
  emit()
}

/** Schedules the next poll of a launch still in flight. */
function schedule(id: string, fn: () => void) {
  const f = pending.get(id)
  if (f) f.poll = setTimeout(fn, LAUNCH_POLL_MS)
}

function subscribe(l: () => void): () => void {
  listeners.add(l)
  return () => listeners.delete(l)
}

/** True while the subject's launch waits for its outcome. */
export function useLaunching(id: string): boolean {
  return useSyncExternalStore(
    subscribe,
    () => pending.has(id),
    () => false,
  )
}

/** Forgets every launch in flight and stops their follow-ups (tests). */
export function resetLaunches() {
  for (const f of pending.values()) stop(f)
  pending.clear()
  emit()
}

function cellOf(cell: string | undefined, scope = ''): string {
  return cell ? `cellule ${cell}` : `cellule du scope ${scope}`
}

function asApi(e: unknown): ApiError {
  return e instanceof ApiError ? e : new ApiError(0, String(e))
}

/**
 * Lancer (spec § Lancer, D1, D2, D13, D19): `POST /subjects/{id}/launch`, the button « Lancement… » while it runs (a second
 * press does nothing), the toast with the exact prompt, then `GET /launches/{launch_id}` every 2 s until launched (toast
 * with the cell and the daemon's tab name, /peers re-read) or failed (its cause), 60 s at most. A refusal: its toast, the
 * button back. En cours: where it runs, no call. Writes no status.
 */
export function useLaunch(): (id: string) => void {
  const qc = useQueryClient()
  const get = useApi()
  const send = useSend()

  const follow = (s: Subject, title: string, first: SessionLaunch) => {
    const tab = first.tab_title
    const tick = async () => {
      if (!pending.has(s.id)) return
      let l: SessionLaunch | null = null
      try {
        l = await get<SessionLaunch>(`/launches/${encodeURIComponent(first.launch_id)}`)
      } catch {
        // a missed read is retried at the next tick, the 60 s bound still holds
      }
      if (!pending.has(s.id)) return
      if (l?.state === 'launched') {
        release(s.id)
        pushToast({ text: LAUNCH_COPY.opened(cellOf(l.cell || first.cell, s.scope), tab) })
        void qc.invalidateQueries({ queryKey: ['peers'] })
        return
      }
      if (l?.state === 'failed') {
        release(s.id)
        pushToast({ text: LAUNCH_COPY.failed(title, l.cause || 'cause inconnue') })
        return
      }
      schedule(s.id, () => void tick())
    }
    schedule(s.id, () => void tick())
  }

  return (id) => {
    const s = (qc.getQueryData<Subject[]>(['subjects']) ?? []).find((x) => x.id === id)
    if (!s || pending.has(id)) return
    const peers = qc.getQueryData<Peer[]>(['peers']) ?? []
    const d = displayStatus(s, liveSet(peers))
    if (d === 'cours') {
      const p = peers.find((x) => x.subject_id === id && x.alive)
      pushToast({ text: LAUNCH_COPY.live(cellOf(p?.cell_id, s.scope)) })
      return
    }
    if (!canLaunch(s, d)) return
    const title = s.short_title || s.title
    // one 60 s bound from the press, the POST included, whatever the polling does (D13)
    pending.set(id, {
      poll: null,
      deadline: setTimeout(() => {
        if (!pending.has(id)) return
        release(id)
        pushToast({ text: LAUNCH_COPY.timeout })
      }, LAUNCH_TIMEOUT_MS),
    })
    emit()
    void (async () => {
      let l: SessionLaunch
      try {
        l = await send<SessionLaunch>('POST', `/subjects/${encodeURIComponent(id)}/launch`)
      } catch (e) {
        // the 60 s bound already gave the button back: a late refusal says nothing more
        if (!pending.has(id)) return
        release(id)
        pushToast({ text: refusalToast(asApi(e), { title, scope: s.scope, action: 'launch' }) })
        return
      }
      if (!pending.has(id)) return
      pushToast({ text: LAUNCH_COPY.opening(cellOf(l?.cell, s.scope)) })
      if (!l?.launch_id) {
        // no id to follow: the next /peers read shows the session
        release(id)
        void qc.invalidateQueries({ queryKey: ['peers'] })
        return
      }
      follow(s, title, l)
    })()
  }
}
