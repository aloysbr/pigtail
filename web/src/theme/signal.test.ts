// @vitest-environment node
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { blocks, props } from './css-parse'

/** One strong colour (DS Hybride, ADR 2026-10-26): the accent exists only as --accent-attn-*, reserved to what awaits an
 *  answer. Link, selection, focus, the primary button and the current step are ink; the subject statuses are neutrals. */
const tokens = readFileSync(new URL('./tokens.css', import.meta.url), 'utf8')
const palette = readFileSync(new URL('./palette.css', import.meta.url), 'utf8')
const combos = blocks(palette).map((b) => ({ sel: b.selector, p: props(b.body) }))
const declaredInTokens = blocks(tokens).flatMap((b) => [...props(b.body).keys()])

describe('the accent does not exist outside --accent-attn-*', () => {
  it('reads the 4 combinations (positive control)', () => {
    expect(combos).toHaveLength(4)
    for (const c of combos) expect(c.p.has('--accent-attn-solid'), c.sel).toBe(true)
  })

  it('no sheet declares a legacy accent or status-colour token', () => {
    const all = [...declaredInTokens, ...combos.flatMap((c) => [...c.p.keys()])]
    const legacy = all.filter((k) => /^--(accent(?!-attn-)[\w-]*|st-[\w-]+|attn(-[\w-]+)?)$/.test(k))
    expect([...new Set(legacy)]).toEqual([])
  })

  it('link, selection, focus, primary, current step are ink (or a neutral), in every combination', () => {
    for (const { sel, p } of combos) {
      for (const k of ['--link', '--focus', '--mark', '--btn-fill']) expect(p.get(k), `${sel} ${k}`).toBe(p.get('--text'))
      expect(p.get('--bg-selected'), sel).not.toMatch(/var\(--accent/)
    }
  })

  it('the subject statuses are neutrals (text1, text2, text3), never the accent', () => {
    for (const { sel, p } of combos) {
      expect(p.get('--s-cours'), sel).toBe(p.get('--text-1'))
      expect(p.get('--s-done'), sel).toBe(p.get('--text-2'))
      expect(p.get('--s-standby'), sel).toBe(p.get('--text-disabled'))
    }
  })
})
