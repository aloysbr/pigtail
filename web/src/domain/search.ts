import type { Subject } from '@/api/types'
import { shortTitle } from './title'

/** NFD, accents stripped, lower case (t10 fold). */
export function fold(x: string): string {
  return x.normalize('NFD').replace(/[\u0300-\u036f]/g, '').toLowerCase()
}

/** Substring match, accents and case ignored, on the short title and the title; a blank query matches all. */
export function matches(s: Subject, query: string): boolean {
  const q = fold(query.trim())
  if (!q) return true
  return fold(shortTitle(s)).includes(q) || fold(s.title).includes(q)
}
