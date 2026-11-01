import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { resetLaunches } from '@/list/launch'
import { resetToasts } from '@/ui/toasts'
import { getUi, setUi } from '@/state/ui'
import { makeBacklog, minutesAgo } from '@/test/backlog'
import { groupButton, renderApp } from '@/test/render'

function cmdK() {
  fireEvent.keyDown(document.activeElement ?? document.body, { key: 'k', code: 'KeyK', metaKey: true })
}

function dialog(): HTMLElement {
  return screen.getByRole('dialog', { name: 'Palette de commandes' })
}

function input(): HTMLInputElement {
  return within(dialog()).getByRole('combobox') as HTMLInputElement
}

function options(): string[] {
  return within(dialog())
    .queryAllByRole('option')
    .map((o) => o.textContent ?? '')
}

function withEte() {
  const d = makeBacklog()
  d.subjects.push({ id: 'ete', theme_id: 'th_meth', title: 'Été atlas', status: 'inbox', scope: 'perso', updated_at: minutesAgo(5), created_at: minutesAgo(900) })
  return d
}

describe('Palette ⌘K', () => {
  it('⌘K opens the palette, focused on its field; ⌘K again closes it', async () => {
    await renderApp()
    expect(screen.queryByRole('dialog', { name: 'Palette de commandes' })).toBeNull()
    cmdK()
    expect(dialog()).toBeInTheDocument()
    expect(input()).toHaveAttribute('placeholder', 'Aller à un sujet, filtrer, grouper…')
    expect(document.activeElement).toBe(input())
    cmdK()
    expect(screen.queryByRole('dialog', { name: 'Palette de commandes' })).toBeNull()
  })

  it('an empty field lists the base commands only, with their keys', async () => {
    await renderApp()
    cmdK()
    const o = options()
    expect(o.some((x) => x.startsWith('Grouper par : Scope'))).toBe(true)
    expect(o.some((x) => x.startsWith('Grouper par : Statut'))).toBe(true)
    expect(o.some((x) => /barre latérale/.test(x))).toBe(true)
    expect(o.some((x) => /raccourcis/.test(x))).toBe(true)
    expect(o.some((x) => x.startsWith('Capturer…'))).toBe(true)
    expect(within(dialog()).queryByRole('group', { name: 'Sujets' })).toBeNull()
    // 5 areas of the fixture (Allotment, Garden, Vide, Sans aire, Vineyard) + 4 groupings + sidebar + keys + capture + 6 theme commands
    expect(o).toHaveLength(18)
    expect(o.slice(0, 2)).toEqual(["Aller à l'aire : Allotmentperso" + 'g' + 'puis' + '1', "Aller à l'aire : Gardenperso" + 'g' + 'puis' + '2'])
  })

  it('under the dock width: the sidebar command says « Ranger » while the floating sidebar is open', async () => {
    await renderApp(undefined, { width: 1024 })
    cmdK()
    expect(options().some((x) => x.startsWith('Ouvrir la barre latérale'))).toBe(true)
    cmdK()
    fireEvent.keyDown(document.body, { key: 'b', code: 'KeyB', metaKey: true })
    cmdK()
    expect(options().some((x) => x.startsWith('Ranger la barre latérale'))).toBe(true)
    expect(options().some((x) => x.startsWith('Ouvrir la barre latérale'))).toBe(false)
  })

  it('with a query: « Commandes », the theme groups, then « Sujets », 6 subjects at most', async () => {
    const { user } = await renderApp()
    cmdK()
    await user.type(input(), 'o')
    const groups = within(dialog()).getAllByRole('group')
    const names = groups.map((g) => g.getAttribute('aria-labelledby')).map((id) => (id ? document.getElementById(id)?.textContent : ''))
    expect(names).toEqual(['Commandes', 'Thème', 'Apparence', 'Réglages', 'Sujets'])
    const subs = within(within(dialog()).getByRole('group', { name: 'Sujets' })).getAllByRole('option')
    expect(subs).toHaveLength(6)
  })

  it('⌘1…⌘6 goes to the Nth subject found, each row shows its key; ⌘7 with 6 subjects does nothing', async () => {
    const { user } = await renderApp()
    cmdK()
    await user.type(input(), 'o')
    const subs = within(within(dialog()).getByRole('group', { name: 'Sujets' })).getAllByRole('option')
    expect(subs.map((o) => o.querySelector('.pal-keys')?.textContent)).toEqual(['⌘1', '⌘2', '⌘3', '⌘4', '⌘5', '⌘6'])
    const second = subs[1].getAttribute('data-value')?.replace(/^sub:/, '')
    fireEvent.keyDown(input(), { key: '7', code: 'Digit7', metaKey: true })
    expect(dialog()).not.toBeNull()
    // AZERTY: ⌘ + the « 1/& » key's neighbour sends « é », the code stays Digit2
    fireEvent.keyDown(input(), { key: 'é', code: 'Digit2', metaKey: true })
    expect(screen.queryByRole('dialog', { name: 'Palette de commandes' })).toBeNull()
    expect(getUi().sel).toBe(second)
  })

  it('every word must match, accents ignored: « ete atla » finds « Été atlas » only', async () => {
    const { user } = await renderApp(withEte())
    cmdK()
    await user.type(input(), 'ete atla')
    const subs = within(within(dialog()).getByRole('group', { name: 'Sujets' })).getAllByRole('option')
    expect(subs).toHaveLength(1)
    expect(subs[0]).toHaveTextContent('Été atlas')
    expect(subs[0]).toHaveTextContent('Allotment › Outils')
  })

  it('each subject shows its status icon, its short title and « aire › projet »', async () => {
    const { user } = await renderApp()
    cmdK()
    await user.type(input(), 'atlas web')
    const opt = within(within(dialog()).getByRole('group', { name: 'Sujets' })).getByRole('option')
    expect(opt.querySelector('.si svg')).not.toBeNull()
    expect(opt).toHaveTextContent('Atlas web')
    expect(opt.querySelector('.muted')).toHaveTextContent('Allotment › pigtail')
  })

  it('a word of a folded subject → ⏎ → the sheet opens on it and its path is unfolded', async () => {
    const { user } = await renderApp()
    // Garden › Seedbed is a folded project by default
    expect(groupButton('th:th_seedbed')).toHaveAttribute('aria-expanded', 'false')
    cmdK()
    await user.type(input(), 'watering')
    await user.keyboard('{Enter}')
    expect(screen.queryByRole('dialog', { name: 'Palette de commandes' })).toBeNull()
    expect(getUi().sel).toBe('watering-1')
    expect(document.querySelector('.lpanel h2')).toHaveTextContent('Watering atlas')
    expect(groupButton('th:th_seedbed')).toHaveAttribute('aria-expanded', 'true')
    expect(document.querySelector('li.lr6[data-id="watering-1"]')).not.toBeNull()
  })

  it('a subject hidden by a filter: the filters clear so its row shows', async () => {
    const { user } = await renderApp()
    setUi({ query: 'relay' })
    cmdK()
    await user.type(input(), 'watering')
    await user.keyboard('{Enter}')
    expect(getUi().query).toBe('')
    expect(document.querySelector('li.lr6[data-id="watering-1"]')).not.toBeNull()
  })

  it('a subject past the project cap: « Afficher les N autres » is opened for it', async () => {
    const { user } = await renderApp()
    cmdK()
    await user.type(input(), 'inbox four')
    await user.keyboard('{Enter}')
    expect(document.querySelector('li.lr6[data-id="pig-i4"]')).not.toBeNull()
  })

  it('a command runs and closes the palette', async () => {
    const { user } = await renderApp()
    cmdK()
    await user.type(input(), 'grouper statut')
    await user.keyboard('{Enter}')
    expect(getUi().group).toBe('statut')
    expect(screen.queryByRole('dialog', { name: 'Palette de commandes' })).toBeNull()
  })

  it('no result: « Aucun résultat pour « q » »', async () => {
    const { user } = await renderApp()
    cmdK()
    await user.type(input(), 'zzzz')
    expect(options()).toHaveLength(0)
    expect(within(dialog()).getByText('Aucun résultat pour « zzzz »')).toBeInTheDocument()
  })
})

