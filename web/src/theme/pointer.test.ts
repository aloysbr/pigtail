// @vitest-environment node
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { blocks } from './css-parse'

const SRC = fileURLToPath(new URL('..', import.meta.url))
function files(dir: string): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n)
    return statSync(p).isDirectory() ? files(p) : p.endsWith('.css') ? [p] : []
  })
}
const COARSE = '@layer components @media (pointer: coarse) '
const pointerCss = () => readFileSync(join(SRC, 'theme', 'pointer.css'), 'utf8')
const coarse = () => blocks(pointerCss()).filter((b) => b.selector.startsWith(COARSE)).map((b) => ({ sels: b.selector.slice(COARSE.length).split(',').map((s) => s.trim()), body: b.body }))
/** The bodies of the coarse rules that list this exact selector. */
const rules = (sel: string) => coarse().filter((r) => r.sels.includes(sel)).map((r) => r.body)

const TOKENS = readFileSync(join(SRC, 'theme', 'tokens.css'), 'utf8')
/** Evaluates a calc()/var() length to px with the token values (so the test checks the resulting size, not the wording). */
function px(expr: string): number {
  let e = expr.trim()
  for (let i = 0; i < 8; i++) {
    e = e.replace(/var\((--[\w-]+)\)/g, (_, name: string) => {
      const m = new RegExp(`${name}:\\s*([^;]+);`).exec(TOKENS)
      if (!m) throw new Error(`unknown token ${name}`)
      return `(${m[1]})`
    })
  }
  e = e.replace(/calc/g, '').replace(/px/g, '')
  if (!/^[\d\s()+\-*/.]+$/.test(e)) throw new Error(`cannot evaluate ${expr} -> ${e}`)
  return Function(`return (${e})`)() as number
}
/** The four insets [top, right, bottom, left] of the ::after rule of a selector. */
function inset(sel: string): number[] {
  const body = rules(`${sel}::after`).join(';')
  const m = /inset:\s*([^;]+)/.exec(body)
  if (!m) throw new Error(`no inset for ${sel}`)
  const parts: string[] = []
  let depth = 0
  let cur = ''
  for (const ch of m[1].trim()) {
    if (ch === '(') depth++
    if (ch === ')') depth--
    if (/\s/.test(ch) && depth === 0) {
      if (cur) parts.push(cur)
      cur = ''
    } else cur += ch
  }
  if (cur) parts.push(cur)
  const v = parts.map(px)
  if (v.length === 2) return [v[0], v[1], v[0], v[1]]
  if (v.length === 4) return v
  throw new Error(`inset of ${sel}: ${m[1]}`)
}
const decl = (sel: string, prop: string): string | undefined => new RegExp(`(?:^|;|\\s)${prop}:\\s*([^;]+)`).exec(rules(sel).join(';'))?.[1]

describe('the resulting hit areas measure 44 px (inset compensates the 1 px border)', () => {
  const BW = px('var(--size-bw)')
  const TOUCH = px('var(--size-touch)')
  // buttons are --size-row high with a border; the absolute ::after sits on the padding box
  it.each(['.ghost', '.fadd', '.pill', '.btnl', '.fsearch'])('%s: zone is at least 44 high and as wide as the border box', (s) => {
    const [t, r, b, l] = inset(s)
    const padH = px('var(--size-row)') - 2 * BW
    expect(padH - t - b, s).toBeGreaterThanOrEqual(TOUCH)
    // sideways: exactly the border box, never spilling onto a neighbour
    expect(-l, s).toBe(BW)
    expect(-r, s).toBe(BW)
    expect(px(decl(s, 'min-width') ?? '0'), `${s} min-width`).toBeGreaterThanOrEqual(TOUCH)
  })

  it('the × reaches 44 x 44 by its ::after, the chip keeps its base size, the overflow stays inside its own chip sideways', () => {
    expect(decl('.fx', 'min-width')).toBeUndefined()
    const [t, r, b, l] = inset('.fx')
    const box = px('var(--size-target)')
    expect(box - t - b).toBeGreaterThanOrEqual(TOUCH)
    expect(box - l - r).toBeGreaterThanOrEqual(TOUCH)
    // right: no further than the chip's own border + padding (css: .fchip padding-right --sp-1)
    expect(-r).toBeLessThanOrEqual(px('var(--sp-1)') + BW)
  })

  it('when the header wraps, no zone of a row reaches the top of the next row zones (x of the chips included), with rounding room', () => {
    const ROW = px('var(--size-row)')
    // how far each control's zone extends past its own 32 px row slot, up and down
    const over = (s: string): { up: number; down: number } => {
      const [t, , b] = inset(s)
      // a button's ::after sits on its padding box (1 px border inside the slot); the x is a smaller button centred in its chip
      const lost = s === '.fx' ? (ROW - px('var(--size-target)')) / 2 : BW
      return { up: -t - lost, down: -b - lost }
    }
    const zones = ['.ghost', '.fadd', '.pill', '.btnl', '.fsearch', '.fx'].map(over)
    const down = Math.max(...zones.map((z) => z.down))
    const up = Math.max(...zones.map((z) => z.up))
    expect(down).toBeGreaterThan(0) // positive control: the evaluator read something
    // every header row gap: the wrapped one-line header, the two lines of level 2 (.d-head.h2l beats .d-head by specificity,
    // so it needs its own coarse rule) and a wrapped line 1 or 2
    for (const sel of ['.d-head', '.d-head.h2l', '.h-l1', '.h-l2']) {
      const gap = px(decl(sel, 'row-gap') ?? '0')
      expect(gap, sel).toBeGreaterThanOrEqual(down + up + 2 * px('var(--sp-1)') / 2)
      // the zones never overlap: previous row's lowest zone edge is above the next row's highest
      expect(ROW + down, sel).toBeLessThanOrEqual(ROW + gap - up)
    }
    // the coarse rule wins over list.css at equal specificity because pointer.css is imported after it
    const g = readFileSync(join(SRC, 'theme', 'globals.css'), 'utf8')
    expect(g.indexOf('./pointer.css')).toBeGreaterThan(g.indexOf('../list/list.css'))
    expect(g.indexOf('../list/list.css')).toBeGreaterThanOrEqual(0)
  })

  it('the session panel link sits in its own positioned 44 px row', () => {
    const body = rules('.lp-link').join(';')
    expect(body).toMatch(/position:\s*relative/)
    expect(body).toMatch(/min-height:\s*var\(--size-touch\)/)
  })

  it('lot 5 round 1: the session panel link is itself 44 high, the panel icon buttons never shrink under 44, the Sessions pills and filter are 44 high at 390', () => {
    expect(rules('.spanel .lnk').join(';')).toMatch(/min-height:\s*var\(--size-touch\)/)
    expect(rules('.ib').join(';')).toMatch(/flex:\s*none/)
    for (const s of ['.s-head .pill', '.s-head .fsearch']) expect(rules(s).join(';'), s).toMatch(/min-height:\s*var\(--size-touch\)/)
    // the pill and the field are 44 themselves: their enlarged ::after zone goes (it would reach 58 high)
    for (const s of ['.s-head .pill::after', '.s-head .fsearch::after']) expect(rules(s).join(';'), s).toMatch(/content:\s*none/)
  })

  it('a link of the sheet covers its 44 px row', () => {
    expect(decl('.links li', 'position')).toBe('relative')
    expect(decl('.lnk::after', 'position')).toBe('absolute')
    const [t, r, b, l] = inset('.lnk')
    expect([t, r, l]).toEqual([0, 0, 0])
    expect(-b).toBe(BW) // the row's bottom rule is inside its 44 px
  })
})

