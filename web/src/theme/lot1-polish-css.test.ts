// @vitest-environment node
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { blocks } from './css-parse'

const SRC = fileURLToPath(new URL('..', import.meta.url))
const css = (rel: string) => blocks(readFileSync(join(SRC, rel), 'utf8'))
const body = (rel: string, selector: string) => css(rel).find((b) => b.selector.endsWith(selector))?.body ?? ''

describe('launch row of the subject sheet', () => {
  it('spaces its buttons by a token, wrapping when narrow', () => {
    const b = body('panel/panel.css', '.lp-acts')
    expect(b).toMatch(/display:\s*flex/)
    expect(b).toMatch(/flex-wrap:\s*wrap/)
    expect(b).toMatch(/gap:\s*var\(--sp-\d+\)/)
  })
})

describe('form fields focus ring (fields.css)', () => {
  const rel = 'ui/fields.css'
  it('the text input aligns with .ms-in / .fsearch: inset ring, no doubled border', () => {
    const b = body(rel, '.tf-i:focus-visible')
    expect(b).toMatch(/outline:\s*var\(--size-focus\) solid var\(--focus\)/)
    expect(b).toMatch(/outline-offset:\s*calc\(-1 \* var\(--size-bw\)\)/)
    expect(b).toMatch(/border-color:\s*transparent/)
  })
  it('no literal px left in a focus rule', () => {
    for (const b of css(rel).filter((x) => /:focus-visible/.test(x.selector))) expect(b.body, b.selector).not.toMatch(/\d+px/)
  })
})
