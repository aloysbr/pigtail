import { act, renderHook, waitFor } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, ApiProvider, makeClient } from './client'
import { installFreshness, makeQueryClient, REFETCH_MS, ROUTES, useBacklog } from './queries'
import { usePeersAll } from './sessions'
import { PEERS_ALL, PEERS_ALL_BODY } from '@/test/peers-fixture'

let visibility: DocumentVisibilityState = 'visible'

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

/** The daemon's real success shape: every route answers {data, meta}. */
function envelope(data: unknown) {
  return { data, meta: { request_id: 'req_x' } }
}

/** A fetch stub that answers every list route with an empty list, counting the calls per path. */
function stubFetch() {
  const calls: string[] = []
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    calls.push(url)
    expect(init?.credentials).toBe('same-origin')
    const h = new Headers(init?.headers)
    expect(h.has('Authorization')).toBe(false)
    expect(h.has('X-Pigtail-Scope')).toBe(false)
    return json(envelope([]))
  })
  return { fn, calls, count: (p: string) => calls.filter((c) => c === p).length }
}

function setup(fetchFn: typeof fetch) {
  const qc = makeQueryClient()
  const get = makeClient({ fetch: fetchFn, reload: vi.fn(), storage: window.sessionStorage })
  const wrapper = ({ children }: { children: ReactNode }) => (
    <ApiProvider value={get}>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </ApiProvider>
  )
  return { qc, wrapper }
}

describe('freshness', () => {
  let uninstall: () => void
  beforeEach(() => {
    visibility = 'visible'
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => visibility })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] })
    uninstall = installFreshness()
  })
  afterEach(() => {
    uninstall()
    vi.useRealTimers()
  })

  it('reads the five routes (the open captures included), scope all where it applies, with no auth or scope header', async () => {
    const f = stubFetch()
    const { wrapper } = setup(f.fn as unknown as typeof fetch)
    const { result } = renderHook(() => useBacklog(), { wrapper })
    await vi.waitFor(() => expect(result.current.ready).toBe(true))
    expect(f.calls.sort()).toEqual(['/captures?scope=all&open=1', '/peers', '/spaces', '/subjects?scope=all', '/themes?scope=all'])
  })

  it('re-reads the captures with the rest, every ~10 s', async () => {
    const f = stubFetch()
    const { wrapper } = setup(f.fn as unknown as typeof fetch)
    renderHook(() => useBacklog(), { wrapper })
    await vi.waitFor(() => expect(f.count('/captures?scope=all&open=1')).toBe(1))
    await act(() => vi.advanceTimersByTimeAsync(10_000))
    expect(f.count('/captures?scope=all&open=1')).toBe(2)
  })

  it('re-reads every ~10 s while the tab is visible', async () => {
    const f = stubFetch()
    const { wrapper } = setup(f.fn as unknown as typeof fetch)
    renderHook(() => useBacklog(), { wrapper })
    await vi.waitFor(() => expect(f.count('/subjects?scope=all')).toBe(1))
    await act(() => vi.advanceTimersByTimeAsync(10_000))
    expect(f.count('/subjects?scope=all')).toBe(2)
    await act(() => vi.advanceTimersByTimeAsync(10_000))
    expect(f.count('/subjects?scope=all')).toBe(3)
  })

  it('after a failure, nextRetryAt is the instant TanStack really re-reads (errorUpdatedAt + REFETCH_MS)', async () => {
    let calls = 0
    const down = vi.fn(async () => {
      calls++
      throw new TypeError('Failed to fetch')
    })
    const { wrapper } = setup(down as unknown as typeof fetch)
    const { result } = renderHook(() => useBacklog(), { wrapper })
    // the first read plus its one retry (1 s later) both fail; the failure time is the retry's
    await act(() => vi.advanceTimersByTimeAsync(1_500))
    await vi.waitFor(() => expect(result.current.nextRetryAt).not.toBeNull())
    const at = result.current.nextRetryAt as number
    expect(at - Date.now()).toBeGreaterThan(0)
    expect(at - Date.now()).toBeLessThanOrEqual(REFETCH_MS)
    const before = calls
    await act(() => vi.advanceTimersByTimeAsync(at - Date.now() - 50))
    expect(calls).toBe(before)
    await act(() => vi.advanceTimersByTimeAsync(100))
    expect(calls).toBeGreaterThan(before)
  })

  it('hidden, no interval reads; visible again → one read; a window focus → one more', async () => {
    const f = stubFetch()
    const { wrapper } = setup(f.fn as unknown as typeof fetch)
    renderHook(() => useBacklog(), { wrapper })
    await vi.waitFor(() => expect(f.count('/peers')).toBe(1))

    visibility = 'hidden'
    document.dispatchEvent(new Event('visibilitychange'))
    window.dispatchEvent(new Event('visibilitychange'))
    await act(() => vi.advanceTimersByTimeAsync(35_000))
    expect(f.count('/peers')).toBe(1)

    visibility = 'visible'
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.waitFor(() => expect(f.count('/peers')).toBe(2))

    window.dispatchEvent(new Event('focus'))
    await vi.waitFor(() => expect(f.count('/peers')).toBe(3))
  })
})

