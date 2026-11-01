// @vitest-environment node
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { blocks, propsOf } from './css-parse'

/** The Hybride structure tokens, compared with the `.ds[data-style="hybride"]` block of t3/ds.css (the reference mockup):
 *  hairlines 0.5 px, radii 6/8/10/12, flat cards, a 56 px header, serif titles, bold areas and groups. */
const SRC = fileURLToPath(new URL('..', import.meta.url))
const THEME = fileURLToPath(new URL('.', import.meta.url))
const root = propsOf(readFileSync(join(THEME, 'tokens.css'), 'utf8'), ':root')

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n)
    if (statSync(p).isDirectory()) return files(p)
    return /\.css$/.test(n) ? [p] : []
  })
}
const sheets = files(SRC).filter((f) => !/(tokens|palette)\.css$/.test(f))

describe('Hybride structure tokens (t3 ds.css values)', () => {
  it('reads tokens.css (positive control)', () => {
    expect(root.size).toBeGreaterThan(60)
  })

  it.each([
    ['--size-bw', '0.5px'],
    ['--radius-1', '6px'],
    ['--radius-2', '8px'],
    ['--radius-3', '10px'],
    ['--radius-4', '12px'],
    ['--radius-btn', '8px'],
    ['--shadow-card', 'none'],
    ['--head-h', '56px'],
    ['--fs-page-title', '21px'],
    ['--fw-page-title', '500'],
    ['--nav-area-fs', '13px'],
    ['--nav-area-fw', '700'],
    ['--fs-g1', '15px'],
    ['--fw-g1', '700'],
    ['--fs-g2', '13px'],
    ['--fw-g2', '600'],
    ['--row-line', 'transparent'],
    ['--group-bg', 'transparent'],
    ['--font-serif', 'ui-serif, "New York", "Iowan Old Style", Georgia, serif'],
    ['--font-title', 'var(--font-serif)'],
    ['--c-g2', 'var(--text-1)'],
  ])('%s = %s', (k, v) => {
    expect(root.get(k)).toBe(v)
  })

  it('only the page title and the panel title read --font-title', () => {
    const readers: string[] = []
    for (const f of sheets)
      for (const b of blocks(readFileSync(f, 'utf8')))
        if (/var\(--font-title\)/.test(b.body)) readers.push(`${relative(SRC, f)}: ${b.selector.replace(/^@layer components /, '')}`)
    expect(readers.sort()).toEqual(['list/list.css: .d-head h1', 'panel/panel.css: .lp-t'])
  })

  it('t3: the hairline under a group head is per area only (level 2 head: bottom border transparent)', () => {
    const all = blocks(readFileSync(join(SRC, 'list', 'list.css'), 'utf8'))
    const body = (sel: RegExp) => all.find((b) => sel.test(b.selector.replace(/^@layer components /, '')))?.body ?? ''
    expect(body(/^\.gbtn$/)).toMatch(/border-bottom:\s*var\(--size-bw\) solid var\(--line\)/)
    expect(body(/^\.g2 \.gbtn$/)).toMatch(/border-bottom-color:\s*transparent/)
  })
})
