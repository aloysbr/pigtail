// @vitest-environment node
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { blocks } from '@/theme/css-parse'

describe('layout.css static guards (the browser round proves the pixels)', () => {
  const css = readFileSync(fileURLToPath(new URL('./layout.css', import.meta.url)), 'utf8')
  const all = blocks(css)

  it('reads the sheet (positive control)', () => {
    expect(all.length).toBeGreaterThan(15)
  })

  const rule = (sel: RegExp) => {
    const b = all.find((x) => sel.test(x.selector.replace(/^@layer components /, '')))
    if (!b) throw new Error(`no rule ${sel}`)
    return b.body
  }

  it('the palette never gets wider than the screen minus a 16 px gutter on each side', () => {
    expect(rule(/^\.palette$/)).toMatch(/max-width:\s*calc\(100vw - 2 \* var\(--sp-6\)\)/)
  })

  it('the wide breakpoint widens the sheet; over and page modes have their own rules', () => {
    expect(rule(/\.bp-wide\.pm-side/)).toMatch(/var\(--size-panel-xl\)/)
    expect(rule(/\.pm-over > \.pwrap$/)).toMatch(/position:\s*absolute/)
    expect(rule(/\.pm-page > \.lmain$/)).toMatch(/display:\s*none/)
  })

  it('G9: the sheet comes in over 120 ms, without motion under prefers-reduced-motion', () => {
    expect(rule(/^\.pwrap$/)).toMatch(/var\(--dur-fast\)/)
    expect(rule(/prefers-reduced-motion: reduce.*\.pwrap/)).toMatch(/animation:\s*none/)
  })

  it('separator is grabbable', () => {
    const h = rule(/^\.split-h$/)
    expect(h).toMatch(/touch-action:\s*none/)
    expect(h).toMatch(/cursor:\s*col-resize/)
    expect(h).toMatch(/width:\s*var\(--size-split-hit\)/)
    // hover, keyboard focus and a drag light the 2 px line in ink (the accent is reserved to what awaits an answer)
    for (const s of [/\.split-h:hover::after/, /\.split-h:focus-visible::after/, /\.split-h\.drag::after/]) expect(rule(s)).toMatch(/var\(--focus\)/)
    expect(rule(/^\.split-tip$/)).toMatch(/pointer-events:\s*none/)
    expect(rule(/^\.split-tip$/)).toMatch(/max-width:\s*var\(--size-split-tip\)/)
    const r = rule(/^\.app\.resizing$/)
    expect(r).toMatch(/cursor:\s*col-resize/)
    expect(r).toMatch(/user-select:\s*none/)
  })

  it('R3: the separator bubble covers no header control and is never cut', () => {
    const tokens = readFileSync(fileURLToPath(new URL('../theme/tokens.css', import.meta.url)), 'utf8')
    const tok = (n: string) => {
      const m = new RegExp(`${n}:\\s*([\\d.]+)px;`).exec(tokens)
      if (!m) throw new Error(`no token ${n}`)
      return Number(m[1])
    }
    const tip = rule(/^\.split-tip$/)
    // anchored at the bottom of the separator, out of the list's header band
    expect(tip).toMatch(/(^|;|\s)bottom:/)
    expect(tip).not.toMatch(/(^|;|\s)top:/)
    // the sidebar's bubble opens over the sidebar, the sheet's over the sheet: never over the list
    expect(rule(/^\.sp-side \.split-tip$/)).toMatch(/right:\s*calc\(100% \+ var\(--sp-3\)\)/)
    expect(rule(/^\.sp-panel \.split-tip$/)).toMatch(/left:\s*calc\(100% \+ var\(--sp-3\)\)/)
    // wraps instead of being cut, and fits a sidebar at its minimum (minus the gap to the line, half the hit area and a margin)
    expect(tip).not.toMatch(/white-space:\s*nowrap/)
    expect(tip).not.toMatch(/overflow:\s*hidden/)
    expect(tok('--size-split-tip')).toBeLessThanOrEqual(tok('--size-sidebar-min') - 2 * tok('--sp-3') - tok('--size-split-hit') / 2)
  })

  it('D3: the sheet over the list covers its sticky header; the separator stays above it, under the floating layers', () => {
    const list = blocks(readFileSync(fileURLToPath(new URL('../list/list.css', import.meta.url)), 'utf8'))
    const z = (bs: typeof all, sel: RegExp) => {
      const b = bs.find((x) => sel.test(x.selector.replace(/^@layer components /, '')))
      if (!b) throw new Error(`no rule ${sel}`)
      const m = /z-index:\s*(-?\d+)/.exec(b.body)
      if (!m) throw new Error(`no z-index in ${sel}`)
      return Number(m[1])
    }
    const head = z(list, /^\.d-head$/)
    const sheet = z(all, /\.pm-over > \.pwrap$/)
    const split = z(all, /^\.split-h$/)
    expect(sheet).toBeGreaterThan(head)
    expect(split).toBeGreaterThan(sheet)
    // the floating sidebar, its scrim, the palette's scrim and the palette stay above the sheet and its separator
    for (const s of [z(list, /^\.side-scrim$/), z(list, /^\.side\.float$/), z(all, /^\.scrim$/), z(all, /^\.palette$/)]) expect(s).toBeGreaterThan(split)
    expect(z(all, /^\.palette$/)).toBeGreaterThan(z(all, /^\.scrim$/))
    expect(z(list, /^\.side\.float$/)).toBeGreaterThan(z(list, /^\.side-scrim$/))
  })

  it('separator hit area is 24 px on touch', () => {
    const p = blocks(readFileSync(fileURLToPath(new URL('../theme/pointer.css', import.meta.url)), 'utf8'))
    const coarse = p.filter((b) => b.selector.includes('@media (pointer: coarse)'))
    const sels = (b: { selector: string }) => b.selector.replace(/^.*@media \(pointer: coarse\) /, '').split(',').map((s) => s.trim())
    const hit = coarse.filter((b) => sels(b).includes('.split-h'))
    expect(hit.map((b) => b.body).join(';')).toMatch(/--size-split-hit:\s*var\(--size-target\)/)
    // the width stays var(--size-split-hit): redefining the token keeps the zone centred on the line
    for (const b of hit) expect(b.body).not.toMatch(/(^|;|\s)width:/)
    expect(coarse.filter((b) => sels(b).includes('.split-tip')).map((b) => b.body).join(';')).toMatch(/display:\s*none/)
  })

  it('G3 then G1: the banner is a neutral strip across the header (--bg-3, --border hairline, written text), 12 px text', () => {
    const b = rule(/^\.d-banner$/)
    expect(b).toMatch(/flex:\s*1 0 100%/)
    expect(b).toMatch(/background:\s*var\(--bg-3\)/)
    expect(b).toMatch(/border-top:[^;]*var\(--border\)/)
    expect(b).not.toMatch(/--st-orange/)
    expect(b).toMatch(/font-size:\s*var\(--fs-12\)/)
  })

  it('the banner icon takes the DS alert colour (--status-late), never a literal colour; the strip itself stays neutral', () => {
    const ico = rule(/^\.d-banner-ico$/)
    expect(ico).toMatch(/color:\s*var\(--status-late\)/)
    expect(ico).not.toMatch(/#[0-9a-f]{3,8}|rgb|oklch|hsl/i)
  })

  it('verify lot 2: the title always keeps its own 56 px row (banner, chip, wrapped tools): its y never moves; the banner sits on the row below', () => {
    const h = rule(/^\.d-head > h1$/)
    expect(h).toMatch(/display:\s*flex/)
    expect(h).toMatch(/align-items:\s*center/)
    expect(h).toMatch(/min-height:\s*calc\(var\(--head-h\) - 2 \* var\(--sp-2\)\)/)
    expect(rule(/^\.d-banner$/)).toMatch(/flex:\s*1 0 100%/)
  })
})
