#!/usr/bin/env node
/* Generates src/theme/palette.css from the frozen colour engine src/theme/palette.ref.cjs (copy of t4/palette.js).
   Every colour is frozen as an oklch() literal: nothing is computed at run time. Four combinations
   (Terra / Ocean × light / dark), each also reachable from any element through data-theme-palette + data-theme
   (the theme card previews the other theme without a second token set).

   `npm run gen:palette` rewrites the file; palette-sync.test.ts fails when the file and this output differ.
   Exported render() is what the test compares against. */
import { writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const ENGINE = fileURLToPath(new URL('../src/theme/palette.ref.cjs', import.meta.url))
const OUT = fileURLToPath(new URL('../src/theme/palette.css', import.meta.url))

/** palette token (engine) → repo token(s). Only tokens a component reads are emitted (tokens-live.test.ts). */
export const MAP = [
  ['--bg', ['--bg']],
  ['--bg-side', ['--bg-2', '--surface-side']],
  ['--surface', ['--surface']],
  ['--surface-3', ['--bg-3']],
  ['--hover', ['--bg-hover']],
  ['--hover-side', ['--bg-hover-side']],
  ['--hover-panel', ['--bg-hover-panel']],
  ['--active-side', ['--bg-active-side']],
  ['--active-pal', ['--bg-active-pal']],
  ['--active', ['--bg-active']],
  ['--sel', ['--bg-selected']],
  ['--line', ['--border', '--line']],
  ['--line-strong', ['--border-strong']],
  ['--text', ['--text']],
  ['--text-1', ['--text-1']],
  ['--text-2', ['--text-2']],
  ['--text-3', ['--text-disabled']],
  ['--overlay', ['--overlay-soft']],
  ['--veil', ['--veil']],
  ['--chip-bg', ['--chip-bg']],
  ['--chip-text', ['--chip-text']],
  ['--pill-on-line', ['--pill-on-line']],
  ['--pill-on-bg', ['--pill-on-bg']],
  ['--link', ['--link']],
  ['--focus', ['--focus']],
  ['--mark', ['--mark']],
  ['--btn-fill', ['--btn-fill']],
  ['--btn-on', ['--btn-on']],
  ['--s-cours', ['--s-cours']],
  ['--s-ouvert', ['--s-ouvert']],
  ['--s-standby', ['--s-standby']],
  ['--s-done', ['--s-done']],
  ['--wait-bg', ['--wait-bg']],
  /* the one strong colour: « attend ta réponse » */
  ['--attn-fill', ['--accent-attn-solid']],
  ['--attn-text', ['--accent-attn-text']],
  ['--attn-soft', ['--accent-attn-tint']],
  ['--attn', ['--accent-attn-dot']],
  ['--attn-line', ['--accent-attn-line']],
  ['--attn-on', ['--accent-attn-on']],
  /* shared statuses (D6) */
  ['--due-late', ['--status-late']],
  ['--ok', ['--status-ok']],
  ['--due-today', ['--status-today']],
]

/** The selectors of one combination: the root form, then the element form (any element carrying both attributes). */
export function selectors(theme, mode) {
  const el = `[data-theme-palette="${theme}"][data-theme="${mode}"]`
  if (theme === 'terra') return [mode === 'light' ? ':root' : ':root[data-theme="dark"]', el]
  return [mode === 'light' ? ':root[data-theme-palette="ocean"]' : ':root[data-theme-palette="ocean"][data-theme="dark"]', el]
}

/** The whole palette.css text: the engine's own css() (4 decimals of L and C, 1 of H). Measured on these rounded
 *  values, every ΔE late/ok ↔ accent keeps ≥ 0.12 (tokens-measure.test.ts): no extra decimals needed. */
export function render() {
  const pal = require(ENGINE)
  const out = [
    '/* GÉNÉRÉ par web/scripts/gen-palette.mjs depuis src/theme/palette.ref.cjs (t4/palette.js) — ne pas éditer à la main.',
    '   `npm run gen:palette` le réécrit ; palette-sync.test.ts échoue sur toute dérive, tokens-measure.test.ts mesure',
    '   contrastes et ΔE sur ces valeurs arrondies. Terra clair = :root ; chaque combinaison est aussi posable sur',
    '   n\'importe quel élément par data-theme-palette + data-theme (aperçu d\'une carte de thème). */',
  ]
  for (const theme of pal.THEMES) {
    for (const mode of ['light', 'dark']) {
      const vars = pal.tokens(mode, theme).vars
      out.push('', `/* ${theme.name} · ${mode === 'light' ? 'clair' : 'sombre'} */`, `${selectors(theme.id, mode).join(',\n')} {`)
      out.push(`  color-scheme: ${mode};`)
      for (const [src, names] of MAP) {
        const c = vars[src]
        if (!c) throw new Error(`palette.ref.cjs has no ${src}`)
        for (const n of names) out.push(`  ${n}: ${pal.css(c)};`)
      }
      out.push('}')
    }
  }
  return out.join('\n') + '\n'
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  writeFileSync(OUT, render())
  console.log(`wrote ${OUT}`)
}
