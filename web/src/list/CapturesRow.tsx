import { ageOf } from '@/domain/age'
import { cn } from '@/lib/utils'
import { setUi, useUi } from '@/state/ui'
import { toggleOpen } from './GroupHeader'
import type { Part } from './model'
import { Icon, nf } from './StatusIcon'

/** t10 I.cap: a capture is not a subject, it has no status shape. */
export function CaptureIcon() {
  return (
    <span className="si si-cap" aria-hidden="true">
      <svg viewBox="0 0 16 16">
        <path d="M3.5 2.5h6l3 3v8h-9z" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinejoin="round" />
        <path d="M5.5 8h5M5.5 10.5h3.5" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
      </svg>
    </span>
  )
}

function CaptureLine({ id, text, created, now }: { id: string; text: string; created: string; now: number }) {
  const selected = useUi((s) => s.sel === id)
  return (
    <li
      className={cn('lr6 cap-row', selected && 'sel')}
      data-cap={id}
      tabIndex={selected ? 0 : -1}
      aria-current={selected ? 'true' : undefined}
      // read only (D14): a click selects it, never a sheet
      onClick={() => setUi((s) => ({ sel: id, panel: false, focusTick: s.focusTick + 1 }))}
    >
      <CaptureIcon />
      <span className="cap-t">{text}</span>
      <span className="lr-d">{ageOf(created, now)}</span>
    </li>
  )
}

/** A project's « Captures · n » (t10 capsBlock): one folded line, then its captures read only — text and age, no checkbox (D14). */
export function CapturesRow({ part, now }: { part: Extract<Part, { type: 'caps' }>; now: number }) {
  return (
    <div className="sub caps">
      <button type="button" className="sline capl" data-g={part.key} aria-expanded={part.open} onClick={() => toggleOpen(part.key, part.open)}>
        <Icon name="chev" className="gchev" />
        <CaptureIcon />
        <span>Captures</span>
        <span className="lg-n">{nf(part.caps.length)}</span>
      </button>
      {part.open ? (
        <ul aria-label="Captures">
          {part.caps.map((c) => (
            <CaptureLine key={c.id} id={c.id} text={c.text} created={c.created_at} now={now} />
          ))}
        </ul>
      ) : null}
    </div>
  )
}
