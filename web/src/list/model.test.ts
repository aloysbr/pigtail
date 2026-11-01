import { describe, expect, it } from 'vitest'
import { cap, makeBacklog, withCaptures } from '@/test/backlog'
import { buildModel, captureKey, revealCapture } from './model'
import { resetUi, getUi } from '@/state/ui'

function model(ui = {}, data = makeBacklog()) {
  resetUi()
  return buildModel(data, { ...getUi(), ...ui })
}

describe('buildModel areas', () => {
  it('« Sans aire » closes each scope that has subjects without an area, so the areas add up to the scope', () => {
    const m = model()
    const perso = m.areas.filter((a) => a.scope === 'perso')
    expect(perso.map((a) => a.name)).toEqual(['Allotment', 'Garden', 'Vide', 'Sans aire'])
    expect(perso.find((a) => a.name === 'Sans aire')?.count).toBe(1)
    expect(perso.reduce((n, a) => n + a.count, 0)).toBe(m.scopeCounts.perso)
    expect(m.areas.filter((a) => a.scope === 'globex').map((a) => a.name)).toEqual(['Vineyard'])
  })

  it('filtering on « Sans aire » keeps only the subjects with no area of that scope', () => {
    const m = model({ areaF: { scope: 'perso', name: 'Sans aire' } })
    expect(m.visible.map((r) => r.s.id)).toEqual(['orphan'])
  })
})

describe('buildModel captures', () => {
  it('Aire and Projet groupings: each project carries its open, unpromoted captures, newest first', () => {
    for (const group of ['aire', 'projet'] as const) {
      const m = model({ group }, withCaptures())
      expect(m.captures['th:th_seedbed'].map((c) => c.id), group).toEqual(['cap-b2', 'cap-b1'])
      expect(m.captures['th:th_board'].map((c) => c.id), group).toEqual(['cap-t1'])
      expect(m.captures['th:th_pig'], group).toBeUndefined()
    }
  })

  it('Statut and Scope groupings carry no captures', () => {
    for (const group of ['statut', 'scope'] as const) {
      const m = model({ group }, withCaptures())
      expect(m.captures, group).toEqual({})
      expect(m.capIds.size, group).toBe(0)
    }
  })

  it('the subject counts ignore the captures', () => {
    const plain = model()
    const caps = model({}, withCaptures())
    expect(caps.total).toBe(plain.total)
    expect(caps.visible.length).toBe(plain.visible.length)
    expect(caps.scopeCounts).toEqual(plain.scopeCounts)
  })

  it('a folded Captures line keeps its captures out of j/k; unfolded, they follow the project rows', () => {
    const folded = model({ open: { 'sp:perso/Garden': true, 'th:th_seedbed': true } }, withCaptures())
    expect(folded.order).toEqual(['watering-1'])
    const open = model({ open: { 'sp:perso/Garden': true, 'th:th_seedbed': true, [captureKey('th_seedbed')]: true } }, withCaptures())
    expect(open.order).toEqual(['watering-1', 'cap-b2', 'cap-b1'])
    expect(open.groupOf.get('cap-b2')).toBe('cap:th_seedbed')
    expect(open.capIds.has('cap-b1')).toBe(true)
  })

  it('a scope switched off hides its captures; a search keeps the captures whose text matches', () => {
    expect(model({ scopesOff: ['globex'] }, withCaptures()).captures['th:th_board']).toBeUndefined()
    const m = model({ query: 'clay' }, withCaptures())
    expect(m.captures['th:th_seedbed'].map((c) => c.id)).toEqual(['cap-b1'])
  })

  it('a project with captures and no subject shown still gets its group (so a new capture can be found)', () => {
    const d = makeBacklog()
    d.themes.push({ id: 'th_new', name: 'Nouveau', kind: 'project', scope: 'perso', space: 'Vide', archived: false })
    d.captures = [cap('cap-n1', 'th_new', 'Première idée')]
    const m = model({}, d)
    const area = m.groups.find((g) => g.key === 'sp:perso/Vide')
    expect(area?.children.map((c) => c.key)).toEqual(['th:th_new'])
    expect(m.captures['th:th_new'].map((c) => c.id)).toEqual(['cap-n1'])
    const byProject = model({ group: 'projet' }, d)
    expect(byProject.groups.some((g) => g.key === 'th:th_new')).toBe(true)
  })

  it('revealCapture opens the area, the project and its Captures line', () => {
    const m = model({}, withCaptures())
    expect(revealCapture(m.groups, 'th_board')).toEqual({ 'sp:globex/Vineyard': true, 'th:th_board': true, 'cap:th_board': true })
  })
})
