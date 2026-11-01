// @vitest-environment node
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { blocks } from '@/theme/css-parse'

const read = (rel: string) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8')
const find = (css: string, sel: RegExp) => {
  const b = blocks(css).find((x) => sel.test(x.selector.replace(/^@layer components /, '')))
  if (!b) throw new Error(`no rule ${sel}`)
  return b.body
}

describe('sidebar structure (the browser round proves the pixels)', () => {
  const atlas = read('../list/list.css')
  const settings = read('../ui/settings.css')

  it('the shell grid has one bounded row, so the sidebar is as tall as the window and never as tall as its content', () => {
    expect(find(atlas, /^\.app$/)).toMatch(/grid-template-rows:\s*minmax\(0,\s*1fr\)/)
  })

  it('the area list scrolls inside the sidebar; the foot stays out of that scroll', () => {
    expect(find(atlas, /^\.side$/)).toMatch(/overflow(-y)?:\s*hidden/)
    const nav = find(atlas, /^\.nav$/)
    expect(nav).toMatch(/flex:\s*1 1 auto/)
    expect(nav).toMatch(/min-height:\s*0/)
    expect(nav).toMatch(/overflow-y:\s*auto/)
    const foot = find(settings, /^\.side-foot$/)
    expect(foot).not.toMatch(/sticky/)
    expect(foot).toMatch(/flex:\s*none/)
  })
})

describe('sidebar focus ring is never clipped by the scrolling nav', () => {
  const atlas = read('../list/list.css')
  const tokens = read('../theme/tokens.css')
  const globals = read('../theme/globals.css')
  const px = (css: string, name: string) => Number(new RegExp(`--${name}:\\s*(\\d+)px`).exec(css)?.[1])

  it('.nav keeps a gutter at least as wide as the global ring (thickness + offset), cancelled by an equal negative margin', () => {
    const ring = find(globals, /(^|\s):focus-visible$/)
    const thick = px(tokens, 'size-focus')
    const offset = Number(/outline-offset:\s*(\d+)px/.exec(ring)?.[1])
    const nav = find(atlas, /^\.nav$/)
    const pad = /padding:\s*var\(--sp-(\d)\)/.exec(nav)
    expect(pad, 'nav padding uses a spacing token').not.toBeNull()
    expect(px(tokens, `sp-${pad![1]}`)).toBeGreaterThanOrEqual(thick + offset)
    expect(nav).toMatch(new RegExp(`margin:\\s*calc\\(-1 \\* var\\(--sp-${pad![1]}\\)\\)`))
  })
})

describe('Réglages theme cards keep a visible focus ring', () => {
  const settings = read('../ui/settings.css')
  it('the pressed card is not stripped of its outline (its border is 1 px, not a ring)', () => {
    const b = blocks(settings).find((x) => /\.thcard\[aria-pressed="true"\]:focus-visible$/.test(x.selector))
    expect(b?.body ?? '').not.toMatch(/outline:\s*none/)
    expect(b?.body ?? '').toMatch(/outline:\s*var\(--size-focus\) solid/)
  })
})
