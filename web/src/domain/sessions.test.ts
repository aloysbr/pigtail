import { describe, expect, it } from 'vitest'
import type { Subject, Theme } from '@/api/types'
import { PEERS_ALL, PEERS_ALL_BODY, peerOf } from '@/test/peers-fixture'
import { bySubject, byGroup, groupOf, lifeLabel, lifeOf, sessionsModel, sinceOf, subjectOf, visible, waitLabel, waitOf } from './sessions'

const subjects: Subject[] = [
  { id: 'sub_ds', theme_id: 'th_pig', title: 'Atlas : DS Hybride et sessions', short_title: 'Atlas DS', status: 'building', scope: 'perso', updated_at: '2026-10-27T08:00:00Z', created_at: '2026-10-20T08:00:00Z' },
  { id: 'sub_watering', theme_id: 'th_seedbed', title: 'Watering recipe sheet', short_title: 'Watering', status: 'inbox', scope: 'perso', updated_at: '2026-10-27T08:00:00Z', created_at: '2026-10-20T08:00:00Z' },
]
const themes: Theme[] = [
  { id: 'th_pig', name: 'pigtail', kind: 'project', scope: 'perso', space: 'Allotment', archived: false },
  { id: 'th_seedbed', name: 'Seedbed', kind: 'project', scope: 'perso', space: 'Garden', archived: false },
]

describe('the fixture is the daemon’s real shape', () => {
  it('is the {data, meta} envelope, a session with no wait has no `waits` key, alive and inbox always set', () => {
    expect(PEERS_ALL_BODY.data).toBe(PEERS_ALL)
    expect(PEERS_ALL_BODY.meta.request_id).toBeTruthy()
    const bare = peerOf('s-shell-bare') as unknown as Record<string, unknown>
    expect('waits' in bare).toBe(false)
    for (const p of PEERS_ALL) {
      expect(typeof p.alive).toBe('boolean')
      expect(typeof p.inbox).toBe('number')
      expect(p.updated_at).toMatch(/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?[+-]\d\d:\d\d$/)
    }
  })

  it('positive control: at least one visible session of each group', () => {
    const shown = PEERS_ALL.filter(visible)
    expect(new Set(shown.map(groupOf))).toEqual(new Set(['wait', 'work', 'idle']))
  })
})

describe('groupOf: the daemon’s rule on the fields of /peers (D14)', () => {
  it.each([
    ['s-wait-bare', 'wait'],
    ['s-wait-perm', 'wait'],
    ['s-idle-arb', 'wait'],
    ['s-idle-q-mac', 'wait'],
    ['s-idle-limit', 'wait'],
    ['s-idle-stale', 'idle'],
    ['s-shell-working', 'work'],
    ['s-shell-bare', 'idle'],
    ['s-busy-q', 'work'],
    ['s-idle-review', 'idle'],
    ['s-wait-review', 'wait'],
    ['s-idle-done', 'idle'],
    ['s-paused', 'idle'],
    ['s-wk-busy', 'work'],
  ])('%s → %s', (id, g) => {
    expect(groupOf(peerOf(id))).toBe(g)
  })
})

describe('waitOf: the retained wait', () => {
  it('waiting with no wait at all: untyped, « En attente »', () => {
    expect(waitOf(peerOf('s-wait-bare'))).toEqual({ type: '', wait: null })
    expect(waitLabel('')).toBe('En attente')
  })

  it('waiting: the auto wait of the dialog', () => {
    expect(waitOf(peerOf('s-wait-perm'))?.type).toBe('permission')
  })

  it('the gravest of declared and held, never waits[0] (mac is first in the array)', () => {
    expect(peerOf('s-idle-q-mac').waits?.[0].type).toBe('mac')
    expect(waitOf(peerOf('s-idle-q-mac'))?.type).toBe('question')
  })

  it('idle: a failure auto wait counts, any other auto wait does not', () => {
    expect(waitOf(peerOf('s-idle-limit'))?.type).toBe('limit')
    expect(waitOf(peerOf('s-idle-stale'))).toBeNull()
  })

  it('a failure wins a tie with a declared wait; a strictly graver declared one wins over it', () => {
    const base = peerOf('s-idle-limit')
    const since = '2026-10-27T09:00:00+01:00'
    const tie = { ...base, waits: [...(base.waits ?? []), { type: 'limit', source: 'declared', since }] }
    expect(waitOf(tie)?.wait?.source).toBe('auto')
    const graver = { ...base, waits: [...(base.waits ?? []), { type: 'blocked', source: 'declared', since }] }
    expect(waitOf(graver)?.type).toBe('blocked')
  })

  it('declared over held on a tie', () => {
    const since = '2026-10-27T09:00:00+01:00'
    const p = { ...peerOf('s-shell-bare'), status: 'idle', waits: [{ type: 'mac', source: 'held', since }, { type: 'mac', source: 'declared', since }] }
    expect(waitOf(p)?.wait?.source).toBe('declared')
  })

  it('busy: no wait shows; paused: none either', () => {
    expect(waitOf(peerOf('s-busy-q'))).toBeNull()
    expect(waitOf(peerOf('s-paused'))).toBeNull()
  })
})

