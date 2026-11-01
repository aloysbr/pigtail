import { describe, expect, it, vi } from 'vitest'
import { ApiError } from './client'
import { makeSend } from './mutate'

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

describe('send: the write client', () => {
  it('POSTs a JSON body, same-origin, with Content-Type, and unwraps {data}', async () => {
    const fetchFn = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => json({ data: { ok: 1 }, meta: { request_id: 'r' } }))
    const send = makeSend({ fetch: fetchFn as unknown as typeof fetch })
    const out = await send<{ ok: number }>('POST', '/x', { a: 1 })
    expect(out).toEqual({ ok: 1 })
    const [url, init] = fetchFn.mock.calls[0]
    expect(url).toBe('/x')
    expect(init?.method).toBe('POST')
    expect(init?.credentials).toBe('same-origin')
    const h = new Headers(init?.headers)
    expect(h.get('Content-Type')).toBe('application/json')
    expect(h.has('Authorization')).toBe(false)
    expect(h.has('X-Pigtail-Scope')).toBe(false)
    expect(h.has('X-Pigtail-Cross-Scope')).toBe(false)
    expect(init?.body).toBe('{"a":1}')
  })

  it('DELETE sends no body', async () => {
    const fetchFn = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => json({ data: { capture: {} } }))
    const send = makeSend({ fetch: fetchFn as unknown as typeof fetch })
    await send('DELETE', '/captures/c1')
    expect(fetchFn.mock.calls[0][1]?.method).toBe('DELETE')
    expect(fetchFn.mock.calls[0][1]?.body).toBeUndefined()
  })

  it('a 409 {error:{code,message,details}} throws ApiError with status, code and details', async () => {
    const fetchFn = vi.fn(async () => json({ error: { code: 'SESSION_LIVE', message: 'a session is live', details: { cell: 'mp' } } }, 409))
    const send = makeSend({ fetch: fetchFn as unknown as typeof fetch })
    const err = await send('POST', '/subjects/s1/launch').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(409)
    expect((err as ApiError).code).toBe('SESSION_LIVE')
    expect((err as ApiError).details).toEqual({ cell: 'mp' })
    expect((err as ApiError).unreadable).toBe(false)
  })

  it('a non-JSON error body is an ApiError marked unreadable, with no code', async () => {
    const fetchFn = vi.fn(async () => new Response('forbidden\n', { status: 403, headers: { 'Content-Type': 'text/plain' } }))
    const send = makeSend({ fetch: fetchFn as unknown as typeof fetch })
    const err = (await send('POST', '/x').catch((e: unknown) => e)) as ApiError
    expect(err.status).toBe(403)
    expect(err.code).toBeUndefined()
    expect(err.unreadable).toBe(true)
  })

  it('a network failure is ApiError(0)', async () => {
    const fetchFn = vi.fn(async () => {
      throw new TypeError('Failed to fetch')
    })
    const send = makeSend({ fetch: fetchFn as unknown as typeof fetch })
    const err = (await send('POST', '/x').catch((e: unknown) => e)) as ApiError
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(0)
  })

  it('a 401 on a write throws and never reloads the page', async () => {
    const reload = vi.fn()
    Object.defineProperty(window, 'location', { configurable: true, value: { ...window.location, reload } })
    const fetchFn = vi.fn(async () => json({ error: { code: 'UNAUTHORIZED', message: 'no' } }, 401))
    const send = makeSend({ fetch: fetchFn as unknown as typeof fetch })
    const err = (await send('POST', '/x').catch((e: unknown) => e)) as ApiError
    expect(err.status).toBe(401)
    expect(reload).not.toHaveBeenCalled()
  })
})
