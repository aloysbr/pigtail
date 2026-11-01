import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import type { Subject } from '@/api/types'
import { getUi, setUi } from '@/state/ui'
import { groupButton, renderApp, type Reply, type WriteCall } from '@/test/render'
import { resetToasts } from '@/ui/toasts'
import { resetLaunches } from './launch'

function selected(): string | undefined {
  return (document.querySelector('li.lr6.sel') as HTMLElement | null)?.dataset.id
}

function key(k: string, code = '') {
  fireEvent.keyDown(document.activeElement ?? document.body, { key: k, code })
}

describe('Keyboard', () => {
  it('→ on a group header unfolds it; j/k and ↓/↑ move the selection; ← folds the selected row’s group', async () => {
    await renderApp()
    groupButton('th:th_pig').focus()
    key('ArrowRight')
    expect(groupButton('th:th_pig')).toHaveAttribute('aria-expanded', 'true')
    key('j')
    expect(selected()).toBe('pig-live')
    key('j')
    expect(selected()).toBe('pig-r1')
    key('ArrowDown')
    expect(selected()).toBe('pig-r2')
    key('k')
    expect(selected()).toBe('pig-r1')
    key('ArrowUp')
    expect(selected()).toBe('pig-live')
    expect(document.activeElement).toBe(document.querySelector('li[data-id="pig-live"]'))
    key('ArrowLeft')
    expect(groupButton('th:th_pig')).toHaveAttribute('aria-expanded', 'false')
  })

  it('v then 1-4 changes the grouping, digits read by code (AZERTY)', async () => {
    await renderApp()
    key('v')
    expect(screen.getByRole('menu', { name: 'Grouper par' })).toBeInTheDocument()
    key("'", 'Digit4')
    expect(screen.queryByRole('menu', { name: 'Grouper par' })).toBeNull()
    expect(screen.getByRole('button', { name: /Grouper/ })).toHaveTextContent('Grouper : Statut')
    key('v')
    key('&', 'Digit1')
    expect(screen.getByRole('button', { name: /Grouper/ })).toHaveTextContent('Grouper : Scope › Aire')
  })

  it('g then 2 goes to the second area, as f 2 and a click on the sidebar entry do', async () => {
    await renderApp()
    key('g')
    expect(getUi().menu).toBe('go')
    key('2', 'Digit2')
    expect(getUi().areaF).toEqual({ scope: 'perso', name: 'Garden' })
    expect(getUi().menu).toBeNull()
  })

  it('g then a non-digit ends the wait; Esc and a click outside end it too', async () => {
    await renderApp()
    key('g')
    key('x')
    expect(getUi().menu).toBeNull()
    expect(getUi().areaF).toBeNull()
    key('g')
    key('Escape')
    expect(getUi().menu).toBeNull()
    key('g')
    fireEvent.pointerDown(document.body)
    expect(getUi().menu).toBeNull()
  })

  it('g then a digit with no such area ends the wait and filters nothing', async () => {
    await renderApp()
    key('g')
    key('9', 'Digit9')
    expect(getUi().menu).toBeNull()
    expect(getUi().areaF).toBeNull()
  })

  it('Scopes menu + digit filters nothing (only Filtre and g pick an area)', async () => {
    await renderApp()
    setUi({ menu: 'scopes' })
    key('2', 'Digit2')
    expect(getUi().areaF).toBeNull()
    expect(getUi().menu).toBe('scopes')
  })

  it('g then Shift+Digit2 (AZERTY) goes to the second area: Shift alone keeps the wait', async () => {
    await renderApp()
    key('g')
    fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Shift', code: 'ShiftLeft', shiftKey: true })
    expect(getUi().menu).toBe('go')
    fireEvent.keyDown(document.activeElement ?? document.body, { key: 'é', code: 'Digit2', shiftKey: true })
    expect(getUi().areaF).toEqual({ scope: 'perso', name: 'Garden' })
  })

  it('a lone modifier keeps the wait', async () => {
    await renderApp()
    key('g')
    for (const k of ['Shift', 'CapsLock', 'Meta', 'Alt', 'Control']) {
      fireEvent.keyDown(document.activeElement ?? document.body, { key: k })
      expect(getUi().menu, k).toBe('go')
    }
  })

  it('keys are read by position: code Digit2 with key é gives area 2', async () => {
    await renderApp()
    key('g')
    key('é', 'Digit2')
    expect(getUi().areaF?.name).toBe('Garden')
  })

  it('Esc closes an open menu without clearing the search', async () => {
    const { user } = await renderApp()
    await user.type(screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' }), 'atlas')
    await user.click(screen.getByRole('button', { name: /Filtre/ }))
    expect(screen.getByRole('menu', { name: 'Filtrer' })).toBeInTheDocument()
    key('Escape')
    expect(screen.queryByRole('menu', { name: 'Filtrer' })).toBeNull()
    expect(screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' })).toHaveValue('atlas')
  })

  it('Esc with no layer open leaves an active search and filters alone', async () => {
    const { user } = await renderApp()
    await user.type(screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' }), 'atlas')
    ;(document.activeElement as HTMLElement).blur()
    setUi({ areaF: { scope: 'perso', name: 'Allotment' }, statusF: 'ouvert' })
    key('Escape')
    expect(screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' })).toHaveValue('atlas')
    expect(getUi().areaF).not.toBeNull()
    expect(getUi().statusF).toBe('ouvert')
  })

  it('/ focuses the search, f opens the filter menu, `,` toggles the footer', async () => {
    await renderApp()
    key('/')
    expect(document.activeElement).toBe(screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' }))
    ;(document.activeElement as HTMLElement).blur()
    key('f')
    expect(screen.getByRole('menu', { name: 'Filtrer' })).toBeInTheDocument()
    key('Escape')
    expect(screen.getByRole('contentinfo', { name: 'Raccourcis clavier' })).toBeInTheDocument()
    key(',')
    expect(screen.queryByRole('contentinfo', { name: 'Raccourcis clavier' })).toBeNull()
    key(',')
    expect(screen.getByRole('contentinfo', { name: 'Raccourcis clavier' })).toBeInTheDocument()
  })

  it('letters typed in the search field are not shortcuts', async () => {
    const { user } = await renderApp()
    await user.type(screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' }), 'gf,')
    expect(screen.queryByRole('menu')).toBeNull()
    expect(screen.getByRole('contentinfo', { name: 'Raccourcis clavier' })).toBeInTheDocument()
  })

  it('keys are printed on their buttons: / on the search, f on Filtre, v on Grouper', async () => {
    await renderApp()
    const keys = (el: Element | null) => [...(el?.querySelectorAll('kbd') ?? [])].map((x) => x.textContent)
    expect(keys(screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' }).closest('label'))).toEqual(['/'])
    expect(keys(screen.getByRole('button', { name: /Filtre/ }))).toEqual(['f'])
    expect(keys(screen.getByRole('button', { name: /Grouper/ }))).toEqual(['v'])
  })

  it('under the docking width the Navigation button prints ⌘B', async () => {
    await renderApp(undefined, { width: 1024 })
    expect([...screen.getByRole('button', { name: 'Navigation' }).querySelectorAll('kbd')].map((x) => x.textContent)).toEqual(['⌘B'])
  })

  it('Esc in the search field does not let the browser clear it behind the app (the field and its width stay)', async () => {
    const { user } = await renderApp()
    const box = screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' })
    await user.type(box, 'atlas')
    expect(fireEvent.keyDown(box, { key: 'Escape' })).toBe(false)
    expect(box).toHaveValue('atlas')
  })
})

/** The daemon's write routes for the matrix: the launch, the status, the ranks; each answers as the daemon would. */
async function matrix() {
  const h = await renderApp()
  h.setRoute('POST /subjects/pig-r1/launch', () => ({ status: 202, body: { data: { launch_id: 'ln_1', subject_id: 'pig-r1', state: 'pending', cell: 'w1', tab_title: 'x' } } }))
  h.setRoute('POST /subjects/pig-r1', (req: WriteCall): Reply => {
    const s = h.data().subjects.find((x) => x.id === 'pig-r1') as Subject
    Object.assign(s, req.body as Partial<Subject>)
    return { status: 200, body: { data: s } }
  })
  h.setRoute('GET /subjects/pig-r1', () => ({ status: 200 }))
  h.setRoute('POST /subjects/ranks', (req: WriteCall): Reply => ({ status: 200, body: { data: (req.body as { changes: unknown[] }).changes } }))
  act(() => setUi({ sel: 'pig-r1' }))
  return h
}

/** What a key did: launches, status writes, rank writes, the capture mode. */
function effects(writes: WriteCall[]) {
  return {
    launch: writes.filter((w) => w.url.endsWith('/launch')).length,
    status: writes.filter((w) => w.url === '/subjects/pig-r1').length,
    rank: writes.filter((w) => w.url === '/subjects/ranks').length,
    capture: getUi().palette === 'capture',
  }
}

const NONE = { launch: 0, status: 0, rank: 0, capture: false }

async function press(k: string, extra: Partial<KeyboardEventInit> = {}, target: Element = document.body) {
  await act(async () => {
    fireEvent.keyDown(target, { key: k, ...extra })
  })
}

describe('Keyboard matrix (lot 3): each of l, h, c, z, ⌥↑↓ does its action and no other', () => {
  afterEach(() => {
    resetToasts()
    resetLaunches()
  })

  it('l launches, nothing else', async () => {
    const h = await matrix()
    await press('l', { code: 'KeyL' })
    await waitFor(() => expect(effects(h.writes())).toEqual({ ...NONE, launch: 1 }))
  })

  it('h writes the status, nothing else', async () => {
    const h = await matrix()
    await press('h', { code: 'KeyH' })
    await waitFor(() => expect(effects(h.writes())).toEqual({ ...NONE, status: 1 }))
    expect(h.writes()[0].body).toEqual({ status: 'parked' })
  })

  it('c opens the capture mode, writes nothing', async () => {
    const h = await matrix()
    await press('c', { code: 'KeyC' })
    expect(effects(h.writes())).toEqual({ ...NONE, capture: true })
  })

  it('⌥↓ writes the ranks, nothing else', async () => {
    const h = await matrix()
    await press('ArrowDown', { altKey: true, code: 'ArrowDown' })
    await waitFor(() => expect(effects(h.writes())).toEqual({ ...NONE, rank: 1 }))
    expect(getUi().sel).toBe('pig-r1')
  })

  it('z with no undoable toast does nothing; after h it undoes h alone', async () => {
    const h = await matrix()
    await press('z', { code: 'KeyZ' })
    expect(effects(h.writes())).toEqual(NONE)
    await press('h', { code: 'KeyH' })
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('en standby'))
    await press('z', { code: 'KeyZ' })
    await waitFor(() => expect(effects(h.writes())).toEqual({ ...NONE, status: 2 }))
    expect(h.writes().at(-1)?.body).toEqual({ status: 'building' })
  })

  it('AZERTY: z is read by e.key — { key: z, code: KeyW } undoes, { key: w, code: KeyZ } does nothing', async () => {
    const h = await matrix()
    await press('h', { code: 'KeyH' })
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('en standby'))
    await press('w', { code: 'KeyZ' })
    expect(effects(h.writes())).toEqual({ ...NONE, status: 1 })
    await press('z', { code: 'KeyW' })
    await waitFor(() => expect(effects(h.writes())).toEqual({ ...NONE, status: 2 }))
  })

  it('none of l, h, c, z, ⌥↑↓ acts in a text field', async () => {
    const h = await matrix()
    const box = screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' })
    box.focus()
    for (const [k, extra] of [
      ['l', {}],
      ['h', {}],
      ['c', {}],
      ['z', {}],
      ['ArrowUp', { altKey: true }],
      ['ArrowDown', { altKey: true }],
    ] as const) {
      await press(k, extra, box)
    }
    expect(effects(h.writes())).toEqual(NONE)
    expect(getUi().sel).toBe('pig-r1')
  })

  it('the lot 1 keys keep their action next to them: j moves, v opens the grouping menu (D9: g is « aller à l\'aire »), ← → fold', async () => {
    await matrix()
    await press('v')
    expect(screen.getByRole('menu', { name: 'Grouper par' })).toBeInTheDocument()
    await press('Escape')
    groupButton('th:th_pig').focus()
    await press('ArrowRight', {}, groupButton('th:th_pig'))
    expect(groupButton('th:th_pig')).toHaveAttribute('aria-expanded', 'true')
    act(() => setUi({ sel: 'pig-r1' }))
    await press('j')
    expect(getUi().sel).toBe('pig-r2')
    expect(getUi().palette).toBe(false)
  })
})
