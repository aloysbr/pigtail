import { QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import type { ReactElement } from 'react'
import { vi } from 'vitest'
import { ApiProvider, makeClient } from '@/api/client'
import { makeSend, SendProvider } from '@/api/mutate'
import { makeQueryClient } from '@/api/queries'

/** One write the stubbed daemon received. */
export interface Posted {
  url: string
  body: unknown
}

export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

/** Renders `ui` over a stubbed daemon: `reads` answers a GET by its exact URL (else 404), every POST is recorded and answered 200 unless `fail` maps it to a status. */
export function renderSdk(ui: ReactElement, opts: { reads?: Record<string, () => Response | Promise<Response>>; fail?: Record<string, { status: number; code?: string }> } = {}) {
  const posted: Posted[] = []
  const fetchFn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if ((init?.method ?? 'GET') === 'POST') {
      posted.push({ url, body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined })
      const f = opts.fail?.[url]
      if (f) return json({ error: { code: f.code ?? 'X', message: 'refused' } }, f.status)
      return json({ data: { ok: true }, meta: { request_id: 'r' } })
    }
    const r = opts.reads?.[url]
    return r ? r() : json({ error: { code: 'NOT_FOUND', message: 'nf' } }, 404)
  })
  const qc = makeQueryClient()
  qc.setDefaultOptions({ queries: { ...qc.getDefaultOptions().queries, retry: false, refetchInterval: false } })
  const get = makeClient({ fetch: fetchFn as unknown as typeof fetch, reload: vi.fn(), storage: null })
  const send = makeSend({ fetch: fetchFn as unknown as typeof fetch })
  const utils = render(
    <ApiProvider value={get}>
      <SendProvider value={send}>
        <QueryClientProvider client={qc}>{ui}</QueryClientProvider>
      </SendProvider>
    </ApiProvider>,
  )
  return { ...utils, posted, fetchFn, qc }
}

/** The words the new texts must never name (no product name hard-coded). */
export const FORBIDDEN = /claude|anthropic|cmux|pigtail|sdk|obsidian|github/i
