import { act, renderHook, waitFor } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from './client'
import { makeQueryClient } from './queries'
import { useWrite } from './mutate'
import { renderApp } from '@/test/render'

function setup() {
  const qc = makeQueryClient()
  qc.setDefaultOptions({ queries: { retry: false, refetchInterval: false } })
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  return { qc, wrapper }
}

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('useWrite', () => {
  it('applies the optimistic change before the answer, keeps it on success and invalidates its keys', async () => {
    const { qc, wrapper } = setup()
    qc.setQueryData(['subjects'], ['before'])
    const answer = deferred<string>()
    const steps: string[] = []
    const { result } = renderHook(
      () =>
        useWrite<string, string>({
          mutationFn: () => answer.promise,
          optimistic: (v) => {
            steps.push('optimistic')
            const prev = qc.getQueryData(['subjects'])
            qc.setQueryData(['subjects'], [v])
            return () => qc.setQueryData(['subjects'], prev)
          },
          invalidate: [['subjects']],
        }),
      { wrapper },
    )
    const spy = vi.spyOn(qc, 'invalidateQueries')
    let done!: Promise<unknown>
    act(() => {
      done = result.current.mutateAsync('after')
    })
    await waitFor(() => expect(qc.getQueryData(['subjects'])).toEqual(['after']))
    expect(steps).toEqual(['optimistic'])
    await act(async () => {
      answer.resolve('ok')
      await done
    })
    expect(qc.getQueryData(['subjects'])).toEqual(['after'])
    expect(spy).toHaveBeenCalledWith({ queryKey: ['subjects'] })
  })

  it('rolls the optimistic change back on failure and rethrows the ApiError', async () => {
    const { qc, wrapper } = setup()
    qc.setQueryData(['subjects'], ['before'])
    const answer = deferred<string>()
    const { result } = renderHook(
      () =>
        useWrite<string, string>({
          mutationFn: () => answer.promise,
          optimistic: (v) => {
            const prev = qc.getQueryData(['subjects'])
            qc.setQueryData(['subjects'], [v])
            return () => qc.setQueryData(['subjects'], prev)
          },
          invalidate: [['subjects']],
        }),
      { wrapper },
    )
    let done!: Promise<unknown>
    act(() => {
      done = result.current.mutateAsync('after').catch((e: unknown) => e)
    })
    await waitFor(() => expect(qc.getQueryData(['subjects'])).toEqual(['after']))
    let err: unknown
    await act(async () => {
      answer.reject(new ApiError(409, 'x', 'RANK_CHANGED'))
      err = await done
    })
    expect(qc.getQueryData(['subjects'])).toEqual(['before'])
    expect((err as ApiError).code).toBe('RANK_CHANGED')
  })
})

describe('render harness: writes', () => {
  it('a POST with no declared route answers 404 and is recorded with its method and body', async () => {
    const { send, writes } = await renderApp()
    const err = (await send('POST', '/subjects/pig-r1', { status: 'parked' }).catch((e: unknown) => e)) as ApiError
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(404)
    expect(writes()).toEqual([{ method: 'POST', url: '/subjects/pig-r1', body: { status: 'parked' } }])
  })

  it('a declared route answers its reply; GET /captures and /launches/{id} come from the data', async () => {
    const data = (await import('@/test/backlog')).makeBacklog()
    data.captures = [{ id: 'cap_1', theme_id: 'th_pig', text: 'idée', scope: 'perso', created_at: new Date().toISOString() }]
    data.launches = { l1: { launch_id: 'l1', subject_id: 'pig-r1', state: 'pending' } as never }
    const { send, get } = await renderApp(data, { routes: { 'POST /subjects/ranks': () => ({ status: 200, body: { data: [] } }) } })
    expect(await send('POST', '/subjects/ranks', { changes: [] })).toEqual([])
    expect(await get('/captures?scope=all&open=1')).toHaveLength(1)
    expect(await get<{ state: string }>('/launches/l1')).toMatchObject({ state: 'pending' })
  })
})
