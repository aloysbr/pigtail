// @vitest-environment node
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { blocks } from './css-parse'

const SRC = fileURLToPath(new URL('..', import.meta.url))
const css = readFileSync(join(SRC, 'list', 'list.css'), 'utf8')
const all = blocks(css)

/** The body of the first rule whose selector (at-rule prefixes included) matches. */
function rule(sel: RegExp): string {
  const b = all.find((x) => sel.test(x.selector.replace(/^@layer components /, '')))
  if (!b) throw new Error(`no rule ${sel}`)
  return b.body
}

describe('list.css static guards (jsdom computes no layout; the browser round proves the pixels)', () => {
  it('reads the sheet (positive control)', () => {
    expect(all.length).toBeGreaterThan(80)
  })

  it('B3: a pressed ghost button takes --bg-active', () => {
    expect(rule(/\.ghost\[aria-pressed="true"\]$/)).toMatch(/background:\s*var\(--bg-active\)/)
  })

  it('C3: in the narrow list the rank keeps its own column before the age, so every age lines up', () => {
    expect(rule(/@container.*\.lr6, \.no-theme \.lr6$/)).toMatch(/grid-template-columns:\s*var\(--size-icon\) minmax\(0, 1fr\) var\(--size-col-rank\) var\(--size-col-age\)/)
    expect(rule(/@container.*\.lr6 \.rk6$/)).toMatch(/order:\s*8/)
    expect(rule(/@container.*\.lr6 \.lr-d$/)).toMatch(/order:\s*9/)
    expect(rule(/@container.*\.lr6 \.lr-st/)).not.toMatch(/rk6/)
  })

  it('the floating sidebar veil covers the whole window, header included', () => {
    expect(rule(/^\.side-scrim$/)).toMatch(/position:\s*fixed/)
  })

  it('one contour on the focused search field: the ring overlaps the border', () => {
    expect(rule(/^\.fsearch:has\(input:focus-visible\)$/)).toMatch(/outline-offset:\s*calc\(-1 \* var\(--size-bw\)\)/)
  })

  it('C4: the group name keeps priority; the sub-label then the status summary give way first', () => {
    expect(rule(/^\.gname$/)).toMatch(/min-width:\s*0/)
    expect(rule(/^\.gsum$/)).toMatch(/flex:\s*none/)
    expect(rule(/@container.*\.gsub/)).toMatch(/display:\s*none/)
    expect(rule(/@container \(max-width: 480px\).*\.gsum/)).toMatch(/display:\s*none/)
  })

  it('C1: the chip text ellipsizes in its own element and the line box covers the descenders', () => {
    expect(rule(/^\.chip-t$/)).toMatch(/text-overflow:\s*ellipsis/)
    expect(rule(/^\.chip$/)).toMatch(/line-height:\s*var\(--size-sdot\)/)
  })

  it('C9: on a phone the keys are hidden (t10 .mobile kbd)', () => {
    expect(rule(/^\.phone kbd/)).toMatch(/display:\s*none/)
  })

  it('M2: the summary icons are full size and the muted ones keep a readable colour', () => {
    expect(rule(/^\.gs \.si$/)).toMatch(/width:\s*var\(--size-icon\)/)
    expect(rule(/\.gsum \.si-dropped/)).toMatch(/color:\s*var\(--text-2\)/)
  })

  it('C5: the Filtre menu has a bordered, opaque, bounded panel; counts and keys are fixed columns', () => {
    const m = rule(/^\.gmenu$/)
    expect(m).toMatch(/border:/)
    expect(m).toMatch(/max-width:\s*calc\(100vw/)
    expect(rule(/^\.gm-n$/)).toMatch(/text-align:\s*right/)
    expect(rule(/^\.gm-k$/)).toMatch(/min-width:\s*var\(--size-kbd\)/)
  })

  it('layout: the list scrolls in its own box, the footer is a sibling below it', () => {
    expect(rule(/^\.lscroll$/)).toMatch(/overflow-y:\s*auto/)
    expect(rule(/^\.d-tools$/)).toMatch(/flex-wrap:\s*nowrap|white-space:\s*nowrap/)
  })

  it('D4: in the narrow list the rows still indent under their project (and status lines under it)', () => {
    expect(rule(/@container.*\.sub \.lr6/)).toMatch(/padding-left:\s*calc\(var\(--sp-6\) \+ var\(--size-group-indent\) \* 2\)/)
    expect(rule(/@container.*\.sub \.sub \.lr6/)).toMatch(/\* 3\)/)
  })

  it('D1 (t10): the search field rests at --size-search-c and widens to --size-search-w on focus; siblings reflow (no overlay)', () => {
    expect(readFileSync(join(SRC, 'theme', 'tokens.css'), 'utf8')).toMatch(/--size-search-c:\s*96px/)
    expect(rule(/^\.d-head \.fsearch input$/)).toMatch(/width:\s*var\(--size-search-c\)/)
    // the wide state is a class held by the component (deferred past a pointer press), not :focus-within
    expect(all.filter((b) => /\.fsearch:focus-within/.test(b.selector)).map((b) => b.selector)).toEqual([])
    expect(rule(/^\.d-head \.fsearch\.wide input$/)).toMatch(/width:\s*var\(--size-search-w\)/)
    for (const b of all.filter((x) => /\.fsearch/.test(x.selector))) expect(b.body).not.toMatch(/margin-right:\s*calc/)
  })

  it('D6 (t10) then D7: header levels fold by class; level 2 (always .h2l) gives the field the rest of line 2, tight drops the counts', () => {
    // the one-line hd-2 rules (field at width 0, widening on focus) are gone: level 2 is always the two-line header
    expect(all.filter((b) => /\.d-head\.hd-2 \.fsearch/.test(b.selector)).map((b) => b.selector)).toEqual([])
    expect(rule(/^\.d-head\.h2l \.h-l2 \.fsearch input$/)).toMatch(/width:\s*auto/)
    expect(rule(/^\.d-head\.tight \.pill-n$/)).toMatch(/display:\s*none/)
  })

  it('D5: the solid button of the empty states', () => {
    const b = rule(/^\.btnl$/)
    expect(b).toMatch(/border:\s*var\(--size-bw\) solid var\(--border-strong\)/)
    expect(b).not.toMatch(/dashed/)
  })

  it('under 640 the row has two lines (D4, D6): step and chip on a meta line under the title', () => {
    const q = '@container (max-width: 640px) '
    const at = (sel: string) => {
      const b = all.find((x) => x.selector === q + sel || x.selector === '@layer components ' + q + sel)
      if (!b) throw new Error(`no rule ${sel} under 640`)
      return b.body
    }
    const row = at('.lr6, .no-theme .lr6')
    expect(row).toMatch(/grid-template-areas:\s*"i t k d" ". m m m"/)
    expect(row).toMatch(/row-gap:\s*var\(--row-meta-gap\)/)
    expect(at('.lr6.no-m')).toMatch(/grid-template-areas:\s*"i t k d"\s*;/)
    const m = at('.lr6 .lr-m')
    expect(m).toMatch(/display:\s*flex/)
    expect(m).toMatch(/grid-area:\s*m/)
    // the step and the chip are shown again (inside .lr-m); the long title stays hidden
    const hidden = all.filter((b) => b.selector.includes(q) && /display:\s*none/.test(b.body)).map((b) => b.selector)
    expect(hidden.some((s) => /\.lr-st|\.lr-th/.test(s))).toBe(false)
    expect(hidden.some((s) => /\.lr-full/.test(s))).toBe(true)
    // outside the query .lr-m lays its children in the six wide columns
    expect(rule(/^\.lr-m$/)).toMatch(/display:\s*contents/)
  })

  it('two-line header is stable (D7, Q3 A): two fixed lines, line 2 never wraps', () => {
    const h = rule(/^\.d-head\.h2l$/)
    expect(h).toMatch(/flex-direction:\s*column/)
    expect(h).toMatch(/flex-wrap:\s*nowrap/)
    expect(rule(/^\.h-l1, \.h-l2$/)).toMatch(/min-height:\s*var\(--size-control\)/)
    expect(rule(/^\.h-l1, \.h-l2$/)).toMatch(/flex-wrap:\s*nowrap/)
    expect(rule(/^\.h-chips$/)).toMatch(/flex-wrap:\s*nowrap/)
    const f = rule(/^\.h-l2 \.fsearch$/)
    expect(f).toMatch(/flex:\s*1 1 auto/)
    expect(f).toMatch(/min-width:\s*0/)
  })

  it('footer tightens under 720 of list: .lmain is a named container, the footer query aims at it', () => {
    expect(rule(/^\.lmain$/)).toMatch(/container:\s*lmain \/ inline-size/)
    expect(rule(/^@container lmain \(max-width: 720px\) \.kbdbar$/)).toMatch(/gap:\s*var\(--sp-4\)/)
    expect(rule(/^@container lmain \(max-width: 720px\) \.kb-items$/)).toMatch(/column-gap:\s*var\(--sp-4\)/)
    expect(rule(/^@container lmain \(max-width: 720px\) \.kb$/)).toMatch(/gap:\s*var\(--sp-2\)/)
  })

  it('footer is one line at every width: the bar never wraps, .kb-items clips what wraps (whole items), nothing hides the « , » toggle', () => {
    expect(rule(/^\.kbdbar$/)).toMatch(/flex-wrap:\s*nowrap/)
    expect(rule(/^\.kb-items$/)).toMatch(/height:\s*var\(--size-target\)/)
    expect(rule(/^\.kb-items$/)).toMatch(/overflow:\s*hidden/)
    // « , » is the only way to hide the bar by pointer: no container rule may fold it
    expect(all.filter((b) => /kbhint-in/.test(b.selector) && /display:\s*none/.test(b.body)).map((b) => b.selector)).toEqual([])
  })

  it('.h-l1 never wraps (D7; verify t1: 3 header lines at 440 px of list): the Scopes label ellipsizes, the keys fold under 520', () => {
    // no rule lets line 1 wrap
    const l1 = all.filter((b) => /\.h-l1/.test(b.selector))
    expect(l1.length).toBeGreaterThan(0)
    for (const b of l1) expect(b.body, b.selector).not.toMatch(/flex-wrap:\s*wrap/)
    // the Scopes menu is the one item of line 1 that gives way, its label ellipsized; the others keep their size
    const m = rule(/^\.h-l1 > \.gmenu-w$/)
    expect(m).toMatch(/flex:\s*0 1 auto/)
    expect(m).toMatch(/min-width:\s*0/)
    expect(rule(/^\.h-l1 > \.gmenu-w > \.ghost$/)).toMatch(/min-width:\s*var\(--size-touch\)/)
    expect(rule(/^\.h-l1 \.gm-l$/)).toMatch(/text-overflow:\s*ellipsis/)
    expect(rule(/^\.h-l1 > :is\(h1, button, \.d-tools\)$/)).toMatch(/flex:\s*none/)
    // under 520 px of list the printed keys of line 1 (⌘B, v) fold; their buttons keep their title and shortcut
    expect(rule(/^@container lmain \(max-width: 520px\) \.h-l1 kbd$/)).toMatch(/display:\s*none/)
  })

  it('verify lot 2: the narrow Lancer overlay leaves the age column and the focus ring uncovered', () => {
    const body = rule(/^@container \(max-width: 640px\) \.lr6 \.lr-l$/)
    expect(body).toMatch(/right:\s*calc\(var\(--sp-6\) \+ var\(--size-col-age\) \+ var\(--sp-4\)\)/)
    expect(body).toMatch(/top:\s*var\(--size-focus\)/)
    expect(body).toMatch(/bottom:\s*var\(--size-focus\)/)
  })
})
