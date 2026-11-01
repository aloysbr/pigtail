// @vitest-environment node
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { propsOf } from './css-parse'
import { contrast, deltaE, parseOklch, type Oklch } from './oklch'

/** Contrasts and ΔE measured on the ROUNDED values of palette.css (never on the engine): a rounding that eats a margin turns red.
 *  Thresholds: text 4.5:1; the accent's solid and text 4.6:1 (the engine's own target); its dot 3:1; ΔE OKLab 0.12 between a
 *  status that means something (late, ok) and the accent. --status-today is a landmark only (ΔE Terra light with the dot = 0.105,
 *  palette untouchable, D6): measured and logged, never asserted. */
const FILE = fileURLToPath(new URL('./palette.css', import.meta.url))
const css = existsSync(FILE) ? readFileSync(FILE, 'utf8') : ''

const COMBOS: Record<string, string> = {
  'Terra clair': ':root, [data-theme-palette="terra"][data-theme="light"]',
  'Terra sombre': ':root[data-theme="dark"], [data-theme-palette="terra"][data-theme="dark"]',
  'Ocean clair': ':root[data-theme-palette="ocean"], [data-theme-palette="ocean"][data-theme="light"]',
  'Ocean sombre': ':root[data-theme-palette="ocean"][data-theme="dark"], [data-theme-palette="ocean"][data-theme="dark"]',
}
/** The selector as css-parse.ts joins it (newlines folded). */
const sel = (s: string) => s.replace(/\s+/g, ' ')

function combo(name: string): (t: string) => Oklch {
  const p = propsOf(css, sel(COMBOS[name]))
  return (t) => {
    const v = p.get(t)
    if (v === undefined) throw new Error(`${name}: ${t} not declared in palette.css`)
    return parseOklch(v)
  }
}

const ACCENT = ['--accent-attn-solid', '--accent-attn-dot', '--accent-attn-text']

describe('palette.css measures (rounded values)', () => {
  it('palette.css exists and declares the 4 combinations (positive control)', () => {
    expect(css, 'palette.css missing: run `npm run gen:palette`').not.toBe('')
    for (const [name, s] of Object.entries(COMBOS)) expect(propsOf(css, sel(s)).size, name).toBeGreaterThan(30)
  })

  for (const name of Object.keys(COMBOS)) {
    describe(name, () => {
      const t = (k: string) => combo(name)(k)

      it.each(['--text', '--text-1', '--text-2'])('%s on --bg, --bg-2, --surface ≥ 4.5:1', (k) => {
        for (const g of ['--bg', '--bg-2', '--surface']) expect(contrast(t(k), t(g)), `${k} on ${g}`).toBeGreaterThanOrEqual(4.5)
      })

      it('--bg-hover-side reads ≥ 1.1:1 on --surface-side and stays apart from --bg-active (hover is not selection)', () => {
        const side = t('--surface-side')
        const hv = t('--bg-hover-side')
        const act = t('--bg-active')
        const dir = side.L > 0.5 ? -1 : 1 // light grounds darken, dark grounds lighten
        console.info(`${name}: --bg-hover-side vs side ${contrast(hv, side).toFixed(3)}; vs --bg-active ${contrast(hv, act).toFixed(3)} (ΔE ${deltaE(hv, act).toFixed(3)})`)
        expect(contrast(hv, side), 'hover vs side ground').toBeGreaterThanOrEqual(1.1)
        // strictly between the ground and the selection, and measurably not the selection
        expect(Math.sign(hv.L - side.L), 'steps away from the ground the same way as the selection').toBe(dir)
        expect(Math.abs(hv.L - side.L), 'hover is a smaller step than the selection').toBeLessThan(Math.abs(act.L - side.L))
        expect(deltaE(hv, act), 'hover ≠ selection').toBeGreaterThanOrEqual(0.004)
      })

      it('--bg-active-side (current sidebar item) reads ≥ 1.1:1 on --surface-side and is one step past --bg-hover-side (D17)', () => {
        const side = t('--surface-side')
        const hv = t('--bg-hover-side')
        const cur = t('--bg-active-side')
        const dir = side.L > 0.5 ? -1 : 1
        console.info(`${name}: --bg-active-side vs side ${contrast(cur, side).toFixed(3)}; vs --bg-hover-side ${contrast(cur, hv).toFixed(3)} (ΔE ${deltaE(cur, hv).toFixed(3)})`)
        expect(contrast(cur, side), 'current vs side ground').toBeGreaterThanOrEqual(1.1)
        expect(Math.sign(cur.L - side.L), 'steps away from the ground the same way as the hover').toBe(dir)
        expect(Math.abs(cur.L - side.L), 'current is a darker step than the hover').toBeGreaterThan(Math.abs(hv.L - side.L))
        // verify lot 2: on the dark side ground the current item must read apart from the hover (was ΔE 0.02, near-identical)
        expect(deltaE(cur, hv), 'current ≠ hover').toBeGreaterThanOrEqual(side.L > 0.5 ? 0.019 : 0.03)
      })

      it('--bg-active-pal (selected ⌘K item) reads ≥ 1.15:1 on --surface (the palette ground)', () => {
        const surf = t('--surface')
        const pal = t('--bg-active-pal')
        console.info(`${name}: --bg-active-pal vs surface ${contrast(pal, surf).toFixed(3)}; --bg-active vs surface ${contrast(t('--bg-active'), surf).toFixed(3)}`)
        expect(contrast(pal, surf), 'selected palette item vs palette surface').toBeGreaterThanOrEqual(1.15)
      })

      it('--accent-attn-on on --accent-attn-solid ≥ 4.6:1', () => {
        expect(contrast(t('--accent-attn-on'), t('--accent-attn-solid'))).toBeGreaterThanOrEqual(4.6)
      })

      it('--accent-attn-text on its tint and every ground ≥ 4.6:1', () => {
        for (const g of ['--accent-attn-tint', '--bg', '--surface', '--bg-2'])
          expect(contrast(t('--accent-attn-text'), t(g)), g).toBeGreaterThanOrEqual(4.6)
      })

      it('--accent-attn-dot on --bg ≥ 3:1', () => {
        expect(contrast(t('--accent-attn-dot'), t('--bg'))).toBeGreaterThanOrEqual(3)
      })

      it.each(['--status-late', '--status-ok'])('%s stays apart from the accent: ΔE OKLab ≥ 0.12', (k) => {
        for (const a of ACCENT) expect(deltaE(t(k), t(a)), `${k} ↔ ${a}`).toBeGreaterThanOrEqual(0.12)
      })

      it('--status-today is a landmark only: measured, logged, no threshold', () => {
        const d = Math.min(...ACCENT.map((a) => deltaE(t('--status-today'), t(a))))
        console.info(`${name}: ΔE --status-today ↔ accent (repère, sans seuil) = ${d.toFixed(3)}`)
        expect(Number.isFinite(d)).toBe(true)
      })

      it('--status-late as text on --bg, --bg-2, --surface ≥ 4.5:1', () => {
        for (const g of ['--bg', '--bg-2', '--surface']) expect(contrast(t('--status-late'), t(g)), g).toBeGreaterThanOrEqual(4.5)
      })
    })
  }
})
