import { useLayoutEffect, type RefObject } from 'react'
import { tokenPx } from '@/app/widths'

type Span = { left: number; right: number }

/** The bounds a menu must keep: the window and, when given, the list's own area (`.lmain`: a sheet beside it covers the rest). */
function bounds(viewport: number, margin: number, area?: Span): Span {
  return { left: Math.max(margin, (area?.left ?? 0) + margin), right: Math.min(viewport - margin, (area?.right ?? viewport) - margin) }
}

/** The width a menu may take inside those bounds (its max-width). */
export function menuRoom({ viewport, margin, area }: { viewport: number; margin: number; area?: Span }): number {
  const b = bounds(viewport, margin, area)
  return Math.max(0, b.right - b.left)
}

/**
 * Where a header menu sits, as an offset in px from the left edge of its button (its `.gmenu-w`): the preferred anchor (`left`: the
 * menu's left edge on the button's; `right`: right edge on right edge), moved just enough to stay `margin` inside the window and
 * inside `area`, the list (verify t3: with the sheet beside a 440 px list the menu ran under it). A menu wider than the room starts
 * at the left bound. Pure: every value in px, in window coordinates.
 */
export function menuOffset({
  button,
  menu,
  viewport,
  margin,
  prefer,
  area,
}: {
  button: Span
  menu: number
  viewport: number
  margin: number
  prefer: 'left' | 'right'
  area?: Span
}): number {
  const b = bounds(viewport, margin, area)
  const want = prefer === 'left' ? button.left : button.right - menu
  const left = Math.max(b.left, Math.min(want, b.right - menu))
  return left - button.left
}

/**
 * Keeps an open header menu inside the window and its list (verify t2: Filtre right of the growing search at 820 px ran 141 px off
 * screen; t3: with the sheet beside the list it ran under the sheet). Its max-width follows the same room. Measured when it opens
 * and on every window resize while open; nothing measured (jsdom) leaves the CSS anchor alone.
 */
export function useMenuAnchor(ref: RefObject<HTMLElement | null>, open: boolean, prefer: 'left' | 'right'): void {
  useLayoutEffect(() => {
    const menu = ref.current
    const wrap = menu?.parentElement
    if (!open || !menu || !wrap) return
    const place = () => {
      menu.style.removeProperty('left')
      menu.style.removeProperty('right')
      const viewport = document.documentElement.clientWidth || window.innerWidth
      const margin = tokenPx('--sp-6', 16)
      const l = menu.closest('.lmain')?.getBoundingClientRect()
      const area = l && l.width > 0 ? { left: l.left, right: l.right } : undefined
      // the width first (a narrow list caps it), then the measure
      menu.style.maxWidth = `${menuRoom({ viewport, margin, area })}px`
      const w = menu.getBoundingClientRect().width
      if (w <= 0) {
        menu.style.removeProperty('max-width')
        return
      }
      const b = wrap.getBoundingClientRect()
      const x = menuOffset({ button: { left: b.left, right: b.right }, menu: w, viewport, margin, prefer, area })
      menu.style.left = `${x}px`
      menu.style.right = 'auto'
    }
    place()
    window.addEventListener('resize', place)
    return () => window.removeEventListener('resize', place)
  }, [ref, open, prefer])
}
