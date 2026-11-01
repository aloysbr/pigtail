import { describe, expect, it } from 'vitest'
import type { LogEntry } from '@/api/types'
import { JOURNAL_FIXTURE, JOURNAL_NOW, JOURNAL_TZ } from '@/test/journal-fixture'
import { buildJournal, type Event } from './journal'

function e(id: string, ts: string, kind: string, text: string): LogEntry {
  return { id, subject_id: 'sub_x', ts, kind, text }
}

/** Every shown row of a day, step by step, newest first. */
function rows(entries = JOURNAL_FIXTURE) {
  const [day] = buildJournal(entries, JOURNAL_NOW, 'write', JOURNAL_TZ)
  return day.steps.flatMap((s) => s.events.map((ev) => ({ step: s.id, ev })))
}

/** Shown events and their folded ones, flat, in display order, with the step and whether folded. */
function flat() {
  return rows().flatMap(({ step, ev }) => [
    { step, ev, folded: false },
    ...ev.fold.map((f) => ({ step, ev: f, folded: true })),
  ])
}

describe('buildJournal (a synthetic journal of sub_demo)', () => {
  it('one day « Aujourd’hui · lundi 26 octobre · 15 événements », 11 rows after folding', () => {
    const days = buildJournal(JOURNAL_FIXTURE, JOURNAL_NOW, 'write', JOURNAL_TZ)
    expect(days).toHaveLength(1)
    expect(days[0].key).toBe('2026-10-26')
    expect(days[0].label).toBe("Aujourd'hui · lundi 26 octobre · 15 événements")
    expect(days[0].count).toBe(15)
    expect(rows()).toHaveLength(11)
  })

  it('steps newest first, write current, with their time spans', () => {
    const [day] = buildJournal(JOURNAL_FIXTURE, JOURNAL_NOW, 'write', JOURNAL_TZ)
    expect(day.steps.map((s) => [s.id, s.name, s.span, s.current])).toEqual([
      ['write', 'write', '15:37 – 15:38', true],
      ['sketch', 'sketch', '14:45 – 15:32', false],
      ['pick', 'pick', '14:11 – 14:13', false],
      ['survey', 'survey', '14:02', false],
      ['', 'Sans étape', '13:55', false],
    ])
    expect(day.steps.find((s) => s.id === 'sketch')?.rounds).toBe('t1 → t3')
    expect(day.steps.find((s) => s.id === 'write')?.rounds).toBeUndefined()
  })

  it('the expected table, line by line (time, start of text, step, type, folded, round)', () => {
    const got = flat().map(({ step, ev, folded }) => [ev.time, step, ev.type, folded, ev.round ?? ''])
    expect(got).toEqual([
      ['15:38', 'write', 'wait', false, ''],
      ['15:37', 'write', 'out', false, ''],
      ['15:32', 'sketch', 'out', false, 't3'],
      ['15:32', 'sketch', 'session', true, 't3'],
      ['15:31', 'sketch', 'decision', false, 't3'],
      ['15:28', 'sketch', 'wait', false, 't3'],
      ['15:07', 'sketch', 'wait', false, 't2'],
      ['14:45', 'sketch', 'out', false, 't1'],
      ['14:45', 'sketch', 'wait', true, 't1'],
      ['14:45', 'sketch', 'status', true, 't1'],
      ['14:13', 'pick', 'idea', false, ''],
      ['14:11', 'pick', 'decision', false, ''],
      ['14:02', 'survey', 'out', false, ''],
      ['13:55', '', 'session', false, ''],
      ['13:55', '', 'status', true, ''],
    ])
    const ids = flat().map(({ ev }) => ev.id)
    expect(ids).toEqual([
      'log_59b50005854955b9',
      'log_2ec636dc41eaa3fb',
      'log_bf66764b3a71f2c5',
      'log_0c0035bc2fb42da8',
      'log_d0c10cf6320c66f7',
      'log_fd705f9a757b4af5',
      'log_809433b445f3248b',
      'log_171b6b525895a18d',
      'log_d94949640ed7344e',
      'log_5c6e7a161089255d',
      'log_3136a05c05149109',
      'log_06d289209481c7c8',
      'log_41fd9546f697f9b7',
      'log_d90fd4939b99ab6b',
      'log_f473246a41b24554',
    ])
  })

  it('main text and detail, split from the logged text', () => {
    const byId = new Map<string, Event>(flat().map(({ ev }) => [ev.id, ev]))
    const md = (id: string) => [byId.get(id)?.main, byId.get(id)?.detail]
    expect(md('log_59b50005854955b9')).toEqual([
      'périmètre face aux manques du capteur et réglage par défaut en attente',
      'Fin de session · brouillon écrit depuis t3',
    ])
    expect(md('log_2ec636dc41eaa3fb')).toEqual(['draft.md (périmètre et réglage par défaut proposés, à confirmer)', undefined])
    expect(md('log_bf66764b3a71f2c5')).toEqual([
      'Handoff — Pilote pluviomètre (2026-10-26)',
      'État — alpha · étape sketch close (t3 validé) → prochaine étape write · statut designing.',
    ])
    expect(md('log_0c0035bc2fb42da8')).toEqual([
      'Fin de session · croquis t3 validé comme référence ; passage au brouillon',
      'cadre pluviomètre, cible complète, boîtier mixte, teintes Sable + Ardoise',
    ])
    expect(md('log_d0c10cf6320c66f7')).toEqual(['t3 validée comme référence', undefined])
    expect(md('log_fd705f9a757b4af5')).toEqual([
      'croquis de référence t3 servi et vérifié, validation avant brouillon',
      'Fin de session · pilote dédié, cible complète, boîtier mixte, teintes Sable + Ardoise, Ardoise gris pur',
    ])
    expect(md('log_171b6b525895a18d')).toEqual([
      'servi et relu (25+12 captures, 9 défauts corrigés), choix du boîtier en attente',
      undefined,
    ])
    expect(md('log_d94949640ed7344e')).toEqual([
      'croquis t1 (4 boîtiers × 6 vues) servi et relu, choix du boîtier en attente',
      'Fin de session · pilote dédié, cible complète',
    ])
    expect(md('log_5c6e7a161089255d')).toEqual(['understanding → designing', undefined])
    expect(md('log_3136a05c05149109')).toEqual([
      'croquis dans sketches/rain-gauge/t<n>/, servis par pigtail serve',
      undefined,
    ])
    expect(md('log_06d289209481c7c8')).toEqual(['pilote dédié, cible complète validée (go ahead)', undefined])
    expect(md('log_d90fd4939b99ab6b')).toEqual([
      'workspace cmux lié (auto)',
      '00000000-0000-4000-8000-000000000001',
    ])
  })

  it('a folded row lists its folded events, most telling first', () => {
    const r = rows()
    const at1445 = r.find(({ ev }) => ev.id === 'log_171b6b525895a18d')?.ev
    expect(at1445?.fold.map((f) => f.type)).toEqual(['wait', 'status'])
    const handoff = r.find(({ ev }) => ev.id === 'log_bf66764b3a71f2c5')?.ev
    expect(handoff?.fold.map((f) => f.type)).toEqual(['session'])
    for (const { ev } of r) for (const f of ev.fold) expect(f.fold).toEqual([])
  })
})

