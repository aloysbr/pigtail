import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { contrast, parseOklch } from '../theme/oklch'

const css = (f: string) => readFileSync(join(process.cwd(), 'src', f), 'utf8')
const list = css('list/list.css')
const sessions = css('sessions/sessions.css')
const palette = css('theme/palette.css')
const tokens = css('theme/tokens.css')

/** Every list row shows a hover, group headers (top, project sub-group) included, in every theme. */
describe('List row hover', () => {
  it('a project sub-group header has its own hover, declared after its resting rule', () => {
    const rest = list.indexOf('.g2 .gbtn {')
    const hover = list.search(/\.g2 \.gbtn:hover \{[^}]*background: var\(--bg-hover\)/)
    expect(rest).toBeGreaterThan(-1)
    expect(hover).toBeGreaterThan(rest)
  })
  it('a top-level group header hovers on --bg-hover', () => {
    expect(list).toMatch(/\.gbtn:hover \{[^}]*background: var\(--bg-hover\)/)
  })
  it('subject rows, Done / Captures lines and Sessions rows hover on --bg-hover', () => {
    expect(list).toMatch(/\.lr6:hover \{[^}]*background: var\(--bg-hover\)/)
    expect(list).toMatch(/\.more:hover, \.sline:hover \{[^}]*background: var\(--bg-hover\)/)
    expect(sessions).toMatch(/\.srow:hover \{[^}]*background: var\(--bg-hover\)/)
  })
  it('every group header rests on --bg (the group background is transparent, or the hover must follow it)', () => {
    expect(tokens).toMatch(/--group-bg: transparent;/)
    expect(list).toMatch(/\.g2 \.gbtn \{[^}]*background: var\(--bg\);/)
  })

  // One block per (palette, scheme); each rests on --bg (groups, sub-groups, rows, Sessions rows).
  const blocks = [...palette.matchAll(/^((?:[^\n{]*\n)?\[data-theme-palette="(\w+)"\]\[data-theme="(\w+)"\])\s*\{([^}]*)\}/gm)]
  it('covers terra and ocean, light and dark', () => {
    expect(blocks.map((m) => `${m[2]}/${m[3]}`).sort()).toEqual(['ocean/dark', 'ocean/light', 'terra/dark', 'terra/light'])
  })
  for (const m of blocks) {
    it(`${m[2]} ${m[3]}: --bg-hover differs from --bg by >= 1.1:1`, () => {
      const get = (n: string) => new RegExp(`${n}:\\s*([^;]+);`).exec(m[4])![1]
      expect(contrast(parseOklch(get('--bg-hover')), parseOklch(get('--bg')))).toBeGreaterThanOrEqual(1.1)
    })
  }
})
