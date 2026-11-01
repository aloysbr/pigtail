import { describe, expect, it } from 'vitest'
import { fitChips } from './fitChips'

describe('fitChips (Q3 A: line 2 never wraps, the chips fold into « +n »)', () => {
  it('folds from the end while the search would drop below 200', () => {
    // a 700 px line, 160 px for the Filtre menu and the gaps; chips Recherche 150, Aire 120, Effacer 140, « +n » 30
    const chips = [150, 120, 140]
    const at = (line: number) => fitChips({ line, fixed: 160, searchMin: 200, chips, more: 30 })
    // all fit: 700 - 160 - 410 = 130 < 200, so not all; without Effacer: 700 - 160 - 270 - 30 = 240 >= 200
    expect(at(700)).toBe(2)
    // +n = the rest; « Effacer tous les filtres » is the last item, so the first one folded
    expect(chips.length - at(700)).toBe(1)
    expect(at(800)).toBe(3)
    expect(at(620)).toBe(1)
    expect(at(400)).toBe(0)
    // the search keeps exactly its minimum: still fits
    expect(fitChips({ line: 770, fixed: 160, searchMin: 200, chips })).toBe(3)
    expect(fitChips({ line: 769, fixed: 160, searchMin: 200, chips })).toBe(2)
  })

  it('counts n - 1 gaps between the shown items (chips, then « +n » when one folds)', () => {
    const at = (line: number) => fitChips({ line, fixed: 0, searchMin: 200, chips: [100, 100], more: 30, gap: 10 })
    // both shown: 100 + 10 + 100 = 210, no gap after the last one
    expect(at(410)).toBe(2)
    expect(at(409)).toBe(1)
    // one shown with « +1 »: 100 + 10 + 30 = 140
    expect(at(340)).toBe(1)
    expect(at(339)).toBe(0)
    // none shown, « +2 » alone: 30, no gap
    expect(at(230)).toBe(0)
  })

  it('nothing measured (jsdom, a hidden header): every chip stays visible', () => {
    expect(fitChips({ line: 0, fixed: 0, searchMin: 200, chips: [0, 0, 0] })).toBe(3)
  })

  it('no chip: nothing to fold', () => {
    expect(fitChips({ line: 300, fixed: 160, searchMin: 200, chips: [] })).toBe(0)
  })
})
