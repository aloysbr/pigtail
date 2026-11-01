import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { vi } from 'vitest'
import { Shell } from '@/app/Shell'
import { ApiProvider, makeClient } from '@/api/client'
import { makeQueryClient, ROUTES } from '@/api/queries'
import { makeSend, SendProvider } from '@/api/mutate'
import { makeBacklog, type BacklogData } from './backlog'

/** Sets the window width the shell reads (jsdom has no layout). */
export function setWidth(w: number) {
  Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: w })
  window.dispatchEvent(new Event('resize'))
}

/** One write the stubbed pigtaild received. */
export interface WriteCall {
  method: string
  url: string
  body: unknown
}

/** A stubbed answer: a JSON body (sent as is), or a raw text body. */
export interface Reply {
  status: number
  body?: unknown
  text?: string
}

/** Answers one write route, keyed "METHOD /path" (exact path, query included). */
export type RouteFn = (req: WriteCall) => Reply | Promise<Reply>

/** Renders the whole atlas over a stubbed pigtaild; data can change between reads; writes answer from opts.routes (else 404). */
export async function renderApp(
  data: BacklogData = makeBacklog(),
  opts: { width?: number; wait?: boolean; routes?: Record<string, RouteFn>; hang?: string[]; fail?: string[] } = {},
) {
  setWidth(opts.width ?? 1440)
  let current = data
  let failing = false
  const failRoutes = new Set(opts.fail ?? [])
  const routes: Record<string, RouteFn> = { ...opts.routes }
  const written: WriteCall[] = []
  const fetchFn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (failing) throw new TypeError('Failed to fetch')
    const url = String(input)
    const method = (init?.method ?? 'GET').toUpperCase()
    if (method !== 'GET') {
      const call: WriteCall = { method, url, body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined }
      written.push(call)
      const fn = routes[`${method} ${url}`]
      const r: Reply = fn ? await fn(call) : { status: 404, body: { error: { code: 'NOT_FOUND', message: 'no stubbed route' } } }
      if (r.text !== undefined) return new Response(r.text, { status: r.status, headers: { 'Content-Type': 'text/plain' } })
      return new Response(r.body === undefined ? null : JSON.stringify(r.body), { status: r.status, headers: { 'Content-Type': 'application/json' } })
    }
    if (method === 'GET' && opts.hang?.includes(url)) return new Promise<Response>(() => {})
    if (method === 'GET' && failRoutes.has(url)) return new Response(JSON.stringify({ error: { code: 'INTERNAL', message: 'boom' } }), { status: 500 })
    const sub = /^\/subjects\/([^/?]+)\/(log|outputs|outputs\/groups)$/.exec(url)
    const one = /^\/subjects\/([^/?]+)$/.exec(url)
    const launch = /^\/launches\/([^/?]+)$/.exec(url)
    const waits = url.startsWith('/waits?') ? new URLSearchParams(url.slice('/waits?'.length)) : null
    const id = sub ? decodeURIComponent(sub[1]) : ''
    const body =
      url === ROUTES.subjects ? current.subjects
      : url === ROUTES.themes ? current.themes
      : url === ROUTES.spaces ? current.spaces
      : url === ROUTES.peers ? current.peers
      : url === ROUTES.peersAll ? (current.peersAll ?? current.peers)
      : url === ROUTES.captures ? (current.captures ?? [])
      : sub?.[2] === 'log' ? (current.logs?.[id] ?? [])
      : sub?.[2] === 'outputs' ? (current.outputs?.[id] ?? [])
      : sub?.[2] === 'outputs/groups' ? (current.groups?.[id] ?? [])
      : one ? (current.subjects.find((x) => x.id === decodeURIComponent(one[1])) ?? null)
      : launch ? (current.launches?.[decodeURIComponent(launch[1])] ?? null)
      : waits ? (current.waits ?? []).filter((w) => (!waits.get('subject') || w.subject_id === waits.get('subject')) && (!waits.get('session') || w.session_id === waits.get('session')))
      : null
    if (body === null) return new Response(JSON.stringify({ error: { code: 'NOT_FOUND', message: 'not found' } }), { status: 404 })
    return new Response(JSON.stringify({ data: body, meta: { request_id: 'req_x' } }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  })
  const qc = makeQueryClient()
  qc.setDefaultOptions({ queries: { ...qc.getDefaultOptions().queries, retry: false, refetchInterval: false } })
  const get = makeClient({ fetch: fetchFn as unknown as typeof fetch, reload: vi.fn(), storage: null })
  const send = makeSend({ fetch: fetchFn as unknown as typeof fetch })
  const user = userEvent.setup()
  const utils = render(
    <ApiProvider value={get}>
      <SendProvider value={send}>
        <QueryClientProvider client={qc}>
          <Shell />
        </QueryClientProvider>
      </SendProvider>
    </ApiProvider>,
  )
  if (opts.wait !== false) await screen.findByRole('region', { name: 'Sujets' })
  return {
    ...utils,
    user,
    qc,
    fetchFn,
    get,
    send,
    /** The writes received so far, in order. */
    writes(): WriteCall[] {
      return written.slice()
    },
    /** Declares (or replaces) the answer of a write route, "METHOD /path". */
    setRoute(key: string, fn: RouteFn) {
      routes[key] = fn
    },
    /** The data the next read returns (to change it in place from a route). */
    data(): BacklogData {
      return current
    },
    /** Makes one GET route answer 500 (on), or answer again (off). */
    setRouteFailing(url: string, on: boolean) {
      if (on) failRoutes.add(url)
      else failRoutes.delete(url)
    },
    /** Makes every next read fail as a network error (daemon down), or answer again. */
    setFailing(v: boolean) {
      failing = v
    },
    /** Replaces the data the next read returns, then re-reads everything. */
    async refetch(next?: BacklogData) {
      if (next) current = next
      await qc.refetchQueries()
    },
  }
}

/** The ids of the rows on screen, in order. */
export function rowIds(): string[] {
  return [...document.querySelectorAll<HTMLElement>('li.lr6[data-id]')].map((e) => e.dataset.id as string)
}

/** Unfolds every group and line and shows every hidden row (« Afficher les N autres »). */
export function unfoldAll() {
  for (let i = 0; i < 200; i += 1) {
    const b = document.querySelector<HTMLButtonElement>('button[data-g][aria-expanded="false"], button.more')
    if (!b) return
    fireEvent.click(b)
  }
  throw new Error('unfoldAll: still folded after 200 clicks')
}

/** The group header button of a key. */
export function groupButton(key: string): HTMLButtonElement {
  const b = document.querySelector<HTMLButtonElement>(`button[data-g="${CSS.escape(key)}"]`)
  if (!b) throw new Error(`no group button ${key}`)
  return b
}
