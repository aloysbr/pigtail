// @vitest-environment node
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { blocks } from './css-parse'

const SRC = fileURLToPath(new URL('..', import.meta.url))

function cssFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n)
    return statSync(p).isDirectory() ? cssFiles(p) : p.endsWith('.css') ? [p] : []
  })
}

describe('focus ring: keyboard only (a past correction: no ring after a mouse click)', () => {
  const files = cssFiles(SRC)

  it('reads the stylesheets (positive control)', () => {
    expect(files.some((f) => f.endsWith('globals.css'))).toBe(true)
  })

  it('draws the ring on :focus-visible, 2 px from --size-focus', () => {
    const g = readFileSync(join(SRC, 'theme', 'globals.css'), 'utf8')
    const ring = blocks(g).find((b) => /:focus-visible/.test(b.selector) && /outline/.test(b.body))
    expect(ring?.body).toMatch(/var\(--size-focus\)/)
  })

  it.each(cssFiles(SRC).map((f) => [f.slice(SRC.length)]))('%s: every :focus-visible rule draws in --focus (ink), never a literal nor the accent', (rel) => {
    const css = readFileSync(join(SRC, rel), 'utf8')
    const bad = blocks(css).filter((b) => /:focus-visible/.test(b.selector) && /outline|box-shadow|background|border-color/.test(b.body) && /var\(--|#|rgb|oklch/.test(b.body))
    for (const b of bad) {
      const colours = [...b.body.matchAll(/(?:outline|box-shadow|background|border-color)\s*:([^;]*)/g)].map((m) => m[1])
      for (const v of colours) {
        if (/^\s*(none|transparent|0)\s*$/.test(v) || !/var\(--(?!size-|sp-|bw)|#|rgb|oklch/.test(v)) continue
        expect(v, `${rel} ${b.selector}`).toMatch(/var\(--focus\)/)
      }
    }
  })

  it.each(cssFiles(SRC).map((f) => [f.slice(SRC.length)]))('%s has no :focus or :focus-within rule that shows a ring', (rel) => {
    const css = readFileSync(join(SRC, rel), 'utf8')
    const bad = blocks(css).filter((b) => /:focus(?!-visible)/.test(b.selector) && /outline|box-shadow|ring|border/.test(b.body))
    expect(bad.map((b) => b.selector)).toEqual([])
  })
})
