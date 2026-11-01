import type { Peer, SessionWait, Subject, Theme } from '@/api/types'
import { fold } from './search'

/**
 * The Sessions view's classing, ported from the daemon's sidebar loop (internal/daemon/cmuxsync/compute.go `waitOf` and `gravest`,
 * internal/waittype `Order` and gravity.go) onto the fields GET /peers exposes. Known gaps, read by the daemon at
 * sources /peers does not expose: (a) BusySeen (« Ton tour »), (b) the native WaitingFor, (c) dropping an auto wait
 * older than the native status. Pure: no I/O, no clock.
 */

/** The three groups of the view: « Attend ta réponse », « Travaille », « Inactives ». */
export type SessionGroup = 'wait' | 'work' | 'idle'

export const GROUP_ORDER: readonly SessionGroup[] = ['wait', 'work', 'idle']

export const GROUP_LABEL: Record<SessionGroup, string> = {
  wait: 'Attend ta réponse',
  work: 'Travaille',
  idle: 'Inactives',
}

/** waittype.Order, most severe first; '' is the untyped « Waiting for you ». A type off the list ranks last. */
export const WAIT_ORDER: readonly string[] = [
  'permission', 'blocked', 'limit', 'login', 'dead', 'question', 'arbitrage', '', 'respawn',
  'api_error', 'mac', 'your_turn', 'review', 'done', 'working', 'wrapped',
]

/** Orange and Red of waittype/gravity.go (D14): the types that ask the user for something. */
const ASKS: ReadonlySet<string> = new Set(['permission', 'blocked', 'question', 'arbitrage', '', 'limit', 'login', 'api_error', 'mac'])

/** waittype.HookOnly: the failures an auto wait carries when a turn ended on one (StopFailure). */
const FAILURES: ReadonlySet<string> = new Set(['limit', 'login', 'api_error'])

function rank(t: string): number {
  const i = WAIT_ORDER.indexOf(t)
  return i < 0 ? WAIT_ORDER.length : i
}

/** waittype.Before: a is strictly more severe than b. */
function before(a: string, b: string): boolean {
  return rank(a) < rank(b)
}

/** The wait the daemon retains for a session: its type ('' = untyped) and, when one is on the wire, the wait itself. */
export interface Retained {
  type: string
  wait: SessionWait | null
}

/** cmuxsync.gravest over every declared and held wait: the most severe, the declared one on a tie; null when none. */
function gravest(waits: readonly SessionWait[]): SessionWait | null {
  let best: SessionWait | null = null
  for (const w of waits) {
    if (w.source !== 'declared' && w.source !== 'held') continue
    if (!best || before(w.type, best.type) || (w.type === best.type && w.source === 'declared' && best.source !== 'declared')) best = w
  }
  return best
}

/** A session the view lists: alive, or paused by pigtaild; never one that ended. */
export function visible(p: Peer): boolean {
  return !p.ended_at && (p.alive === true || !!p.paused_at)
}

/** The wait the daemon shows for a session (cmuxsync.waitOf), null when it shows none. */
export function waitOf(p: Peer): Retained | null {
  if (p.alive !== true) return null
  const waits = p.waits ?? []
  const auto = waits.find((w) => w.source === 'auto') ?? null
  switch (p.status) {
    case 'waiting': {
      // a dialog is open: the hook's type, else the gravest declared/held, else untyped
      if (auto) return { type: auto.type, wait: auto }
      const g = gravest(waits)
      return g ? { type: g.type, wait: g } : { type: '', wait: null }
    }
    case 'idle':
    case 'shell': {
      // « shell » is Claude's idle with a background shell alive (cmuxsync/native.go turnStatus)
      const g = gravest(waits)
      if (auto && FAILURES.has(auto.type)) {
        // the failure wins over anything it outranks or ties (D8, compute.go:260-267)
        if (g && before(g.type, auto.type)) return { type: g.type, wait: g }
        return { type: auto.type, wait: auto }
      }
      return g ? { type: g.type, wait: g } : null
    }
    default:
      // busy: a declaration made mid-turn shows once the agent hands back
      return null
  }
}

/** The group of a session (D14): Attend for an Orange or Red type, or any open dialog; Travaille for busy or working. */
export function groupOf(p: Peer): SessionGroup {
  if (p.alive !== true) return 'idle'
  if (p.status === 'busy') return 'work'
  if (p.status === 'waiting') return 'wait'
  const w = waitOf(p)
  if (!w) return 'idle'
  if (ASKS.has(w.type)) return 'wait'
  if (w.type === 'working') return 'work'
  return 'idle'
}

/** C1 `attente-libelles`: the French label of a wait type. */
const LABELS: Record<string, string> = {
  permission: 'Permission',
  blocked: 'Bloqué',
  question: 'Question',
  arbitrage: 'Arbitrage',
  '': 'En attente',
  limit: 'Limite',
  login: 'Connexion',
  api_error: 'Erreur API',
  mac: 'Mac occupé',
  review: 'Revue',
  done: 'Terminé',
  working: 'En arrière-plan',
  wrapped: 'Clôturée',
}

export function waitLabel(type: string): string {
  return LABELS[type] ?? LABELS['']
}

/** Since when: the retained wait's `since` for a session that waits, else `updated_at` (no real last activity on the wire). */
export function sinceOf(p: Peer): string {
  if (groupOf(p) === 'wait') {
    const w = waitOf(p)?.wait
    if (w?.since) return w.since
  }
  return p.updated_at ?? ''
}

