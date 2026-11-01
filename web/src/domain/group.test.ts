import { describe, expect, it } from 'vitest'
import { SPACES, subj, theme } from '@/test/fixtures'
import { buildGroups, enrich, GROUP_LABEL, GROUPINGS, scopesOf, type Group } from './group'

const themes = [
  theme({ id: 'th_a', name: 'pigtail', space: 'Allotment' }),
  theme({ id: 'th_b', name: 'Outils', kind: 'ongoing', space: 'Allotment' }),
  theme({ id: 'th_c', name: 'Seedbed', space: 'Garden' }),
  theme({ id: 'th_t', name: 'Board', scope: 'globex', space: 'Vineyard' }),
  theme({ id: 'th_nospace', name: 'Vrac', space: '' }),
]

function flat(gs: Group[]): Group[] {
  return gs.flatMap((g) => [g, ...flat(g.children)])
}
function ids(g: Group): string[] {
  return g.children.length ? g.children.flatMap(ids) : g.rows.map((r) => r.s.id)
}

describe('groupings', () => {
  it('has the four t10 groupings, Aire › Projet among them', () => {
    expect(GROUPINGS).toEqual(['scope', 'aire', 'projet', 'statut'])
    expect(GROUP_LABEL.aire).toBe('Aire › Projet')
  })
})

describe('buildGroups — Aire › Projet', () => {
  const subjects = [
    subj({ id: 'a1', theme_id: 'th_a' }),
    subj({ id: 'b1', theme_id: 'th_b' }),
    subj({ id: 'c1', theme_id: 'th_c' }),
    subj({ id: 't1', theme_id: 'th_t', scope: 'globex' }),
    subj({ id: 'orphan', theme_id: '', scope: 'perso' }),
    subj({ id: 'nospace', theme_id: 'th_nospace' }),
  ]
  const rows = enrich(subjects, themes, new Set())
  const gs = buildGroups(rows, 'aire', { scopes: scopesOf(SPACES, themes, subjects), spaces: SPACES })

  it('areas in /spaces order, scope by scope, « Sans aire » last in its scope', () => {
    expect(gs.map((g) => `${g.label}|${g.sub}`)).toEqual([
      'Allotment|aire · perso',
      'Garden|aire · perso',
      'Sans aire|aire · perso',
      'Vineyard|aire · globex',
    ])
  })

  it('a subject with no theme stays under its scope, in « Sans aire › Sans projet »', () => {
    const none = gs.find((g) => g.label === 'Sans aire')!
    const proj = none.children.find((c) => c.label === 'Sans projet')!
    expect(proj.rows.map((r) => r.s.id)).toEqual(['orphan'])
    expect(none.scope).toBe('perso')
  })

  it('a subject whose theme has no space stays under its scope, in « Sans aire »', () => {
    const none = gs.find((g) => g.label === 'Sans aire')!
    expect(none.children.find((c) => c.label === 'Vrac')!.rows.map((r) => r.s.id)).toEqual(['nospace'])
  })

  it('marks an ongoing theme « continu »', () => {
    const sys = gs[0]
    expect(sys.children.find((c) => c.label === 'Outils')!.sub).toBe('projet · continu')
    expect(sys.children.find((c) => c.label === 'pigtail')!.sub).toBe('projet')
  })

  it('every subject appears exactly once', () => {
    const all = gs.flatMap(ids)
    expect(all.sort()).toEqual(subjects.map((s) => s.id).sort())
    expect(flat(gs).every((g) => g.count === (g.children.length ? g.children.reduce((a, c) => a + c.count, 0) : g.rows.length))).toBe(true)
  })
})

describe('buildGroups — the other groupings', () => {
  const live = new Set(['l1'])
  const subjects = [
    subj({ id: 'l1', theme_id: 'th_a' }),
    subj({ id: 'o1', theme_id: 'th_c' }),
    subj({ id: 'p1', theme_id: 'th_t', scope: 'globex', status: 'parked' }),
    subj({ id: 'd1', theme_id: 'th_a', status: 'done' }),
  ]
  const rows = enrich(subjects, themes, live)
  const ctx = { scopes: scopesOf(SPACES, themes, subjects), spaces: SPACES }

  it('Scope › Aire: scope first, its areas under it', () => {
    const gs = buildGroups(rows, 'scope', ctx)
    expect(gs.map((g) => g.label)).toEqual(['perso', 'globex'])
    expect(gs[0].children.map((c) => c.label)).toEqual(['Allotment', 'Garden'])
    expect(gs[0].children[0].sub).toBe('aire')
  })

  it('Projet: one level, projects only', () => {
    const gs = buildGroups(rows, 'projet', ctx)
    expect(gs.every((g) => g.children.length === 0)).toBe(true)
    expect(gs.map((g) => g.label).sort()).toEqual(['Board', 'Seedbed', 'pigtail'])
  })

  it('Statut: one group per displayed status, in order', () => {
    const gs = buildGroups(rows, 'statut', ctx)
    expect(gs.map((g) => g.label)).toEqual(['En cours', 'Ouvert', 'Standby', 'Done'])
    expect(gs[0].rows.map((r) => r.s.id)).toEqual(['l1'])
  })
})

describe('scopesOf', () => {
  it('collects the scopes from /spaces, /themes and the subjects, never from a fixed list', () => {
    expect(scopesOf([{ name: 'X', scope: 'zeta' }], [theme({ id: 't', scope: 'alpha' })], [subj({ scope: 'beta' })])).toEqual(['zeta', 'alpha', 'beta'])
  })
})
