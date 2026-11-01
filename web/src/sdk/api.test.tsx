import { QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, makeClient } from '@/api/client'
import { makeSend, SendProvider } from '@/api/mutate'
import { makeQueryClient } from '@/api/queries'
import type { SdkEvent, SdkState } from '@/api/types'
import { SDK_EVENTS_MS, useSdkActions, useSdkEvents, useSdkSessions } from './api'
import events from './fixtures/events.json'
import list from './fixtures/list.json'
import off from './fixtures/off.json'

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function setup(fetchFn: typeof fetch) {
  const qc = makeQueryClient()
  qc.setDefaultOptions({ queries: { ...qc.getDefaultOptions().queries, retry: false, refetchInterval: false } })
  const get = makeClient({ fetch: fetchFn, reload: vi.fn(), storage: null })
  const send = makeSend({ fetch: fetchFn })
  const wrapper = ({ children }: { children: ReactNode }) => (
    <ApiProvider value={get}>
      <SendProvider value={send}>
        <QueryClientProvider client={qc}>{children}</QueryClientProvider>
      </SendProvider>
    </ApiProvider>
  )
  return { wrapper, qc }
}

afterEach(() => vi.useRealTimers())

describe('useSdkSessions', () => {
  it('reads GET /sdk-sessions?subject= and rends launch_enabled', async () => {
    const urls: string[] = []
    const fetchFn = vi.fn(async (input: RequestInfo | URL) => {
      urls.push(String(input))
      return json(list)
    })
    const { wrapper } = setup(fetchFn as unknown as typeof fetch)
    const { result } = renderHook(() => useSdkSessions('sub_p'), { wrapper })
    await waitFor(() => expect(result.current.ready).toBe(true))
    expect(urls).toEqual(['/sdk-sessions?subject=sub_p'])
    expect(result.current.off).toBe(false)
    expect(result.current.launchEnabled).toBe(true)
    expect(result.current.sessions.map((s) => s.id)).toEqual(['sdk_p1', 'sdk_p2', 'sdk_t1'])
  })

  it('a 503 SDK_OFF rends {off: true} without an error', async () => {
    const fetchFn = vi.fn(async () => json(off, 503))
    const { wrapper } = setup(fetchFn as unknown as typeof fetch)
    const { result } = renderHook(() => useSdkSessions('sub_p'), { wrapper })
    await waitFor(() => expect(result.current.ready).toBe(true))
    expect(result.current.off).toBe(true)
    expect(result.current.sessions).toEqual([])
    expect(result.current.launchEnabled).toBe(false)
  })
})

describe('useSdkSessions — other 503', () => {
  it('a 503 REGISTRY_UNREADABLE is an error, not off: the block must say it', async () => {
    const fetchFn = vi.fn(async () => json({ error: { code: 'REGISTRY_UNREADABLE', message: 'unreadable' }, meta: { request_id: 'r' } }, 503))
    const { wrapper } = setup(fetchFn as unknown as typeof fetch)
    const { result } = renderHook(() => useSdkSessions('sub_p'), { wrapper })
    await waitFor(() => expect(result.current.ready).toBe(true))
    expect(result.current.off).toBe(false)
    expect(result.current.error).toBe(true)
  })
})

describe('useSdkEvents', () => {
  const ev = (seq: number, kind = 'state'): SdkEvent => ({ seq, at: '2026-10-29T10:00:00Z', kind, data: {} })
  const body = (events: SdkEvent[], after: number) => ({ data: { events, last_seq: events.length ? events[events.length - 1].seq : after }, meta: { request_id: 'r' } })

  beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }))

  it('re-reads ?after=<last_seq> every second while the block is visible and only appends the new seq', async () => {
    const urls: string[] = []
    const fetchFn = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      urls.push(url)
      if (url.endsWith('after=0')) return json(events)
      if (url.endsWith('after=4')) return json(body([ev(4), ev(5)], 4)) // a replayed seq 4 is not added twice
      return json(body([], Number(url.split('=')[1])))
    })
    const { wrapper } = setup(fetchFn as unknown as typeof fetch)
    const { result } = renderHook(() => useSdkEvents('sdk_p1', 'waiting'), { wrapper })
    await waitFor(() => expect(result.current.events.map((e) => e.seq)).toEqual([1, 2, 3, 4]))
    expect(urls[0]).toBe('/sdk-sessions/sdk_p1/events?after=0')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(SDK_EVENTS_MS + 50)
    })
    await waitFor(() => expect(result.current.events.map((e) => e.seq)).toEqual([1, 2, 3, 4, 5]))
    expect(urls[1]).toBe('/sdk-sessions/sdk_p1/events?after=4')
  })

  it.each<[SdkState, boolean]>([
    ['idle', true],
    ['running', true],
    ['ended', false],
    ['failed', false],
    ['lost', false],
  ])('in %s the polling %s', async (state, polls) => {
    const fetchFn = vi.fn(async (input: RequestInfo | URL) => json(body([], Number(String(input).split('=')[1]))))
    const { wrapper } = setup(fetchFn as unknown as typeof fetch)
    renderHook(() => useSdkEvents('sdk_x', state), { wrapper })
    await waitFor(() => expect(fetchFn).toHaveBeenCalledTimes(1))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(SDK_EVENTS_MS * 3 + 100)
    })
    if (polls) expect(fetchFn.mock.calls.length).toBeGreaterThan(1)
    else expect(fetchFn).toHaveBeenCalledTimes(1)
  })
})

describe('useSdkActions', () => {
  it('answer, say, end and launch POST through the existing write client', async () => {
    const calls: { url: string; method?: string; body?: unknown }[] = []
    const fetchFn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), method: init?.method, body: init?.body ? JSON.parse(String(init.body)) : undefined })
      return json({ data: { ok: true }, meta: { request_id: 'r' } })
    })
    const { wrapper } = setup(fetchFn as unknown as typeof fetch)
    const { result } = renderHook(() => useSdkActions(), { wrapper })
    await result.current.answer('sdk_p1', 'r1', { behavior: 'allow', answers: { 'Which fruits?': ['Apple'] } })
    await result.current.say('sdk_p1', 'continue')
    await result.current.end('sdk_p1')
    await result.current.launch('sub_p')
    expect(calls).toEqual([
      { url: '/sdk-sessions/sdk_p1/requests/r1/answer', method: 'POST', body: { behavior: 'allow', answers: { 'Which fruits?': ['Apple'] } } },
      { url: '/sdk-sessions/sdk_p1/messages', method: 'POST', body: { text: 'continue' } },
      { url: '/sdk-sessions/sdk_p1/end', method: 'POST', body: undefined },
      { url: '/subjects/sub_p/sdk-sessions', method: 'POST', body: undefined },
    ])
  })
})
