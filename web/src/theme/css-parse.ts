/** A tiny CSS reader for the theme tests: blocks by selector, custom properties by block. Not used at runtime. */
export const DARK_SYSTEM = '@media (prefers-color-scheme: dark) :root:not([data-theme="light"])'

/** One innermost rule: its full selector (at-rule prefixes joined) and its raw body. */
export interface Block {
  selector: string
  body: string
}

/** Strips comments, then lists every innermost `selector { body }` with its at-rule prefix (e.g. "@media (…) :root"). */
export function blocks(css: string): Block[] {
  const src = css.replace(/\/\*[\s\S]*?\*\//g, '')
  const out: Block[] = []
  const stack: string[] = []
  let buf = ''
  for (const ch of src) {
    if (ch === '{') {
      stack.push(buf.slice(buf.lastIndexOf(';') + 1).trim())
      buf = ''
    } else if (ch === '}') {
      const sel = stack.pop() ?? ''
      if (buf.trim() !== '' || !/^@/.test(sel)) out.push({ selector: [...stack, sel].join(' ').replace(/\s+/g, ' ').trim(), body: buf })
      buf = ''
    } else {
      buf += ch
    }
  }
  return out
}

/** The custom properties declared in a block body, in order. */
export function props(body: string): Map<string, string> {
  const m = new Map<string, string>()
  for (const d of body.split(';')) {
    const i = d.indexOf(':')
    if (i < 0) continue
    const k = d.slice(0, i).trim()
    if (k.startsWith('--')) m.set(k, d.slice(i + 1).trim())
  }
  return m
}

/** Merges the custom properties of every block whose selector equals sel. */
export function propsOf(css: string, sel: string): Map<string, string> {
  const m = new Map<string, string>()
  for (const b of blocks(css)) if (b.selector === sel) for (const [k, v] of props(b.body)) m.set(k, v)
  return m
}

/** True for a value that is a colour (or carries one, like a shadow). */
export function isColour(v: string): boolean {
  return /#[0-9a-fA-F]{3,8}\b|rgba?\(|hsla?\(|oklch\(/.test(v)
}

/** The custom properties a value references through var(). */
export function refs(v: string): string[] {
  return [...v.matchAll(/var\(\s*(--[\w-]+)/g)].map((x) => x[1])
}
