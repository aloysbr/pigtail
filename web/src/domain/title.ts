import type { Subject } from '@/api/types'

/**
 * The bold title: the stored short title, else the whole title (whitespace collapsed). The row cuts it with a CSS ellipsis
 * at the column width, so a title without a short one is never cut by a derived 28-character rule (D15).
 */
export function shortTitle(s: Subject): string {
  const st = (s.short_title ?? '').trim()
  if (st) return st
  return s.title.split(/\s+/).filter(Boolean).join(' ')
}

function escRe(x: string): string {
  return x.replace(/[.*+?^${}()|[\]\\/]/g, '\\$&')
}

/** The long title shown grey after the short one, without a prefix that repeats it (t10 desc). */
export function longTitle(s: Subject): string {
  const short = shortTitle(s)
  // no stored short title: the bold text already is the whole title
  if (!(s.short_title ?? '').trim()) return ''
  let t = s.title.trim()
  if (t === short || `${t}` === short.replace(/…$/, '')) return ''
  const re = new RegExp(`^${escRe(short.replace(/…$/, ''))}\\s*[:—–·-]\\s*`, 'i')
  for (let m = re.exec(t); t && m; m = re.exec(t)) t = t.slice(m[0].length)
  if (t === short) return ''
  return t ? t.charAt(0).toUpperCase() + t.slice(1) : ''
}
