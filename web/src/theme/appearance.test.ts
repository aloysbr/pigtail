import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { runInNewContext } from 'node:vm'
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { applyChoice, followSystem, readChoice, saveChoice, useAppearance } from './appearance'
import { inlineBoot } from './boot-plugin'

const BOOT = readFileSync(join(process.cwd(), 'src/theme/boot.js'), 'utf8')
const INDEX = readFileSync(join(process.cwd(), 'index.html'), 'utf8')

/** A matchMedia whose prefers-color-scheme answer the test flips; listeners hear the flip. */
function fakeMedia(dark: boolean) {
  const listeners = new Set<(e: { matches: boolean }) => void>()
  const mql = {
    get matches() {
      return dark
    },
    media: '(prefers-color-scheme: dark)',
    addEventListener: (_: string, f: (e: { matches: boolean }) => void) => listeners.add(f),
    removeEventListener: (_: string, f: (e: { matches: boolean }) => void) => listeners.delete(f),
  }
  return {
    matchMedia: (q: string) => {
      if (!/prefers-color-scheme:\s*dark/.test(q)) throw new Error(`unexpected query ${q}`)
      return mql
    },
    flip(next: boolean) {
      dark = next
      for (const f of listeners) f({ matches: next })
    },
    listeners,
  }
}

const root = () => document.documentElement
const attrs = (el: Element) => [el.getAttribute('data-theme-palette'), el.getAttribute('data-theme')]

