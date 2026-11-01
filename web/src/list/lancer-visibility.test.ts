import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

const css = readFileSync(join(process.cwd(), 'src/list/list.css'), 'utf8')

describe('Lancer: at most one enabled button visible in the list', () => {
  it('a stale focus-within on a row (a click, a moved selection) never reveals its button: only a keyboard focus on the button does', () => {
    expect(css).not.toMatch(/\.lr6:focus-within \.launch-btn/)
    expect(css).toMatch(/\.lr6:has\(\.launch-btn:focus-visible\) \.launch-btn/)
  })

  it('the selected row yields while another row is hovered (disabled and keyboard-focused buttons stay)', () => {
    expect(css).toMatch(/\.ll6:has\(\.lr6:hover\) \.lr6\.sel:not\(:hover\) \.launch-btn:not\(\[disabled\]\):not\(:focus-visible\) \{ opacity: 0; \}/)
  })

  it('the narrow list (hover shows no button there) gives the selected row its button back', () => {
    const i = css.indexOf('@container (max-width: 640px)')
    const narrow = css.slice(i, css.indexOf('@container (max-width: 480px)'))
    expect(narrow).toMatch(/\.ll6:has\(\.lr6:hover\) \.lr6\.sel:not\(:hover\) \.launch-btn:not\(\[disabled\]\):not\(:focus-visible\) \{ opacity: 1; \}/)
  })
})

describe('Lancer: its focus ring clears the list column edge and the classic scrollbar', () => {
  it('the list scroll box reserves its scrollbar gutter, so a classic scrollbar never eats the row padding or moves the column', () => {
    expect(css).toMatch(/\n  \.lscroll \{[^}]*scrollbar-gutter: stable/)
  })

  it('the Lancer cell keeps a ring-wide clearance (--sp-2) on its end side, wide grid and narrow overlay alike', () => {
    expect(css).toMatch(/\n  \.lr-l \{[^}]*padding-inline-end: var\(--sp-2\)/)
  })
})
