import type { Row } from '@/domain/group'
import { phaseOf, PHASES, type Display } from '@/domain/status'

function phase(stored: string, d: Display): number | null {
  return d === 'cours' || d === 'ouvert' ? phaseOf(stored) : null
}

/** True when PhaseMark renders something for this row: the same condition as its render. */
export function hasPhaseMark(row: Row): boolean {
  return phase(row.s.status, row.d) !== null
}

/** Three ticks and the phase name (D37), only for an En cours or Ouvert subject that has a phase. */
export function PhaseMark({ stored, d }: { stored: string; d: Display }) {
  const p = phase(stored, d)
  if (p === null) return null
  return (
    <>
      <span className="steps3" aria-hidden="true">
        {[0, 1, 2].map((i) => (
          <i key={i} className={i < p ? 'done' : i === p ? 'cur' : ''} />
        ))}
      </span>
      <span>{PHASES[p]}</span>
    </>
  )
}
