import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

const css = (f: string) => readFileSync(join(process.cwd(), 'src', f), 'utf8')
const reader = css('reader/reader.css')

describe('Dock buttons', () => {
  it('the dock footer buttons (primary, back) are centred and at least the secondary height', () => {
    expect(reader).toMatch(/\.sd-f \.btnp \{[^}]*justify-content: center/)
    expect(reader).toMatch(/\.sd-f \.btnp \{[^}]*min-height: var\(--size-row\)/)
  })
  it('under 600 px the dock buttons take a 44 px touch height', () => {
    expect(reader).toMatch(/@media \(max-width: 599\.98px\) \{[^@]*\.sd-f \.btnp[^{]*\{[^}]*min-height: var\(--size-touch\)/)
    expect(reader).toMatch(/@media \(max-width: 599\.98px\) \{[^@]*\.sd-back[^{]*\{[^}]*min-height: var\(--size-touch\)/)
    expect(reader).toMatch(/@media \(max-width: 599\.98px\) \{[^@]*\.sd-launch[^{]*\{[^}]*min-height: var\(--size-touch\)/)
  })
})
