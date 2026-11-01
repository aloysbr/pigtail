import { fireEvent, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { makeBacklog, shownIds } from '@/test/backlog'
import { groupButton, renderApp, rowIds, unfoldAll } from '@/test/render'

describe('List', () => {
  it('groups by Aire › Projet by default: areas of each scope in /spaces order, « Sans aire » last in its scope', async () => {
    await renderApp()
    expect(screen.getByRole('button', { name: /Grouper/ })).toHaveTextContent('Grouper : Aire › Projet')
    const areas = [...document.querySelectorAll('h2.gh .gname')].map((e) => e.textContent)
    expect(areas).toEqual(['Allotment', 'Garden', 'Sans aire', 'Vineyard'])
  })

  it('shows every subject not hidden exactly once, under its scope, area and project', async () => {
    const data = makeBacklog()
    await renderApp(data)
    unfoldAll()
    const ids = rowIds()
    expect(ids.slice().sort()).toEqual(shownIds(data).sort())
    expect(new Set(ids).size).toBe(ids.length)
    for (const s of data.subjects.filter((x) => shownIds(data).includes(x.id))) {
      const li = document.querySelector(`li[data-id="${s.id}"]`) as HTMLElement
      const project = li.closest('section[data-group^="th:"]') as HTMLElement
      const area = li.closest('section[data-group^="sp:"]') as HTMLElement
      expect(project.dataset.group).toBe(s.theme_id ? `th:${s.theme_id}` : 'th:none:perso')
      const theme = data.themes.find((t) => t.id === s.theme_id)
      expect(area.dataset.group).toBe(`sp:${s.scope}/${theme?.space ?? ''}`)
    }
  })

  it('header totals equal the rows: « N sujets », each group count = its rows', async () => {
    const data = makeBacklog()
    await renderApp(data)
    unfoldAll()
    expect(screen.getByText(`${shownIds(data).length} sujets`)).toBeInTheDocument()
    for (const sec of document.querySelectorAll<HTMLElement>('section[data-group]')) {
      const n = Number(within(sec.querySelector('.gbtn') as HTMLElement).getByTestId('count').textContent)
      expect(n, sec.dataset.group).toBe(sec.querySelectorAll('li.lr6').length)
    }
  })

  it('a row keeps its icon whole and truncates its title (shrink-0, min-w-0 truncate)', async () => {
    await renderApp()
    fireOpen('th:th_pig')
    const li = document.querySelector('li[data-id="pig-r1"]') as HTMLElement
    expect(li.querySelector('.si')).toHaveClass('shrink-0')
    expect(li.querySelector('.lr-t')).toHaveClass('min-w-0', 'truncate')
    expect(li.querySelector('.lr-t b')).toHaveTextContent('Atlas web')
    expect(li.querySelector('.lr-full')).toHaveTextContent('Backlog en lecture')
  })
})

function fireOpen(key: string) {
  const b = groupButton(key)
  if (b.getAttribute('aria-expanded') === 'false') fireEvent.click(b)
}
