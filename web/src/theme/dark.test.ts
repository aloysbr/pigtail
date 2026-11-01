// @vitest-environment node
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { blocks, props } from './css-parse'

/** The dark themes (and Ocean) live in palette.css, generated: 4 blocks, each redefining the same colour tokens.
 *  No prefers-color-scheme block any more: the appearance (light / dark / system) is resolved by the boot script
 *  and theme/appearance.ts, which set data-theme on the root. */
const palette = readFileSync(new URL('./palette.css', import.meta.url), 'utf8')
const tokens = readFileSync(new URL('./tokens.css', import.meta.url), 'utf8')
const COMBOS = blocks(palette)

describe('palette.css: four combinations, the same tokens in each', () => {
  it('has exactly 4 blocks, Terra and Ocean in light and dark (positive control)', () => {
    expect(COMBOS.map((b) => b.selector)).toEqual([
      ':root, [data-theme-palette="terra"][data-theme="light"]',
      ':root[data-theme="dark"], [data-theme-palette="terra"][data-theme="dark"]',
      ':root[data-theme-palette="ocean"], [data-theme-palette="ocean"][data-theme="light"]',
      ':root[data-theme-palette="ocean"][data-theme="dark"], [data-theme-palette="ocean"][data-theme="dark"]',
    ])
  })

  it('every block redefines the same colour tokens', () => {
    const keys = COMBOS.map((b) => [...props(b.body).keys()].sort())
    expect(keys[0].length).toBeGreaterThan(30)
    for (const k of keys.slice(1)) expect(k).toEqual(keys[0])
  })

  it('each block switches the form controls (color-scheme matches the mode)', () => {
    for (const b of COMBOS) {
      const mode = /data-theme="dark"/.test(b.selector) ? 'dark' : 'light'
      expect(b.body, b.selector).toMatch(new RegExp(`color-scheme:\\s*${mode};`))
    }
  })

  it('tokens.css holds no prefers-color-scheme block and redefines no palette colour', () => {
    expect(tokens).not.toMatch(/prefers-color-scheme/)
    const colours = new Set(props(COMBOS[0].body).keys())
    const clash = blocks(tokens).flatMap((b) => [...props(b.body).keys()]).filter((k) => colours.has(k))
    expect(clash).toEqual([])
  })
})
