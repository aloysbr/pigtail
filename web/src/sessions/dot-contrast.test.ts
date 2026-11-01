// @vitest-environment node
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { blocks, propsOf } from '@/theme/css-parse'
import { contrast, luminance, parseOklch, toLinear, type Oklch } from '@/theme/oklch'

/** WCAG 1.4.11: the state dot (fill or ring) is a non-text indicator, ≥ 3:1 against the surface it sits on, in the four
 *  palettes; the waiting dot's halo must be visible beside the surface (the tint of the chip was not). */
const read = (f: string) => readFileSync(fileURLToPath(new URL(f, import.meta.url)), 'utf8')
const sessions = blocks(read('./sessions.css'))
const palette = read('../theme/palette.css')

const COMBOS: Record<string, string> = {
  'Terra clair': ':root, [data-theme-palette="terra"][data-theme="light"]',
  'Terra sombre': ':root[data-theme="dark"], [data-theme-palette="terra"][data-theme="dark"]',
  'Ocean clair': ':root[data-theme-palette="ocean"], [data-theme-palette="ocean"][data-theme="light"]',
  'Ocean sombre': ':root[data-theme-palette="ocean"][data-theme="dark"], [data-theme-palette="ocean"][data-theme="dark"]',
}

const tokenIn = (selector: string, prop: string): string => {
  const b = sessions.find((x) => x.selector.replace(/^@layer components /, '') === selector)
  if (!b) throw new Error(`no rule ${selector}`)
  const decl = new RegExp(`(?:^|;)\\s*${prop}:([^;]*)`).exec(b.body)
  const vars = [...(decl?.[1] ?? '').matchAll(/var\((--[\w-]+)\)/g)].map((x) => x[1])
  const last = vars.at(-1)
  if (!last) throw new Error(`no token for ${prop} in ${selector}`)
  return last
}

const enc = (x: number) => (x <= 0.0031308 ? 12.92 * x : 1.055 * x ** (1 / 2.4) - 0.055)
/** An rgba token over an opaque one, mixed on the encoded sRGB like a browser, as an opaque colour's luminance. */
function over(top: Oklch, base: Oklch): number {
  const t = toLinear(top).map(enc)
  const b = toLinear(base).map(enc)
  const dec = (x: number) => (x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4)
  const m = t.map((v, i) => dec(v * top.a + b[i] * (1 - top.a)))
  return 0.2126 * m[0] + 0.7152 * m[1] + 0.0722 * m[2]
}

describe('state dot contrast (non-text, ≥ 3:1) in the four palettes', () => {
  const ring = tokenIn('.srow.sg-idle .dot::before, .sdot-w.sg-idle .dot::before', 'border')
  const fill = tokenIn('.sess-wait .dot::before', 'background')
  const halo = tokenIn('.sess-wait .dot::before', 'box-shadow')

  it('reads the tokens the rules cite (positive control)', () => {
    expect(ring).toMatch(/^--/)
    expect(fill).toMatch(/^--/)
    expect(halo).toMatch(/^--/)
  })

  for (const [name, selector] of Object.entries(COMBOS)) {
    const p = propsOf(palette, selector.replace(/\s+/g, ' '))
    const t = (k: string) => parseOklch(p.get(k) ?? '')
    it(`${name}: the grey ring and the waiting dot ≥ 3:1 on --surface; the halo is seen`, () => {
      const surface = t('--surface')
      expect(contrast(t(ring), surface), ring).toBeGreaterThanOrEqual(3)
      expect(contrast(t(fill), surface), fill).toBeGreaterThanOrEqual(3)
      const haloL = over(t(halo), surface)
      const sL = luminance(surface)
      const ratio = (Math.max(haloL, sL) + 0.05) / (Math.min(haloL, sL) + 0.05)
      expect(ratio, `${halo} over --surface`).toBeGreaterThanOrEqual(1.3)
    })
  }
})
