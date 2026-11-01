import { describe, expect, it } from 'vitest'
import { menuOffset, menuRoom } from './menuAnchor'

// verify t2: at 820 px, by finger, hd-2: Filtre at x 741.5-804, its 220 px menu anchored left ran to 961.5 (141 px off screen)
const M = 16 // --sp-6

describe('menuOffset: a header menu always stays inside the window, --sp-6 from each edge', () => {
  it('Filtre near the right edge (verify t2, 820 px): the menu moves left to end 16 px from the edge', () => {
    const x = menuOffset({ button: { left: 741.5, right: 804 }, menu: 220, viewport: 820, margin: M, prefer: 'left' })
    // menu left (in the window) = 741.5 + x; its right edge = 820 - 16
    expect(741.5 + x + 220).toBe(820 - M)
    expect(x).toBeLessThan(0)
  })

  it('Filtre on the left (1000 px, sheet open, list 440): the left anchor holds, no shift', () => {
    expect(menuOffset({ button: { left: 300, right: 362 }, menu: 220, viewport: 1000, margin: M, prefer: 'left' })).toBe(0)
  })

  it('Grouper (anchored right): no shift while it fits, moves right when it would pass the left edge', () => {
    // right anchor: menu left = button.right - menu
    expect(menuOffset({ button: { left: 700, right: 760 }, menu: 220, viewport: 800, margin: M, prefer: 'right' })).toBe(60 - 220)
    const x = menuOffset({ button: { left: 100, right: 160 }, menu: 220, viewport: 400, margin: M, prefer: 'right' })
    expect(100 + x).toBe(M)
  })

  it('Scopes (anchored left) at a narrow width: kept inside on the right', () => {
    const x = menuOffset({ button: { left: 200, right: 290 }, menu: 220, viewport: 390, margin: M, prefer: 'left' })
    expect(200 + x + 220).toBe(390 - M)
  })

  it('a menu wider than the room starts at the left margin (its max-width caps it in CSS)', () => {
    const x = menuOffset({ button: { left: 100, right: 160 }, menu: 400, viewport: 300, margin: M, prefer: 'left' })
    expect(100 + x).toBe(M)
  })
})

describe('menuOffset inside the list (verify t3: sheet beside the list, the menu ran under section.lpanel)', () => {
  it('list 440 (window 1000, sheet from x 441): Filtre at 339.5-402, its 220 px menu ends at 440 - 16 at most', () => {
    const area = { left: 0, right: 440 }
    const x = menuOffset({ button: { left: 339.5, right: 402 }, menu: 220, viewport: 1000, margin: M, prefer: 'left', area })
    expect(339.5 + x + 220).toBe(440 - M)
    expect(339.5 + x).toBeGreaterThanOrEqual(M)
  })

  it('list 540 (window 1100, sheet from x 541): same', () => {
    const area = { left: 0, right: 540 }
    const x = menuOffset({ button: { left: 439.5, right: 502 }, menu: 220, viewport: 1100, margin: M, prefer: 'left', area })
    expect(439.5 + x + 220).toBe(540 - M)
    expect(439.5 + x).toBeGreaterThanOrEqual(M)
  })

  it('a menu wider than the list starts at the list left bound', () => {
    const x = menuOffset({ button: { left: 300, right: 360 }, menu: 300, viewport: 1000, margin: M, prefer: 'left', area: { left: 240, right: 480 } })
    expect(300 + x).toBe(240 + M)
  })

  it('a list wider than the window is cut by the window (the green cases hold: 820 → right 804)', () => {
    const x = menuOffset({ button: { left: 741.5, right: 804 }, menu: 220, viewport: 820, margin: M, prefer: 'left', area: { left: 0, right: 900 } })
    expect(741.5 + x + 220).toBe(820 - M)
  })

  it('menuRoom: the width a menu may take inside list and window', () => {
    expect(menuRoom({ viewport: 1000, margin: M, area: { left: 240, right: 680 } })).toBe(440 - 2 * M)
    expect(menuRoom({ viewport: 390, margin: M })).toBe(390 - 2 * M)
  })
})
