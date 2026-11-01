import { useEffect, useLayoutEffect, type RefObject } from 'react'
import { tokenPx } from './widths'

/**
 * How many filter chips line 2 of the two-line header shows (Q3 A): the line never wraps, the search keeps at least `searchMin`,
 * and the chips fold from the end (« Effacer tous les filtres » first) into « +n ». Pure: every width in px. `chips` and `more` (the
 * width of « +n » once shown) are bare widths; `gap` sits between two shown items of the group, n - 1 times.
 * `fixed` is what the line gives to everything but the search and the chips (its own gaps included).
 * Nothing measured (`line` 0: jsdom, a hidden header) keeps every chip visible.
 */
export function fitChips({
  line,
  fixed,
  searchMin,
  chips,
  more = 0,
  gap = 0,
}: {
  line: number
  fixed: number
  searchMin: number
  chips: readonly number[]
  more?: number
  gap?: number
}): number {
  if (line <= 0) return chips.length
  let used = chips.reduce((a, w) => a + w, 0)
  for (let k = chips.length; k > 0; k--) {
    const items = k + (k < chips.length ? 1 : 0)
    if (line - fixed - used - (k < chips.length ? more : 0) - gap * (items - 1) >= searchMin) return k
    used -= chips[k - 1]
  }
  return 0
}

const width = (e: Element) => e.getBoundingClientRect().width
const gapOf = (e: Element) => parseFloat(getComputedStyle(e).columnGap) || 0

/** Measures line 2 (`.h-l2`) and puts `hidden` on the chips that do not fit, from the end; « +n » (`.h-more`) says how many. */
function fit(line: HTMLElement) {
  const box = line.querySelector<HTMLElement>(':scope > .h-chips')
  if (!box) return
  const items = [...box.querySelectorAll<HTMLElement>(':scope > .h-ch')]
  const more = box.querySelector<HTMLElement>(':scope > .h-more')
  // measure with every chip shown, and « +n » at its widest
  for (const i of items) i.hidden = false
  if (more) {
    more.textContent = `+${items.length}`
    more.hidden = false
  }
  const lineGap = gapOf(line)
  const chipGap = gapOf(box)
  const others = [...line.children].filter((c) => c !== box && !c.classList.contains('fsearch'))
  const fixed = others.reduce((a, c) => a + width(c), 0) + lineGap * Math.max(0, line.children.length - 1)
  const k = fitChips({
    line: width(line),
    fixed,
    searchMin: tokenPx('--size-search-w', 200),
    chips: items.map(width),
    more: more ? width(more) : 0,
    gap: chipGap,
  })
  items.forEach((i, n) => {
    i.hidden = n >= k
  })
  if (more) {
    more.hidden = k === items.length
    more.textContent = k === items.length ? '' : `+${items.length - k}`
  }
}

/** Keeps the chips of line 2 fitted: after every render (the chips change) and on every resize of the line. */
export function useFitChips(ref: RefObject<HTMLElement | null>): void {
  useLayoutEffect(() => {
    if (ref.current) fit(ref.current)
  })
  useEffect(() => {
    const line = ref.current
    if (!line || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(() => fit(line))
    ro.observe(line)
    return () => ro.disconnect()
  }, [ref])
}
