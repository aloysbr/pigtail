import { digitOf } from '@/list/Menus'
import { isTyping } from '@/lib/utils'

/** The most links a digit opens (1-9). */
export const LINK_KEYS = 9

/**
 * The link digit of a key (1-9), read from its physical code (`Digit2` is « é » on AZERTY without Shift, `Numpad2`),
 * never from `e.key`; null with a modifier, a held key, or in a field.
 */
export function linkDigit(e: KeyboardEvent): number | null {
  if (e.shiftKey || e.altKey || e.metaKey || e.ctrlKey || e.repeat) return null
  if (isTyping(e.target)) return null
  return digitOf(e.code)
}

/** Opens a safe link: a web one in a new tab (no opener), an app one (obsidian://) through an anchor, as a click on it would. */
export function openLink(href: string): void {
  if (/^https?:/i.test(href)) {
    window.open(href, '_blank', 'noopener,noreferrer')
    return
  }
  const a = document.createElement('a')
  a.href = href
  a.click()
}
