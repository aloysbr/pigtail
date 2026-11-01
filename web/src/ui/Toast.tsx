import { useLayoutEffect, useRef } from 'react'
import { undoLast, useToasts } from './toasts'

/**
 * The toast of the last action (t10 .toast6), read by a screen reader (role status). An undoable
 * one carries « annuler »: a button everywhere (the phone's only way), with its `z` key on a desktop.
 */
export function ToastHost() {
  const toasts = useToasts()
  const t = toasts[toasts.length - 1]
  const ref = useRef<HTMLDivElement>(null)
  // the list reserves the toast's height under its last row while it shows (t10 --toast-h)
  useLayoutEffect(() => {
    if (!t) return
    const root = document.documentElement.style
    root.setProperty('--toast-h', `${ref.current?.offsetHeight ?? 0}px`)
    return () => {
      root.removeProperty('--toast-h')
    }
  }, [t])
  if (!t) return null
  return (
    <div className="toast6" role="status" key={t.id} ref={ref}>
      <span className="toast-t">{t.text}</span>
      {t.undo ? (
        <button type="button" className="toast-undo" onClick={() => void undoLast()}>
          <kbd aria-hidden="true">z</kbd>
          annuler
        </button>
      ) : null}
    </div>
  )
}
