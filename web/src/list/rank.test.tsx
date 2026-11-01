import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import type { Subject } from '@/api/types'
import { setUi } from '@/state/ui'
import { makeBacklog, type BacklogData } from '@/test/backlog'
import { groupButton, renderApp, rowIds, type Reply, type WriteCall } from '@/test/render'
import { resetToasts } from '@/ui/toasts'

// toasts live in a module store: one test's toast must not show in the next
afterEach(() => resetToasts())

type Change = { id: string; from: number; to: number }

/** The daemon's rank route over the test data: checks every `from`, writes the `to`, answers the changed subjects. */
function ranksRoute(data: () => BacklogData) {
  return (req: WriteCall): Reply => {
    const { changes } = req.body as { changes: Change[] }
    const subjects = data().subjects
    for (const c of changes) {
      const s = subjects.find((x) => x.id === c.id)
      if (!s) return { status: 404, body: { error: { code: 'NOT_FOUND', message: 'no' } } }
      if ((s.rank ?? 0) !== c.from) return { status: 409, body: { error: { code: 'RANK_CHANGED', message: 'stale' } } }
    }
    const out: Subject[] = []
    for (const c of changes) {
      const s = subjects.find((x) => x.id === c.id) as Subject
      if (c.to) s.rank = c.to
      else delete s.rank
      out.push(s)
    }
    return { status: 200, body: { data: out } }
  }
}

function rankWrites(w: WriteCall[]) {
  return w.filter((c) => c.url === '/subjects/ranks')
}

function row(id: string): HTMLElement {
  const li = document.querySelector<HTMLElement>(`li[data-id="${id}"]`)
  if (!li) throw new Error(`no row ${id}`)
  return li
}

async function alt(k: 'ArrowUp' | 'ArrowDown', target: Element = document.body) {
  await act(async () => {
    fireEvent.keyDown(target, { key: k, altKey: true })
  })
}

function select(id: string) {
  act(() => setUi({ sel: id }))
}

/** A fake DataTransfer (jsdom has none). */
function dt() {
  return { dropEffect: 'none', effectAllowed: 'all', setData() {}, getData: () => '', types: [] as string[] }
}

async function setup(data: BacklogData = makeBacklog(), width = 1440) {
  const h = await renderApp(data, { width })
  h.setRoute('POST /subjects/ranks', ranksRoute(h.data))
  await act(async () => {
    fireEvent.click(groupButton('th:th_pig'))
  })
  return h
}

