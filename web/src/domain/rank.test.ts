import { describe, expect, it } from 'vitest'
import type { Subject } from '@/api/types'
import { sortSubjects } from './order'
import { applyChanges, planDrop, planMove, type RankChange } from './rank'
import type { Display } from './status'

let clock = 0
/** A subject; each new one is older than the previous (so the tie-break « newest first » keeps creation order). */
function s(id: string, rank?: number, extra: Partial<Subject> = {}): Subject {
  clock += 1
  return { id, theme_id: 't', title: id, status: 'inbox', scope: 'perso', rank, updated_at: new Date(Date.UTC(2026, 0, 1) - clock * 60_000).toISOString(), created_at: '2026-01-01T00:00:00Z', ...extra }
}

const LIVE = new Set<string>()
function g(list: Subject[], d: Display = 'ouvert') {
  return list.map((subject) => ({ subject, display: d }))
}
function order(list: Subject[], changes: RankChange[], live: ReadonlySet<string> = LIVE): string[] {
  return sortSubjects(applyChanges(list, changes), live).map((x) => x.id)
}

describe('planMove', () => {
  it('two ranked neighbours (1 and 2): swap their values', () => {
    const a = s('a', 1)
    const b = s('b', 2)
    const ch = planMove(g([a, b]), [a, b], 'a', 1)
    expect(ch).toEqual([
      { id: 'a', from: 1, to: 2 },
      { id: 'b', from: 2, to: 1 },
    ])
    expect(order([a, b], ch)).toEqual(['b', 'a'])
  })

  it('edges: ⌥↑ on the first and ⌥↓ on the last do nothing', () => {
    const a = s('a', 1)
    const b = s('b', 2)
    expect(planMove(g([a, b]), [a, b], 'a', -1)).toEqual([])
    expect(planMove(g([a, b]), [a, b], 'b', 1)).toEqual([])
  })

  it('duplicates: two rank 5, ⌥↓ on the first: distinct ranks and the order inverts', () => {
    const a = s('a', 5)
    const b = s('b', 5)
    const c = s('c', 6)
    const scope = [a, b, c]
    const shown = sortSubjects(scope, LIVE)
    expect(shown.map((x) => x.id)).toEqual(['a', 'b', 'c'])
    const ch = planMove(g(shown), scope, 'a', 1)
    const after = applyChanges(scope, ch)
    expect(after.find((x) => x.id === 'a')?.rank).not.toBe(after.find((x) => x.id === 'b')?.rank)
    expect(order(scope, ch)).toEqual(['b', 'a', 'c'])
  })

  it('tie (D18): the line going up keeps r, the other r+1, ranked > r of the scope +1', () => {
    const a = s('a', 5)
    const b = s('b', 5)
    const c = s('c', 6)
    const far = s('far', 9, { status: 'done' })
    const low = s('low', 2)
    const scope = [low, a, b, c, far]
    // ⌥↑ on b: b goes up, keeps 5; a takes 6; c 6→7; far 9→10; low untouched
    const ch = planMove(g([a, b, c]), scope, 'b', -1)
    expect(ch).toEqual(
      expect.arrayContaining([
        { id: 'a', from: 5, to: 6 },
        { id: 'c', from: 6, to: 7 },
        { id: 'far', from: 9, to: 10 },
      ]),
    )
    expect(ch).toHaveLength(3)
    expect(order([a, b, c], ch)).toEqual(['b', 'a', 'c'])
  })

  it('an unranked line moving up to a ranked neighbour takes its rank; the neighbour and ranked ≥ n shift +1', () => {
    const r1 = s('r1', 1)
    const r2 = s('r2', 2)
    const r3 = s('r3', 3, { status: 'parked' })
    const u = s('u')
    const scope = [r1, r2, r3, u]
    const ch = planMove(g([r1, r2, u]), scope, 'u', -1)
    expect(ch).toEqual(
      expect.arrayContaining([
        { id: 'u', from: 0, to: 2 },
        { id: 'r2', from: 2, to: 3 },
        { id: 'r3', from: 3, to: 4 },
      ]),
    )
    expect(ch).toHaveLength(3)
    expect(order([r1, r2, u], ch)).toEqual(['r1', 'u', 'r2'])
  })

  it('two unranked lines: nothing', () => {
    const u1 = s('u1')
    const u2 = s('u2')
    expect(planMove(g([u1, u2]), [u1, u2], 'u1', 1)).toEqual([])
    expect(planMove(g([u1, u2]), [u1, u2], 'u2', -1)).toEqual([])
  })

  it('the last ranked line moving down to an unranked one: nothing (D18)', () => {
    const r = s('r', 1)
    const u = s('u')
    expect(planMove(g([r, u]), [r, u], 'r', 1)).toEqual([])
  })

  it('a neighbour of another scope: nothing (D18)', () => {
    const a = s('a', 1)
    const b = s('b', 2, { scope: 'globex' })
    expect(planMove(g([a, b]), [a], 'a', 1)).toEqual([])
    expect(planMove(g([a, b]), [b], 'b', -1)).toEqual([])
  })

  it('the En cours | Ouvert border is a group edge, both ways (D20)', () => {
    const live = s('live', 1)
    const open = s('open', 2)
    const group = [
      { subject: live, display: 'cours' as const },
      { subject: open, display: 'ouvert' as const },
    ]
    expect(planMove(group, [live, open], 'live', 1)).toEqual([])
    expect(planMove(group, [live, open], 'open', -1)).toEqual([])
  })

  it('property: every non-empty plan, applied then sorted, swaps the line with its neighbour', () => {
    const list = [s('a', 1), s('b', 3), s('c', 3), s('d', 4), s('e'), s('f')]
    const shown = sortSubjects(list, LIVE)
    for (let i = 0; i < shown.length; i += 1) {
      for (const dir of [-1, 1] as const) {
        const ch = planMove(g(shown), list, shown[i].id, dir)
        if (!ch.length) continue
        const exp = shown.map((x) => x.id)
        const j = i + dir
        ;[exp[i], exp[j]] = [exp[j], exp[i]]
        expect(order(list, ch), `${shown[i].id} ${dir}`).toEqual(exp)
      }
    }
  })
})

