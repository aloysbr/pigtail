// Wire shapes of the routes the web front reads (docs/wire-protocol.md); only the fields it uses.

/** A subject as GET /subjects returns it (pkg/pigtail/types.go Subject). */
export interface Subject {
  id: string
  theme_id: string
  title: string
  status: string
  slug?: string
  rank?: number
  short_title?: string
  scope?: string
  vault_path?: string
  updated_at: string
  created_at: string
}

/** A pigtail theme: the project level; space is the area's name, "" when none. */
export interface Theme {
  id: string
  name: string
  kind: string
  scope?: string
  space?: string
  archived: boolean
}

/** An area (pigtail space), GET /spaces in folder order. */
export interface Space {
  name: string
  scope: string
}

/** One wait of a session (pkg/pigtail/messages.go SessionWait): at most one auto, one declared, one held (type mac). */
export interface SessionWait {
  id?: string
  /** permission|question|arbitrage|review|blocked|done|working|wrapped; limit|login|api_error (auto only); mac (held only). */
  type: string
  reason?: string
  source: 'auto' | 'declared' | 'held' | string
  since: string
  until?: string
  holder_pid?: number
}

/** One option of an arbitrage or a question (pkg/pigtail/waits.go WaitOption). */
export interface WaitOption {
  id: string
  label: string
  detail?: string
  reco?: boolean
  url?: string
}

/** One item of a review (pkg/pigtail/waits.go WaitItem). */
export interface WaitItem {
  label: string
  detail?: string
  url?: string
}

/** The typed content of a wait (pkg/pigtail/waits.go WaitDetails); only the fields the web front reads. */
export interface WaitDetails {
  title?: string
  summary?: string
  options?: WaitOption[]
  why?: string
  items?: WaitItem[]
  lead?: string
  /** permission: what Claude Code asks to run, and where. */
  cmd?: string
  where?: string
}

/** The answer kept on a wait (pkg/pigtail/waits.go WaitAnswer). */
export interface WaitAnswer {
  choice?: string
  text?: string
  items?: { n: number; ok: boolean; note?: string }[]
  seen?: boolean
  reason?: string
  at: string
}

/** The body of POST /waits/{id}/answer: one form exactly (choice, text or seen), reason a note on it. */
export interface WaitAnswerRequest {
  choice?: string
  text?: string
  seen?: boolean
  reason?: string
}

/**
 * A wait record as GET /waits answers it (a subset of pkg/pigtail/waits.go Wait). An answerable wait bound to a subject
 * belongs to the subject: `session_ended` says its emitting session is gone while the wait stays (D29-A).
 */
export interface Wait {
  id: string
  session_id: string
  session_label?: string
  session_ended?: boolean
  subject_id?: string
  subject_short_title?: string
  type: string
  /** declared (pigtail session wait) | auto (a Claude Code dialog, answered in the terminal only). */
  source: 'declared' | 'auto' | string
  reason?: string
  /** open | answered | replaced | cleared */
  state: string
  opened_at: string
  closed_at?: string
  snoozed_until?: string
  details?: WaitDetails
  answer?: WaitAnswer
  delivered_to?: string
}

/**
 * A session as GET /peers lists it (live ones only without ?all; pkg/pigtail/messages.go PeerSession). Only the fields
 * the web front reads; the daemon omits the empty ones (omitempty), `waits` included when there is none. No model,
 * context, account, real last activity, PR nor servers: none of them is on the wire.
 */
export interface Peer {
  session_id: string
  subject_id?: string
  scope?: string
  /** Computed per response: the daemon always sets it. */
  alive?: boolean
  /** The cell the session runs in (named in the Standby toast). */
  cell_id?: string
  /** Claude's native status: busy | idle | waiting | shell (idle with a live background shell) | … */
  status?: string
  /** cmux agent_lifecycle (needsInput, …). */
  lifecycle?: string
  workspace_name?: string
  /** The title cmux shows on the session's tab (GET /peers cmux_title). */
  cmux_title?: string
  /** Claude's native session name. */
  name?: string
  /** The cmux tab (workspace UUID) the session runs in; absent outside cmux. « Aller à la session » needs it. */
  cmux_workspace_id?: string
  subject_short_title?: string
  /** When pigtaild paused the session; absent = not paused. */
  paused_at?: string
  last_worktree?: string
  inbox?: number
  updated_at?: string
  ended_at?: string
  waits?: SessionWait[]
}

