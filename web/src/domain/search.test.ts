import { describe, expect, it } from 'vitest'
import { subj } from '@/test/fixtures'
import { fold, matches } from './search'

describe('search', () => {
  it('folds accents and case', () => {
    expect(fold('Été CAFÉ')).toBe('ete cafe')
  })

  it('« éte » finds « été »', () => {
    expect(matches(subj({ title: 'Préparer l’été' }), 'éte')).toBe(true)
    expect(matches(subj({ title: 'Préparer l’été' }), 'ETE')).toBe(true)
  })

  it('searches the short title and the title', () => {
    const s = subj({ title: 'Long title about the daemon', short_title: 'Atlas' })
    expect(matches(s, 'atlas')).toBe(true)
    expect(matches(s, 'daemon')).toBe(true)
    expect(matches(s, 'nothing')).toBe(false)
  })

  it('an empty or blank query matches everything', () => {
    expect(matches(subj(), '')).toBe(true)
    expect(matches(subj(), '   ')).toBe(true)
  })
})
