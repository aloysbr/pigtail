import { describe, expect, it } from 'vitest'
import { cn } from './utils'

describe('cn', () => {
  it('joins classes and lets the later Tailwind utility win', () => {
    expect(cn('px-2', false && 'hidden', 'px-4')).toBe('px-4')
    expect(cn('truncate', { 'min-w-0': true })).toBe('truncate min-w-0')
  })
})
