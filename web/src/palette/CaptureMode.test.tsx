import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import type { Capture } from '@/api/types'
import { getUi, setUi } from '@/state/ui'
import { makeBacklog, withCaptures, type BacklogData } from '@/test/backlog'
import { renderApp, type Reply, type WriteCall } from '@/test/render'
import { resetToasts } from '@/ui/toasts'

afterEach(() => resetToasts())

let n = 0

/** The daemon's POST /captures over the test data: 201 with the new capture, which the next read lists. */
function createRoute(data: () => BacklogData) {
  return (req: WriteCall): Reply => {
    const { theme_id, text } = req.body as { theme_id: string; text: string }
    const t = data().themes.find((x) => x.id === theme_id)
    n += 1
    const c: Capture = { id: `cap_new${n}`, theme_id, text, scope: t?.scope ?? '', created_at: new Date().toISOString() }
    data().captures = [...(data().captures ?? []), c]
    return { status: 201, body: { data: c } }
  }
}

function deleteRoute(data: () => BacklogData) {
  return (req: WriteCall): Reply => {
    const id = decodeURIComponent(req.url.slice('/captures/'.length))
    data().captures = (data().captures ?? []).filter((c) => c.id !== id)
    return { status: 204 }
  }
}

async function setup(data: BacklogData = withCaptures(), width = 1440) {
  const h = await renderApp(data, { width })
  h.setRoute('POST /captures', createRoute(h.data))
  return h
}

async function press(k: string, extra: Partial<KeyboardEventInit> = {}, target: Element = document.activeElement ?? document.body) {
  await act(async () => {
    fireEvent.keyDown(target, { key: k, ...extra })
  })
}

function dialog(): HTMLElement {
  return screen.getByRole('dialog', { name: 'Capture' })
}

function field(): HTMLInputElement {
  return within(dialog()).getByRole('combobox', { name: 'Texte de la capture' }) as HTMLInputElement
}

function projects(): HTMLElement[] {
  return within(within(dialog()).getByRole('listbox', { name: 'Projet' })).getAllByRole('option')
}

function picked(): string {
  return projects().find((o) => o.getAttribute('aria-selected') === 'true')?.querySelector('.pal-n')?.textContent ?? ''
}

function toast(): HTMLElement {
  const t = screen.getAllByRole('status').filter((x) => x.classList.contains('toast6'))
  if (t.length !== 1) throw new Error(`toasts: ${t.length}`)
  return t[0]
}

function capLine(theme: string): HTMLButtonElement | null {
  return document.querySelector<HTMLButtonElement>(`button.capl[data-g="cap:${theme}"]`)
}

const posts = (h: { writes(): WriteCall[] }) => h.writes().filter((w) => w.method === 'POST')

