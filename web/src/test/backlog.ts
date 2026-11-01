import type { Capture, LogEntry, Output, OutputGroup, Peer, SessionLaunch, Space, Subject, Theme, Wait } from '@/api/types'

/** The four collections the web front reads, plus the per-subject log and outputs the sheet reads, the open captures and the launches. */
export interface BacklogData {
  subjects: Subject[]
  themes: Theme[]
  spaces: Space[]
  peers: Peer[]
  logs?: Record<string, LogEntry[]>
  outputs?: Record<string, Output[]>
  /** GET /subjects/{id}/outputs/groups, by subject id; [] when absent */
  groups?: Record<string, OutputGroup[]>
  /** GET /captures?scope=all&open=1 */
  captures?: Capture[]
  /** GET /launches/{id}, by launch id */
  launches?: Record<string, SessionLaunch>
  /** GET /peers?all=1 (the Sessions view); `peers` when absent */
  peersAll?: Peer[]
  /** GET /waits?subject=…|session=…: the records, filtered by the query's subject or session */
  waits?: Wait[]
}

/** An ISO date `min` minutes ago. */
export function minutesAgo(min: number): string {
  return new Date(Date.now() - min * 60_000).toISOString()
}

function s(id: string, theme_id: string, title: string, status: string, extra: Partial<Subject> = {}): Subject {
  return { id, theme_id, title, status, scope: 'perso', updated_at: minutesAgo(30), created_at: minutesAgo(10_000), ...extra }
}

/**
 * A small two-scope backlog. perso: Allotment (pigtail, Outils « continu »), Garden (Seedbed), Vide (no subject),
 * and one subject with no theme; globex: Vineyard (Board). One live session on « pig-live ».
 */
export function makeBacklog(): BacklogData {
  const spaces: Space[] = [
    { name: 'Allotment', scope: 'perso' },
    { name: 'Garden', scope: 'perso' },
    { name: 'Vide', scope: 'perso' },
    { name: 'Vineyard', scope: 'globex' },
  ]
  const themes: Theme[] = [
    { id: 'th_pig', name: 'pigtail', kind: 'project', scope: 'perso', space: 'Allotment', archived: false },
    { id: 'th_meth', name: 'Outils', kind: 'ongoing', scope: 'perso', space: 'Allotment', archived: false },
    { id: 'th_seedbed', name: 'Seedbed', kind: 'project', scope: 'perso', space: 'Garden', archived: false },
    { id: 'th_board', name: 'Board', kind: 'project', scope: 'globex', space: 'Vineyard', archived: false },
  ]
  const subjects: Subject[] = [
    s('pig-live', 'th_pig', 'Session relay', 'building', { updated_at: minutesAgo(2) }),
    s('pig-r1', 'th_pig', 'Atlas web : backlog en lecture', 'building', { rank: 1, short_title: 'Atlas web' }),
    s('pig-r2', 'th_pig', 'Atlas palette', 'designing', { rank: 2 }),
    s('pig-u1', 'th_pig', 'Daemon restart', 'understanding', { updated_at: minutesAgo(90) }),
    s('pig-i1', 'th_pig', 'Été : nettoyage', 'inbox', { updated_at: minutesAgo(60 * 30) }),
    s('pig-i2', 'th_pig', 'Inbox two', 'inbox', { updated_at: minutesAgo(60 * 50) }),
    s('pig-i3', 'th_pig', 'Inbox three', 'inbox', { updated_at: minutesAgo(60 * 70) }),
    s('pig-i4', 'th_pig', 'Inbox four', 'inbox', { updated_at: minutesAgo(60 * 90) }),
    s('pig-p1', 'th_pig', 'Parked one', 'parked', { rank: 5 }),
    s('pig-p2', 'th_pig', 'Parked two', 'parked'),
    s('pig-d1', 'th_pig', 'Done atlas', 'done'),
    s('pig-d2', 'th_pig', 'Done two', 'done'),
    s('pig-x1', 'th_pig', 'Dropped one', 'dropped'),
    s('pig-c1', 'th_pig', 'Captured one', 'captured'),
    s('meth-1', 'th_meth', 'Règle des PR', 'inbox'),
    s('watering-1', 'th_seedbed', 'Watering atlas', 'inbox'),
    s('watering-d', 'th_seedbed', 'Seedbed sown', 'done'),
    s('orphan', '', 'Sans thème du tout', 'inbox'),
    s('wk-1', 'th_board', 'Board sync', 'designing', { scope: 'globex' }),
    s('wk-2', 'th_board', 'Board export atlas', 'inbox', { scope: 'globex' }),
    s('wk-x', 'th_board', 'Board old', 'dropped', { scope: 'globex' }),
  ]
  const peers: Peer[] = [{ session_id: 'sess-1', subject_id: 'pig-live', scope: 'perso', alive: true }]
  return { subjects, themes, spaces, peers }
}

/** A capture of a theme, `min` minutes old; open (not promoted, not closed) unless `extra` says otherwise. */
export function cap(id: string, theme_id: string, text: string, scope = 'perso', min = 60, extra: Partial<Capture> = {}): Capture {
  return { id, theme_id, text, scope, created_at: minutesAgo(min), ...extra }
}

/**
 * makeBacklog plus its open captures: two in perso « Seedbed » (the newest first on screen), one in globex « Board »,
 * and one already promoted in « pigtail » (never counted, D14).
 */
export function withCaptures(d: BacklogData = makeBacklog()): BacklogData {
  d.captures = [
    cap('cap-b1', 'th_seedbed', 'Order more clay', 'perso', 60 * 30),
    cap('cap-b2', 'th_seedbed', 'Photo of the vase', 'perso', 90),
    cap('cap-t1', 'th_board', 'Export CSV du board', 'globex', 60 * 5),
    cap('cap-p1', 'th_pig', 'Déjà promue', 'perso', 60, { promoted_subject_id: 'pig-r1' }),
  ]
  return d
}

/** Ids of the subjects of a backlog that the list shows by default (not dropped or captured). */
export function shownIds(d: BacklogData): string[] {
  return d.subjects.filter((x) => x.status !== 'dropped' && x.status !== 'captured').map((x) => x.id)
}
