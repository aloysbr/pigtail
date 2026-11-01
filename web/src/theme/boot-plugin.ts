import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Plugin } from 'vite'

const BOOT = join(dirname(fileURLToPath(import.meta.url)), 'boot.js')
const MARK = '<!-- boot -->'

/** Vite plugin: replaces the `<!-- boot -->` mark of index.html (at the head of <head>, before any stylesheet) with
 *  the text of boot.js as a classic inline script, in dev and at build. A missing mark fails the build. */
export function inlineBoot(): Plugin {
  return {
    name: 'pigtail-inline-boot',
    transformIndexHtml(html: string) {
      if (!html.includes(MARK)) throw new Error(`index.html has no ${MARK} mark for the theme boot script`)
      return html.replace(MARK, `<script>\n${readFileSync(BOOT, 'utf8').trim()}\n</script>`)
    },
  }
}