describe('Capture › (palette mode)', () => {
  it('↑↓ choose the project, ⇥ proposes the next one', async () => {
    await setup()
    await press('c')
    expect(picked()).toBe('pigtail')
    await press('ArrowDown', {}, field())
    expect(picked()).toBe('Outils')
    await press('ArrowUp', {}, field())
    expect(picked()).toBe('pigtail')
    await press('Tab', {}, field())
    expect(picked()).toBe('Outils')
    await press('Tab', {}, field())
    expect(picked()).toBe('Seedbed')
  })

  it('each project line carries « aire · scope »; the current project is the selected subject’s', async () => {
    await setup()
    act(() => setUi({ sel: 'wk-1' }))
    await press('c')
    expect(picked()).toBe('Board')
    expect(projects()[0]).toHaveTextContent('Vineyard · globex')
    expect(projects()[0]).toHaveTextContent('projet courant')
  })

  it('⌫ on an empty field goes back to the commands; Esc closes', async () => {
    await setup()
    await press('c')
    await press('Backspace', {}, field())
    expect(screen.queryByRole('dialog', { name: 'Capture' })).toBeNull()
    expect(screen.getByRole('dialog', { name: 'Palette de commandes' })).toBeInTheDocument()
    expect(getUi().palette).toBe(true)
    setUi({ palette: 'capture' })
    await waitFor(() => expect(dialog()).toBeInTheDocument())
    await press('Escape', {}, field())
    expect(screen.queryByRole('dialog', { name: 'Capture' })).toBeNull()
    expect(getUi().palette).toBe(false)
  })

  it('⌫ with text deletes a letter, it does not leave the mode', async () => {
    const h = await setup()
    await press('c')
    await h.user.type(field(), 'ab{Backspace}')
    expect(field()).toHaveValue('a')
    expect(dialog()).toBeInTheDocument()
  })

  it('an empty field + ⏎ (or blanks only): no call, the mode stays', async () => {
    const h = await setup()
    await press('c')
    await press('Enter', {}, field())
    await h.user.type(field(), '   ')
    await press('Enter', {}, field())
    expect(h.writes()).toEqual([])
    expect(dialog()).toBeInTheDocument()
  })

  it('text + a globex project + ⏎: POST /captures with its theme_id, toast « Capturée dans Board », Captures +1, the capture selected and its line unfolded', async () => {
    const h = await setup()
    await press('c')
    await h.user.type(field(), '  Une idée board  ')
    while (picked() !== 'Board') await press('ArrowDown', {}, field())
    await press('Enter', {}, field())
    await waitFor(() => expect(posts(h)).toHaveLength(1))
    expect(posts(h)[0]).toEqual({ method: 'POST', url: '/captures', body: { theme_id: 'th_board', text: 'Une idée board' } })
    expect(screen.queryByRole('dialog', { name: 'Capture' })).toBeNull()
    await waitFor(() => expect(toast()).toHaveTextContent('Capturée dans Board'))
    await waitFor(() => expect(getUi().sel).toBe('cap_new' + n))
    expect(capLine('th_board')).toHaveAttribute('aria-expanded', 'true')
    expect(capLine('th_board')).toHaveTextContent(/Captures\s*2/)
    expect(document.querySelector(`li[data-cap="cap_new${n}"]`)).toHaveTextContent('Une idée board')
  })

  it('from the Statut grouping with a search that hides it: the grouping goes back to Aire and the search clears so the capture shows', async () => {
    const h = await setup()
    act(() => setUi({ group: 'statut', query: 'zzz' }))
    await press('c')
    await h.user.type(field(), 'idée')
    await press('Enter', {}, field())
    await waitFor(() => expect(posts(h)).toHaveLength(1))
    expect(getUi().group).toBe('aire')
    expect(getUi().query).toBe('')
    await waitFor(() => expect(document.querySelector(`li.cap-row.sel`)).not.toBeNull())
  })

  it('z deletes the capture (DELETE /captures/<id>): « Annulé · Capture supprimée », Captures back', async () => {
    const h = await setup()
    h.setRoute(`DELETE /captures/cap_new${n + 1}`, deleteRoute(h.data))
    await press('c')
    await h.user.type(field(), 'à annuler')
    while (picked() !== 'Seedbed') await press('ArrowDown', {}, field())
    await press('Enter', {}, field())
    await waitFor(() => expect(toast()).toHaveTextContent('Capturée dans Seedbed'))
    await waitFor(() => expect(capLine('th_seedbed')).toHaveTextContent(/Captures\s*3/))
    ;(document.activeElement as HTMLElement | null)?.blur()
    await press('z', {}, document.body)
    await waitFor(() => expect(h.writes().filter((w) => w.method === 'DELETE')).toHaveLength(1))
    expect(h.writes().at(-1)).toEqual({ method: 'DELETE', url: `/captures/cap_new${n}`, body: undefined })
    await waitFor(() => expect(toast()).toHaveTextContent('Annulé · Capture supprimée'))
    await waitFor(() => expect(capLine('th_seedbed')).toHaveTextContent(/Captures\s*2/))
  })

  it('423: the maintenance toast, no capture shown (rolled back)', async () => {
    const h = await setup()
    h.setRoute('POST /captures', () => ({ status: 423, body: { error: { code: 'MAINTENANCE_FROZEN', message: 'frozen' } } }))
    await press('c')
    await h.user.type(field(), 'gelée')
    while (picked() !== 'Seedbed') await press('ArrowDown', {}, field())
    await press('Enter', {}, field())
    await waitFor(() => expect(toast()).toHaveTextContent('Maintenance en cours : réessaie quand elle est finie.'))
    expect(posts(h)).toHaveLength(1)
    await waitFor(() => expect(document.querySelectorAll('li.cap-row')).toHaveLength(2))
    expect(capLine('th_seedbed')).toHaveTextContent(/Captures\s*2/)
    expect(screen.queryByText('gelée')).toBeNull()
  })

  it('an empty project list: the D21 copy, ⏎ writes nothing', async () => {
    const data = makeBacklog()
    data.themes = []
    const h = await setup(data)
    await press('c')
    expect(within(dialog()).getByText('Aucun projet à proposer : crée-en un depuis la CLI.')).toBeInTheDocument()
    await h.user.type(field(), 'perdue')
    await press('Enter', {}, field())
    expect(h.writes()).toEqual([])
  })
})

describe('Capture at 390 px: « + » and the bottom sheet', () => {
  it('the header carries « + » (Capturer); it opens the bottom sheet; « Enregistrer dans X » writes; the toast has its « annuler » button', async () => {
    const h = await setup(withCaptures(), 390)
    h.setRoute(`DELETE /captures/cap_new${n + 1}`, deleteRoute(h.data))
    const plus = within(document.querySelector('header.d-head') as HTMLElement).getByRole('button', { name: 'Capturer' })
    await h.user.click(plus)
    const sheet = dialog()
    expect(sheet).toHaveClass('msheet')
    const save = within(sheet).getByRole('button', { name: /^Enregistrer dans / })
    expect(save).toBeDisabled()
    await h.user.type(within(sheet).getByRole('textbox', { name: 'Texte de la capture' }), 'au doigt')
    await h.user.click(within(sheet).getByRole('button', { name: 'Seedbed' }))
    expect(within(sheet).getByRole('button', { name: 'Seedbed' })).toHaveAttribute('aria-pressed', 'true')
    await h.user.click(within(sheet).getByRole('button', { name: 'Enregistrer dans Seedbed' }))
    await waitFor(() => expect(posts(h)).toEqual([{ method: 'POST', url: '/captures', body: { theme_id: 'th_seedbed', text: 'au doigt' } }]))
    expect(screen.queryByRole('dialog', { name: 'Capture' })).toBeNull()
    await waitFor(() => expect(toast()).toHaveTextContent('Capturée dans Seedbed'))
    await h.user.click(within(toast()).getByRole('button', { name: /annuler/ }))
    await waitFor(() => expect(h.writes().filter((w) => w.method === 'DELETE')).toHaveLength(1))
  })

  it('the « + » sits on the header’s first row, right after the scopes (before the search), not alone on a row of its own', async () => {
    await setup(withCaptures(), 390)
    const head = document.querySelector('header.d-head') as HTMLElement
    const plus = within(head).getByRole('button', { name: 'Capturer' })
    const search = within(head).getByRole('searchbox', { name: 'Rechercher dans le backlog' })
    expect(plus.compareDocumentPosition(search) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('the « + » shows on the phone only', async () => {
    await setup(withCaptures(), 1440)
    expect(within(document.querySelector('header.d-head') as HTMLElement).queryByRole('button', { name: 'Capturer' })).toBeNull()
  })
})
