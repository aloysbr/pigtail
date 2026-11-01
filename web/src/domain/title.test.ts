import { describe, expect, it } from 'vitest'
import { subj } from '@/test/fixtures'
import { longTitle, shortTitle } from './title'

describe('shortTitle (same rule as pkg/pigtail ShortTitle)', () => {
  it('uses the stored short title when set', () => {
    expect(shortTitle(subj({ short_title: '  Atlas  ', title: 'x' }))).toBe('Atlas')
  })
  it('keeps a title that fits, whitespace collapsed', () => {
    expect(shortTitle(subj({ title: 'Atlas   web\tlecture' }))).toBe('Atlas web lecture')
  })
  it('D15: without a stored short title the full title is shown, never cut (CSS ellipsis does the truncation)', () => {
    const title = 'Atlas web : backlog en lecture seule pour tous'
    expect(shortTitle(subj({ title }))).toBe(title)
    expect(longTitle(subj({ title }))).toBe('')
  })
})

describe('longTitle', () => {
  it('drops a prefix that repeats the short title', () => {
    expect(longTitle(subj({ short_title: 'Atlas', title: 'Atlas — backlog en lecture' }))).toBe('Backlog en lecture')
  })
  it('is empty when the title is the short title', () => {
    expect(longTitle(subj({ title: 'Atlas web' }))).toBe('')
  })
  it('keeps the long title after a stored short title that is only a word prefix (t10 desc strips a separator only)', () => {
    expect(longTitle(subj({ short_title: 'Release notes', title: 'Release notes tool for changelogs' }))).toBe('Release notes tool for changelogs')
  })
})
