import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'
import type { Display } from '@/domain/status'

// SVGs from t10 (app.js SI and I): one shape per status, so it never rests on colour alone.

const STATUS: Record<Display, ReactNode> = {
  cours: (
    <>
      <circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path d="M8 4.5a3.5 3.5 0 0 1 0 7z" fill="currentColor" />
    </>
  ),
  ouvert: <circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" strokeWidth="1.5" />,
  standby: (
    <>
      <circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path d="M6.5 6v4M9.5 6v4" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </>
  ),
  done: (
    <>
      <circle cx="8" cy="8" r="6.75" fill="currentColor" />
      <path className="ck" d="M5.5 8.2l1.7 1.7 3.3-3.6" fill="none" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
    </>
  ),
  dropped: (
    <>
      <circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path d="M6 6l4 4M10 6l-4 4" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
    </>
  ),
}

/** The status icon: role img with its label, an optional tooltip. */
export function StatusIcon({ d, label, title, className }: { d: Display; label?: string; title?: string; className?: string }) {
  return (
    <span className={cn('si', `si-${d}`, 'shrink-0', className)} role={label ? 'img' : undefined} aria-label={label} aria-hidden={label ? undefined : true} title={title}>
      <svg viewBox="0 0 16 16" aria-hidden="true">
        {STATUS[d]}
      </svg>
    </span>
  )
}

const PATHS = {
  search: (
    <>
      <circle cx="7" cy="7" r="4.5" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path d="M10.5 10.5 14 14" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </>
  ),
  plus: <path d="M8 3v10M3 8h10" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />,
  list: <path d="M5.5 4h8M5.5 8h8M5.5 12h8M2.5 4h.5M2.5 8h.5M2.5 12h.5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />,
  chev: <path d="M4 6l4 4 4-4" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />,
  up: <path d="M4 10l4-4 4 4" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />,
  menu: <path d="M2.5 4.5h11M2.5 8h11M2.5 11.5h11" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />,
  group: <path d="M2.5 4h11M5 8h8.5M7.5 12h6" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />,
  eyeoff: (
    <path d="M1.5 8s2.5-4.5 6.5-4.5S14.5 8 14.5 8s-2.5 4.5-6.5 4.5S1.5 8 1.5 8zM3 13 13 3" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
  ),
  x: <path d="M4.5 4.5l7 7M11.5 4.5l-7 7" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />,
  check: <path d="M3.5 8.5 6.5 11.5 12.5 4.5" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" />,
  sliders: <path d="M2.5 5h6M11.5 5h2M2.5 11h2M7.5 11h6M9 3v4M6 9v4" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />,
  sun: (
    <>
      <circle cx="8" cy="8" r="2.6" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path d="M8 1.8v1.5M8 12.7v1.5M1.8 8h1.5M12.7 8h1.5M3.6 3.6l1 1M11.4 11.4l1 1M12.4 3.6l-1 1M4.6 11.4l-1 1" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
    </>
  ),
  moon: <path d="M13 9.5A5.5 5.5 0 0 1 6.5 3a5.5 5.5 0 1 0 6.5 6.5z" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" />,
  display: <path d="M2.5 3.5h11v7h-11zM6 13.5h4M8 10.5v3" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />,
  back: <path d="M10 3.5 5.5 8l4.5 4.5" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />,
  /* t3 I.sessions: a terminal window */
  sessions: (
    <>
      <rect x="1.75" y="2.75" width="12.5" height="10.5" rx="2" fill="none" stroke="currentColor" strokeWidth="1.4" />
      <path d="M4.75 6.25 6.75 8l-2 1.75M8.5 10h2.75" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
    </>
  ),
  fwd: <path d="M6 3.5 10.5 8 6 12.5" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />,
}

export type IconName = keyof typeof PATHS

/** A decorative t10 icon. */
export function Icon({ name, className }: { name: keyof typeof PATHS; className?: string }) {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true" className={className}>
      {PATHS[name]}
    </svg>
  )
}

/** Keys printed as t10 does: one kbd per space-separated key; « puis » and « … » are words between keys, not keys. */
export function Kbd({ k }: { k: string }) {
  return (
    <>
      {k.split(' ').map((x, i) =>
        x === 'puis' || x === '…' ? (
          <span key={i} className="k-then">
            {x}
          </span>
        ) : (
          <kbd key={i}>{x}</kbd>
        ),
      )}
    </>
  )
}

/** Thousands with a space, as t10 nf. */
export function nf(n: number): string {
  return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ' ')
}
