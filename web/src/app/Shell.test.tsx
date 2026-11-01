import { describe, expect, it, vi } from 'vitest'
import { buildModel } from '@/list/model'
import { getUi, setUi } from '@/state/ui'
import { makeBacklog } from '@/test/backlog'
import { handleKey, type KeyActions } from './Shell'

function ev(init: KeyboardEventInit, target: EventTarget = document.body): KeyboardEvent {
  const e = new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init })
  Object.defineProperty(e, 'target', { value: target })
  return e
}

function actions() {
  const moveRank = vi.fn<KeyActions['moveRank']>()
  const toggleStandby = vi.fn<KeyActions['toggleStandby']>()
  return { moveRank, toggleStandby } satisfies KeyActions
}

describe('handleKey: actions', () => {
  const model = () => buildModel(makeBacklog(), getUi())

  it('⌥↑ / ⌥↓ go to actions.moveRank with the selection', () => {
    setUi({ sel: 'pig-r1' })
    const a = actions()
    const up = ev({ key: 'ArrowUp', altKey: true })
    handleKey(up, model(), true, true, a)
    handleKey(ev({ key: 'ArrowDown', altKey: true }), model(), true, true, a)
    expect(a.moveRank.mock.calls).toEqual([
      ['pig-r1', -1],
      ['pig-r1', 1],
    ])
    expect(up.defaultPrevented).toBe(true)
  })

  it('any other ⌥ key stays ignored (no move of the selection either)', () => {
    setUi({ sel: 'pig-r1' })
    const a = actions()
    handleKey(ev({ key: 'j', altKey: true }), model(), true, true, a)
    handleKey(ev({ key: 'ArrowLeft', altKey: true }), model(), true, true, a)
    expect(a.moveRank).not.toHaveBeenCalled()
    expect(getUi().sel).toBe('pig-r1')
  })

  it('⌥↑ in a field, with ⌘ or ⇧, or without a selection does nothing', () => {
    const a = actions()
    setUi({ sel: 'pig-r1' })
    const input = document.createElement('input')
    handleKey(ev({ key: 'ArrowUp', altKey: true }, input), model(), true, true, a)
    handleKey(ev({ key: 'ArrowUp', altKey: true, metaKey: true }), model(), true, true, a)
    handleKey(ev({ key: 'ArrowUp', altKey: true, shiftKey: true }), model(), true, true, a)
    setUi({ sel: null })
    handleKey(ev({ key: 'ArrowUp', altKey: true }), model(), true, true, a)
    expect(a.moveRank).not.toHaveBeenCalled()
  })

  it('↑ without ⌥ still moves the selection, not the rank', () => {
    const a = actions()
    const m = model()
    setUi({ sel: m.order[1] ?? null })
    handleKey(ev({ key: 'ArrowUp' }), m, true, true, a)
    expect(a.moveRank).not.toHaveBeenCalled()
  })

  it('h goes to actions.toggleStandby with the selection; not while typing, with ⌘ or without a selection', () => {
    const a = actions()
    setUi({ sel: 'pig-r1' })
    handleKey(ev({ key: 'h' }), model(), true, true, a)
    handleKey(ev({ key: 'h' }, document.createElement('input')), model(), true, true, a)
    handleKey(ev({ key: 'h', metaKey: true }), model(), true, true, a)
    setUi({ sel: null })
    handleKey(ev({ key: 'h' }), model(), true, true, a)
    expect(a.toggleStandby.mock.calls).toEqual([['pig-r1']])
  })
})
