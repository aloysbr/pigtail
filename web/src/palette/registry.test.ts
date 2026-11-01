import type { Subject } from '@/api/types'
import { getUi, setUi } from '@/state/ui'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { clearCommands, commands, registerBuiltins, registerCommand, type PaletteCtx } from './registry'

const AREAS = [
  { scope: 'perso', name: 'Allotment', count: 13, projects: [] },
  { scope: 'perso', name: 'Garden', count: 2, projects: [] },
  { scope: 'globex', name: 'Vineyard', count: 2, projects: [] },
]
const ctx: PaletteCtx = { docked: true, wide: true, phone: false, areas: AREAS }

afterEach(() => {
  clearCommands()
})

describe('palette registry', () => {
  it('adds commands in registration order; re-registering an id replaces it in place', () => {
    registerCommand({ id: 'a', label: 'Alpha', group: 'nav', run: () => {} })
    registerCommand({ id: 'b', label: 'Beta', group: 'nav', run: () => {} })
    registerCommand({ id: 'c', label: 'Gamma', group: 'act', run: () => {} })
    expect(commands(ctx).map((c) => c.label)).toEqual(['Alpha', 'Beta', 'Gamma'])
    registerCommand({ id: 'a', label: 'Alpha 2', group: 'nav', run: () => {} })
    expect(commands(ctx).map((c) => c.label)).toEqual(['Alpha 2', 'Beta', 'Gamma'])
  })

  it('« when » hides a command for a context; a label may depend on the context', () => {
    registerCommand({ id: 'wide', label: 'Wide only', group: 'nav', when: (c) => c.wide, run: () => {} })
    registerCommand({ id: 'side', label: (c) => (c.docked ? 'Ranger' : 'Ouvrir'), group: 'nav', run: () => {} })
    expect(commands(ctx).map((c) => c.label)).toEqual(['Wide only', 'Ranger'])
    expect(commands({ docked: false, wide: false, phone: false, areas: [] }).map((c) => c.label)).toEqual(['Ouvrir'])
  })

  it('the sidebar command reads the real state: docked or floating open both offer « Ranger »', () => {
    registerBuiltins()
    const label = (c: PaletteCtx) => commands(c).find((x) => x.id === 'sidebar')?.label
    const floating: PaletteCtx = { docked: false, wide: false, phone: false, areas: [] }
    expect(label(floating)).toBe('Ouvrir la barre latérale')
    setUi({ floatOpen: true })
    expect(label(floating)).toBe('Ranger la barre latérale')
    expect(label(ctx)).toBe('Ranger la barre latérale')
    setUi({ floatOpen: false })
  })

  it('runs a command with the context it was listed for', () => {
    const run = vi.fn()
    registerCommand({ id: 'x', label: 'X', group: 'nav', run })
    commands(ctx)[0].run()
    expect(run).toHaveBeenCalledWith(ctx)
  })

  it('the built-ins register as any other command, idempotently', () => {
    registerBuiltins()
    registerBuiltins()
    const labels = commands(ctx).map((c) => c.label)
    expect(labels).toEqual([
      "Aller à l'aire : Allotment",
      "Aller à l'aire : Garden",
      "Aller à l'aire : Vineyard",
      'Grouper par : Scope',
      'Grouper par : Aire',
      'Grouper par : Projet',
      'Grouper par : Statut',
      'Ranger la barre latérale',
      'Masquer les raccourcis',
      'Theme: Terra',
      'Theme: Ocean',
      'Appearance: Light',
      'Appearance: Dark',
      'Appearance: System',
      'Open Settings',
    ])
    expect(commands(ctx).find((c) => c.label === 'Grouper par : Aire')?.keys).toBe('v puis 2')
    // the next lot adds one more call, nothing else
    registerCommand({ id: 'capture', label: 'Capturer…', group: 'act', run: () => {} })
    expect(commands(ctx).map((c) => c.label)).toContain('Capturer…')
  })

  it('area commands come first', () => {
    registerBuiltins()
    const list = commands(ctx)
    expect(list.slice(0, 3).map((c) => [c.label, c.keys, c.hint])).toEqual([
      ["Aller à l'aire : Allotment", 'g puis 1', 'perso'],
      ["Aller à l'aire : Garden", 'g puis 2', 'perso'],
      ["Aller à l'aire : Vineyard", 'g puis 3', 'globex'],
    ])
    expect(list.find((c) => c.id.startsWith('group:'))?.hint).toBeUndefined()
    // only as many as there are areas, and at most nine
    expect(commands({ ...ctx, areas: [] }).some((c) => c.id.startsWith('area:'))).toBe(false)
    const many = Array.from({ length: 12 }, (_, i) => ({ scope: 'perso', name: `A${i}`, count: 0, projects: [] }))
    expect(commands({ ...ctx, areas: many }).filter((c) => c.id.startsWith('area:'))).toHaveLength(9)
  })

  it('an area command filters on its area', () => {
    registerBuiltins()
    commands(ctx)[1].run()
    expect(getUi().areaF).toEqual({ scope: 'perso', name: 'Garden' })
  })
})

