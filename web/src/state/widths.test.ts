import { afterEach, describe, expect, it, vi } from 'vitest'
import { getWidths, resetPanelW, resetSideW, resetWidths, setPanelW, setSideW } from '@/state/widths'

describe('widths store (D2: the widths live in localStorage)', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('a width survives a reload', () => {
    setSideW(300, true)
    expect(localStorage.getItem('pigtail-side-w')).toBe('300')
    resetWidths()
    expect(getWidths().side).toBe(300)
    setPanelW(480, true)
    resetWidths()
    expect(getWidths().panel).toBe(480)
  })

  it('a width set without persisting is not kept', () => {
    setSideW(280, false)
    expect(getWidths().side).toBe(280)
    expect(localStorage.getItem('pigtail-side-w')).toBeNull()
  })

  it('reset clears it', () => {
    setSideW(300, true)
    setPanelW(480, true)
    resetSideW()
    resetPanelW()
    expect(getWidths()).toEqual({ side: null, panel: null })
    expect(localStorage.getItem('pigtail-side-w')).toBeNull()
    expect(localStorage.getItem('pigtail-panel-w')).toBeNull()
  })

  it('a stored sidebar outside 200-360 is clamped on read', () => {
    localStorage.setItem('pigtail-side-w', '900')
    resetWidths()
    expect(getWidths().side).toBe(360)
    localStorage.setItem('pigtail-side-w', '20')
    resetWidths()
    expect(getWidths().side).toBe(200)
    localStorage.setItem('pigtail-side-w', 'abc')
    resetWidths()
    expect(getWidths().side).toBeNull()
  })

  it('a throwing localStorage gives the defaults', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    resetWidths()
    expect(getWidths()).toEqual({ side: null, panel: null })
    expect(() => setSideW(300, true)).not.toThrow()
    expect(() => resetSideW()).not.toThrow()
  })

  it('resetWidths rereads localStorage', () => {
    localStorage.setItem('pigtail-side-w', '320')
    resetWidths()
    expect(getWidths().side).toBe(320)
  })
})
