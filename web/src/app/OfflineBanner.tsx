import { useEffect, useState } from 'react'

/**
 * G3: the strip of the header (warning icon in --status-late) while pigtaild does not answer. The last data stays on screen; the strip counts down to
 * TanStack's next attempt (the re-read interval restarts at each failure) and goes at the first success.
 */
export function OfflineBanner({ nextRetryAt, fetching, onRetry }: { nextRetryAt: number; fetching: boolean; onRetry?: () => void }) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [])
  const n = Math.ceil((nextRetryAt - now) / 1000)
  return (
    <div className="d-banner" role="status">
      <svg className="d-banner-ico" viewBox="0 0 16 16" aria-hidden="true">
        <path d="M8 1.75 15 14H1z" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" />
        <path d="M8 6.5v3.5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
        <circle cx="8" cy="12" r="0.9" fill="currentColor" />
      </svg>
      <span>pigtaild ne répond pas — {fetching || n <= 0 ? 'nouvel essai en cours' : `nouvel essai dans ${n} s`}</span>
      {onRetry ? (
        <button type="button" className="d-banner-retry" onClick={onRetry} disabled={fetching}>
          Réessayer maintenant
        </button>
      ) : null}
    </div>
  )
}