/** One line of a subject's pigtail log, GET /subjects/{id}/log (newest first; pkg/pigtail/types.go SubjectLogEntry). */
export interface LogEntry {
  id: string
  subject_id: string
  ts: string
  kind: string
  text: string
}

/** One artifact produced for a subject, GET /subjects/{id}/outputs (seq ascending; pkg/pigtail/subject_outputs.go). Times are epoch ms. */
export interface Output {
  seq: number
  id: string
  subject_id: string
  kind: string
  path?: string
  url?: string
  key: string
  title: string
  ts: number
  purged_at?: number
  /** Whether the file is still on disk (GET only; path outputs). */
  exists?: boolean
  /** The URL the outputs server serves it at (GET only; a path output that exists, the listener up). */
  served_url?: string
}

/** One round of a deliverable (pkg/pigtail OutputTour). */
export interface OutputTour {
  /** 0 outside a round. */
  n: number
  /** The round folder's name (t42-teams), the file name outside a round. */
  label: string
  output_id: string
  title: string
  /** Epoch ms. */
  ts: number
  path: string
  served_url?: string
  exists: boolean
}

/** A deliverable seen as its rounds (GET /subjects/{id}/outputs/groups, D2): tours by N ascending, the last one the latest. */
export interface OutputGroup {
  key: string
  name: string
  kind: string
  first_seq: number
  tours: OutputTour[]
}

/** A capture as GET /captures lists it (pkg/pigtail/types.go Capture); scope derived from its theme. */
export interface Capture {
  id: string
  theme_id: string
  text: string
  from_subject_id?: string
  from_prev_status?: string
  promoted_subject_id?: string
  scope: string
  created_at: string
  closed_at?: string
}

/** One session launch (pkg/pigtail/session_launch.go SessionLaunch); every field on the wire, "" = unknown. */
export interface SessionLaunch {
  launch_id: string
  subject_id: string
  state: 'pending' | 'launched' | 'failed'
  cause: string
  origin: string
  workspace_id: string
  workspace_ref: string
  cell: string
  permission_mode: string
  cwd: string
  /** The cmux tab's name (the subject's ShortTitle): set on the POST answer only, "" on GET /launches/{id}. */
  tab_title: string
  started_at: string
  updated_at: string
  deduplicated: boolean
}

/** The states of a SDK session (docs/wire-protocol.md § SDK sessions); `idle` = a turn is over, the session waits for a reply. */
export type SdkState = 'starting' | 'running' | 'waiting' | 'idle' | 'ended' | 'failed' | 'lost'

/** One question of an AskUserQuestion request. */
export interface SdkQuestion {
  question: string
  header?: string
  multiSelect?: boolean
  options: { label: string; description?: string }[]
}

/** A pending request of a SDK session: a question (AskUserQuestion) or a permission (canUseTool). `input` is the tool's own input. */
export interface SdkRequest {
  rid: string
  kind: 'question' | 'permission'
  tool: string
  input: { questions?: SdkQuestion[] } & Record<string, unknown>
  opened_at: string
}

/** GET /sdk-sessions[/{id}]: one SDK session. */
export interface SdkSession {
  id: string
  subject_id: string
  cwd: string
  config_dir: string
  claude_session_id: string
  state: SdkState
  reason?: string
  resume_command?: string
  host_pid: number
  claude_pids: number[]
  turn: number
  pending: SdkRequest[]
  live: boolean
  started_at: string
}

/** One line of a session's journal. */
export interface SdkEvent {
  seq: number
  at: string
  kind: string
  data: Record<string, unknown>
}

/** GET /sdk-sessions?subject=: the sessions and whether `Lancer ici` is open (`-sdk-launch`). */
export interface SdkList {
  sessions: SdkSession[]
  launch_enabled: boolean
}

/** POST /sdk-sessions/{id}/requests/{rid}/answer body. */
export interface SdkAnswer {
  behavior: 'allow' | 'deny'
  answers?: Record<string, string | string[]>
  message?: string
}
