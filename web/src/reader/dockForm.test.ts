import { act, renderHook } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { setWidth } from '@/test/render'
import { clampCard, DOCK_CARD_MIN, DOCK_SIDE_MIN, dockFormOf, useDockForm } from './dockForm'

describe('dockForm (D6, D7): the dock follows the window width', () => {
  it('1800 / 1440 / 390 → side / card / sheet', () => {
    expect(dockFormOf(1800)).toBe('side')
    expect(dockFormOf(1440)).toBe('card')
    expect(dockFormOf(390)).toBe('sheet')
  })

  it('the bounds: 1700 is a side band, 1699 a card; 600 is a card, 599 a sheet', () => {
    expect(DOCK_SIDE_MIN).toBe(1700)
    expect(DOCK_CARD_MIN).toBe(600)
    expect(dockFormOf(1700)).toBe('side')
    expect(dockFormOf(1699)).toBe('card')
    expect(dockFormOf(600)).toBe('card')
    expect(dockFormOf(599)).toBe('sheet')
  })

  it('useDockForm re-renders on a resize', () => {
    setWidth(1800)
    const { result } = renderHook(() => useDockForm())
    expect(result.current).toBe('side')
    act(() => setWidth(1440))
    expect(result.current).toBe('card')
    act(() => setWidth(390))
    expect(result.current).toBe('sheet')
  })

  it('clampCard keeps the card inside its box (under the bar, inside the window)', () => {
    const box = { w: 1440, h: 856 }
    const card = { w: 360, h: 400 }
    expect(clampCard({ x: -50, y: -80 }, card, box)).toEqual({ x: 0, y: 0 })
    expect(clampCard({ x: 5000, y: 5000 }, card, box)).toEqual({ x: 1080, y: 456 })
    expect(clampCard({ x: 200, y: 100 }, card, box)).toEqual({ x: 200, y: 100 })
    // a card taller than its box sticks to the top: its header stays reachable
    expect(clampCard({ x: 10, y: 50 }, { w: 360, h: 900 }, box)).toEqual({ x: 10, y: 0 })
  })

  it('clampCard keeps a margin from the box edges (the card never touches the window)', () => {
    const box = { w: 600, h: 800 }
    const card = { w: 360, h: 400 }
    // dragged at 1280 wide to x=487, then the window shrinks to 600: 600 - 360 - 16 = 224
    expect(clampCard({ x: 487, y: 100 }, card, box, 16)).toEqual({ x: 224, y: 100 })
    expect(clampCard({ x: -5, y: -5 }, card, box, 16)).toEqual({ x: 16, y: 16 })
    // a box narrower than card + margins: sticks to the left margin, the header reachable
    expect(clampCard({ x: 300, y: 0 }, card, { w: 380, h: 800 }, 16)).toEqual({ x: 16, y: 16 })
  })
})
