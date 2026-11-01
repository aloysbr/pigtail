import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { contrast, parseOklch } from '../theme/oklch'

const css = (f: string) => readFileSync(join(process.cwd(), 'src', f), 'utf8')
const reader = css('reader/reader.css')
const palette = css('theme/palette.css')

describe('Picker row hover (dark panel)', () => {
  const blocks = [...palette.matchAll(/^((?:[^\n{]*\n)?\[data-theme-palette="(\w+)"\]\[data-theme="(\w+)"\])\s*\{([^}]*)\}/gm)]
  for (const m of blocks) {
    const get = (n: string) => new RegExp(`${n}:\\s*([^;]+);`).exec(m[4])?.[1]
    it(`${m[2]} ${m[3]}: --bg-hover-panel is declared`, () => {
      expect(get('--bg-hover-panel')).toBeTruthy()
    })
    if (m[3] === 'dark') {
      it(`${m[2]} dark: --bg-hover-panel differs from the panel (--surface) by >= 1.15:1`, () => {
        expect(contrast(parseOklch(get('--bg-hover-panel') ?? 'oklch(0.308 0 0)'), parseOklch(get('--surface')!))).toBeGreaterThanOrEqual(1.15)
      })
    } else {
      it(`${m[2]} light: --bg-hover-panel stays --bg-hover`, () => {
        expect(get('--bg-hover-panel')).toBe(get('--bg-hover'))
      })
    }
  }
  it('a picker row hovers on --bg-hover-panel', () => {
    expect(reader).toMatch(/\.pk-b:hover \{[^}]*background: var\(--bg-hover-panel\)/)
  })
  it('the list keeps a gap under its label so the focus ring does not touch it', () => {
    expect(reader).toMatch(/\.srows\.pk-list \{[^}]*padding-top: var\(--sp-1\)/)
  })
})
