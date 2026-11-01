import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { getUi, setUi } from '@/state/ui'
import { cap, makeBacklog, shownIds, withCaptures } from '@/test/backlog'
import { groupButton, renderApp } from '@/test/render'
import { resetToasts } from '@/ui/toasts'

afterEach(() => resetToasts())

function capLine(theme: string): HTMLButtonElement | null {
  return document.querySelector<HTMLButtonElement>(`button.capl[data-g="cap:${theme}"]`)
}

function capIds(): string[] {
  return [...document.querySelectorAll<HTMLElement>('li.cap-row[data-cap]')].map((e) => e.dataset.cap as string)
}

function openWatering() {
  if (groupButton('th:th_seedbed').getAttribute('aria-expanded') === 'false') fireEvent.click(groupButton('th:th_seedbed'))
}

async function press(k: string, extra: Partial<KeyboardEventInit> = {}) {
  await act(async () => {
    fireEvent.keyDown(document.activeElement ?? document.body, { key: k, ...extra })
  })
}

describe('Captures · n', () => {
  it('2 open captures in a project: one folded line « Captures 2 » at the end of the project; unfolded, text and age, no checkbox', async () => {
    await renderApp(withCaptures())
    openWatering()
    const line = capLine('th_seedbed') as HTMLButtonElement
    expect(line).toHaveAttribute('aria-expanded', 'false')
    expect(line).toHaveTextContent(/Captures\s*2/)
    expect(capIds()).toEqual([])
    fireEvent.click(line)
    expect(line).toHaveAttribute('aria-expanded', 'true')
    expect(capIds()).toEqual(['cap-b2', 'cap-b1'])
    const row = document.querySelector('li[data-cap="cap-b2"]') as HTMLElement
    expect(row).toHaveTextContent('Photo of the vase')
    expect(row.querySelector('.lr-d')).toHaveTextContent('1 h')
    expect(row.querySelector('input, [role="checkbox"]')).toBeNull()
    // the project's subjects come first, the Captures line closes the project
    const section = row.closest('section[data-group="th:th_seedbed"]') as HTMLElement
    const kids = [...section.querySelectorAll('li.lr6, button.capl')]
    expect(kids.indexOf(line)).toBeGreaterThan(kids.indexOf(section.querySelector('li[data-id="watering-1"]') as Element))
  })

  it('0 open captures (or only promoted ones): no line', async () => {
    await renderApp(withCaptures())
    fireEvent.click(groupButton('th:th_pig'))
    expect(capLine('th_pig')).toBeNull()
    openWatering()
    expect(capLine('th_seedbed')).not.toBeNull()
  })

  it('the Statut and Scope groupings show no Captures line', async () => {
    await renderApp(withCaptures())
    for (const group of ['statut', 'scope'] as const) {
      act(() => setUi({ group }))
      for (const b of document.querySelectorAll<HTMLButtonElement>('button[data-g][aria-expanded="false"]')) fireEvent.click(b)
      expect(document.querySelector('button.capl'), group).toBeNull()
    }
  })

  it('« N sujets » does not count the captures', async () => {
    const data = withCaptures()
    await renderApp(data)
    expect(screen.getByText(`${shownIds(data).length} sujets`)).toBeInTheDocument()
  })

  it('a capture made at the CLI shows at the next read', async () => {
    const h = await renderApp(withCaptures())
    openWatering()
    expect(capLine('th_seedbed')).toHaveTextContent(/Captures\s*2/)
    h.data().captures?.push(cap('cap-b3', 'th_seedbed', 'Faite à la CLI', 'perso', 1))
    await act(() => h.refetch())
    await waitFor(() => expect(capLine('th_seedbed')).toHaveTextContent(/Captures\s*3/))
  })

  it('j/k go through the capture rows; a click selects a capture and opens no sheet', async () => {
    await renderApp(withCaptures())
    openWatering()
    fireEvent.click(capLine('th_seedbed') as HTMLButtonElement)
    act(() => setUi({ sel: 'watering-1' }))
    await press('j')
    expect(getUi().sel).toBe('cap-b2')
    expect(document.activeElement).toBe(document.querySelector('li[data-cap="cap-b2"]'))
    await press('j')
    expect(getUi().sel).toBe('cap-b1')
    await press('k')
    expect(getUi().sel).toBe('cap-b2')
    fireEvent.click(document.querySelector('li[data-cap="cap-b1"]') as HTMLElement)
    expect(getUi().sel).toBe('cap-b1')
    expect(getUi().panel).toBe(false)
    expect(document.querySelector('li[data-cap="cap-b1"]')).toHaveAttribute('aria-current', 'true')
  })

  it('on a selected capture, h, l, ⌥↑ ⌥↓ and ⏎ do nothing: no write, no sheet', async () => {
    const h = await renderApp(withCaptures())
    openWatering()
    fireEvent.click(capLine('th_seedbed') as HTMLButtonElement)
    act(() => setUi({ sel: 'cap-b2' }))
    await press('h')
    await press('l')
    await press('ArrowUp', { altKey: true })
    await press('ArrowDown', { altKey: true })
    await press('Enter')
    expect(h.writes()).toEqual([])
    expect(getUi().panel).toBe(false)
    expect(getUi().sel).toBe('cap-b2')
  })

  it('a project with captures and no subject still shows, with its Captures line', async () => {
    const d = makeBacklog()
    d.themes.push({ id: 'th_new', name: 'Nouveau', kind: 'project', scope: 'perso', space: 'Vide', archived: false })
    d.captures = [cap('cap-n1', 'th_new', 'Première idée')]
    await renderApp(d)
    fireEvent.click(groupButton('th:th_new'))
    expect(capLine('th_new')).toHaveTextContent(/Captures\s*1/)
  })
})
