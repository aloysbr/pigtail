// @vitest-environment node
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const SRC = fileURLToPath(new URL('..', import.meta.url))

function files(dir: string, ext: RegExp): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n)
    return statSync(p).isDirectory() ? files(p, ext) : ext.test(p) ? [p] : []
  })
}

/**
 * B1 root cause (lot 2 verify): Shell.tsx imported list.css, and main.tsx imports Shell before
 * globals.css, so the bundle began with `@layer components{…}` and that first mention fixed the
 * layer order components < base: Tailwind's preflight (`* {padding:0; border:0 solid}`, in base)
 * then beat every padding and border of the web front. One stylesheet entry (globals.css) must
 * declare the layers first (`@import "tailwindcss"`) and then pull list.css in.
 */
describe('stylesheet entry: one root, Tailwind first (B1)', () => {
  it('reads the sources (positive control)', () => {
    expect(files(SRC, /\.tsx?$/).length).toBeGreaterThan(10)
  })

  it('no component imports a stylesheet: the layer order would depend on the module order', () => {
    const offenders = files(SRC, /\.tsx?$/)
      .filter((f) => !f.endsWith('main.tsx'))
      .filter((f) => /^\s*import\s+['"][^'"]+\.css['"]/m.test(readFileSync(f, 'utf8')))
      .map((f) => f.slice(SRC.length))
    expect(offenders).toEqual([])
  })

  it('globals.css imports tailwindcss, then tokens, then the web front list sheet', () => {
    const g = readFileSync(join(SRC, 'theme', 'globals.css'), 'utf8')
    const at = (s: string) => g.indexOf(s)
    expect(at('@import "tailwindcss"')).toBeGreaterThanOrEqual(0)
    expect(at('@import "../list/list.css"')).toBeGreaterThan(at('@import "tailwindcss"'))
  })
  it('pointer.css is imported last', () => {
    const g = readFileSync(join(SRC, 'theme', 'globals.css'), 'utf8')
    const imports = [...g.matchAll(/@import\s+"([^"]+)"/g)].map((m) => m[1])
    expect(imports.at(-1)).toBe('./pointer.css')
  })
})
