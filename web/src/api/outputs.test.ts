import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createElement, type ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { ApiProvider, makeClient } from './client'
import { groupLabel, outputGroupsKey, outputsKey, useOutputGroups } from './outputs'
import type { OutputGroup, OutputTour } from './types'

function tour(n: number, extra: Partial<OutputTour> = {}): OutputTour {
  return { n, label: `t${n}`, output_id: `o${n}`, title: `t${n}`, ts: n, path: `/x/t${n}/index.html`, exists: true, ...extra }
}

function group(kind: string, tours: number): OutputGroup {
  return { key: '/x', name: 'x', kind, first_seq: 1, tours: Array.from({ length: tours }, (_, i) => tour(i + 1)) }
}

describe('groupLabel', () => {
  it('says « 1 tour » in the singular', () => {
    expect(groupLabel(group('mockup', 1))).toBe('Maquette · 1 tour')
  })
  it('counts the rounds of a mockup', () => {
    expect(groupLabel(group('mockup', 21))).toBe('Maquette · 21 tours')
  })
  it('names a report as such', () => {
    expect(groupLabel(group('report', 3))).toBe('Rapport · 3 tours')
  })
})

describe('query keys', () => {
  it('reads the groups under the outputs key of the subject', () => {
    expect(outputsKey('sub_1')).toEqual(['outputs', 'sub_1'])
    expect(outputGroupsKey('sub_1')).toEqual(['outputs', 'sub_1', 'groups'])
  })

  it('invalidating the outputs of a subject invalidates its groups too, never another subject', async () => {
    const qc = new QueryClient()
    qc.setQueryData(outputsKey('a'), [])
    qc.setQueryData(outputGroupsKey('a'), [])
    qc.setQueryData(outputGroupsKey('b'), [])
    await qc.invalidateQueries({ queryKey: outputsKey('a') })
    expect(qc.getQueryState(outputsKey('a'))?.isInvalidated).toBe(true)
    expect(qc.getQueryState(outputGroupsKey('a'))?.isInvalidated).toBe(true)
    expect(qc.getQueryState(outputGroupsKey('b'))?.isInvalidated).toBe(false)
  })

  it('useOutputGroups reads GET /subjects/{id}/outputs/groups', async () => {
    const calls: string[] = []
    const fetchFn = vi.fn(async (input: RequestInfo | URL) => {
      calls.push(String(input))
      return new Response(JSON.stringify({ data: [group('mockup', 2)], meta: { request_id: 'r' } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    })
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const get = makeClient({ fetch: fetchFn as unknown as typeof fetch, reload: vi.fn(), storage: null })
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(ApiProvider, { value: get, children: createElement(QueryClientProvider, { client: qc }, children) })
    const { result } = renderHook(() => useOutputGroups('sub_1'), { wrapper })
    await waitFor(() => expect(result.current.data?.length).toBe(1))
    expect(calls).toEqual(['/subjects/sub_1/outputs/groups'])
    expect(qc.getQueryData(outputGroupsKey('sub_1'))).toBeDefined()
  })
})
