import type { LogEntry, Subject } from '@/api/types'

/** The stored statuses shown « Ouvert ». */
export type OpenStatus = 'inbox' | 'understanding' | 'designing' | 'building'

const OPEN: readonly string[] = ['inbox', 'understanding', 'designing', 'building']

function isOpen(s: string): s is OpenStatus {
  return OPEN.includes(s)
}

/** Standby / Reprendre applies to a stored open or parked subject only; never Done, Abandonné nor a capture (D3). */
export function canToggleStandby(s: Subject): boolean {
  return isOpen(s.status) || s.status === 'parked'
}

/** « prev → next » of a status log line (the daemon may add « · … » after it). */
function transition(e: LogEntry): [string, string] | null {
  if (e.kind !== 'status') return null
  const m = /^(\S+) → (\S+)/.exec(e.text)
  return m ? [m[1], m[2]] : null
}

/**
 * The status a Standby subject resumes to (D6), from its log newest first: the old status of the
 * most recent « <open> → parked » line, else the last new open status read, else inbox.
 */
export function resumeStatus(log: readonly LogEntry[]): OpenStatus {
  for (const e of log) {
    const t = transition(e)
    if (t && t[1] === 'parked' && isOpen(t[0])) return t[0]
  }
  for (const e of log) {
    const t = transition(e)
    if (t && isOpen(t[1])) return t[1]
  }
  return 'inbox'
}

/** The status `h` writes: parked for an open subject, the resume status for a parked one; else unchanged. */
export function nextStatus(s: Subject, log: readonly LogEntry[]): string {
  if (isOpen(s.status)) return 'parked'
  if (s.status === 'parked') return resumeStatus(log)
  return s.status
}