let media: ReturnType<typeof fakeMedia>
beforeEach(() => {
  media = fakeMedia(false)
  vi.stubGlobal('matchMedia', media.matchMedia)
  root().removeAttribute('data-theme-palette')
  root().removeAttribute('data-theme')
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('readChoice / applyChoice', () => {
  it('empty storage → terra + system, resolved by the system (light then dark)', () => {
    expect(readChoice()).toEqual({ palette: 'terra', appearance: 'system' })
    applyChoice(readChoice())
    expect(attrs(root())).toEqual(['terra', 'light'])
    media.flip(true)
    applyChoice(readChoice())
    expect(attrs(root())).toEqual(['terra', 'dark'])
  })

  it('ocean + dark saved → restored', () => {
    saveChoice({ palette: 'ocean', appearance: 'dark' })
    expect(localStorage.getItem('pigtail-palette')).toBe('ocean')
    expect(localStorage.getItem('pigtail-appearance')).toBe('dark')
    expect(readChoice()).toEqual({ palette: 'ocean', appearance: 'dark' })
    applyChoice(readChoice())
    expect(attrs(root())).toEqual(['ocean', 'dark'])
  })

  it('an unknown value reads as the default', () => {
    localStorage.setItem('pigtail-palette', 'neon')
    localStorage.setItem('pigtail-appearance', 'sepia')
    expect(readChoice()).toEqual({ palette: 'terra', appearance: 'system' })
  })

  it('a storage that throws reads as the default, without an exception', () => {
    const boom = () => {
      throw new Error('blocked')
    }
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(boom)
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(boom)
    expect(() => readChoice()).not.toThrow()
    expect(readChoice()).toEqual({ palette: 'terra', appearance: 'system' })
    expect(() => saveChoice({ palette: 'ocean', appearance: 'dark' })).not.toThrow()
  })
})

describe('System follows the Mac live, without a reload', () => {
  it('in system, data-theme follows a prefers-color-scheme change', () => {
    applyChoice(readChoice())
    const stop = followSystem()
    expect(root().getAttribute('data-theme')).toBe('light')
    media.flip(true)
    expect(root().getAttribute('data-theme')).toBe('dark')
    media.flip(false)
    expect(root().getAttribute('data-theme')).toBe('light')
    stop()
    expect(media.listeners.size).toBe(0)
  })

  it.each(['light', 'dark'] as const)('forced %s: a system change no longer moves it', (appearance) => {
    saveChoice({ palette: 'terra', appearance })
    applyChoice(readChoice())
    const stop = followSystem()
    media.flip(appearance === 'light')
    expect(root().getAttribute('data-theme')).toBe(appearance)
    stop()
  })

  it('useAppearance saves, applies and re-renders; in system it tracks the Mac', () => {
    const { result, unmount } = renderHook(() => useAppearance())
    expect(result.current.choice).toEqual({ palette: 'terra', appearance: 'system' })
    expect(result.current.resolved).toBe('light')
    act(() => media.flip(true))
    expect(result.current.resolved).toBe('dark')
    expect(root().getAttribute('data-theme')).toBe('dark')
    act(() => result.current.set({ palette: 'ocean', appearance: 'light' }))
    expect(result.current.choice).toEqual({ palette: 'ocean', appearance: 'light' })
    expect(attrs(root())).toEqual(['ocean', 'light'])
    expect(localStorage.getItem('pigtail-palette')).toBe('ocean')
    act(() => media.flip(false))
    act(() => media.flip(true))
    expect(root().getAttribute('data-theme')).toBe('light')
    unmount()
  })
})

describe('boot.js agrees with readChoice + applyChoice', () => {
  /** Runs the boot script in a fresh document with its own storage and matchMedia. */
  function boot(stored: Record<string, string>, dark: boolean, throwing = false): (string | null)[] {
    const doc = document.implementation.createHTMLDocument('boot')
    const store = {
      getItem: (k: string) => {
        if (throwing) throw new Error('blocked')
        return k in stored ? stored[k] : null
      },
    }
    const win = { localStorage: store, matchMedia: fakeMedia(dark).matchMedia, document: doc }
    runInNewContext(BOOT, { window: win, document: doc })
    return attrs(doc.documentElement)
  }

  const STORES: Record<string, string>[] = [
    {},
    { 'pigtail-palette': 'ocean', 'pigtail-appearance': 'system' },
    { 'pigtail-palette': 'ocean', 'pigtail-appearance': 'light' },
  ]
  const cases = STORES.flatMap((s) => [false, true].map((dark) => [JSON.stringify(s), dark] as const))

  it.each(cases)('storage %s, system dark %s', (s, dark) => {
    const stored = JSON.parse(s) as Record<string, string>
    for (const [k, v] of Object.entries(stored)) localStorage.setItem(k, v)
    media = fakeMedia(dark)
    vi.stubGlobal('matchMedia', media.matchMedia)
    applyChoice(readChoice())
    expect(boot(stored, dark)).toEqual(attrs(root()))
  })

  it('a blocked storage: no exception, the default', () => {
    expect(() => boot({}, true, true)).not.toThrow()
    expect(boot({}, true, true)).toEqual(['terra', 'dark'])
  })

  it('is plain ES5: no let/const/arrow/template, no import', () => {
    const code = BOOT.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '')
    expect(code).not.toMatch(/\b(let|const|import|export|class)\b|=>|`/)
  })
})

describe('index.html: the boot script is inline, at the head of <head>', () => {
  const html = (inlineBoot().transformIndexHtml as (h: string) => string)(INDEX)

  it('the build inlines boot.js as a classic script (positive control: the text is there)', () => {
    expect(html).toContain(BOOT.trim())
    expect(html).toMatch(/<script>\s*\/\*/)
    expect(html).not.toContain('<!-- boot -->')
  })

  it('the script comes before </head> and before any stylesheet link', () => {
    const at = html.indexOf('<script>')
    expect(at).toBeGreaterThan(-1)
    expect(at).toBeLessThan(html.indexOf('</head>'))
    const link = html.search(/<link[^>]+rel="stylesheet"/)
    if (link >= 0) expect(at).toBeLessThan(link)
    // Vite adds the built stylesheet before </head>: the script stays ahead of it
    expect(html.indexOf('<script>')).toBeLessThan(html.indexOf('<title>'))
  })
})
