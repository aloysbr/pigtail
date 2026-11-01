import { useEffect, useRef, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import type { OutputTour } from '@/api/types'
import { tourWhen } from '@/panel/Links'

/**
 * The reader's round menu (`gmenu`, t1): every round from the latest to the oldest, the shown one checked, tagged
 * « dernière » and « introuvable ». ↑ ↓ Home End move, Enter or a click picks, Esc or a click outside closes it
 * (the reader stays); the focus goes to the checked round on open.
 */
export function ToursMenu({
  tours,
  current,
  onPick,
  onClose,
}: {
  tours: readonly OutputTour[]
  /** The index of the shown round in `tours`. */
  current: number
  onPick: (index: number) => void
  /** `refocus`: closed from the keyboard (Esc), the focus goes back to the round button; a click outside keeps its own. */
  onClose: (refocus: boolean) => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  const last = tours.length - 1

  useEffect(() => {
    ref.current?.querySelector<HTMLElement>('[aria-checked="true"]')?.focus()
  }, [])

  useEffect(() => {
    const onDown = (e: PointerEvent) => {
      if (e.target instanceof Element && e.target.closest('.rd-tools')) return
      onClose(false)
    }
    document.addEventListener('pointerdown', onDown)
    return () => document.removeEventListener('pointerdown', onDown)
  }, [onClose])

  const onKey = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const items = Array.from(ref.current?.querySelectorAll<HTMLElement>('[role="menuitemradio"]') ?? [])
    const at = items.indexOf(document.activeElement as HTMLElement)
    const to = (i: number) => items[Math.max(0, Math.min(items.length - 1, i))]?.focus()
    if (e.key === 'Escape') {
      // the menu only: the reader stays open
      e.preventDefault()
      e.stopPropagation()
      onClose(true)
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      to(at + 1)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      to(at - 1)
    } else if (e.key === 'Home') {
      e.preventDefault()
      to(0)
    } else if (e.key === 'End') {
      e.preventDefault()
      to(items.length - 1)
    } else if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
      // inside the menu ← → do not change the round behind it
      e.stopPropagation()
    }
  }

  const order = tours.map((t, i) => ({ t, i })).reverse()
  return (
    <div className="gmenu rd-menu" role="menu" aria-label="Tours" ref={ref} onKeyDown={onKey}>
      <div className="gm-sec">
        {tours.length} {tours.length === 1 ? 'tour' : 'tours'}
      </div>
      {order.map(({ t, i }) => (
        <button
          key={`${t.output_id}`}
          type="button"
          role="menuitemradio"
          aria-checked={i === current}
          tabIndex={i === current ? 0 : -1}
          onClick={() => onPick(i)}
        >
          <span>{t.label}</span>
          <span className="gm-r">
            {i === last ? <span className="gm-tag">dernière</span> : null}
            {!t.exists ? <span className="gm-tag bad">introuvable</span> : null}
            <span>{tourWhen(t.ts)}</span>
          </span>
        </button>
      ))}
    </div>
  )
}
