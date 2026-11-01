// @vitest-environment node
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { blocks, propsOf, refs } from './css-parse'

const tokens = readFileSync(new URL('./tokens.css', import.meta.url), 'utf8')
const palette = readFileSync(new URL('./palette.css', import.meta.url), 'utf8')
const globals = readFileSync(new URL('./globals.css', import.meta.url), 'utf8')

/** The shadcn theme colours (Tailwind v4 names) the copied components read, plus the radius. */
const SHADCN = [
  'background', 'foreground', 'card', 'card-foreground', 'popover', 'popover-foreground',
  'primary', 'primary-foreground', 'secondary', 'secondary-foreground', 'muted', 'muted-foreground',
  'accent', 'accent-foreground', 'destructive', 'border', 'input', 'ring',
]

/** The 4 colour combinations of palette.css, and the theme-invariant :root of tokens.css. */
const combos = blocks(palette).map((b) => propsOf(palette, b.selector))
const invariant = propsOf(tokens, ':root')
const mapped = new Map([...propsOf(globals, ':root'), ...propsOf(globals, '@theme inline')])

/** Follows var() through globals.css down to the tokens; returns the token leaves. */
function leaves(name: string, seen = new Set<string>()): string[] {
  if (seen.has(name)) throw new Error(`cycle at ${name}`)
  seen.add(name)
  const v = mapped.get(name)
  if (v === undefined) return [name]
  const r = refs(v)
  if (r.length === 0) throw new Error(`${name} is a literal in globals.css (${v}): it must come from the token sheets`)
  return r.flatMap((x) => leaves(x, seen))
}

describe('theme: shadcn variables come from the token sheets', () => {
  it('reads 4 combinations (positive control)', () => {
    expect(combos).toHaveLength(4)
  })

  it.each(SHADCN)('--color-%s exists and resolves in every combination', (n) => {
    const name = `--color-${n}`
    expect(mapped.has(name), `${name} missing from globals.css`).toBe(true)
    for (const leaf of leaves(name)) {
      if (invariant.has(leaf) && !combos.some((c) => c.has(leaf))) continue
      combos.forEach((c, i) => expect(c.has(leaf), `${name} → ${leaf}: not in palette.css block ${i}`).toBe(true))
    }
  })

  it('primary and ring are ink, not the accent', () => {
    expect(mapped.get('--color-primary')).toBe('var(--btn-fill)')
    expect(mapped.get('--color-primary-foreground')).toBe('var(--btn-on)')
    expect(mapped.get('--color-ring')).toBe('var(--focus)')
  })

  it('--radius comes from a token', () => {
    expect(mapped.get('--radius')).toMatch(/var\(--radius-\d\)/)
  })

  it('keeps the status and phase tokens as named variables in every combination', () => {
    for (const v of ['--s-cours', '--s-ouvert', '--s-standby', '--s-done', '--mark', '--text-disabled'])
      for (const c of combos) expect(c.has(v), v).toBe(true)
  })
})
