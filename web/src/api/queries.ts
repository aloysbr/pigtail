import { focusManager, keepPreviousData, QueryClient, useQuery } from '@tanstack/react-query'
import { ApiError, useApi } from './client'
import type { Capture, Peer, Space, Subject, Theme } from './types'

/** The reads of the backlog (D4: every ~10 s and on return to the tab). */
export const ROUTES = {
  subjects: '/subjects?scope=all',
  themes: '/themes?scope=all',
  spaces: '/spaces',
  peers: '/peers',
  /** Every session, paused and ended ones included (≈ 450 Ko against 16 Ko): read by the Sessions view and an open sheet only (usePeersAll). */
  peersAll: '/peers?all=1',
  /** Open captures of every scope (« Captures · n » of each project). */
  captures: '/captures?scope=all&open=1',
  /** SDK sessions (docs/wire-protocol.md § SDK sessions); read per subject by the sheet's block only (web/src/sdk/api.ts). */
  sdkSessions: '/sdk-sessions',
} as const

/** The re-read interval (D4); after a failure the next attempt comes this long after it (the interval restarts on each update). */
export const REFETCH_MS = 10_000

/** The QueryClient of the web front: ~10 s re-read while visible, previous data kept across a re-read. */
export function makeQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        refetchInterval: REFETCH_MS,
        refetchIntervalInBackground: false,
        refetchOnWindowFocus: true,
        placeholderData: keepPreviousData,
        staleTime: 0,
        retry: (n, err) => !(err instanceof ApiError && err.status === 401) && n < 1,
      },
    },
  })
}

/**
 * Re-read on return to the tab: TanStack's focus signal listens to visibilitychange only;
 * this adds the window's focus event (a visible window brought back to the front).
 * Returns the function that puts the default listener back.
 */
export function installFreshness(): () => void {
  focusManager.setEventListener((handleFocus) => {
    const onChange = () => handleFocus()
    window.addEventListener('visibilitychange', onChange, false)
    document.addEventListener('visibilitychange', onChange, false)
    window.addEventListener('focus', onChange, false)
    return () => {
      window.removeEventListener('visibilitychange', onChange)
      document.removeEventListener('visibilitychange', onChange)
      window.removeEventListener('focus', onChange)
    }
  })
  return () => {
    focusManager.setEventListener((handleFocus) => {
      const l = () => handleFocus()
      window.addEventListener('visibilitychange', l, false)
      return () => window.removeEventListener('visibilitychange', l)
    })
  }
}

/** What the list reads: the five collections. `ready` waits for four of them; peers (live marks only, slow on a loaded Mac) enrich the rows when they arrive. */
export interface Backlog {
  subjects: Subject[]
  themes: Theme[]
  spaces: Space[]
  peers: Peer[]
  /** GET /peers has answered at least once (« N vivantes » waits for it). */
  peersReady: boolean
  /** The open captures (GET /captures?scope=all&open=1), re-read with the rest. */
  captures: Capture[]
  ready: boolean
  error: ApiError | null
  updatedAt: number
  /** When the next attempt runs after a failed read (the earliest failure + REFETCH_MS); null while every read is fine. */
  nextRetryAt: number | null
  /** A read is in flight (first read or re-read). */
  fetching: boolean
}

const EMPTY: never[] = []

/** The backlog's five queries. */
export function useBacklog(): Backlog {
  const get = useApi()
  const subjects = useQuery({ queryKey: ['subjects'], queryFn: () => get<Subject[]>(ROUTES.subjects) })
  const themes = useQuery({ queryKey: ['themes'], queryFn: () => get<Theme[]>(ROUTES.themes) })
  const spaces = useQuery({ queryKey: ['spaces'], queryFn: () => get<Space[]>(ROUTES.spaces) })
  const peers = useQuery({ queryKey: ['peers'], queryFn: () => get<Peer[]>(ROUTES.peers) })
  const captures = useQuery({ queryKey: ['captures'], queryFn: () => get<Capture[]>(ROUTES.captures) })
  const all = [subjects, themes, spaces, peers, captures]
  const err = all.find((q) => q.error)?.error
  const failed = all.filter((q) => q.isError)
  return {
    subjects: subjects.data ?? EMPTY,
    themes: themes.data ?? EMPTY,
    spaces: spaces.data ?? EMPTY,
    peers: peers.data ?? EMPTY,
    peersReady: peers.data !== undefined,
    captures: captures.data ?? EMPTY,
    // peers is left out: GET /peers can take minutes and only marks live sessions; its failure still reaches `error`
    ready: [subjects, themes, spaces, captures].every((q) => q.data !== undefined),
    error: err instanceof ApiError ? err : err ? new ApiError(0, String(err)) : null,
    updatedAt: Math.max(...all.map((q) => q.dataUpdatedAt)),
    nextRetryAt: failed.length ? Math.min(...failed.map((q) => q.errorUpdatedAt)) + REFETCH_MS : null,
    fetching: all.some((q) => q.isFetching),
  }
}