describe('finger targets live in one pointer: coarse block (D3, D11)', () => {
  it('reads the sheets (positive control)', () => {
    expect(files(SRC).length).toBeGreaterThan(4)
    expect(coarse().length).toBeGreaterThan(5)
  })

  it('exactly one coarse media query, in pointer.css', () => {
    const hits = files(SRC).flatMap((f) => (readFileSync(f, 'utf8').match(/@media\s*\(\s*pointer:\s*coarse\s*\)/g) ?? []).map(() => f.slice(SRC.length)))
    expect(hits).toEqual(['theme/pointer.css'])
  })

  it('coarse rows are 44 px', () => {
    for (const s of ['.lr6', '.gbtn', '.sline', '.more', '.nav-i', '.gmenu button', '.pal-i', '.links li', '.thcard', '.segd button', '.ghost.sf-set', '.ql-nav', '.hv-more', '.ql-z', '.srow'])
      expect(rules(s).join(';'), s).toMatch(/min-height:\s*var\(--size-touch\)/)
  })

  it('the preview side buttons are 44 x 44 and stay centred on the stage', () => {
    const b = rules('.ql-nav').join(';')
    expect(b).toMatch(/width:\s*var\(--size-touch\)/)
    expect(b).toMatch(/height:\s*var\(--size-touch\)/)
    expect(b).toMatch(/margin-top:\s*calc\(var\(--size-touch\) \/ -2\)/)
  })

  it('coarse hit areas are 44 px', () => {
    expect(rules('.ib').join(';')).toMatch(/width:\s*var\(--size-touch\)/)
    expect(rules('.ib').join(';')).toMatch(/height:\s*var\(--size-touch\)/)
    for (const s of ['.ghost', '.fadd', '.pill', '.fsearch', '.btnl', '.fx'])
      expect(rules(`${s}::after`).join(';'), s).toMatch(/inset:[^;]*--size-touch/)
  })

  it('a narrow button is 44 px wide itself: no hit area spills sideways onto a neighbour', () => {
    for (const s of ['.ghost', '.fadd', '.pill', '.btnl', '.fsearch']) expect(rules(s).join(';'), s).toMatch(/min-width:\s*var\(--size-touch\)/)
    for (const r of coarse().filter((r) => r.sels.some((x) => x.endsWith('::after'))))
      expect(r.body, r.sels.join()).not.toMatch(/inset-inline/)
  })

  it('.fchip gets no ::after (it would paint over the remove button)', () => {
    expect(coarse().some((r) => r.sels.some((s) => s.startsWith('.fchip') && s.includes('::after')))).toBe(false)
    expect(rules('.fchip').join(';')).toMatch(/position:\s*relative/)
    expect(rules('.fchip').join(';')).toMatch(/min-height/)
  })

  it('.fsearch::after sits behind the input', () => {
    expect(rules('.fsearch::after').join(';')).toMatch(/z-index:\s*-1/)
    expect(rules('.fsearch').join(';')).toMatch(/z-index:\s*0/)
  })

  it('the panel head and the detail head reach 44 px, the close button is nudged', () => {
    expect(rules('.d-head').join(';')).toMatch(/min-height:\s*calc\(var\(--size-touch\) \+ 2 \* var\(--sp-2\)\)/)
    expect(rules('.lp-h').join(';')).toMatch(/min-height:\s*var\(--size-touch\)/)
  })

  it('keyboard hints are hidden', () => {
    for (const s of ['kbd', '.kbdbar', '.kbhint']) expect(rules(s).join(';'), s).toMatch(/display:\s*none/)
  })

  it('the key inside a sentence stays: the Réglages footer reads « aussi dans ⌘K « thème » » at every width', () => {
    expect(rules('.pop-foot kbd').join(';')).toMatch(/display:\s*inline-flex/)
  })
})