describe('reorder: ⌥↑ ⌥↓', () => {
  it('⌥↓ on the first of two ranked Ouvert: one POST with both from/to, the rows swap, a « Rang de « X » modifié » toast with « annuler »', async () => {
    const h = await setup()
    select('pig-r1')
    await alt('ArrowDown')
    expect(rankWrites(h.writes())).toEqual([
      {
        method: 'POST',
        url: '/subjects/ranks',
        body: {
          changes: [
            { id: 'pig-r1', from: 1, to: 2 },
            { id: 'pig-r2', from: 2, to: 1 },
          ],
        },
      },
    ])
    await waitFor(() => expect(rowIds().indexOf('pig-r2')).toBeLessThan(rowIds().indexOf('pig-r1')))
    expect(await screen.findByRole('status')).toHaveTextContent('Rang de « Atlas web » modifié')
    expect(screen.getByRole('button', { name: 'annuler' })).toBeInTheDocument()
  })

  it('⌥↑ at the head of a group: no call, no toast', async () => {
    const h = await setup()
    await act(async () => {
      fireEvent.click(groupButton('th:th_seedbed'))
    })
    select('watering-1')
    await alt('ArrowUp')
    expect(rankWrites(h.writes())).toEqual([])
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('the En cours | Ouvert border is a group edge (D20): ⌥↓ on the last En cours, ⌥↑ on the first Ouvert = no call', async () => {
    const h = await setup()
    select('pig-live')
    await alt('ArrowDown')
    select('pig-r1')
    await alt('ArrowUp')
    expect(rankWrites(h.writes())).toEqual([])
  })

  it('⌥↓ from the last ranked line onto an unranked one: no call (D18)', async () => {
    const h = await setup()
    select('pig-r2')
    await alt('ArrowDown')
    expect(rankWrites(h.writes())).toEqual([])
  })

  it('a Standby, Done or Abandonné line: no call', async () => {
    const h = await setup()
    for (const id of ['pig-p1', 'pig-d1', 'pig-x1']) {
      select(id)
      await alt('ArrowUp')
      await alt('ArrowDown')
    }
    expect(rankWrites(h.writes())).toEqual([])
  })

  it('⌥ with another key is ignored; ⌥↑ while typing does nothing', async () => {
    const h = await setup()
    select('pig-r2')
    await act(async () => {
      fireEvent.keyDown(document.body, { key: 'j', altKey: true })
    })
    expect(document.querySelector('li.lr6.sel')?.getAttribute('data-id')).toBe('pig-r2')
    const search = screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' })
    search.focus()
    await alt('ArrowUp', search)
    expect(rankWrites(h.writes())).toEqual([])
  })

  it('an unranked line ⌥↑ onto a ranked one: classed, its #N shows', async () => {
    const h = await setup()
    select('pig-u1')
    await alt('ArrowUp')
    expect(rankWrites(h.writes())[0].body).toEqual({
      changes: expect.arrayContaining([
        { id: 'pig-u1', from: 0, to: 2 },
        { id: 'pig-r2', from: 2, to: 3 },
        { id: 'pig-p1', from: 5, to: 6 },
      ]),
    })
    await waitFor(() => expect(row('pig-u1').querySelector('.rk6')).toHaveTextContent('#2'))
  })

  it('a 409 RANK_CHANGED: the order before comes back, with its toast', async () => {
    const h = await setup()
    const before = rowIds()
    h.setRoute('POST /subjects/ranks', () => ({ status: 409, body: { error: { code: 'RANK_CHANGED', message: 'stale' } } }))
    select('pig-r1')
    await alt('ArrowDown')
    expect(await screen.findByRole('status')).toHaveTextContent("Le rang de « Atlas web » a changé entre-temps : rien n'a été modifié.")
    expect(rowIds()).toEqual(before)
  })

  it('a 423 MAINTENANCE_FROZEN: the order before comes back, with the maintenance toast', async () => {
    const h = await setup()
    const before = rowIds()
    h.setRoute('POST /subjects/ranks', () => ({ status: 423, body: { error: { code: 'MAINTENANCE_FROZEN', message: 'frozen' } } }))
    select('pig-r1')
    await alt('ArrowDown')
    expect(await screen.findByRole('status')).toHaveTextContent('Maintenance en cours : réessaie quand elle est finie.')
    expect(rowIds()).toEqual(before)
  })

  it('z within 8 s writes the original ranks back: « Annulé · Rang de « X » rétabli »', async () => {
    const h = await setup()
    select('pig-r1')
    await alt('ArrowDown')
    await screen.findByText('Rang de « Atlas web » modifié')
    await act(async () => {
      fireEvent.keyDown(document.body, { key: 'z' })
    })
    await waitFor(() => expect(rankWrites(h.writes())).toHaveLength(2))
    expect(rankWrites(h.writes())[1].body).toEqual({
      changes: [
        { id: 'pig-r1', from: 2, to: 1 },
        { id: 'pig-r2', from: 1, to: 2 },
      ],
    })
    expect(await screen.findByRole('status')).toHaveTextContent('Annulé · Rang de « Atlas web » rétabli')
    await waitFor(() => expect(rowIds().indexOf('pig-r1')).toBeLessThan(rowIds().indexOf('pig-r2')))
  })
})

/** Four ranked Ouvert in pigtail: r1 #1, r2 #2, u1 #3, i1 #4; i2.. unranked. */
function fourRanked(): BacklogData {
  const d = makeBacklog()
  for (const s of d.subjects) {
    if (s.id === 'pig-u1') s.rank = 3
    if (s.id === 'pig-i1') s.rank = 4
  }
  return d
}

async function drag(from: string, to: string) {
  const t = dt()
  await act(async () => {
    fireEvent.dragStart(row(from), { dataTransfer: t })
  })
  let allowed = false
  await act(async () => {
    allowed = !fireEvent.dragOver(row(to), { dataTransfer: t })
  })
  return { t, allowed }
}

async function drop(from: string, to: string) {
  const r = await drag(from, to)
  await act(async () => {
    fireEvent.drop(row(to), { dataTransfer: r.t })
    fireEvent.dragEnd(row(from), { dataTransfer: r.t })
  })
  return r
}

describe('reorder: drag and drop', () => {
  it('drop on the neighbour = the same changes as ⌥', async () => {
    const h = await setup()
    const { allowed } = await drop('pig-r1', 'pig-r2')
    expect(allowed).toBe(true)
    expect(rankWrites(h.writes()).map((w) => w.body)).toEqual([
      {
        changes: [
          { id: 'pig-r1', from: 1, to: 2 },
          { id: 'pig-r2', from: 2, to: 1 },
        ],
      },
    ])
  })

  it('a drop three lines further: one POST with the merged list of planDrop', async () => {
    const h = await setup(fourRanked())
    await drop('pig-r1', 'pig-i1')
    const w = rankWrites(h.writes())
    expect(w).toHaveLength(1)
    expect((w[0].body as { changes: Change[] }).changes).toEqual(
      expect.arrayContaining([
        { id: 'pig-r1', from: 1, to: 4 },
        { id: 'pig-r2', from: 2, to: 1 },
        { id: 'pig-u1', from: 3, to: 2 },
        { id: 'pig-i1', from: 4, to: 3 },
      ]),
    )
    await waitFor(() => expect(rowIds().indexOf('pig-r1')).toBeGreaterThan(rowIds().indexOf('pig-i1')))
  })

  it('a drop across unranked lines: no call', async () => {
    const h = await setup()
    const { allowed } = await drop('pig-r1', 'pig-i2')
    expect(allowed).toBe(false)
    expect(rankWrites(h.writes())).toEqual([])
  })

  it('a drop across the En cours | Ouvert border: no call (D20)', async () => {
    const h = await setup()
    const { allowed } = await drop('pig-r1', 'pig-live')
    expect(allowed).toBe(false)
    expect(rankWrites(h.writes())).toEqual([])
  })

  it('a target in another group is refused natively: no preventDefault, no .over, no call (D22)', async () => {
    const h = await setup()
    await act(async () => {
      fireEvent.click(groupButton('th:th_meth'))
    })
    const { t, allowed } = await drag('pig-r2', 'meth-1')
    expect(allowed).toBe(false)
    expect(t.dropEffect).toBe('none')
    expect(row('meth-1')).not.toHaveClass('over')
    expect(row('pig-r2')).toHaveClass('dragging')
    await act(async () => {
      fireEvent.drop(row('meth-1'), { dataTransfer: t })
      fireEvent.dragEnd(row('pig-r2'), { dataTransfer: t })
    })
    expect(rankWrites(h.writes())).toEqual([])
    expect(row('pig-r2')).not.toHaveClass('dragging')
  })

  it('an allowed target carries .over while dragged over', async () => {
    await setup()
    await drag('pig-r1', 'pig-r2')
    expect(row('pig-r2')).toHaveClass('over')
    expect(row('pig-r1')).toHaveClass('dragging')
  })

  it('no draggable row at 390 px', async () => {
    await setup(makeBacklog(), 390)
    expect(document.querySelectorAll('li.lr6[draggable="true"]')).toHaveLength(0)
  })
})