describe('commands of the selected subject (D15)', () => {
  const sub = (status: string): Subject => ({ id: 's1', theme_id: 'th', title: 'Sujet', status, scope: 'perso', updated_at: '', created_at: '' })
  const at = (status: string, display: PaletteCtx['display'], actions = { moveRank: vi.fn(), toggleStandby: vi.fn(), launch: vi.fn() }): PaletteCtx => ({
    ...ctx,
    selected: sub(status),
    display,
    actions,
  })
  const labels = (c: PaletteCtx) => {
    registerBuiltins()
    return commands(c).map((x) => x.label)
  }

  it('« Lancer » (l) on Ouvert and Standby; absent on En cours, Done, Abandonné and with no selection', () => {
    expect(labels(at('inbox', 'ouvert'))).toContain('Lancer')
    expect(labels(at('parked', 'standby'))).toContain('Lancer')
    expect(labels(at('building', 'cours'))).not.toContain('Lancer')
    expect(labels(at('done', 'done'))).not.toContain('Lancer')
    expect(labels(at('dropped', 'dropped'))).not.toContain('Lancer')
    expect(labels(ctx)).not.toContain('Lancer')
    expect(commands(at('inbox', 'ouvert')).find((c) => c.label === 'Lancer')?.keys).toBe('l')
  })

  it('« Mettre en standby » on Ouvert and En cours, « Reprendre » on Standby (h); neither on Done or Abandonné', () => {
    expect(labels(at('inbox', 'ouvert'))).toContain('Mettre en standby')
    expect(labels(at('building', 'cours'))).toContain('Mettre en standby')
    expect(labels(at('parked', 'standby'))).toContain('Reprendre')
    expect(labels(at('parked', 'standby'))).not.toContain('Mettre en standby')
    expect(labels(at('done', 'done')).filter((l) => /standby|Reprendre/.test(l))).toEqual([])
    expect(labels(at('dropped', 'dropped')).filter((l) => /standby|Reprendre/.test(l))).toEqual([])
    expect(commands(at('parked', 'standby')).find((c) => c.label === 'Reprendre')?.keys).toBe('h')
  })

  it('runs the action on the selected subject', () => {
    const actions = { moveRank: vi.fn(), toggleStandby: vi.fn(), launch: vi.fn() }
    registerBuiltins()
    commands(at('inbox', 'ouvert', actions)).find((c) => c.label === 'Lancer')?.run()
    commands(at('inbox', 'ouvert', actions)).find((c) => c.label === 'Mettre en standby')?.run()
    expect(actions.launch).toHaveBeenCalledWith('s1')
    expect(actions.toggleStandby).toHaveBeenCalledWith('s1')
  })
})

describe('theme and appearance commands (T9)', () => {
  const THEME_IDS = ['theme-terra', 'theme-ocean', 'appearance-light', 'appearance-dark', 'appearance-system', 'open-settings']
  const selected: PaletteCtx = {
    ...ctx,
    selected: { id: 's1', theme_id: 'th', title: 'S', status: 'inbox', scope: 'perso', updated_at: '', created_at: '' },
    display: 'ouvert',
    actions: { toggleStandby: () => {}, launch: () => {} },
  }

  it('lists the 6 commands in order, with keywords for « thème » and French descriptions', () => {
    registerBuiltins()
    const list = commands(ctx).filter((c) => THEME_IDS.includes(c.id))
    expect(list.map((c) => c.id)).toEqual(THEME_IDS)
    expect(list.map((c) => c.hint)).toEqual(['Ivoire et terre cuite', 'Gris pur et indigo', 'Clair', 'Sombre', 'Suit le Mac', 'thème et apparence'])
    for (const c of list) expect(c.keywords, c.id).toMatch(/thème apparence mode clair sombre système palette réglages/)
    expect(list.find((c) => c.id === 'open-settings')?.keys).toBe('⌘,')
  })

  it('ids are unique, labels of the built-ins are English (R16), no two commands print the same keys', () => {
    registerBuiltins()
    const list = commands(selected)
    const ids = list.map((c) => c.id)
    expect(new Set(ids).size).toBe(ids.length)
    for (const c of list.filter((x) => THEME_IDS.includes(x.id))) expect(c.label, c.id).toMatch(/^[A-Za-z]+( [A-Za-z]+)?(: [A-Za-z]+)?$/)
    const keys = list.map((c) => c.keys).filter((k): k is string => !!k)
    expect(keys.length).toBeGreaterThan(5)
    expect(new Set(keys).size).toBe(keys.length)
  })

  it('the badge « Actif » marks the current theme and the current appearance, one per family', () => {
    registerBuiltins()
    const active = () => commands(ctx).filter((c) => c.badge).map((c) => [c.id, c.badge])
    expect(active()).toEqual([
      ['theme-terra', 'Actif'],
      ['appearance-system', 'Actif'],
    ])
    localStorage.setItem('pigtail-palette', 'ocean')
    localStorage.setItem('pigtail-appearance', 'dark')
    expect(active()).toEqual([
      ['theme-ocean', 'Actif'],
      ['appearance-dark', 'Actif'],
    ])
  })

  it('running a command applies and saves the choice; Open Settings opens the popover', () => {
    registerBuiltins()
    const run = (id: string) => commands(ctx).find((c) => c.id === id)?.run()
    run('theme-ocean')
    expect(document.documentElement.getAttribute('data-theme-palette')).toBe('ocean')
    expect(localStorage.getItem('pigtail-palette')).toBe('ocean')
    run('appearance-light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
    expect(localStorage.getItem('pigtail-appearance')).toBe('light')
    expect(localStorage.getItem('pigtail-palette')).toBe('ocean')
    run('open-settings')
    expect(getUi().settings).toBe(true)
  })
})
