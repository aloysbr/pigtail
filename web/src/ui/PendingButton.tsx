import type { MouseEvent } from 'react'
import { Kbd } from '@/list/StatusIcon'
import { cn } from '@/lib/utils'

/**
 * A button with an « in progress » state (t10 .btnl, spec § Apports DS): at rest its label and key; pending, the busy label
 * greyed with a spinner, `aria-busy` and disabled (the key: `aria-keyshortcuts`), so a second press does nothing.
 */
export function PendingButton({
  pending,
  label,
  busy,
  keys,
  onClick,
  className,
  icon,
  primary,
}: {
  pending: boolean
  label: string
  busy: string
  keys?: string
  onClick: (e: MouseEvent<HTMLButtonElement>) => void
  className?: string
  /** The path of a 16-px stroke icon shown before the label at rest. */
  icon?: string
  /** The card's primary action: the filled .btnp (reader.css), not the bordered .btnl. */
  primary?: boolean
}) {
  return (
    <button type="button" className={cn(primary ? 'btnp' : 'btnl', className)} disabled={pending} aria-busy={pending || undefined} aria-keyshortcuts={keys} onClick={onClick}>
      {pending ? (
        <>
          <svg className="spin" viewBox="0 0 16 16" aria-hidden="true">
            <circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" strokeWidth="1.5" strokeDasharray="22 16" strokeLinecap="round" />
          </svg>
          {busy}
        </>
      ) : (
        <>
          {icon ? (
            <svg viewBox="0 0 16 16" aria-hidden="true">
              <path d={icon} fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
            </svg>
          ) : null}
          {label}
          {/* the key is said by aria-keyshortcuts: the name stays the label */}
          {keys ? (
            <span className="kbs" aria-hidden="true">
              <Kbd k={keys} />
            </span>
          ) : null}
        </>
      )}
    </button>
  )
}
