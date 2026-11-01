// @vitest-environment node
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { blocks, props, refs, type Block } from './css-parse'

/** The Hybride material on the sheet, the ⌘K palette and capture, the toasts and the banner: only tokens, the titles' serif, the
 *  raised ground of the palette, and the stack of z-indexes of the current tree (the lesson « fiche sous l'en-tête collant »). */
const SRC = fileURLToPath(new URL('..', import.meta.url))
const THEME = fileURLToPath(new URL('.', import.meta.url))

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n)
    return statSync(p).isDirectory() ? files(p) : /\.css$/.test(n) ? [p] : []
  })
}
const strip = (s: string) => s.replace(/^@layer components /, '')
const all: (Block & { file: string })[] = files(SRC)
  .filter((f) => !/(tokens|palette)\.css$/.test(f))
  .flatMap((f) => blocks(readFileSync(f, 'utf8')).map((b) => ({ ...b, selector: strip(b.selector), file: relative(SRC, f) })))

const declared = new Set<string>()
for (const f of ['tokens.css', 'palette.css'])
  for (const b of blocks(readFileSync(join(THEME, f), 'utf8'))) for (const k of props(b.body).keys()) declared.add(k)

const rule = (sel: string): string => {
  const b = all.find((x) => x.selector === sel)
  if (!b) throw new Error(`no rule ${sel}`)
  return b.body
}
const z = (sel: string): number => {
  const m = /z-index:\s*(\d+)/.exec(rule(sel))
  if (!m) throw new Error(`no z-index on ${sel}`)
  return Number(m[1])
}

describe('Hybride on the sheet, palette, capture and states', () => {
  it('reads the sheets (positive control)', () => {
    expect(all.length).toBeGreaterThan(150)
    expect(declared.size).toBeGreaterThan(100)
  })

  it('the banner, the sheet (.lp-*), the palette (.pfx, .ms-*) and the toasts read only declared tokens (none removed)', () => {
    const own = (b: { selector: string }) => /(^|[ ,>])\.(d-banner|lp-[\w-]+|lpanel|pwrap|pfx|ms-[\w-]+|msheet|palette|pal-[\w-]+|toast6|toast-[\w-]+)\b/.test(b.selector)
    const picked = all.filter(own)
    expect(picked.length).toBeGreaterThan(25)
    const missing = picked.flatMap((b) => refs(b.body).filter((t) => !declared.has(t) && t !== '--toast-h').map((t) => `${b.file}: ${b.selector} → ${t}`))
    expect(missing).toEqual([])
  })

  it('the sheet title is serif (--font-title) with the titles\' letter-spacing, and its status icon centres on the 21 px line', () => {
    expect(rule('.lp-t')).toMatch(/font-family:\s*var\(--font-title\)/)
    expect(rule('.lp-t')).toMatch(/letter-spacing:\s*var\(--ls-title\)/)
    expect(rule('.d-head h1')).toMatch(/letter-spacing:\s*var\(--ls-title\)/)
    const ic = rule('.lp-ttl > .si')
    expect(ic).toMatch(/--fs-page-title/)
    expect(ic).not.toMatch(/--fs-17/)
  })

  it('the palette and the capture sheet lie on the raised ground (--surface), the hairline stays', () => {
    expect(rule('.palette')).toMatch(/background:\s*var\(--surface\)/)
    expect(rule('.msheet')).toMatch(/background:\s*var\(--surface\)/)
    expect(rule('.palette')).toMatch(/border:\s*var\(--size-bw\) solid var\(--border\)/)
  })

  it('the sheet lies on the raised ground (--surface), not on the page ground', () => {
    const own = all.find((x) => x.file.endsWith('panel/panel.css') && x.selector === '.pwrap')!.body
    expect(own).toMatch(/background:\s*var\(--surface\)/)
    expect(own).not.toMatch(/background:\s*var\(--bg\)/)
    // a chip drawn on the sheet follows the sheet's ground
    expect(rule('.cy-sw')).toMatch(/background:\s*var\(--surface\)/)
  })

  it('the banner reaches both edges of the header: negative side margins are paired with a grow (a flex-basis alone stops one gutter short)', () => {
    const b = rule('.d-banner')
    expect(b).toMatch(/margin:[^;]*calc\(-1 \* var\(--sp-6\)\)[^;]*calc\(-1 \* var\(--sp-2\)\)/)
    expect(b).toMatch(/flex:\s*1 0 100%/)
  })

  it('G1: the offline banner is neutral (--bg-3 and a hairline), never the strong colour', () => {
    const b = rule('.d-banner')
    expect(b).toMatch(/background:\s*var\(--bg-3\)/)
    expect(b).toMatch(/border-top:\s*var\(--size-bw\) solid var\(--border\)/)
    expect(b).not.toMatch(/--accent-attn|--attn|--status-|orange|amber/)
  })

  it('the stack of the current tree: sheet over the sticky header, palette over the sheet, the toast never over the palette', () => {
    const head = z('.d-head')
    const fiche = z('.app.pm-over > .pwrap')
    const palette = z('.palette')
    const toast = z('.toast6')
    expect([head, fiche, palette, toast]).toEqual([5, 6, 10, 6])
    expect(fiche, 'fiche > sticky header').toBeGreaterThan(head)
    expect(palette, 'palette > fiche').toBeGreaterThan(fiche)
    expect(toast, 'the toast sits under the palette (it lives in the list column)').toBeLessThan(palette)
  })
})
