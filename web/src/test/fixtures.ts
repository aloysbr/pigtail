import type { Peer, Space, Subject, Theme } from '@/api/types'

let n = 0

/** A subject with sane defaults; ids are unique per test file. */
export function subj(p: Partial<Subject> = {}): Subject {
  n += 1
  return {
    id: `sub_${n}`,
    theme_id: 'th_a',
    title: `Sujet ${n}`,
    status: 'inbox',
    scope: 'perso',
    updated_at: '2026-10-23T10:00:00Z',
    created_at: '2026-10-01T10:00:00Z',
    ...p,
  }
}

/** A theme (project) with sane defaults. */
export function theme(p: Partial<Theme> & { id: string }): Theme {
  return { name: p.id, kind: 'project', scope: 'perso', space: 'Allotment', archived: false, ...p }
}

/** A live peer session working on a subject. */
export function peer(subjectId: string, p: Partial<Peer> = {}): Peer {
  return { session_id: `s-${subjectId}`, subject_id: subjectId, scope: 'perso', alive: true, ...p }
}

export const SPACES: Space[] = [
  { name: 'Allotment', scope: 'perso' },
  { name: 'Garden', scope: 'perso' },
  { name: 'Vineyard', scope: 'globex' },
]
