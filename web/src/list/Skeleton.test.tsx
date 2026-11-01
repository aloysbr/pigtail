import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Skeleton } from './EmptyStates'

const css = readFileSync(join(process.cwd(), 'src', 'list/list.css'), 'utf8')

/** The loading state is the silhouette of the list: group headers, then rows (status dot, title bar, age bar). */
describe('Skeleton', () => {
  it('draws group headers and rows with a status dot, a title bar and an age bar, all decorative', () => {
    const { container } = render(<Skeleton />)
    const root = container.firstElementChild as HTMLElement
    expect(root).toHaveAttribute('aria-busy', 'true')
    expect(root).toHaveAttribute('aria-label', 'Chargement du backlog')
    const heads = root.querySelectorAll('.skel-g')
    expect(heads.length).toBeGreaterThanOrEqual(1)
    for (const h of heads) {
      expect(h.querySelector('.skel-name')).not.toBeNull()
      expect(h.querySelector('.skel-n')).not.toBeNull()
    }
    const rows = root.querySelectorAll('.skel')
    expect(rows.length).toBeGreaterThanOrEqual(6)
    for (const r of rows) {
      expect(r.querySelector('.skel-dot')).not.toBeNull()
      expect(r.querySelector('.skel-t')).not.toBeNull()
      expect(r.querySelector('.skel-age')).not.toBeNull()
    }
    const widths = [...root.querySelectorAll<HTMLElement>('.skel-t')].map((t) => t.style.getPropertyValue('--w'))
    expect(new Set(widths).size).toBeGreaterThan(2)
    for (const el of [...heads, ...rows]) expect(el).toHaveAttribute('aria-hidden', 'true')
  })

  it('is deterministic (same widths on every render)', () => {
    const w = () => [...render(<Skeleton />).container.querySelectorAll<HTMLElement>('.skel-t')].map((t) => t.style.getPropertyValue('--w'))
    expect(w()).toEqual(w())
  })

  it('styles the silhouette from tokens and stops the shimmer under reduced motion', () => {
    expect(css).toMatch(/\.skel \{[^}]*min-height: var\(--row-h\)[^}]*border-bottom: var\(--size-bw\) solid var\(--row-line\)/)
    expect(css).toMatch(/\.skel-dot \{[^}]*border-radius: var\(--radius-pill\)/)
    expect(css).toMatch(/\.skel-t \{[^}]*width: var\(--w\)/)
    expect(css).toMatch(/@media \(prefers-reduced-motion: reduce\) \{[\s\S]*\.skel-b, \.skel-dot \{ animation: none; \}/)
  })
})
