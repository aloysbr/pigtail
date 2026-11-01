import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

/** Joins class names; a later Tailwind utility wins over an earlier one (the shadcn helper). */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs))
}

/** True when the key goes to a field (input, textarea, select, contenteditable): a bare key is then typing, never a shortcut. */
export function isTyping(t: EventTarget | null): boolean {
  if (!(t instanceof HTMLElement)) return false
  return t.isContentEditable || t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT'
}
