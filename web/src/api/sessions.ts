import { useQuery } from '@tanstack/react-query'
import { useApi } from './client'
import { ROUTES } from './queries'
import type { Peer } from './types'

/** The cache key of GET /peers?all=1: under ['peers'], so a mutation's invalidate of ['peers'] covers it by prefix. */
export const PEERS_ALL_KEY = ['peers', 'all'] as const

/**
 * Every session (GET /peers?all=1): the Sessions view, its panel and the linked sessions of a subject sheet share this
 * one query. `enabled` is off unless one of them is on screen: ≈ 450 Ko a read, never polled in the background.
 */
export function usePeersAll(opts: { enabled?: boolean } = {}) {
  const get = useApi()
  return useQuery({ queryKey: PEERS_ALL_KEY, queryFn: () => get<Peer[]>(ROUTES.peersAll), enabled: opts.enabled ?? true })
}