describe('buildJournal (edge rules)', () => {
  const now = new Date('2026-10-26T14:40:00Z')

  it('an empty journal is []', () => {
    expect(buildJournal([], now, undefined, JOURNAL_TZ)).toEqual([])
  })

  it('a past day reads « <long day> · <n> événements », the year added when not the current one', () => {
    const days = buildJournal(
      [
        e('a', '2026-10-25T09:00:00Z', 'idea', 'une'),
        e('b', '2026-10-25T09:10:00Z', 'idea', 'deux'),
        e('c', '2026-10-25T09:20:00Z', 'idea', 'trois'),
        e('d', '2025-12-31T09:20:00Z', 'idea', 'vieille'),
      ],
      now,
      undefined,
      JOURNAL_TZ,
    )
    expect(days.map((d) => d.label)).toEqual(['dimanche 25 octobre · 3 événements', 'mercredi 31 décembre 2025 · 1 événement'])
  })

  it('a session-end line never makes a step of its own', () => {
    const [day] = buildJournal([e('a', '2026-10-26T09:00:00Z', 'session', 'session-end s-1 · draft → done')], now, undefined, JOURNAL_TZ)
    expect(day.steps.map((s) => s.id)).toEqual([''])
  })

  it('an entry no rule places goes under « Sans étape »', () => {
    const [day] = buildJournal([e('a', '2026-10-26T09:00:00Z', 'idea', 'une idée sans étape')], now, undefined, JOURNAL_TZ)
    expect(day.steps.map((s) => [s.id, s.name])).toEqual([['', 'Sans étape']])
  })

  it('note, progress and phase take their default types', () => {
    const [day] = buildJournal(
      [
        e('n', '2026-10-26T09:00:00Z', 'note', 'une note'),
        e('p', '2026-10-26T10:00:00Z', 'progress', 'un progrès'),
        e('h', '2026-10-26T11:00:00Z', 'phase', 'une phase'),
      ],
      now,
      undefined,
      JOURNAL_TZ,
    )
    const types = Object.fromEntries(day.steps.flatMap((s) => s.events).map((ev) => [ev.id, ev.type]))
    expect(types).toEqual({ n: 'idea', p: 'session', h: 'status' })
  })

  it('the step is whatever word the line carries: `step=<name>` or `<name> →`; a status line places nothing by itself', () => {
    const [day] = buildJournal(
      [
        e('x', '2026-10-26T09:00:00Z', 'phase', 'understanding → quelque chose'),
        e('t', '2026-10-26T10:00:00Z', 'progress', 'session-end step=watering-test round=2 · fini'),
        e('s', '2026-10-26T12:00:00Z', 'status', 'designing → building'),
      ],
      now,
      undefined,
      JOURNAL_TZ,
    )
    // no list of steps: « understanding » and « watering-test » are steps as written; the status takes the nearest earlier placed step
    const step = Object.fromEntries(day.steps.flatMap((s) => s.events.map((ev) => [ev.id, s.id])))
    expect(step).toEqual({ x: 'understanding', t: 'watering-test', s: 'watering-test' })
  })

  it('a status before any placed event goes under « Sans étape »', () => {
    const [day] = buildJournal([e('s', '2026-10-26T12:00:00Z', 'status', 'designing → building')], now, undefined, JOURNAL_TZ)
    expect(day.steps.map((s) => s.id)).toEqual([''])
  })

  it('only a « saved » line (`write saved →`) is reduced to its file name; another step keeps its path', () => {
    const [day] = buildJournal(
      [
        e('a', '2026-10-26T09:00:00Z', 'note', 'write saved → notes/x/draft.md (v1)'),
        e('b', '2026-10-26T10:00:00Z', 'note', 'survey → notes/x/research.md (faits)'),
      ],
      now,
      undefined,
      JOURNAL_TZ,
    )
    const main = Object.fromEntries(day.steps.flatMap((s) => s.events.map((ev) => [ev.id, ev.main])))
    expect(main).toEqual({ a: 'draft.md (v1)', b: 'notes/x/research.md (faits)' })
  })
})