describe('planDrop', () => {
  it('three lines further: one merged list (original from, final to), order as expected', () => {
    const list = [s('a', 1), s('b', 2), s('c', 3), s('d', 4)]
    const ch = planDrop(g(list), list, 'a', 'd')
    expect(ch).toEqual(
      expect.arrayContaining([
        { id: 'a', from: 1, to: 4 },
        { id: 'b', from: 2, to: 1 },
        { id: 'c', from: 3, to: 2 },
        { id: 'd', from: 4, to: 3 },
      ]),
    )
    expect(ch).toHaveLength(4)
    expect(order(list, ch)).toEqual(['b', 'c', 'd', 'a'])
  })

  it('upwards, from the bottom to the top', () => {
    const list = [s('a', 1), s('b', 2), s('c', 3)]
    const ch = planDrop(g(list), list, 'c', 'a')
    expect(order(list, ch)).toEqual(['c', 'a', 'b'])
  })

  it('across ranked then unranked lines: nothing (one step is empty)', () => {
    const list = [s('a', 1), s('b', 2), s('u1'), s('u2')]
    expect(planDrop(g(list), list, 'a', 'u2')).toEqual([])
    expect(planDrop(g(list), list, 'u2', 'a')).toEqual([])
  })

  it('across the En cours | Ouvert border: nothing (D20)', () => {
    const live = s('live', 1)
    const a = s('a', 2)
    const b = s('b', 3)
    const group = [{ subject: live, display: 'cours' as const }, ...g([a, b])]
    expect(planDrop(group, [live, a, b], 'b', 'live')).toEqual([])
  })

  it('on the neighbour: the same changes as planMove', () => {
    const list = [s('a', 1), s('b', 2), s('c', 3)]
    expect(planDrop(g(list), list, 'b', 'c')).toEqual(planMove(g(list), list, 'b', 1))
    expect(planDrop(g(list), list, 'b', 'a')).toEqual(planMove(g(list), list, 'b', -1))
  })

  it('on itself, or on a line outside the group: nothing', () => {
    const list = [s('a', 1), s('b', 2)]
    expect(planDrop(g(list), list, 'a', 'a')).toEqual([])
    expect(planDrop(g(list), list, 'a', 'zzz')).toEqual([])
  })

  it('an unranked line dropped two lines up past ranked ones: inserted, the others shift', () => {
    const list = [s('a', 1), s('b', 2), s('u')]
    const ch = planDrop(g(list), list, 'u', 'a')
    expect(order(list, ch)).toEqual(['u', 'a', 'b'])
    expect(ch.find((c) => c.id === 'u')).toEqual({ id: 'u', from: 0, to: 1 })
  })
})

/** A subject with an explicit age: `min` minutes before 2026-01-01 (smaller = newer, first in a tie). */
function aged(id: string, rank: number | undefined, min: number): Subject {
  return s(id, rank, { updated_at: new Date(Date.UTC(2026, 0, 1) - min * 60_000).toISOString() })
}

