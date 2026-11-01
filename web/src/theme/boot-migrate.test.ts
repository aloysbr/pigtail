import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

const BOOT = readFileSync(join(process.cwd(), 'src/theme/boot.js'), 'utf8')
const KEYS = ['palette', 'appearance', 'scopes', 'side', 'kb', 'side-w', 'panel-w']

function run(store: Record<string, string>): Record<string, string> {
  const ls = {
    getItem: (k: string) => (k in store ? store[k] : null),
    setItem: (k: string, v: string) => { store[k] = String(v) },
    removeItem: (k: string) => { delete store[k] },
  }
  const doc = document.implementation.createHTMLDocument('boot')
  new Function('window', 'document', BOOT)({ localStorage: ls, matchMedia: () => ({ matches: false }) }, doc)
  return store
}

describe('boot.js migrates the legacy cockpit-* keys once', () => {
  it('copies every legacy key and removes it', () => {
    const legacy = Object.fromEntries(KEYS.map((k) => [`cockpit-${k}`, `v-${k}`]))
    const out = run({ ...legacy })
    for (const k of KEYS) {
      expect(out[`pigtail-${k}`]).toBe(`v-${k}`)
      expect(out[`cockpit-${k}`]).toBeUndefined()
    }
  })
  it('never overwrites a pigtail-* value already set', () => {
    const out = run({ 'cockpit-palette': 'ocean', 'pigtail-palette': 'terra' })
    expect(out['pigtail-palette']).toBe('terra')
    expect(out['cockpit-palette']).toBeUndefined()
  })
  it('applies the migrated palette on the first paint', () => {
    const doc = document.implementation.createHTMLDocument('boot')
    const store: Record<string, string> = { 'cockpit-palette': 'ocean' }
    const ls = { getItem: (k: string) => store[k] ?? null, setItem: (k: string, v: string) => { store[k] = v }, removeItem: (k: string) => { delete store[k] } }
    new Function('window', 'document', BOOT)({ localStorage: ls, matchMedia: () => ({ matches: false }) }, doc)
    expect(doc.documentElement.getAttribute('data-theme-palette')).toBe('ocean')
  })
  it('never throws when storage throws', () => {
    const throwing = { getItem: () => { throw new Error('x') }, setItem: () => { throw new Error('x') }, removeItem: () => { throw new Error('x') } }
    const doc = document.implementation.createHTMLDocument('boot')
    expect(() => new Function('window', 'document', BOOT)({ localStorage: throwing, matchMedia: () => ({ matches: false }) }, doc)).not.toThrow()
  })
})
