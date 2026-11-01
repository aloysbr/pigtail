import { describe, expect, it } from 'vitest'
import type { Theme } from '@/api/types'
import { MAX_PROJECTS, proposeProjects, whyOf } from './projects'

function th(id: string, name: string, extra: Partial<Theme> = {}): Theme {
  return { id, name, kind: 'project', scope: 'perso', space: 'Allotment', archived: false, ...extra }
}

const THEMES: Theme[] = [
  th('a', 'pigtail'),
  th('b', 'Outils', { kind: 'ongoing' }),
  th('c', 'Seedbed', { space: 'Garden' }),
  th('d', 'Board', { scope: 'globex', space: 'Vineyard' }),
  th('q', 'À qualifier'),
  th('e', 'Jardin'),
  th('f', 'Vieux', { archived: true }),
  th('g', 'Freelance'),
  th('h', 'Asso'),
]

const ids = (l: Theme[]) => l.map((t) => t.id)

describe('proposeProjects', () => {
  it('no text, no current project: the fixed list (the themes as read), 5 at most, never « À qualifier » nor an archived one', () => {
    const l = proposeProjects(THEMES, {})
    expect(MAX_PROJECTS).toBe(5)
    expect(ids(l)).toEqual(['a', 'b', 'c', 'd', 'e'])
  })

  it('the current project comes first, then the fixed list without it', () => {
    expect(ids(proposeProjects(THEMES, { current: 'd' }))).toEqual(['d', 'a', 'b', 'c', 'e'])
  })

  it('a project named in the line (a whole word, accents ignored) comes before the current one', () => {
    expect(ids(proposeProjects(THEMES, { named: 'revoir le seedbed de juin', current: 'd' }))).toEqual(['c', 'd', 'a', 'b', 'e'])
    expect(ids(proposeProjects(THEMES, { named: 'OUTILS et jardin', current: 'd' }))).toEqual(['b', 'e', 'd', 'a', 'c'])
    // a part of a word does not name a project
    expect(ids(proposeProjects(THEMES, { named: 'seedbeds', current: 'd' }))[0]).toBe('d')
  })

  it('no duplicate when the named project is also the current one', () => {
    const l = proposeProjects(THEMES, { named: 'board', current: 'd' })
    expect(ids(l)).toEqual(['d', 'a', 'b', 'c', 'e'])
  })

  it('covers every scope, and an unknown current project is ignored', () => {
    expect(proposeProjects(THEMES, { current: 'nope' }).some((t) => t.scope === 'globex')).toBe(true)
  })

  it('« À qualifier » named in the line or current is still never proposed', () => {
    expect(ids(proposeProjects(THEMES, { named: 'à qualifier', current: 'q' }))).not.toContain('q')
  })

  it('no theme at all: an empty list', () => {
    expect(proposeProjects([], { named: 'x' })).toEqual([])
  })

  it('whyOf says why a project is proposed', () => {
    expect(whyOf(THEMES[2], { named: 'le seedbed', current: 'c' })).toBe('nommé dans la ligne')
    expect(whyOf(THEMES[2], { current: 'c' })).toBe('projet courant')
    expect(whyOf(THEMES[0], { current: 'c' })).toBe('')
  })
})
