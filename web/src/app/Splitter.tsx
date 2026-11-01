import { useEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from 'react'
import { cn } from '@/lib/utils'
import { clamp, tokenPx } from './widths'

/** A vertical separator that sets a panel's width (D2): pointer, keys, double-click. No dependency. */
export interface SplitterProps {
  /** Accessible name (« Largeur de la fiche »). */
  label: string
  value: number
  min: number
  max: number
  /** 1: the panel grows to the right (the sidebar, width = clientX); -1: it grows to the left (the sheet, width = innerWidth - clientX). */
  dir: 1 | -1
  /** A new width, already bounded; `persist` at the end of a drag and on a key. */
  onChange: (px: number, persist: boolean) => void
  /** Double-click: back to the default width, forgotten. */
  onReset: () => void
  /** Called with true when a drag starts, false when it ends (the shell then shows the resize cursor everywhere). */
  onResizing?: (on: boolean) => void
  valueText: string
  className?: string
  controls?: string
}

const KEYS: ReadonlySet<string> = new Set(['ArrowLeft', 'ArrowRight', 'Home', 'End'])

export function Splitter({ label, value, min, max, dir, onChange, onReset, onResizing, valueText, className, controls }: SplitterProps) {
  const [dragging, setDragging] = useState(false)
  // read by the move and up handlers of the same gesture, before React re-renders
  const live = useRef(false)
  const frame = useRef<number | null>(null)
  const last = useRef<number | null>(null)
  const bounds = useRef({ min, max })
  bounds.current = { min, max }

  const widthAt = (clientX: number) => clamp(Math.round(dir === 1 ? clientX : window.innerWidth - clientX), bounds.current.min, bounds.current.max)

  const cancelFrame = () => {
    if (frame.current !== null && typeof cancelAnimationFrame === 'function') cancelAnimationFrame(frame.current)
    frame.current = null
  }
  // unmounted mid-drag (the sheet became a page): the shell must not keep its resize cursor
  const ending = useRef(onResizing)
  ending.current = onResizing
  useEffect(
    () => () => {
      cancelFrame()
      if (live.current) ending.current?.(false)
    },
    [],
  )

  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return
    e.preventDefault()
    e.currentTarget.setPointerCapture?.(e.pointerId)
    e.currentTarget.focus()
    last.current = null
    live.current = true
    setDragging(true)
    onResizing?.(true)
  }

  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    if (!live.current) return
    last.current = e.clientX
    // one width per frame; without requestAnimationFrame, at once
    if (typeof requestAnimationFrame !== 'function') {
      onChange(widthAt(e.clientX), false)
      return
    }
    if (frame.current !== null) return
    frame.current = requestAnimationFrame(() => {
      frame.current = null
      if (last.current !== null) onChange(widthAt(last.current), false)
    })
  }

  const end = (e: PointerEvent<HTMLDivElement>) => {
    if (!live.current) return
    live.current = false
    cancelFrame()
    e.currentTarget.releasePointerCapture?.(e.pointerId)
    setDragging(false)
    onResizing?.(false)
    // a press without a move (a click, the first press of a double-click) keeps nothing
    if (last.current !== null) onChange(widthAt(last.current), true)
    last.current = null
  }

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!KEYS.has(e.key) || e.metaKey || e.ctrlKey || e.altKey) return
    e.preventDefault()
    // the list's own arrows (fold, unfold) must not see it
    e.stopPropagation()
    const step = tokenPx('--size-split-step', 16) * (e.shiftKey ? 4 : 1)
    const next =
      e.key === 'Home' ? min
      : e.key === 'End' ? max
      : value + (e.key === 'ArrowRight' ? 1 : -1) * dir * step
    onChange(clamp(next, min, max), true)
  }

  return (
    <div
      className={cn('split-h', className, dragging && 'drag')}
      role="separator"
      aria-orientation="vertical"
      aria-label={label}
      aria-controls={controls}
      aria-valuenow={value}
      aria-valuemin={min}
      aria-valuemax={max}
      aria-valuetext={valueText}
      tabIndex={0}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={end}
      onPointerCancel={end}
      onDoubleClick={onReset}
      onKeyDown={onKeyDown}
    >
      <span className="split-tip" aria-hidden="true">
        <kbd>←</kbd>
        <kbd>→</kbd>
        {/* U+2011 / U+00A0: « double‑clic » and « par défaut » never break */}
        <span>{'largeur · double‑clic : par\u00a0défaut'}</span>
      </span>
    </div>
  )
}
