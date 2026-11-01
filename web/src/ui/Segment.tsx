import { useRef, type KeyboardEvent } from 'react'
import { Icon } from '@/list/StatusIcon'

export interface SegmentOption<T extends string> {
  value: T
  label: string
  icon?: Parameters<typeof Icon>[0]['name']
}

/** A short single-choice control (t3 .segd): a radiogroup of buttons, the checked one alone in the tab order; the arrows move the choice and the focus, Space picks the focused one. */
export function Segment<T extends string>({
  labelledBy,
  options,
  value,
  onChange,
}: {
  labelledBy: string
  options: readonly SegmentOption<T>[]
  value: T
  onChange: (v: T) => void
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([])
  const at = options.findIndex((o) => o.value === value)
  const go = (i: number) => {
    const n = (i + options.length) % options.length
    onChange(options[n].value)
    refs.current[n]?.focus()
  }
  const onKey = (e: KeyboardEvent, i: number) => {
    const by = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 0
    if (!by) return
    e.preventDefault()
    go(i + by)
  }
  return (
    <div className="segd" role="radiogroup" aria-labelledby={labelledBy}>
      {options.map((o, i) => (
        <button
          key={o.value}
          ref={(el) => {
            refs.current[i] = el
          }}
          type="button"
          role="radio"
          aria-checked={o.value === value}
          tabIndex={i === (at < 0 ? 0 : at) ? 0 : -1}
          onClick={() => onChange(o.value)}
          onKeyDown={(e) => onKey(e, i)}
        >
          {o.icon ? <Icon name={o.icon} /> : null}
          {o.label}
        </button>
      ))}
    </div>
  )
}
