import type { Peer } from '@/api/types'
import { makeBacklog, type BacklogData } from './backlog'

/**
 * GET /peers?all=1 at the daemon's real shape (pkg/pigtail/messages.go PeerSession, handlers/peers.go GetPeers):
 * `{data: [...], meta}` on the wire (PEERS_ALL_BODY), the array once the client unwrapped it (PEERS_ALL).
 * Go's JSON: times RFC 3339 with nanoseconds and the local offset; `updated_at` and `inbox` always present (no
 * omitempty); `alive` a *bool the decorator always sets; `waits`, `paused_at`, `ended_at`, `subject_*` omitted when
 * empty (omitempty: a session with no wait has NO `waits` key, never `waits: []`). Fields the web front does not read
 * (config_dir, cwd, pid, cmux ids, label, source) are kept to stay the real row.
 *
 * Subjects: sub_ds (theme th_pig « pigtail »), sub_watering (th_seedbed « Seedbed »); sub_ghost is unknown to the caches.
 */
function row(id: string, p: Partial<Peer> & Record<string, unknown>): Peer {
  return {
    session_id: id,
    config_dir: '/Users/ada/.claude-alt',
    cell_id: 'w3',
    scope: 'perso',
    cwd: `/Users/ada/code/home/pigtail-wt/${id}`,
    name: id,
    pid: 41000 + id.length,
    cmux_workspace_id: '8C1F0A52-6D1B-4E59-9A0F-0B2D3C4E5F60',
    cmux_surface_id: 'E3A1B2C4-5D6E-4F70-8192-A3B4C5D6E7F8',
    source: 'snapshot',
    updated_at: '2026-10-27T09:40:12.418305+01:00',
    label: id,
    alive: true,
    inbox: 0,
    ...p,
  } as Peer
}