describe('Palette: Capture ›', () => {
  function withMany() {
    const d = makeBacklog()
    for (const [id, name] of [['th_x1', 'Jardin'], ['th_x2', 'Freelance'], ['th_x3', 'Asso'], ['th_q', 'À qualifier']]) {
      d.themes.push({ id, name, kind: 'project', scope: 'perso', space: 'Allotment', archived: false })
    }
    return d
  }

  function capture(): HTMLElement {
    return screen.getByRole('dialog', { name: 'Capture' })
  }

  it('c opens the mode: « Capture › », the field, at most 5 projects, never « À qualifier »', async () => {
    await renderApp(withMany())
    fireEvent.keyDown(document.body, { key: 'c', code: 'KeyC' })
    expect(getUi().palette).toBe('capture')
    expect(within(capture()).getByText('Capture ›')).toBeInTheDocument()
    const field = within(capture()).getByRole('combobox', { name: 'Texte de la capture' })
    expect(field).toHaveAttribute('placeholder', 'Une idée, un besoin, en une ligne')
    expect(document.activeElement).toBe(field)
    const opts = within(within(capture()).getByRole('listbox', { name: 'Projet' })).getAllByRole('option')
    expect(opts.length).toBe(5)
    expect(opts.map((o) => o.textContent).join(' ')).not.toMatch(/qualifier/i)
  })

  it('c typed in the search field is a letter, not the mode', async () => {
    const { user } = await renderApp()
    await user.type(screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' }), 'c')
    expect(screen.queryByRole('dialog', { name: 'Capture' })).toBeNull()
  })

  it('the command « Capturer… » opens the same mode', async () => {
    const { user } = await renderApp(withMany())
    cmdK()
    await user.type(input(), 'captur')
    await user.keyboard('{Enter}')
    expect(screen.queryByRole('dialog', { name: 'Palette de commandes' })).toBeNull()
    expect(within(capture()).getByRole('combobox', { name: 'Texte de la capture' })).toBeInTheDocument()
    expect(within(within(capture()).getByRole('listbox', { name: 'Projet' })).getAllByRole('option')).toHaveLength(5)
  })
})

describe('Palette: the selected subject’s commands (D15)', () => {
  afterEach(() => {
    resetToasts()
    resetLaunches()
  })

  it('pig-r1 selected: « Lancer » and « Mettre en standby » listed; « Lancer » sends exactly one POST …/launch', async () => {
    const h = await renderApp()
    h.setRoute('POST /subjects/pig-r1/launch', () => ({ status: 202, body: { data: { launch_id: 'ln_1', subject_id: 'pig-r1', state: 'pending', cell: 'w1', tab_title: '' } } }))
    act(() => setUi({ sel: 'pig-r1' }))
    cmdK()
    const labels = options().map((o) => o.replace(/[lh]$/, ''))
    expect(labels).toContain('Lancer')
    expect(labels).toContain('Mettre en standby')
    const launch = within(dialog()).getAllByRole('option').find((o) => o.textContent === 'Lancerl') as HTMLElement
    await h.user.click(launch)
    await waitFor(() => expect(h.writes().filter((w) => w.method === 'POST')).toEqual([{ method: 'POST', url: '/subjects/pig-r1/launch', body: undefined }]))
    expect(screen.queryByRole('dialog', { name: 'Palette de commandes' })).toBeNull()
  })

  it('pig-d1 (Done) selected: neither « Lancer » nor « Mettre en standby / Reprendre »', async () => {
    await renderApp()
    act(() => setUi({ sel: 'pig-d1' }))
    cmdK()
    expect(options().filter((o) => /^(Lancer|Mettre en standby|Reprendre)/.test(o))).toEqual([])
  })
})

describe('Palette: theme and appearance commands (T9)', () => {
  afterEach(() => {
    document.documentElement.removeAttribute('data-theme-palette')
    document.documentElement.removeAttribute('data-theme')
  })

  it('« thème » (accent ignored) lists the 6 commands in order, by their keywords', async () => {
    const { user } = await renderApp()
    cmdK()
    await user.type(input(), 'thème')
    const cmds = ['Thème', 'Apparence', 'Réglages'].flatMap((name) => within(within(dialog()).getByRole('group', { name })).getAllByRole('option'))
    const heads = cmds.map((o) => o.querySelector('.pal-t')?.textContent)
    expect(heads).toEqual(['Theme: Terra', 'Theme: Ocean', 'Appearance: Light', 'Appearance: Dark', 'Appearance: System', 'Open Settings'])
  })

  it('the current theme and appearance carry « Actif » (a check and written text), one per family', async () => {
    const { user } = await renderApp()
    cmdK()
    await user.type(input(), 'theme')
    const cur = [...dialog().querySelectorAll('.pal-cur')]
    expect(cur.map((c) => c.textContent)).toEqual(['Actif', 'Actif'])
    expect(cur.map((c) => c.closest('[role="option"]')?.querySelector('.pal-t')?.textContent)).toEqual(['Theme: Terra', 'Appearance: System'])
    expect(cur.every((c) => c.querySelector('svg'))).toBe(true)
    expect(dialog().querySelector('.badge')).toBeNull()
  })

  it('⏎ on « Theme: Ocean » sets the attribute, saves it and closes', async () => {
    const { user } = await renderApp()
    cmdK()
    await user.type(input(), 'theme ocean')
    await user.keyboard('{Enter}')
    expect(document.documentElement.getAttribute('data-theme-palette')).toBe('ocean')
    expect(localStorage.getItem('pigtail-palette')).toBe('ocean')
    expect(screen.queryByRole('dialog', { name: 'Palette de commandes' })).toBeNull()
  })

  it('« Open Settings » opens the popover', async () => {
    const { user } = await renderApp()
    cmdK()
    await user.type(input(), 'open settings')
    await user.keyboard('{Enter}')
    expect(screen.queryByRole('dialog', { name: 'Palette de commandes' })).toBeNull()
    expect(screen.getByRole('dialog', { name: 'Réglages' })).toBeInTheDocument()
  })

  it('« thème »: three headed groups (Thème, Apparence, Réglages), one icon per row, the prefix in bold', async () => {
    const { user } = await renderApp()
    cmdK()
    await user.type(input(), 'thème')
    const groups = within(dialog()).getAllByRole('group')
    const names = groups.map((g) => document.getElementById(g.getAttribute('aria-labelledby') ?? '')?.textContent)
    expect(names.slice(0, 3)).toEqual(['Thème', 'Apparence', 'Réglages'])
    const rows = groups.slice(0, 3).flatMap((g) => within(g).getAllByRole('option'))
    expect(rows).toHaveLength(6)
    for (const r of rows) expect(r.querySelector('.pal-l > svg, .pal-l > .thsw'), r.textContent ?? '').not.toBeNull()
    const theme = within(groups[0]).getAllByRole('option')
    expect(theme.map((r) => r.querySelector('.thsw')?.getAttribute('data-theme-palette'))).toEqual(['terra', 'ocean'])
    expect(within(groups[1]).getAllByRole('option').map((r) => r.querySelector('.pal-t b')?.textContent)).toEqual(['Appearance', 'Appearance', 'Appearance'])
    expect(theme.map((r) => r.querySelector('.pal-t b')?.textContent)).toEqual(['Theme', 'Theme'])
    expect(within(groups[2]).getByRole('option')).toHaveTextContent('Open Settings')
  })

  it('in the read-only Sessions view, a backlog selection offers neither « Lancer » nor « Mettre en standby »', async () => {
    await renderApp()
    act(() => setUi({ sel: 'pig-r1', view: 'sessions' }))
    cmdK()
    expect(options().filter((o) => /^(Lancer|Mettre en standby|Reprendre)/.test(o))).toEqual([])
  })

  it('« Open Settings » opens Réglages and never closes them (idempotent)', async () => {
    await renderApp()
    act(() => setUi({ settings: true }))
    cmdK()
    const o = within(dialog()).getAllByRole('option').find((x) => x.textContent?.startsWith('Open Settings')) as HTMLElement
    fireEvent.click(o)
    expect(getUi().settings).toBe(true)
  })
})
