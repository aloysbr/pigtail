import type { Subject } from '@/api/types'

/** The displayed status (D8): one shape and one colour each, t10. */
export type Display = 'cours' | 'ouvert' | 'standby' | 'done' | 'dropped'

/** Display order, t10 ORDER. */
export const ORDER: readonly Display[] = ['cours', 'ouvert', 'standby', 'done', 'dropped']

/** Labels of the displayed statuses (t10 SL). */
export const LABEL: Record<Display, string> = {
  cours: 'En cours',
  ouvert: 'Ouvert',
  standby: 'Standby',
  done: 'Done',
  dropped: 'Abandonné',
}

const STORED: Record<string, Display> = {
  inbox: 'ouvert',
  understanding: 'ouvert',
  designing: 'ouvert',
  building: 'ouvert',
  parked: 'standby',
  done: 'done',
  dropped: 'dropped',
  captured: 'dropped',
}

/** True for a stored status pigtail knows (pkg/pigtail/types.go). */
export function isKnownStatus(stored: string): boolean {
  return stored in STORED
}

/** En cours when a live peer works on it; else from the stored status; an unknown one is Ouvert. */
export function displayStatus(s: Subject, live: ReadonlySet<string>): Display {
  if (live.has(s.id)) return 'cours'
  return STORED[s.status] ?? 'ouvert'
}

/** Dropped and captured are hidden until « Abandonnés · N » reveals them. */
export function isHidden(d: Display): boolean {
  return d === 'dropped'
}

/** The label of a row's status; a captured subject says « Capturé » (G10). */
export function statusLabel(s: Subject, d: Display): string {
  if (d === 'dropped' && s.status === 'captured') return 'Capturé'
  return LABEL[d]
}

/** The tooltip of a status: the stored value. */
export function statusTitle(s: Subject): string {
  return `Statut stocké : ${s.status}`
}

/** The three phases (D37): Comprendre, Concevoir, Construire. */
export const PHASES = ['Comprendre', 'Concevoir', 'Construire'] as const

/** The phase index of a stored status, null when it has none. */
export function phaseOf(stored: string): 0 | 1 | 2 | null {
  if (stored === 'understanding') return 0
  if (stored === 'designing') return 1
  if (stored === 'building') return 2
  return null
}
