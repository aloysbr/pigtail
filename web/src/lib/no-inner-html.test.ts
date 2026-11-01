// @vitest-environment node
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const SRC = fileURLToPath(new URL('..', import.meta.url))

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n)
    return statSync(p).isDirectory() ? files(p) : /\.tsx?$/.test(p) && !/\.test\.tsx?$/.test(p) ? [p] : []
  })
}

/**
 * The lint rule of the plan (Tâche 11, sécurité front): the web front draws data written by agents (titles, log lines, output URLs),
 * so no source may inject HTML. web/ has no eslint; this test is the rule.
 */
describe('no raw HTML injection in the web front sources', () => {
  it('reads the sources (positive control)', () => {
    expect(files(SRC).length).toBeGreaterThan(20)
    expect(files(SRC).some((f) => f.endsWith('Row.tsx'))).toBe(true)
  })

  it('no dangerouslySetInnerHTML, innerHTML, outerHTML, insertAdjacentHTML or document.write', () => {
    const bad = /dangerouslySetInnerHTML|\.innerHTML\b|\.outerHTML\b|insertAdjacentHTML|document\.write/
    const offenders = files(SRC)
      .filter((f) => bad.test(readFileSync(f, 'utf8')))
      .map((f) => f.slice(SRC.length))
    expect(offenders).toEqual([])
  })
})