export const PEERS_ALL: Peer[] = [
  // waiting, a dialog open, nothing typed → Attend, untyped (« En attente »)
  row('s-wait-bare', { status: 'waiting', lifecycle: 'needsInput', subject_id: 'sub_ds', subject_slug: 'atlas-ds', subject_short_title: 'Atlas DS', workspace_name: 'atlas-ds-sessions', updated_at: '2026-10-27T09:31:00.5+01:00' }),
  // waiting + the hook's auto permission → Attend, Permission
  row('s-wait-perm', {
    status: 'waiting',
    lifecycle: 'needsInput',
    cell_id: 'h2',
    subject_id: 'sub_ds',
    subject_short_title: 'Atlas DS',
    waits: [{ id: 'wait_9f2c01', type: 'permission', reason: 'Bash: gh pr merge 412', source: 'auto', since: '2026-10-27T09:37:02.113+01:00' }],
  }),
  // idle + declared arbitrage → Attend, Arbitrage
  row('s-idle-arb', {
    status: 'idle',
    subject_id: 'sub_watering',
    subject_short_title: 'Watering',
    workspace_name: 'watering',
    waits: [{ id: 'wait_7d11aa', type: 'arbitrage', reason: 'Maquette t1 : quel style ?', source: 'declared', since: '2026-10-27T09:12:44.9+01:00' }],
  }),
  // idle + held mac FIRST in waits, then declared question → the gravest: question (never waits[0])
  row('s-idle-q-mac', {
    status: 'idle',
    subject_id: 'sub_ds',
    subject_short_title: 'Atlas DS',
    waits: [
      { type: 'mac', reason: 'busy', source: 'held', since: '2026-10-27T09:05:00+01:00', holder_pid: 51234 },
      { id: 'wait_3b88e0', type: 'question', reason: 'Quel compte ?', source: 'declared', since: '2026-10-27T09:20:00+01:00' },
    ],
  }),
  // idle + auto limit (a StopFailure) → Attend, Limite
  row('s-idle-limit', {
    status: 'idle',
    cell_id: 'w1',
    waits: [{ type: 'limit', source: 'auto', since: '2026-10-27T08:58:10+01:00', until: '2026-10-27T11:00:00+01:00' }],
  }),
  // idle + a stale auto permission (a dialog long closed) → ignored → Inactives
  row('s-idle-stale', {
    status: 'idle',
    subject_id: 'sub_ghost',
    subject_short_title: 'Fantôme',
    waits: [{ id: 'wait_01aa02', type: 'permission', source: 'auto', since: '2026-10-27T07:00:00+01:00' }],
  }),
  // shell (idle with a live background shell) + declared working → Travaille
  row('s-shell-working', {
    status: 'shell',
    subject_id: 'sub_watering',
    subject_short_title: 'Watering',
    waits: [{ id: 'wait_5c0e9d', type: 'working', reason: 'CI', source: 'declared', since: '2026-10-27T09:25:00+01:00' }],
  }),
  // shell, nothing declared → Inactives
  row('s-shell-bare', { status: 'shell' }),
  // busy + declared question → Travaille (a mid-turn declaration shows once the agent hands back)
  row('s-busy-q', {
    status: 'busy',
    subject_id: 'sub_ds',
    subject_short_title: 'Atlas DS',
    waits: [{ id: 'wait_77aa10', type: 'question', source: 'declared', since: '2026-10-27T09:33:00+01:00' }],
  }),
  // idle + declared review → Inactives (D14: green, the session rests)
  row('s-idle-review', {
    status: 'idle',
    subject_id: 'sub_ds',
    subject_short_title: 'Atlas DS',
    waits: [{ id: 'wait_8e0f21', type: 'review', reason: 'PR #278', source: 'declared', since: '2026-10-27T08:40:00+01:00' }],
  }),
  // waiting + declared review → Attend (a dialog is open: waiting wins over the green type)
  row('s-wait-review', {
    status: 'waiting',
    lifecycle: 'needsInput',
    subject_id: 'sub_watering',
    subject_short_title: 'Watering',
    waits: [{ id: 'wait_8e0f22', type: 'review', reason: 'Plan', source: 'declared', since: '2026-10-27T09:01:00+01:00' }],
  }),
  // idle + declared done → Inactives
  row('s-idle-done', {
    status: 'idle',
    waits: [{ id: 'wait_d0e001', type: 'done', source: 'declared', since: '2026-10-27T08:00:00+01:00' }],
  }),
  // paused by pigtaild: alive false, paused_at set → visible, Inactives
  row('s-paused', {
    status: 'idle',
    alive: false,
    paused_at: '2026-10-27T07:45:30.25+01:00',
    subject_id: 'sub_ds',
    subject_short_title: 'Atlas DS',
    last_worktree: '/Users/ada/code/home/pigtail-wt/atlas-ds-sessions',
  }),
  // ended (SessionEnd): not visible
  row('s-ended', { status: 'idle', alive: false, ended_at: '2026-10-26T22:10:00+01:00' }),
  // globex, busy, no subject → Travaille, « Sans sujet »
  row('s-wk-busy', { scope: 'globex', cell_id: 'w4', status: 'busy' }),
]

/** The wire body of GET /peers?all=1: the {data, meta} envelope the client unwraps. */
export const PEERS_ALL_BODY = { data: PEERS_ALL, meta: { request_id: 'req_peers_all' } }

/** One peer of the fixture by id. */
export function peerOf(id: string): Peer {
  const p = PEERS_ALL.find((x) => x.session_id === id)
  if (!p) throw new Error(`no peer ${id} in the fixture`)
  return p
}

/** The web front's backlog plus the fixture's two subjects (sub_ds in « pigtail », sub_watering in « Seedbed ») and its sessions: /peers lists the live ones, /peers?all=1 every one. */
export function withSessions(d: BacklogData = makeBacklog()): BacklogData {
  d.subjects.push(
    { id: 'sub_ds', theme_id: 'th_pig', title: 'Atlas : DS Hybride et sessions', short_title: 'Atlas DS', status: 'building', scope: 'perso', updated_at: '2026-10-27T08:00:00Z', created_at: '2026-10-20T08:00:00Z' },
    { id: 'sub_watering', theme_id: 'th_seedbed', title: 'Watering recipe sheet', short_title: 'Watering', status: 'inbox', scope: 'perso', updated_at: '2026-10-27T08:00:00Z', created_at: '2026-10-20T08:00:00Z' },
  )
  d.peers = PEERS_ALL.filter((p) => p.alive === true)
  d.peersAll = PEERS_ALL
  return d
}
