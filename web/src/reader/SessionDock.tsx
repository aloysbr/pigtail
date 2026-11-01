import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState, type PointerEvent as RPointerEvent } from 'react'
import { ApiError, useApi } from '@/api/client'
import { useSend } from '@/api/mutate'
import { REFETCH_MS, ROUTES } from '@/api/queries'
import { PEERS_ALL_KEY } from '@/api/sessions'
import type { Peer, Subject, Wait } from '@/api/types'
import { answerText, useSessionWaits, useSubjectWaits } from '@/api/waits'
import { hhmm, peerTitle, waitLabel } from '@/domain/sessions'
import { cn, isTyping } from '@/lib/utils'
import { getUi } from '@/state/ui'
import { PendingButton } from '@/ui/PendingButton'
import { pushToast } from '@/ui/toasts'
import { answerForm, WaitCard } from '@/ui/WaitCard'
import { CARD_MARGIN, clampCard, useDockForm, type Pos } from './dockForm'
import { Dot, type DotGroup } from './Dot'
import { NoSession } from './NoSession'

/** What the dock shows of its session (spec § Panneau de session). */
export type DockKind = 'wait' | 'native' | 'sent' | 'ended' | 'work'

export interface DockView {
  kind: DockKind
  /** The wait in the card: the open one, or the one just answered (« Réponse envoyée »). */
  card: Wait | null
  /** The open wait outlived its session (D29-A): its answer goes to the subject's next session. */
  ended: boolean
  /** The answered waits shown above the card, oldest first. */
  exchanges: Wait[]
}

function time(iso: string | undefined): number {
  const t = iso ? Date.parse(iso) : Number.NaN
  return Number.isNaN(t) ? 0 : t
}

/** Live peers of a subject, the most recent first. */
function liveOf(subjectId: string, peers: readonly Peer[]): Peer[] {
  return peers
    .filter((p) => p.subject_id === subjectId && p.alive === true && !p.ended_at)
    .sort((a, b) => time(b.updated_at) - time(a.updated_at))
}

/**
 * The session the dock shows (D8): one picked in the list (T14) first; else the one an open wait of the subject comes
 * from (the waits never wait for /peers, R33); else the subject's most recent live session; null when none.
 */
export function pickSession(subjectId: string, waits: readonly Wait[], peers: readonly Peer[], chosen: string | null): string | null {
  if (chosen) return chosen
  const open = waits.filter((w) => w.state === 'open' && w.subject_id === subjectId).sort((a, b) => time(b.opened_at) - time(a.opened_at))
  if (open.length) return open[0].session_id
  return liveOf(subjectId, peers)[0]?.session_id ?? null
}

/**
 * The state of the shown session from its own waits (never another session's) and its peer: an open wait first (a native
 * dialog or one to answer; its end read from `session_ended`); else an answer given since the dock opened (« Réponse
 * envoyée », ended session included: the card stays mounted, a refusal gives the draft back); else ended or working, read from /peers (unknown while /peers has not answered: working).
 */
export function dockView(waits: readonly Wait[], peer: Peer | undefined, peersRead: boolean, since: number): DockView {
  const open = waits.filter((w) => w.state === 'open').sort((a, b) => time(b.opened_at) - time(a.opened_at))[0]
  const answered = waits
    .filter((w) => w.state !== 'open' && w.answer)
    .sort((a, b) => time(a.closed_at ?? a.answer?.at) - time(b.closed_at ?? b.answer?.at))
  if (open) return { kind: answerForm(open) === 'native' ? 'native' : 'wait', card: open, ended: !!open.session_ended, exchanges: answered.slice(-1) }
  const ended = peersRead && (!peer || peer.alive !== true || !!peer.ended_at)
  const last = answered.at(-1)
  if (last && time(last.answer?.at) >= since) return { kind: 'sent', card: last, ended, exchanges: answered.slice(0, -1).slice(-2) }
  return { kind: ended ? 'ended' : 'work', card: null, ended, exchanges: answered.slice(-2) }
}

/** The dot's group: waiting (the accent and its halo), working (filled grey), ended (the grey ring). */
function dotOf(v: DockView): DotGroup {
  if (v.kind === 'ended' || v.ended) return 'idle'
  return v.kind === 'wait' || v.kind === 'native' ? 'wait' : 'work'
}

