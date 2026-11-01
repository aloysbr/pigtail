import { describe, expect, it } from 'vitest'
import { ago, ageOf } from './age'

describe('ago (minutes)', () => {
  it.each([
    [-5, "à l'instant"],
    [0, "à l'instant"],
    [1, '1 min'],
    [59, '59 min'],
    [60, '1 h'],
    [60 * 24 - 1, '23 h'],
    [60 * 24, '1 j'],
    [60 * 24 * 14 - 1, '14 j'],
    [60 * 24 * 14, '2 sem'],
    [60 * 24 * 7 * 10, '10 sem'],
  ])('%s min → %s', (m, want) => {
    expect(ago(m)).toBe(want)
  })
})

describe('ageOf (ISO date, now)', () => {
  const now = Date.parse('2026-10-23T12:00:00Z')
  it('measures from the last update', () => {
    expect(ageOf('2026-10-23T11:30:00Z', now)).toBe('30 min')
    expect(ageOf('2026-10-23T11:59:31Z', now)).toBe("à l'instant")
  })
  it('an unreadable date gives no age', () => {
    expect(ageOf('', now)).toBe('')
    expect(ageOf('not a date', now)).toBe('')
  })
})