describe('an answer of the human (log kind « wait », written by the daemon)', () => {
  const at = '2026-10-26T14:00:00Z'
  const one = (text: string) => rows([e('l1', at, 'wait', text)])[0].ev

  it('is an « Attente » event with a readable line, never the raw ids', () => {
    const ev = one('Ada → Project Alpha [answer wait_295f0a1b2c3d]: B · Fenêtre — plutôt séparée')
    expect(ev.type).toBe('wait')
    expect(ev.main).toBe('Réponse envoyée à Project Alpha')
    expect(ev.detail).toBe('B · Fenêtre — plutôt séparée')
    expect(JSON.stringify(ev)).not.toMatch(/wait_295f/)
  })

  it('strips every wait id from a detail, with its parentheses or brackets', () => {
    const ev = one('Fin de session Project Alpha ; B validée (wait_54790d09a9d6fa4c) : a « Session d abord » ; fini')
    const all = JSON.stringify(ev)
    expect(all).not.toMatch(/wait_[0-9a-f]/)
    expect(ev.detail ?? ev.main).not.toMatch(/\(\)|\[\]|  /)
    expect(
      rows([e('l2', at, 'note', 'Choix ; reponse (wait_54790d09a9d6fa4c) : a « Session d abord »')])[0].ev.main,
    ).toBe('Choix ; reponse : a « Session d abord »')
    const d = rows([e('l3', at, 'handoff', 'Titre\nAda [answer wait_54790d09a9d6fa4c] : a « Session d abord »')])[0].ev
    expect(d.detail).toBe('Ada : a « Session d abord »')
  })

  it('kept for the subject’s next session', () => {
    const ev = one("Ada → kept for the subject's next session [answer wait_295f0a1b2c3d]: Oui")
    expect(ev.main).toBe('Réponse gardée pour la prochaine session du sujet')
    expect(ev.detail).toBe('Oui')
  })

  it('renders any configured name, and a name that is a step word is not a step', () => {
    for (const name of ['Ada', 'the user', 'plan', 'review']) {
      const ev = one(`${name} → Project Alpha [answer wait_295f0a1b2c3d]: B · Fenêtre`)
      expect(ev.type).toBe('wait')
      expect(ev.main).toBe('Réponse envoyée à Project Alpha')
      expect(ev.detail).toBe('B · Fenêtre')
    }
  })

  it('a kind « wait » line of another shape keeps its text as an Attente', () => {
    const ev = one('Quelque chose')
    expect(ev.type).toBe('wait')
    expect(ev.main).toBe('Quelque chose')
  })

  it('renders handoff-auto as an out line with its automatic header', () => {
    const ev = rows([
      e(
        'l4',
        at,
        'handoff-auto',
        '[automatique — brouillon mécanique, non validé par la session]\nsession: s · fin: end · dernier tour: 2026-10-31T09:00:00Z\n--- dernier résumé ---\nFait.',
      ),
    ])[0].ev
    expect(ev.type).toBe('out')
    expect(ev.main.startsWith('[automatique')).toBe(true)
    expect(ev.main).not.toMatch(/résumé/)
    expect(ev.detail).toMatch(/^session: s · fin: end/)
  })
})