/** The group's lines as shown, the mover moved exactly one notch, every other line in its order. */
function oneNotch(shown: string[], id: string, dir: -1 | 1): string[] {
  const exp = shown.slice()
  const i = exp.indexOf(id)
  ;[exp[i], exp[i + dir]] = [exp[i + dir], exp[i]]
  return exp
}

/** Tiny deterministic generator (no flaky property). */
function rng(seed: number) {
  let x = seed
  return (n: number) => {
    x = (x * 1103515245 + 12345) % 2147483648
    return x % n
  }
}

describe('planMove: ties never let the tie-break decide (verify F1)', () => {
  it('⌥↓ on Signature(5) under Guide(5) onto Indexation(7), Indexation newer than Guide: exactly one notch', () => {
    const guide = aged('guide', 5, 10)
    const sign = aged('signature', 5, 20)
    const index = aged('indexation', 7, 1)
    const list = [guide, sign, index]
    const shown = sortSubjects(list, LIVE).map((x) => x.id)
    expect(shown).toEqual(['guide', 'signature', 'indexation'])
    const ch = planMove(g(sortSubjects(list, LIVE)), list, 'signature', 1)
    expect(order(list, ch)).toEqual(['guide', 'indexation', 'signature'])
  })

  it('⌥↑ on Indexation(7) onto Signature(5) with Guide(5) above, Signature newer than Guide after the swap: exactly one notch', () => {
    const guide = aged('guide', 5, 10)
    const sign = aged('signature', 5, 20)
    const index = aged('indexation', 7, 1)
    const list = [guide, sign, index]
    const ch = planMove(g(sortSubjects(list, LIVE)), list, 'indexation', -1)
    expect(order(list, ch)).toEqual(['guide', 'indexation', 'signature'])
  })

  it('the mover side: ⌥↑ on Guide(5) onto Top(3), Guide newer than Other(3): Guide never jumps above Other; ⌥↓ on Top the same', () => {
    const other = aged('other', 3, 30)
    const top = aged('top', 3, 40)
    const guide = aged('guide', 5, 1)
    const tail = aged('tail', 5, 50)
    const list = [other, top, guide, tail]
    const shown = sortSubjects(list, LIVE).map((x) => x.id)
    expect(shown).toEqual(['other', 'top', 'guide', 'tail'])
    const ch = planMove(g(sortSubjects(list, LIVE)), list, 'guide', -1)
    expect(order(list, ch)).toEqual(['other', 'guide', 'top', 'tail'])
    const ch2 = planMove(g(sortSubjects(list, LIVE)), list, 'top', 1)
    expect(order(list, ch2)).toEqual(['other', 'guide', 'top', 'tail'])
  })

  it('property: over many tie configurations, every non-empty planMove moves the line exactly one notch, the others keep their order', () => {
    const r = rng(7)
    for (let k = 0; k < 300; k += 1) {
      const n = 3 + r(4)
      const list: Subject[] = []
      for (let i = 0; i < n; i += 1) list.push(aged(`s${k}-${i}`, r(5) ? 1 + r(3) * 2 + r(2) : undefined, r(60)))
      // a ranked subject of the scope outside the group, tied too
      const outside = aged(`out${k}`, 1 + r(6), r(60))
      const scope = [...list, outside]
      const shown = sortSubjects(list, LIVE)
      const ids = shown.map((x) => x.id)
      for (const x of ids) {
        for (const dir of [-1, 1] as const) {
          const ch = planMove(g(shown), scope, x, dir)
          if (!ch.length) continue
          expect(order(list, ch), `${k} ${x} ${dir} ${JSON.stringify(shown.map((y) => [y.id, y.rank, y.updated_at]))}`).toEqual(oneNotch(ids, x, dir))
        }
      }
    }
  })

  it('property: planDrop with ties lands the line on the target spot, the others in their order', () => {
    const r = rng(11)
    for (let k = 0; k < 200; k += 1) {
      const n = 3 + r(4)
      const list: Subject[] = []
      for (let i = 0; i < n; i += 1) list.push(aged(`d${k}-${i}`, 1 + r(3), r(60)))
      const shown = sortSubjects(list, LIVE)
      const ids = shown.map((x) => x.id)
      const from = r(n)
      const to = r(n)
      if (from === to) continue
      const ch = planDrop(g(shown), list, ids[from], ids[to])
      expect(ch.length, `${k}`).toBeGreaterThan(0)
      const exp = ids.slice()
      const [m] = exp.splice(from, 1)
      exp.splice(to, 0, m)
      expect(order(list, ch), `${k} ${ids[from]}→${ids[to]}`).toEqual(exp)
    }
  })
})
