import { describe, expect, it } from 'vitest'
import type { SdkEvent } from '@/api/types'
import { eventText, stateLabel } from './state'

const ev = (kind: string, data: Record<string, unknown> = {}): SdkEvent => ({ seq: 1, at: '2026-10-29T10:00:00Z', kind, data }) as SdkEvent

describe('thread wording', () => {
  it('the state keeps the D18 label « Tour terminé »', () => {
    expect(stateLabel('idle')).toBe('Tour terminé')
    expect(eventText(ev('state', { state: 'idle' }))).toBe('Tour terminé')
  })
  it('the result event reads « Fin du tour », never the same line as the idle state', () => {
    expect(eventText(ev('result'))).toBe('Fin du tour')
    expect(eventText(ev('result'))).not.toBe(eventText(ev('state', { state: 'idle' })))
  })
})
