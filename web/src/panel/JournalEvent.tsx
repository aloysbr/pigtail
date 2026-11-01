import { useState, type ReactNode } from 'react'
import type { Event, EventType } from '@/domain/journal'
import { cn } from '@/lib/utils'

/** The type of a journal event, written in full (R9) beside its icon (C9). */
export const TYPE_LABEL: Record<EventType, string> = {
  wait: 'Attente',
  decision: 'Décision',
  out: 'Sortie',
  status: 'Statut',
  session: 'Session',
  idea: 'Idée',
}

/** t4 `app.js` icons (16 × 16, currentColor), one per type. */
const TYPE_ICON: Record<EventType, ReactNode> = {
  wait: (
    <path
      d="M4.5 2.5h7M4.5 13.5h7M5.25 2.5c0 3 5.5 3.4 5.5 5.5s-5.5 2.5-5.5 5.5M10.75 2.5c0 3-5.5 3.4-5.5 5.5s5.5 2.5 5.5 5.5"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.3"
      strokeLinecap="round"
      strokeLinejoin="round"
    />
  ),
  decision: (
    <>
      <path d="M8 1.75 14.25 8 8 14.25 1.75 8z" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinejoin="round" />
      <path d="M5.6 8.1 7.3 9.7 10.4 6.4" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
    </>
  ),
  out: (
    <>
      <path d="M4 2.5h5.5L12 5v8.5H4z" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinejoin="round" />
      <path d="M6 8h4M6 10.5h3" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
    </>
  ),
  status: (
    <>
      <circle cx="8" cy="8" r="5.75" fill="none" stroke="currentColor" strokeWidth="1.3" />
      <path d="M8 4.25A3.75 3.75 0 0 1 8 11.75z" fill="currentColor" />
    </>
  ),
  session: (
    <>
      <rect x="1.75" y="2.75" width="12.5" height="10.5" rx="2" fill="none" stroke="currentColor" strokeWidth="1.4" />
      <path d="M4.75 6.25 6.75 8l-2 1.75M8.5 10h2.75" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
    </>
  ),
  idea: (
    <path
      d="M6 12.25h4M6.5 14h3M8 2a4.25 4.25 0 0 0-2.5 7.7c.4.3.6.8.6 1.3v.25h3.8V11c0-.5.2-1 .6-1.3A4.25 4.25 0 0 0 8 2z"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.3"
      strokeLinecap="round"
      strokeLinejoin="round"
    />
  ),
}

/** « 2 de plus · attente, statut » (C9). */
export function foldLabel(fold: Event[]): string {
  return `${fold.length} de plus · ${fold.map((f) => TYPE_LABEL[f.type].toLowerCase()).join(', ')}`
}

/** One journal event: icon, type in full, text (round chip hanging before it), time on the right, detail in grey below; its folded events unfold on a click. */
export function JournalEvent({ ev }: { ev: Event }) {
  const [open, setOpen] = useState(false)
  return (
    <li className="hv-ev" data-id={ev.id}>
      <svg className="hv-ic" viewBox="0 0 16 16" aria-hidden="true">
        {TYPE_ICON[ev.type]}
      </svg>
      <span className="hv-ty">{TYPE_LABEL[ev.type]}</span>
      <span className={cn('hv-tx', ev.round && 'hv-tx-r')}>
        {ev.round ? (
          <>
            <span className="hv-r">{ev.round}</span>
            <span className="hv-m">{ev.main}</span>
          </>
        ) : (
          ev.main
        )}
      </span>
      <span className="hv-t">{ev.time}</span>
      {ev.detail ? <span className="hv-d">{ev.detail}</span> : null}
      {ev.fold.length ? (
        <button type="button" className="hv-more" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
          <svg className="hv-chev" viewBox="0 0 16 16" aria-hidden="true">
            <path d="M4 6l4 4 4-4" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
          {foldLabel(ev.fold)}
        </button>
      ) : null}
      {open && ev.fold.length ? (
        <ol className="hv-fold">
          {ev.fold.map((f) => (
            <JournalEvent key={f.id} ev={f} />
          ))}
        </ol>
      ) : null}
    </li>
  )
}