describe('labels, life, since, subject', () => {
  it('C1 labels', () => {
    expect(waitLabel('arbitrage')).toBe('Arbitrage')
    expect(waitLabel('permission')).toBe('Permission')
    expect(waitLabel('limit')).toBe('Limite')
    expect(waitLabel('mac')).toBe('Mac occupé')
    expect(waitLabel('api_error')).toBe('Erreur API')
  })

  it('a paused session is visible and inactive; an ended one is not visible', () => {
    expect(visible(peerOf('s-paused'))).toBe(true)
    expect(groupOf(peerOf('s-paused'))).toBe('idle')
    expect(visible(peerOf('s-ended'))).toBe(false)
    expect(lifeLabel(lifeOf(peerOf('s-paused')))).toMatch(/^En pause depuis \d\d:\d\d$/)
    expect(lifeLabel(lifeOf(peerOf('s-wait-bare')))).toBe('Vivante')
    expect(lifeLabel(lifeOf(peerOf('s-ended')))).toBe('Inactive')
  })

  it('sinceOf: the retained wait’s since when it waits, else updated_at', () => {
    expect(sinceOf(peerOf('s-idle-q-mac'))).toBe('2026-10-27T09:20:00+01:00')
    expect(sinceOf(peerOf('s-wait-bare'))).toBe(peerOf('s-wait-bare').updated_at)
    expect(sinceOf(peerOf('s-busy-q'))).toBe(peerOf('s-busy-q').updated_at)
  })

  it('« projet › titre court »; no subject or an unknown one → « Sans sujet »', () => {
    expect(subjectOf(peerOf('s-idle-arb'), subjects, themes)).toEqual({ id: 'sub_watering', project: 'Seedbed', title: 'Watering' })
    expect(subjectOf(peerOf('s-idle-stale'), subjects, themes).title).toBe('Sans sujet')
    expect(subjectOf(peerOf('s-shell-bare'), subjects, themes)).toEqual({ id: null, project: '', title: 'Sans sujet' })
  })

  it('bySubject: oldest wait first, blocks by subject', () => {
    const waiting = PEERS_ALL.filter((p) => visible(p) && groupOf(p) === 'wait')
    const blocks = bySubject(waiting, subjects, themes)
    // s-idle-limit (08:58, no subject) is the oldest, then s-wait-review (09:01, Watering) …
    expect(blocks[0].subject.title).toBe('Sans sujet')
    expect(blocks.flatMap((b) => b.peers.map((p) => p.session_id))[0]).toBe('s-idle-limit')
    expect(blocks.map((b) => b.subject.title)).toEqual(['Sans sujet', 'Watering', 'Atlas DS'])
  })

  it('sessionsModel counts each group and lists the order; byGroup sorts wait > work > idle', () => {
    const m = sessionsModel(PEERS_ALL, subjects, themes)
    expect(m.groups.map((g) => [g.g, g.count])).toEqual([['wait', 6], ['work', 3], ['idle', 5]])
    expect(m.order).not.toContain('s-ended')
    expect(m.order).toHaveLength(14)
    expect(byGroup([peerOf('s-shell-bare'), peerOf('s-busy-q'), peerOf('s-idle-arb')]).map((p) => p.session_id)).toEqual(['s-idle-arb', 's-busy-q', 's-shell-bare'])
  })
})
