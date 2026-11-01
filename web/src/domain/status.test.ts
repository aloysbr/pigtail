import { describe, expect, it } from 'vitest'
import { subj } from '@/test/fixtures'
import { displayStatus, isHidden, phaseOf, PHASES, statusLabel, statusTitle } from './status'

describe('displayStatus', () => {
  const none = new Set<string>()
  it.each([
    ['inbox', 'ouvert'],
    ['understanding', 'ouvert'],
    ['designing', 'ouvert'],
    ['building', 'ouvert'],
    ['parked', 'standby'],
    ['done', 'done'],
    ['dropped', 'dropped'],
    ['captured', 'dropped'],
  ])('%s → %s', (stored, want) => {
    expect(displayStatus(subj({ status: stored }), none)).toBe(want)
  })

  it('a live peer on the subject makes it En cours, whatever its stored status', () => {
    const s = subj({ status: 'parked' })
    expect(displayStatus(s, new Set([s.id]))).toBe('cours')
  })

  it('an unknown stored status is Ouvert, never lost, with the raw value in its title', () => {
    const s = subj({ status: 'researching' })
    expect(displayStatus(s, none)).toBe('ouvert')
    expect(statusTitle(s)).toBe('Statut stocké : researching')
  })

  it('hides dropped and captured by default, nothing else', () => {
    expect(isHidden('dropped')).toBe(true)
    expect(isHidden('ouvert')).toBe(false)
    expect(isHidden('standby')).toBe(false)
  })

  it('labels captured as Capturé, dropped as Abandonné (G10)', () => {
    expect(statusLabel(subj({ status: 'captured' }), 'dropped')).toBe('Capturé')
    expect(statusLabel(subj({ status: 'dropped' }), 'dropped')).toBe('Abandonné')
    expect(statusLabel(subj({ status: 'building' }), 'cours')).toBe('En cours')
  })
})

describe('phaseOf', () => {
  it.each([
    ['understanding', 0],
    ['designing', 1],
    ['building', 2],
    ['inbox', null],
    ['parked', null],
    ['done', null],
    ['dropped', null],
  ])('%s → %s', (stored, want) => {
    expect(phaseOf(stored)).toBe(want)
  })

  it('names the three phases', () => {
    expect(PHASES).toEqual(['Comprendre', 'Concevoir', 'Construire'])
  })
})