const WORDS: Record<DockKind, string> = { wait: 'Attend ta réponse', native: 'Attend ta réponse', sent: 'Reprend', work: 'Travaille', ended: '' }

/** The toast of a refused « Aller à la session » (POST /peers/{session}/reveal): what happened, what to do. */
export const REVEAL_COPY = {
  closed: "Son onglet cmux est fermé : la session n'a pas pu être affichée. Retrouve-la dans cmux.",
  unknown: "Cette session est introuvable : elle n'a pas pu être affichée.",
  cmux: "cmux ne répond pas : la session n'a pas pu être affichée. Réessaie dans un instant.",
} as const

function revealRefusal(e: unknown): string {
  if (e instanceof ApiError && e.status === 404) return REVEAL_COPY.unknown
  return e instanceof ApiError && e.status === 409 ? REVEAL_COPY.closed : REVEAL_COPY.cmux
}

/** « Attend ta réponse · Vivante », « Travaille · Vivante », « Terminée ». */
function lifeLine(v: DockView): string {
  const life = v.kind === 'ended' || v.ended ? 'Terminée' : 'Vivante'
  return WORDS[v.kind] && !v.ended ? `${WORDS[v.kind]} · ${life}` : life
}

function Exchanges({ waits }: { waits: Wait[] }) {
  return (
    <ol className="sd-x" aria-label="Échanges récents">
      {waits.map((w) => (
        <li key={w.id}>
          <span className="sd-xk">
            {waitLabel(w.type)} · {hhmm(w.closed_at ?? w.answer?.at ?? '')}
          </span>
          {w.reason ? <span className="sd-xq">{w.reason}</span> : null}
          <span className="sd-xa">
            <b>Toi</b> {w.answer ? answerText(w, w.answer) : ''}
          </span>
        </li>
      ))}
    </ol>
  )
}

const I_FOLD_DOWN = 'M4.5 6.5 8 10l3.5-3.5'
const I_FOLD_SIDE = 'M6 3.5 10.5 8 6 12.5'
const I_GRIP = 'M6 4h.01M10 4h.01M6 8h.01M10 8h.01M6 12h.01M10 12h.01'

