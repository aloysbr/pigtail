import { createContext, createElement, useContext, type ReactNode } from 'react'

/**
 * A failed call: status is the HTTP status, 0 for a network error (daemon unreachable).
 * code and details come from the daemon's error body `{error:{code,message,details}}`;
 * unreadable is true when that body was not JSON (a plain-text 403, a proxy page).
 */
export class ApiError extends Error {
  readonly status: number
  readonly code?: string
  readonly details?: unknown
  readonly unreadable: boolean
  constructor(status: number, message: string, code?: string, details?: unknown, unreadable = false) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.details = details
    this.unreadable = unreadable
  }
}

/** Reads one JSON route of pigtaild; resolves to the `data` of its `{data, meta}` envelope. */
export type Get = <T>(path: string) => Promise<T>

/** Writes to one route of pigtaild (cookie writes allowlist, docs/wire-protocol.md § Web); resolves to the `data` of the answer. */
export type Send = <T>(method: 'POST' | 'DELETE', path: string, body?: unknown) => Promise<T>

/** The ApiError of a failed answer, read from the daemon's error body when it is JSON. */
export async function errorOf(res: Response, path: string): Promise<ApiError> {
  const text = await res.text().catch(() => '')
  try {
    const body = JSON.parse(text) as { error?: { code?: unknown; message?: unknown; details?: unknown } } | null
    const e = body && typeof body === 'object' ? body.error : undefined
    if (e && typeof e === 'object') {
      const code = typeof e.code === 'string' ? e.code : undefined
      const msg = typeof e.message === 'string' ? e.message : `${path} : HTTP ${res.status}`
      return new ApiError(res.status, msg, code, e.details)
    }
    return new ApiError(res.status, `${path} : HTTP ${res.status}`)
  } catch {
    return new ApiError(res.status, `${path} : HTTP ${res.status}`, undefined, undefined, true)
  }
}

type FlagStore = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

/** What the client needs from the browser; injectable for the tests. */
export interface ClientDeps {
  fetch: typeof fetch
  reload: () => void
  storage: FlagStore | null
}

const RELOADED = 'pigtail-reloaded-on-401'

function sessionStore(): FlagStore | null {
  try {
    return window.sessionStorage
  } catch {
    return null
  }
}

function flag(st: FlagStore | null, op: 'get' | 'set' | 'clear'): boolean {
  if (!st) return false
  try {
    if (op === 'get') return st.getItem(RELOADED) === '1'
    if (op === 'set') st.setItem(RELOADED, '1')
    else st.removeItem(RELOADED)
    return true
  } catch {
    return false
  }
}

/**
 * Same-origin reads with the session cookie only: no auth header, no scope (the server sets it).
 * A 401 reloads the page once so the server shows its « Lien expiré » page; a second 401 with
 * no 200 in between throws instead (no loop). Without sessionStorage it never reloads.
 */
export function makeClient(deps: Partial<ClientDeps> = {}): Get {
  const doFetch = deps.fetch ?? ((...a: Parameters<typeof fetch>) => window.fetch(...a))
  const reload = deps.reload ?? (() => window.location.reload())
  const storage = deps.storage !== undefined ? deps.storage : sessionStore()
  return async <T,>(path: string): Promise<T> => {
    let res: Response
    try {
      res = await doFetch(path, { credentials: 'same-origin', headers: { Accept: 'application/json' } })
    } catch (e) {
      throw new ApiError(0, e instanceof Error ? e.message : String(e))
    }
    if (res.status === 401) {
      if (!flag(storage, 'get') && flag(storage, 'set')) {
        reload()
        throw new ApiError(401, 'session expirée : rechargement')
      }
      throw new ApiError(401, 'session expirée')
    }
    if (!res.ok) {
      // The message stays « path : HTTP n »; the daemon's error code rides along for the callers that tell failures apart (SDK_OFF).
      const e = await errorOf(res, path)
      throw new ApiError(res.status, `${path} : HTTP ${res.status}`, e.code, e.details)
    }
    flag(storage, 'clear')
    const body = (await res.json()) as { data?: T | null } | null
    // Every success is {data, meta}; a null data is an empty list (Go's nil slice).
    return (body?.data ?? []) as T
  }
}

const ApiContext = createContext<Get | null>(null)

/** Provides the client to the queries; the app uses the default browser client. */
export function ApiProvider({ value, children }: { value: Get; children: ReactNode }) {
  return createElement(ApiContext.Provider, { value }, children)
}

let browserClient: Get | null = null

/** The client of the nearest ApiProvider, else the browser one. */
export function useApi(): Get {
  const ctx = useContext(ApiContext)
  if (ctx) return ctx
  browserClient ??= makeClient()
  return browserClient
}