describe('client: envelope', () => {
  it('unwraps {data} once for every ok answer', async () => {
    const fetchFn = vi.fn(async () => json(envelope([{ id: 'a' }])))
    const get = makeClient({ fetch: fetchFn as unknown as typeof fetch, reload: vi.fn(), storage: null })
    expect(await get('/subjects')).toEqual([{ id: 'a' }])
  })

  it('data: null (an empty list) reads as an empty array', async () => {
    const fetchFn = vi.fn(async () => json(envelope(null)))
    const get = makeClient({ fetch: fetchFn as unknown as typeof fetch, reload: vi.fn(), storage: null })
    expect(await get('/peers')).toEqual([])
  })

  it('useBacklog hands arrays to the domain, null lists included', async () => {
    const fetchFn = vi.fn(async (input: RequestInfo | URL) =>
      json(envelope(String(input).startsWith('/subjects') ? [{ id: 's1' }] : null)),
    )
    const { wrapper } = setup(fetchFn as unknown as typeof fetch)
    const { result } = renderHook(() => useBacklog(), { wrapper })
    await vi.waitFor(() => expect(result.current.ready).toBe(true))
    expect(result.current.subjects).toEqual([{ id: 's1' }])
    expect(result.current.themes).toEqual([])
    expect(result.current.peers).toEqual([])
    expect(result.current.captures).toEqual([])
  })

  it('useBacklog hands the captures read to the list', async () => {
    const caps = [{ id: 'cap_1', theme_id: 'th_a', text: 'idée', scope: 'perso', created_at: '2026-10-25T10:00:00Z' }]
    const fetchFn = vi.fn(async (input: RequestInfo | URL) => json(envelope(String(input).startsWith('/captures') ? caps : [])))
    const { wrapper } = setup(fetchFn as unknown as typeof fetch)
    const { result } = renderHook(() => useBacklog(), { wrapper })
    await vi.waitFor(() => expect(result.current.ready).toBe(true))
    expect(result.current.captures).toEqual(caps)
  })
})

describe('client: 401', () => {
  it('reloads once; a second 401 in a row does not reload and throws', async () => {
    const reload = vi.fn()
    const fetchFn = vi.fn(async () => json({ error: 'unauthorized' }, 401))
    const get = makeClient({ fetch: fetchFn as unknown as typeof fetch, reload, storage: window.sessionStorage })
    await expect(get('/subjects?scope=all')).rejects.toBeInstanceOf(ApiError)
    expect(reload).toHaveBeenCalledTimes(1)
    await expect(get('/subjects?scope=all')).rejects.toMatchObject({ status: 401 })
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('a 200 in between clears the flag: the next 401 reloads again', async () => {
    const reload = vi.fn()
    const answers = [401, 200, 401]
    const fetchFn = vi.fn(async () => json(envelope([]), answers.shift()))
    const get = makeClient({ fetch: fetchFn as unknown as typeof fetch, reload, storage: window.sessionStorage })
    await get('/peers').catch(() => undefined)
    await get('/peers')
    await get('/peers').catch(() => undefined)
    expect(reload).toHaveBeenCalledTimes(2)
  })

  it('no storage (blocked): never reloads, so it can never loop', async () => {
    const reload = vi.fn()
    const fetchFn = vi.fn(async () => json({}, 401))
    const get = makeClient({ fetch: fetchFn as unknown as typeof fetch, reload, storage: null })
    await get('/peers').catch(() => undefined)
    expect(reload).not.toHaveBeenCalled()
  })

  it('a network error is an ApiError with status 0 (the daemon banner, lot 3)', async () => {
    const fetchFn = vi.fn(async () => {
      throw new TypeError('Failed to fetch')
    })
    const get = makeClient({ fetch: fetchFn as unknown as typeof fetch, reload: vi.fn(), storage: null })
    await expect(get('/peers')).rejects.toMatchObject({ status: 0 })
  })
})

describe('useBacklog without data', () => {
  it('is not ready while loading', async () => {
    const pending = vi.fn(() => new Promise<Response>(() => undefined))
    const { wrapper } = setup(pending as unknown as typeof fetch)
    const { result } = renderHook(() => useBacklog(), { wrapper })
    await waitFor(() => expect(pending).toHaveBeenCalled())
    expect(result.current.ready).toBe(false)
    expect(result.current.subjects).toEqual([])
  })
})

describe('usePeersAll (GET /peers?all=1, the Sessions view)', () => {
  it('reads /peers?all=1 under the key [peers, all] and hands the unwrapped array', async () => {
    const calls: string[] = []
    const fetchFn = vi.fn(async (input: RequestInfo | URL) => {
      calls.push(String(input))
      return json(PEERS_ALL_BODY)
    })
    const { qc, wrapper } = setup(fetchFn as unknown as typeof fetch)
    const { result } = renderHook(() => usePeersAll(), { wrapper })
    await vi.waitFor(() => expect(result.current.data).toEqual(PEERS_ALL))
    expect(ROUTES.peersAll).toBe('/peers?all=1')
    expect(calls).toEqual(['/peers?all=1'])
    expect(qc.getQueryData(['peers', 'all'])).toEqual(PEERS_ALL)
  })

  it('enabled:false sends no request', async () => {
    const fetchFn = vi.fn(async () => json(PEERS_ALL_BODY))
    const { wrapper } = setup(fetchFn as unknown as typeof fetch)
    const { result } = renderHook(() => usePeersAll({ enabled: false }), { wrapper })
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20))
    })
    expect(fetchFn).not.toHaveBeenCalled()
    expect(result.current.data).toBeUndefined()
  })

  it('an invalidation of [peers] covers it (prefix)', async () => {
    const fetchFn = vi.fn(async () => json(PEERS_ALL_BODY))
    const { qc, wrapper } = setup(fetchFn as unknown as typeof fetch)
    const { result } = renderHook(() => usePeersAll(), { wrapper })
    await vi.waitFor(() => expect(result.current.data).toBeDefined())
    await act(() => qc.invalidateQueries({ queryKey: ['peers'] }))
    expect(fetchFn).toHaveBeenCalledTimes(2)
  })
})
