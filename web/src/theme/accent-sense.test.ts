// @vitest-environment node
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { blocks } from './css-parse'

/** The rule of the DS Hybride (ADR 2026-10-26): ONE strong colour, reserved to what awaits an answer; ink for link,
 *  selection, focus and the primary button. Executable here: the legacy blue is gone, and --accent-attn-* is cited only
 *  by the selectors listed below (tasks 15-17 add theirs, each one a surface that shows a wait). */
export const ATTN_SELECTORS: string[] = [
  // T8: the theme card's mock draws a miniature « attend ta réponse » dot and chip in the accent OF THAT THEME (the only thing telling Terra from Ocean at a glance); decorative, aria-hidden
  '.tp-dot',
  '.tp-chip',
  '.tp-chip::after',
  // ⌘K: the swatch of a theme command (t3 .thsw) wears the same miniature accent dot, decorative, aria-hidden
  '.thsw b',
  // T15: the Sessions view, only on what waits for an answer: the type chip and the dot of a waiting row, the « Attend ta réponse » count
  '.sess-wait .chip-attn',
  '.sess-wait .dot::before',
  '.grp-attn .cnt',
  // T16: the session panel's read-only wait card (t3 .wait, .wait-h, .wait-h .kind)
  '.wait',
  '.wait-h',
  '.wait-h .kind',
  // T15 (SDK lot): the state chip of a SDK session that waits for an answer (« Attend ta réponse », D18)
  '.sdk-st[data-state="waiting"]',
]

const SRC = fileURLToPath(new URL('..', import.meta.url))
const TOKEN_SHEETS = ['theme/tokens.css', 'theme/palette.css']

function cssFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n)
    return statSync(p).isDirectory() ? cssFiles(p) : p.endsWith('.css') ? [p] : []
  })
}
const sheets = cssFiles(SRC)
  .map((f) => ({ rel: relative(SRC, f), css: readFileSync(f, 'utf8') }))
  .filter((s) => !TOKEN_SHEETS.includes(s.rel))

const LEGACY = /var\(\s*--(accent|accent-soft|accent-text|accent-fill|accent-on|st-blue|st-violet|st-orange[\w-]*)\s*\)/

describe('one strong colour, reserved to what awaits an answer', () => {
  it('reads the component sheets and globals.css (positive control)', () => {
    expect(sheets.some((s) => s.rel === 'theme/globals.css')).toBe(true)
    expect(sheets.length).toBeGreaterThan(5)
  })

  it.each(sheets.map((s) => [s.rel]))('%s cites no legacy accent (--accent, --accent-soft/-text/-fill, --st-blue, --st-violet, --st-orange*)', (rel) => {
    const css = sheets.find((s) => s.rel === rel)!.css
    const bad = blocks(css).filter((b) => LEGACY.test(b.body)).map((b) => b.selector)
    expect(bad).toEqual([])
  })

  it('--accent-attn-* is cited only in a rule whose selector is in ATTN_SELECTORS', () => {
    const bad = sheets.flatMap((s) =>
      blocks(s.css)
        .filter((b) => /var\(\s*--accent-attn-/.test(b.body) && !ATTN_SELECTORS.includes(b.selector.replace(/^@layer \w+ /, '')))
        .map((b) => `${s.rel}: ${b.selector}`),
    )
    expect(bad).toEqual([])
  })
})
