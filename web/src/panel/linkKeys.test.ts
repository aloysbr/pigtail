import { describe, expect, it } from 'vitest'
import { linkDigit } from './linkKeys'

function key(init: KeyboardEventInit, target?: EventTarget): KeyboardEvent {
  const e = new KeyboardEvent('keydown', { bubbles: true, ...init })
  if (target) Object.defineProperty(e, 'target', { value: target })
  return e
}

describe('linkDigit: the physical digit key, never the character', () => {
  it('AZERTY without Shift: code Digit2, key « é » → 2', () => {
    expect(linkDigit(key({ code: 'Digit2', key: 'é' }))).toBe(2)
  })

  it('the numeric keypad: Numpad3 → 3', () => {
    expect(linkDigit(key({ code: 'Numpad3', key: '3' }))).toBe(3)
  })

  it('reads the code, not the key: key « 2 » on another code is not a digit', () => {
    expect(linkDigit(key({ code: 'KeyQ', key: '2' }))).toBeNull()
  })

  it('a modifier, or a held key, is no link key', () => {
    expect(linkDigit(key({ code: 'Digit2', key: '2', shiftKey: true }))).toBeNull()
    expect(linkDigit(key({ code: 'Digit2', key: '2', altKey: true }))).toBeNull()
    expect(linkDigit(key({ code: 'Digit2', key: '2', metaKey: true }))).toBeNull()
    expect(linkDigit(key({ code: 'Digit2', key: '2', ctrlKey: true }))).toBeNull()
    expect(linkDigit(key({ code: 'Digit2', key: '2', repeat: true }))).toBeNull()
  })

  it('typing in a field is never a link key', () => {
    expect(linkDigit(key({ code: 'Digit2', key: 'é' }, document.createElement('input')))).toBeNull()
    expect(linkDigit(key({ code: 'Digit2', key: 'é' }, document.createElement('textarea')))).toBeNull()
  })

  it('0 opens nothing', () => {
    expect(linkDigit(key({ code: 'Digit0', key: 'à' }))).toBeNull()
  })
})
