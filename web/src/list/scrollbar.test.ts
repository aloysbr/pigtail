import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

const css = (f: string) => readFileSync(join(process.cwd(), 'src', f), 'utf8')

/** WebKit draws its overlay scrollbar over the rows and scrollbar-gutter does not reserve it: the list owns a classic thin one. */
describe('List scrollbar', () => {
  const list = css('list/list.css')
  it('.lscroll draws a thin classic scrollbar from a token, transparent track, rounded thumb', () => {
    expect(list).toMatch(/\.lscroll::-webkit-scrollbar \{[^}]*width: var\(--size-scrollbar-list\)/)
    expect(list).toMatch(/\.lscroll::-webkit-scrollbar-track \{[^}]*background: transparent/)
    expect(list).toMatch(/\.lscroll::-webkit-scrollbar-thumb \{[^}]*border-radius: var\(--radius-pill\)/)
    expect(list).toMatch(/\.lscroll::-webkit-scrollbar-thumb:hover \{[^}]*background/)
    expect(list).toMatch(/@supports not selector\(::-webkit-scrollbar\) \{\s*\.lscroll \{ scrollbar-width: thin; scrollbar-color: var\(--scroll-thumb\) transparent/)
  })
  it('the width is a token and the thumb colours come from theme tokens (no literal)', () => {
    expect(css('theme/tokens.css')).toMatch(/--size-scrollbar-list: 8px;/)
    const block = list.match(/\.lscroll::-webkit-scrollbar[^\n]*\n[^\n]*\n?[^\n]*/g)?.join('') ?? ''
    expect(block).not.toMatch(/#[0-9a-f]{3,6}|rgb/i)
  })
})
