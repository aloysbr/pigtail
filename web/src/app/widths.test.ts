import { describe, expect, it } from 'vitest'
import { clamp, panelBounds, sideBounds } from '@/app/widths'

// jsdom reads no stylesheet: tokenPx falls back, so the fallbacks must equal tokens.css (200, 360, 400, 400)
describe('width bounds (D2, D5)', () => {
  it('sideBounds keeps the list minimum', () => {
    expect(sideBounds(1440, 560, 'side')).toEqual({ min: 200, max: 360 })
    expect(sideBounds(1000, 560, 'side').max).toBe(Math.max(200, 1000 - 560 - 400))
    // a sheet over the list takes nothing from it
    expect(sideBounds(1000, 560, 'over')).toEqual({ min: 200, max: 360 })
  })

  it('panelBounds', () => {
    expect(panelBounds(1200, 560, 'side')).toEqual({ min: 400, max: 800 })
    expect(panelBounds(700, 560, 'side')).toEqual({ min: 400, max: 400 })
  })

  it('panelBounds over keeps the default inside', () => {
    // 1120 docked: avail 880, the formula alone gives {400, 480} while the default sheet is 560
    const b = panelBounds(880, 560, 'over')
    expect(b).toEqual({ min: 400, max: 560 })
    expect(clamp(560, b.min, b.max)).toBe(560)
    expect(clamp(800, b.min, b.max)).toBe(560)
  })

  it('clamp', () => {
    expect(clamp(120, 200, 360)).toBe(200)
    expect(clamp(400, 200, 360)).toBe(360)
    expect(clamp(250, 200, 360)).toBe(250)
  })
})
