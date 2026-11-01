// @vitest-environment node
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { blocks, props, refs } from './css-parse'

/** tokens.css keeps only the tokens something reads; colours come from palette.css (generated) and nowhere else.
 *  Three explicit lists hold the exceptions, each entry with its reason; a stale entry turns the test red. */

const SRC = fileURLToPath(new URL('..', import.meta.url))
const THEME = fileURLToPath(new URL('.', import.meta.url))
const TOKEN_FILES = ['tokens.css', 'palette.css'].map((f) => join(THEME, f))

/** Declared now, read by a later task of the plan.
 *  Each task that adds the reader removes its entries in the same commit; task 16 asserts the list empty. */
export const PENDING_READERS: Record<string, string> = {}

/** Kept with no component reader in this plan (no due-date screen); written as an exception in the ADR (T5). */
export const RESERVED: Record<string, string> = {
  '--status-ok': 'measured against the accent: the ΔE ≥ 0.12 contract of tokens-measure.test.ts',
  '--status-today': 'named by the spec; a landmark only, measured without threshold (ΔE Terra light with --accent-attn-dot = 0.105, palette untouchable, D6)',
}

/** TRANSITOIRE block of tokens.css (T2): legacy tokens components still read until T3 moved them to ink. Empty since T3. */
export const LEGACY_UNTIL_T3: string[] = []

/** Read but set at run time, never declared in a sheet. */
const SET_IN_JS: Record<string, string> = {
  '--toast-h': 'ui/Toast.tsx measures the toast and sets it',
  '--w': 'list/EmptyStates.tsx sets each skeleton bar width inline',
}

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n)
    if (statSync(p).isDirectory()) return n === 'test' ? [] : files(p)
    return /\.(css|tsx?)$/.test(n) && !/\.test\.tsx?$/.test(n) ? [p] : []
  })
}

const sources = files(SRC).filter((f) => !TOKEN_FILES.includes(f))
const tokenCss = TOKEN_FILES.map((f) => readFileSync(f, 'utf8'))

/** Every token declared in tokens.css and palette.css (all blocks), with the values it takes. */
const declared = new Map<string, string[]>()
for (const css of tokenCss)
  for (const b of blocks(css)) for (const [k, v] of props(b.body)) declared.set(k, [...(declared.get(k) ?? []), v])

/** Tokens read outside the token sheets: var(--x), tokenPx('--x'), any '--x' string. */
function readsOf(text: string): string[] {
  return [...text.matchAll(/var\(\s*(--[\w-]+)/g), ...text.matchAll(/['"`](--[\w-]+)['"`]/g)].map((m) => m[1])
}
const direct = new Set(sources.flatMap((f) => readsOf(readFileSync(f, 'utf8'))))

/** Follows the var() inside the token values: a token another read token points to is read too. */
const reached = new Set<string>()
const queue = [...direct]
while (queue.length) {
  const t = queue.pop() as string
  if (reached.has(t)) continue
  reached.add(t)
  for (const v of declared.get(t) ?? []) queue.push(...refs(v))
}

describe('tokens.css keeps only the tokens components read', () => {
  it('reads the sources (positive control: at least 100 tokens reached)', () => {
    expect(sources.length).toBeGreaterThan(20)
    expect(reached.size).toBeGreaterThanOrEqual(100)
  })

  it('no declared token is left without a reader (outside PENDING_READERS and RESERVED)', () => {
    const dead = [...declared.keys()].filter((k) => !reached.has(k) && !(k in PENDING_READERS) && !(k in RESERVED)).sort()
    expect(dead).toEqual([])
  })

  it('T16: every pending reader has arrived, the list is empty', () => {
    expect(PENDING_READERS).toEqual({})
  })

  it('PENDING_READERS is not stale: no entry has a reader yet, and each is declared', () => {
    for (const k of Object.keys(PENDING_READERS)) {
      expect(declared.has(k), `${k} pending but not declared`).toBe(true)
      expect(reached.has(k), `${k} now has a reader: remove it from PENDING_READERS`).toBe(false)
    }
  })

  it('RESERVED cites --status-ok and --status-today, each with its written reason, and nothing reads them', () => {
    expect(Object.keys(RESERVED).sort()).toEqual(['--status-ok', '--status-today'])
    for (const [k, why] of Object.entries(RESERVED)) {
      expect(why.length, k).toBeGreaterThan(20)
      expect(declared.has(k), k).toBe(true)
      expect(reached.has(k), `${k} has a reader: it is no longer reserved`).toBe(false)
    }
  })

  it('LEGACY_UNTIL_T3 is empty and tokens.css has no TRANSITOIRE block left (T3)', () => {
    expect(LEGACY_UNTIL_T3).toEqual([])
    expect(readFileSync(join(THEME, 'tokens.css'), 'utf8')).not.toMatch(/TRANSITOIRE/)
  })

  it('the session dock (outputs-consultables T13): --size-sess-card 360 px and --size-sess-side 380 px, declared and read', () => {
    expect(declared.get('--size-sess-card')).toEqual(['360px'])
    expect(declared.get('--size-sess-side')).toEqual(['380px'])
    expect(direct.has('--size-sess-card')).toBe(true)
    expect(direct.has('--size-sess-side')).toBe(true)
  })

  it('every token read is declared (except the ones set in JS)', () => {
    const undeclared = [...direct].filter((k) => !declared.has(k) && !(k in SET_IN_JS)).sort()
    expect(undeclared).toEqual([])
  })

  it('no colour literal outside tokens.css and palette.css (#hex, rgb(, hsl(, oklch()', () => {
    const bad: string[] = []
    for (const f of sources.filter((f) => /\.(css|tsx)$/.test(f))) {
      const text = readFileSync(f, 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
      for (const m of text.matchAll(/#[0-9a-fA-F]{3,8}\b(?![\w-])|\brgba?\(|\bhsla?\(|\boklch\(/g)) bad.push(`${relative(SRC, f)}: ${m[0]}`)
    }
    expect(bad).toEqual([])
  })
})