/**
 * The readable name of a session, the rule of `pigtail session ls` LABEL without its handle and cell: the subject's short
 * title, else the title of its cmux tab, else its worktree, native name; '' when only the id is left (the caller names a
 * fallback before the short id, the last resort).
 */
export function peerTitle(p: Peer): string {
  return p.subject_short_title || p.cmux_title || p.workspace_name || p.name || ''
}

/** The session's readable name, the short id as the last resort. */
export function peerName(p: Peer): string {
  return peerTitle(p) || p.session_id.slice(0, 8)
}

/** C6 `vie-statuts`: the life of a session in words. */
export type Life = { kind: 'alive' } | { kind: 'paused'; at: string } | { kind: 'inactive' }

export function lifeOf(p: Peer): Life {
  if (p.alive === true) return { kind: 'alive' }
  if (p.paused_at) return { kind: 'paused', at: p.paused_at }
  return { kind: 'inactive' }
}

/** « HH:MM » of an ISO time in the browser's zone; '' when unreadable. */
export function hhmm(iso: string): string {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return ''
  const d = new Date(t)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

export function lifeLabel(l: Life): string {
  if (l.kind === 'alive') return 'Vivante'
  if (l.kind === 'paused') return `En pause depuis ${hhmm(l.at)}`
  return 'Inactive'
}

/** C4: a session with no subject, or one the caches do not know. */
export const NO_SUBJECT = 'Sans sujet'

/** The subject a session works on, as the view names it. */
export interface SubjectRef {
  /** The subject id, null for « Sans sujet ». */
  id: string | null
  /** The project (theme) name, '' when none. */
  project: string
  title: string
}

export function subjectOf(p: Peer, subjects: readonly Subject[], themes: readonly Theme[]): SubjectRef {
  const s = p.subject_id ? subjects.find((x) => x.id === p.subject_id) : undefined
  if (!s) return { id: null, project: '', title: NO_SUBJECT }
  const th = themes.find((t) => t.id === s.theme_id)
  return { id: s.id, project: th?.name ?? '', title: p.subject_short_title || s.short_title || s.title }
}

/** One subject block of a group: « projet › titre court » and its sessions. */
export interface SubjectBlock {
  key: string
  subject: SubjectRef
  peers: Peer[]
}

function time(iso: string): number {
  const t = Date.parse(iso)
  return Number.isNaN(t) ? Number.POSITIVE_INFINITY : t
}

/** Sessions oldest first (stable), grouped by subject, each block placed by its oldest session. */
export function bySubject(peers: readonly Peer[], subjects: readonly Subject[], themes: readonly Theme[]): SubjectBlock[] {
  const sorted = peers
    .map((p, i) => ({ p, i, t: time(sinceOf(p)) }))
    .sort((a, b) => a.t - b.t || a.i - b.i)
    .map((x) => x.p)
  const blocks = new Map<string, SubjectBlock>()
  for (const p of sorted) {
    const subject = subjectOf(p, subjects, themes)
    const key = subject.id ?? '∅'
    const b = blocks.get(key)
    if (b) b.peers.push(p)
    else blocks.set(key, { key, subject, peers: [p] })
  }
  return [...blocks.values()]
}

/** The view's model: each group's sessions by subject, and its count. */
export interface SessionsModel {
  groups: { g: SessionGroup; count: number; blocks: SubjectBlock[] }[]
  /** Session ids in screen order (j/k, ↑/↓). */
  order: string[]
}

export function sessionsModel(peers: readonly Peer[], subjects: readonly Subject[], themes: readonly Theme[]): SessionsModel {
  const shown = peers.filter(visible)
  const groups = GROUP_ORDER.map((g) => {
    const mine = shown.filter((p) => groupOf(p) === g)
    return { g, count: mine.length, blocks: bySubject(mine, subjects, themes) }
  })
  const order = groups.flatMap((x) => x.blocks.flatMap((b) => b.peers.map((p) => p.session_id)))
  return { groups, order }
}

/** Attend before Travaille before Inactives, stable inside each (the linked sessions of a subject sheet). */
export function byGroup(peers: readonly Peer[]): Peer[] {
  return peers
    .map((p, i) => ({ p, i, g: GROUP_ORDER.indexOf(groupOf(p)) }))
    .sort((a, b) => a.g - b.g || a.i - b.i)
    .map((x) => x.p)
}

/** « N vivantes »: the live sessions of GET /peers (the backlog's own read, always mounted); the sidebar and the view's header both read it here. */
export function liveCount(peers: readonly Peer[]): number {
  return peers.filter((p) => p.alive === true).length
}

/** The Sessions view's text filter: subject, project, cell, worktree, wait type and reason, accents and case ignored. */
export function sessionMatches(p: Peer, query: string, subjects: readonly Subject[], themes: readonly Theme[]): boolean {
  const q = fold(query.trim())
  if (!q) return true
  const s = subjectOf(p, subjects, themes)
  const w = groupOf(p) === 'wait' ? waitOf(p) : null
  const hay = [s.title, s.project, p.cell_id, p.workspace_name, p.last_worktree, p.session_id, w ? waitLabel(w.type) : '', w?.wait?.reason]
  return hay.some((x) => !!x && fold(x).includes(q))
}