function Icon({ d }: { d: string }) {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d={d} fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

/** Every session (GET /peers?all=1), re-read at the backlog's cadence while the dock shows: it says a session ended. */
function usePeersAllLive() {
  const get = useApi()
  return useQuery({ queryKey: PEERS_ALL_KEY, queryFn: () => get<Peer[]>(ROUTES.peersAll), refetchInterval: REFETCH_MS })
}

/**
 * The session dock (D5–D8, t2 « .sp »), inside the reader: the session of the open deliverable's subject, in three forms
 * by width (side band, floating card that moves by its head, bottom sheet), folded into a pill. `r` outside a field folds
 * and unfolds (focus into the field); Esc in the field folds, a second Esc closes the reader (Shell).
 */
export function SessionDock({ subjectId }: { subjectId: string }) {
  const form = useDockForm()
  const qc = useQueryClient()
  const send = useSend()
  const [revealing, setRevealing] = useState(false)
  const [since] = useState(() => Date.now())
  const [chosen, setChosen] = useState<string | null>(null)
  const [folded, setFolded] = useState<boolean | null>(null)
  const [pos, setPos] = useState<Pos | null>(null)
  const panel = useRef<HTMLElement>(null)
  const field = useRef<HTMLTextAreaElement>(null)
  const pill = useRef<HTMLButtonElement>(null)
  const focusNext = useRef<'field' | 'pill' | null>(null)
  const drag = useRef<{ px: number; py: number; x: number; y: number; w: number; h: number; bw: number; bh: number } | null>(null)

  const subjectWaits = useSubjectWaits(subjectId, { closed: 2 }, true)
  const peers = usePeersAllLive()
  const sessionWaits = useSessionWaits(chosen ?? '', { closed: 2 }, true)
  // the shown session stays once picked: when it ends it leaves the live list, and the dock then says « Session terminée »
  const kept = useRef<{ subjectId: string; sid: string } | null>(null)
  const picked = pickSession(subjectId, subjectWaits.data ?? [], peers.data ?? [], chosen)
  const sid = picked ?? (kept.current?.subjectId === subjectId ? kept.current.sid : null)
  if (sid) kept.current = { subjectId, sid }
  const peer = sid ? peers.data?.find((p) => p.session_id === sid) : undefined
  // a session picked in the list reads its own waits (?session=), never the subject's: the dock never mixes two sessions
  const waits = chosen ? (sessionWaits.data ?? []) : (subjectWaits.data ?? []).filter((w) => w.session_id === sid)
  const view = sid ? dockView(waits, peer, peers.data !== undefined, since) : null
  const isFolded = folded ?? !view

  const toggle = () => {
    const next = !isFolded
    focusNext.current = next ? 'pill' : 'field'
    setFolded(next)
  }

  useEffect(() => {
    const want = focusNext.current
    if (!want) return
    focusNext.current = null
    if (want === 'pill') pill.current?.focus()
    else (field.current ?? panel.current)?.focus()
  })

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey || e.key !== 'r' || isTyping(e.target) || getUi().preview) return
      e.preventDefault()
      toggle()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  })

  // a card dragged wide then a narrower window: the stored corner is clamped again (answer field and Send stay on screen)
  useEffect(() => {
    if (!pos || form !== 'card') return
    const fit = () => {
      const card = panel.current
      const box = card?.closest('.rd-body')
      if (!card || !box) return
      const c = card.getBoundingClientRect()
      const b = box.getBoundingClientRect()
      const next = clampCard(pos, { w: c.width, h: c.height }, { w: b.width, h: b.height }, CARD_MARGIN)
      if (next.x !== pos.x || next.y !== pos.y) setPos(next)
    }
    fit()
    window.addEventListener('resize', fit)
    return () => window.removeEventListener('resize', fit)
  }, [pos, form, isFolded])

  const subjects = qc.getQueryData<Subject[]>(['subjects']) ?? []
  const s = subjects.find((x) => x.id === subjectId)
  const title = s?.short_title || s?.title || view?.card?.subject_short_title || ''
  const name = !sid
    ? ''
    : (peer ? peerTitle(peer) : '') || view?.card?.session_label || waits.find((w) => w.session_label)?.session_label || sid.slice(0, 8)
  // no session on the subject (D8, T14): the grey ring, « Aucune session », the launch and the scope's sessions
  const label = view ? `Session ${name}` : 'Aucune session'
  const g = view ? dotOf(view) : 'idle'
  const waiting = view?.kind === 'wait' || view?.kind === 'native'
  // « Aller à la session » (T15): the native card only, and only for a session cmux holds a tab of
  const canReveal = view?.kind === 'native' && !!sid && !!peer?.cmux_workspace_id
  const reveal = () => {
    if (!sid || revealing) return
    setRevealing(true)
    send('POST', `/peers/${encodeURIComponent(sid)}/reveal`)
      .catch((e: unknown) => pushToast({ text: revealRefusal(e) }))
      .finally(() => setRevealing(false))
  }
  const choose = (id: string) => {
    focusNext.current = 'field'
    setChosen(id)
  }
  // the way back to the picker: the dock forgets the picked session (and the one it kept)
  const back = () => {
    kept.current = null
    focusNext.current = 'field'
    setChosen(null)
  }

  if (isFolded) {
    const badge = !view ? null : waiting ? (
      <span className="chip-attn">{waitLabel(view.card?.type ?? '')}</span>
    ) : (
      <span className="muted">{view.ended || view.kind === 'ended' ? 'Terminée' : view.kind === 'sent' ? 'Reprend' : 'Travaille'}</span>
    )
    return (
      <div className="sd" data-session-dock="">
        <button ref={pill} type="button" className={cn('sd-pill', waiting && 'sess-wait')} aria-label={`${label}, déplier (r)`} aria-keyshortcuts="r" onClick={toggle}>
          <Dot g={g} />
          <b>{view ? name : label}</b>
          {badge}
          <kbd>r</kbd>
        </button>
      </div>
    )
  }

  const onDown = (e: RPointerEvent<HTMLDivElement>) => {
    const card = panel.current
    const box = card?.closest('.rd-body')
    if (form !== 'card' || !card || !box || e.button !== 0 || (e.target as HTMLElement).closest('button')) return
    const c = card.getBoundingClientRect()
    const b = box.getBoundingClientRect()
    drag.current = { px: e.clientX, py: e.clientY, x: c.left - b.left, y: c.top - b.top, w: c.width, h: c.height, bw: b.width, bh: b.height }
    try {
      e.currentTarget.setPointerCapture(e.pointerId)
    } catch {
      // no capture (an old engine): the moves still reach the head while the pointer stays on it
    }
  }
  const onMove = (e: RPointerEvent<HTMLDivElement>) => {
    const d = drag.current
    if (!d) return
    setPos(clampCard({ x: d.x + e.clientX - d.px, y: d.y + e.clientY - d.py }, { w: d.w, h: d.h }, { w: d.bw, h: d.bh }, CARD_MARGIN))
  }
  const onUp = () => {
    drag.current = null
  }

  const note =
    view?.kind === 'wait' && view.ended ? (
      <p className="sd-note">
        <b>Session terminée</b> — ta réponse ira à sa prochaine session
      </p>
    ) : null
  const foot =
    view?.kind === 'native' ? (
      <>
        <p className="sd-note">
          <b>Cette question se répond dans le terminal.</b>
        </p>
        {canReveal ? (
          <PendingButton primary pending={revealing} label="Aller à la session" busy="Aller à la session" onClick={reveal} />
        ) : null}
      </>
    ) : view?.kind === 'sent' && view.ended ? (
      <p className="sd-note">
        <b>Session terminée</b> — ta réponse ira à sa prochaine session
      </p>
    ) : view?.kind === 'sent' ? (
      <p className="sd-note">Tu pourras répondre à sa prochaine attente.</p>
    ) : view?.kind === 'work' ? (
      <p className="sd-note">
        <b>La session travaille.</b> Tu pourras lui répondre à sa prochaine attente.
      </p>
    ) : view?.kind === 'ended' ? (
      <p className="sd-note">
        <b>Session terminée.</b>
      </p>
    ) : null

  // no body, no empty band: a working or ended session has only its foot note
  const hasBody = !view || view.exchanges.length > 0 || !!note || !!view.card

  return (
    <div className="sd" data-session-dock="">
      <section
        ref={panel}
        className={cn('sd-panel', `sd-${form}`)}
        role="region"
        aria-label={label}
        tabIndex={-1}
        style={form === 'card' && pos ? { left: pos.x, top: pos.y, right: 'auto', bottom: 'auto' } : undefined}
      >
        <div className="sd-h" onPointerDown={onDown} onPointerMove={onMove} onPointerUp={onUp} onPointerCancel={onUp}>
          {form === 'card' ? (
            <span className="sd-grip" aria-hidden="true">
              <Icon d={I_GRIP} />
            </span>
          ) : null}
          <Dot g={g} />
          <span className="sd-id">
            <b>{title}</b>
            <span>{view ? `${lifeLine(view)}${name && name !== title ? ` · ${name}` : ''}` : label}</span>
          </span>
          <span className="c-sp" />
          <button type="button" className="ib sd-fold" aria-label="Replier (r)" aria-keyshortcuts="r" onClick={toggle}>
            <Icon d={form === 'side' ? I_FOLD_SIDE : I_FOLD_DOWN} />
            <kbd>r</kbd>
          </button>
        </div>
        {hasBody ? (
        <div className="sd-b">
          {!view ? <NoSession subjectId={subjectId} peers={peers.data ?? []} now={Date.now()} onChoose={choose} /> : null}
          {view?.exchanges.length ? <Exchanges waits={view.exchanges} /> : null}
          {note}
          {/* one slot, one key: the card survives wait → sent → refused, and its draft with it */}
          {view?.card ? <WaitCard key={view.card.id} variant="answer" wait={view.card} now={Date.now()} fieldRef={field} onFieldEscape={toggle} ended={view.ended} /> : null}
        </div>
        ) : null}
        {foot || chosen ? (
          <div className="sd-f">
            {foot}
            {chosen ? (
              <button type="button" className="ql-z sd-back" onClick={back}>
                Choisir une autre session
              </button>
            ) : null}
          </div>
        ) : null}
      </section>
    </div>
  )
}
