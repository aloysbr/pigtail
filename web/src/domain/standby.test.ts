import { describe, expect, it } from 'vitest'
import type { LogEntry, Subject } from '@/api/types'
import { canToggleStandby, nextStatus, resumeStatus } from './standby'

let n = 0
function st(text: string, kind = 'status'): LogEntry {
  n += 1
  return { id: `l${n}`, subject_id: 's', ts: '2026-10-26T10:00:00Z', kind, text }
}
const sub = (status: string): Subject => ({ id: 's', theme_id: 't', title: 'S', status, updated_at: '', created_at: '' })

describe('resumeStatus (D6): the last open status read in the log', () => {
  it('[building → parked, inbox → building] = building', () => {
    expect(resumeStatus([st('building → parked'), st('inbox → building')])).toBe('building')
  })
  it('an empty log = inbox', () => {
    expect(resumeStatus([])).toBe('inbox')
  })
  it('done → parked alone = inbox', () => {
    expect(resumeStatus([st('done → parked')])).toBe('inbox')
  })
  it('the most recent « <open> → parked » wins over older ones; other kinds and decorations are read past', () => {
    expect(resumeStatus([st('note libre', 'note'), st('designing → parked · atlas'), st('building → parked')])).toBe('designing')
  })
  it('no « → parked » line: the last new open status read', () => {
    expect(resumeStatus([st('parked → done'), st('understanding → designing'), st('inbox → understanding')])).toBe('designing')
  })
})

describe('canToggleStandby (D3): stored open or parked only', () => {
  it.each(['inbox', 'understanding', 'designing', 'building', 'parked'])('%s = true', (s) => {
    expect(canToggleStandby(sub(s))).toBe(true)
  })
  it.each(['done', 'dropped', 'captured'])('%s = false', (s) => {
    expect(canToggleStandby(sub(s))).toBe(false)
  })
})

describe('nextStatus', () => {
  it('an open subject goes to parked; a parked one to its resume status', () => {
    expect(nextStatus(sub('building'), [])).toBe('parked')
    expect(nextStatus(sub('parked'), [st('designing → parked')])).toBe('designing')
    expect(nextStatus(sub('parked'), [])).toBe('inbox')
  })
})
