import { useMutation, useQueryClient, type QueryKey } from '@tanstack/react-query'
import { createContext, createElement, useContext, type ReactNode } from 'react'
import { ApiError, errorOf, type Send } from './client'

/** What the write client needs from the browser; injectable for the tests. */
export interface SendDeps {
  fetch: typeof fetch
}

/**
 * Same-origin writes with the session cookie only: JSON body, no auth header, no scope (the server
 * sets the master scope and the cross opt-in, docs/wire-protocol.md § Web). A 401 throws and
 * never reloads the page: the action's toast says the session expired (D21), the reads reload.
 */
export function makeSend(deps: Partial<SendDeps> = {}): Send {
  const doFetch = deps.fetch ?? ((...a: Parameters<typeof fetch>) => window.fetch(...a))
  return async <T,>(method: 'POST' | 'DELETE', path: string, body?: unknown): Promise<T> => {
    const headers: Record<string, string> = { Accept: 'application/json' }
    const init: RequestInit = { method, credentials: 'same-origin', headers }
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json'
      init.body = JSON.stringify(body)
    }
    let res: Response
    try {
      res = await doFetch(path, init)
    } catch (e) {
      throw new ApiError(0, e instanceof Error ? e.message : String(e))
    }
    if (!res.ok) throw await errorOf(res, path)
    const text = await res.text()
    if (!text) return undefined as T
    let parsed: { data?: T } | null
    try {
      parsed = JSON.parse(text) as { data?: T } | null
    } catch {
      throw new ApiError(res.status, `${path} : réponse illisible`, undefined, undefined, true)
    }
    return parsed?.data as T
  }
}

const SendContext = createContext<Send | null>(null)

/** Provides the write client; the app uses the default browser one. */
export function SendProvider({ value, children }: { value: Send; children: ReactNode }) {
  return createElement(SendContext.Provider, { value }, children)
}

let browserSend: Send | null = null

/** The write client of the nearest SendProvider, else the browser one. */
export function useSend(): Send {
  const ctx = useContext(SendContext)
  if (ctx) return ctx
  browserSend ??= makeSend()
  return browserSend
}

/** A write: its call, its optimistic change (returning the undo of that change) and the reads to refresh once settled. */
export interface WriteOptions<TVars, TData> {
  mutationFn: (vars: TVars) => Promise<TData>
  /** Applied to the cache at the call, before the answer; returns what puts the cache back. */
  optimistic?: (vars: TVars) => () => void
  /** The query keys invalidated (and re-read when shown) once the write settles, success or failure. */
  invalidate: readonly QueryKey[]
}

/**
 * A TanStack mutation with an optimistic change: applied at the call (after cancelling the reads it
 * touches, so a re-read in flight cannot overwrite it), rolled back on failure, then the keys are
 * invalidated whatever the outcome (D1: the list re-reads without waiting 10 s).
 */
export function useWrite<TVars, TData>(opts: WriteOptions<TVars, TData>) {
  const qc = useQueryClient()
  return useMutation<TData, unknown, TVars, { rollback?: () => void }>({
    mutationFn: opts.mutationFn,
    onMutate: async (vars) => {
      await Promise.all(opts.invalidate.map((queryKey) => qc.cancelQueries({ queryKey })))
      return { rollback: opts.optimistic?.(vars) }
    },
    onError: (_err, _vars, ctx) => {
      ctx?.rollback?.()
    },
    onSettled: () => {
      // not awaited: the caller's toast does not wait for the re-read
      for (const queryKey of opts.invalidate) void qc.invalidateQueries({ queryKey })
    },
  })
}
