import { describe, expect, it } from 'vitest'
import { subj } from '@/test/fixtures'
import { sortSubjects } from './order'

describe('sortSubjects', () => {
  it('En cours, Ouvert, Standby, Done; ranked before unranked, rank ascending; newest next', () => {
    const live = subj({ id: 'live', status: 'inbox' })
    const r2 = subj({ id: 'r2', status: 'building', rank: 2 })
    const r1 = subj({ id: 'r1', status: 'inbox', rank: 1 })
    const old = subj({ id: 'old', status: 'inbox', updated_at: '2026-10-01T00:00:00Z' })
    const recent = subj({ id: 'recent', status: 'designing', updated_at: '2026-10-22T00:00:00Z' })
    const parked = subj({ id: 'parked', status: 'parked' })
    const done = subj({ id: 'done', status: 'done' })
    const got = sortSubjects([done, old, parked, r2, recent, live, r1], new Set(['live']))
    expect(got.map((s) => s.id)).toEqual(['live', 'r1', 'r2', 'recent', 'old', 'parked', 'done'])
  })

  it('does not mutate its input', () => {
    const a = [subj({ status: 'done' }), subj({ status: 'inbox' })]
    const ids = a.map((s) => s.id)
    sortSubjects(a, new Set())
    expect(a.map((s) => s.id)).toEqual(ids)
  })
})
