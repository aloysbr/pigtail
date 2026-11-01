// @vitest-environment node
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { blocks } from '@/theme/css-parse'

describe('sessions.css static guards (the browser round proves the pixels)', () => {
  const css = readFileSync(fileURLToPath(new URL('./sessions.css', import.meta.url)), 'utf8')
  const all = blocks(css)
  const rule = (sel: RegExp) => {
    const b = all.find((x) => sel.test(x.selector.replace(/^@layer components /, '')))
    if (!b) throw new Error(`no rule ${sel}`)
    return b.body
  }

  it('reads the sheet (positive control)', () => {
    expect(all.length).toBeGreaterThan(15)
  })

  it('a wide row is one line: dot · title · state · cell · since', () => {
    expect(rule(/^\.srow$/)).toMatch(/grid-template-areas:\s*"i t s c d"/)
  })

  it('under 640 px the row goes to two lines: dot · title · since, then state, life and cell under the title', () => {
    const narrow = rule(/@container \(max-width: 640px\) \.srow$/)
    expect(narrow).toMatch(/grid-template-areas:\s*"i t d" "\. m m"/)
    expect(rule(/@container \(max-width: 640px\) \.srow \.s-m$/)).toMatch(/grid-area:\s*m/)
  })

  it('a linked session in the subject sheet (compact) is always two lines, at any width', () => {
    expect(rule(/^\.srow\.compact$/)).toMatch(/grid-template-areas:\s*"i t d" "\. m m"/)
    expect(rule(/^\.srow\.compact \.s-m$/)).toMatch(/grid-area:\s*m/)
  })

  it('the wait card: border --accent-attn-line, background --wait-bg, a 3 px inset band of --accent-attn-dot; .kind only under .wait-h', () => {
    const card = rule(/^\.wait$/)
    expect(card).toMatch(/border:[^;]*var\(--accent-attn-line\)/)
    expect(card).toMatch(/background:\s*var\(--wait-bg\)/)
    expect(card).toMatch(/box-shadow:\s*inset var\(--size-wait-band\) 0 0 var\(--accent-attn-dot\)/)
    expect(rule(/^\.wait-h$/)).toMatch(/color:\s*var\(--accent-attn-text\)/)
    expect(all.filter((b) => /\.kind\b/.test(b.selector)).map((b) => b.selector.replace(/^@layer components /, ''))).toEqual(['.wait-h .kind'])
  })

  it('round 1: the selected row is a rounded, inset fill with no square ring; the cell stays a visible chip on it', () => {
    const row = rule(/^\.srow$/)
    expect(row).toMatch(/margin:\s*0 var\(--row-inset\)/)
    expect(row).toMatch(/border-radius:\s*var\(--radius-row\)/)
    expect(rule(/^\.srow:not\(\.compact\):focus-visible$/)).toMatch(/outline:\s*none/)
    expect(rule(/^\.srow\.sel$/)).toMatch(/background:\s*var\(--bg-selected\)/)
    expect(rule(/^\.srow:hover \.cell, \.srow\.sel \.cell$/)).toMatch(/background:\s*var\(--bg\)/)
  })

  it('round 1: a first-level group header has more room above it than a sub-group header', () => {
    expect(rule(/^\.sgrp \+ \.sgrp \.sgh\.g1$/)).toMatch(/margin-top:\s*var\(--g1-pt\)/)
  })

  it('round 1: the project prefix of a sub-group header survives the narrow list (list.css hides .gsub there)', () => {
    expect(rule(/^\.sgh \.gsub$/)).toMatch(/display:\s*inline/)
  })

  it('round 1: narrow rows start at their sub-group header text; the panel dot is centred on the title line', () => {
    expect(rule(/@container \(max-width: 640px\) \.srow$/)).toMatch(/padding-left:\s*calc\(var\(--sp-6\) \+ var\(--size-group-indent\) - var\(--row-inset\)\)/)
    expect(rule(/^\.lp-ttl > \.sdot-w$/)).toMatch(/margin-top:\s*calc\(/)
  })

  it('round 1: a linked session (compact) keeps sp-3 / sp-4 inside its fill, no margin', () => {
    const c = rule(/^\.srow\.compact$/)
    expect(c).toMatch(/padding:\s*var\(--sp-3\) var\(--sp-4\)/)
    expect(c).toMatch(/margin:\s*(0|var\(--sp-0\))/)
    expect(rule(/@container \(max-width: 640px\) \.srow\.compact$/)).toMatch(/padding:\s*var\(--sp-3\) var\(--sp-4\)/)
  })

  it('no raw colour: every value comes from the tokens', () => {
    expect(css).not.toMatch(/#[0-9a-fA-F]{3,8}\b|rgb\(|oklch\(|hsl\(/)
  })

  it('the attention accent sits only on what waits', () => {
    for (const b of all) {
      if (!/--accent-attn-/.test(b.body)) continue
      expect(b.selector, b.selector).toMatch(/\.sess-wait|\.grp-attn|\.wait\b|\.wait-h/)
    }
  })
})
