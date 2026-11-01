import { useQuery } from '@tanstack/react-query'
import { useApi } from '@/api/client'
import { ROUTES } from '@/api/queries'
import type { Peer, Subject } from '@/api/types'
import { liveSet } from '@/domain/group'
import { ageOf } from '@/domain/age'
import { byGroup, groupOf, lifeLabel, lifeOf, peerName, sinceOf, visible, waitLabel, waitOf } from '@/domain/sessions'
import { displayStatus } from '@/domain/status'
import { canLaunch, useLaunch, useLaunching } from '@/list/launch'
import { STATE_TEXT } from '@/sessions/SessionRow'
import { cn } from '@/lib/utils'
import { PendingButton } from '@/ui/PendingButton'
import { Dot } from './Dot'

const I_PLUS = 'M8 3.5v9M3.5 8h9'

/** The sessions the list offers: the visible ones of the subject's scope, whatever subject they are bound to (D12), waiting first, then working, then resting. */
export function scopeSessions(scope: string | undefined, peers: readonly Peer[]): Peer[] {
  return byGroup(peers.filter((p) => visible(p) && (p.scope ?? '') === (scope ?? '')))
}

/**
 * One session of the picker: a real button (Tab, Enter, Space) telling it apart from the others: its readable name,
 * the wait's whole question when it waits, then its subject's short title (« Libre » when bound to none), its state written once, and since when.
 */
function PickRow({ p, now, onChoose }: { p: Peer; now: number; onChoose: (id: string) => void }) {
  const g = groupOf(p)
  const w = g === 'wait' ? waitOf(p) : null
  const life = lifeOf(p)
  const age = ageOf(sinceOf(p), now)
  const state = life.kind === 'alive' ? STATE_TEXT[g] : `${STATE_TEXT[g]} · ${lifeLabel(life)}`
  const subject = p.subject_short_title || 'Libre'
  const meta = [subject === peerName(p) ? '' : subject, state, age ? `depuis ${age}` : ''].filter(Boolean).join(' · ')
  return (
    <li className={cn('pk', `sg-${g}`, g === 'wait' && 'sess-wait')} data-sid={p.session_id}>
      <button type="button" className="pk-b" onClick={() => onChoose(p.session_id)}>
        <Dot g={g} />
        <span className="pk-t">
          <span className="pk-n">
            <b>{peerName(p)}</b>
            {w ? <span className="chip-attn">{waitLabel(w.type)}</span> : null}
          </span>
          {w?.wait?.reason ? <span className="pk-q">{w.wait.reason}</span> : null}
          <span className="pk-m">{meta}</span>
        </span>
      </button>
    </li>
  )
}

/**
 * The dock's body with no session on the subject (D8): « Lancer une session sur ce sujet » at the head (one button,
 * `useLaunch`, « Lancement… » from `useLaunching`), then « Sessions en cours » of the subject's scope; a row chosen is
 * shown in the dock (`onChoose`). A launched session shows once /peers carries it for the subject (pickSession).
 */
export function NoSession({ subjectId, peers, now, onChoose }: { subjectId: string; peers: readonly Peer[]; now: number; onChoose: (id: string) => void }) {
  const get = useApi()
  // the backlog's own reads (same keys): a change of status or of the live sessions re-renders the dock
  const subjects = useQuery({ queryKey: ['subjects'], queryFn: () => get<Subject[]>(ROUTES.subjects) })
  const live = useQuery({ queryKey: ['peers'], queryFn: () => get<Peer[]>(ROUTES.peers) })
  const launch = useLaunch()
  const busy = useLaunching(subjectId)
  const s = subjects.data?.find((x) => x.id === subjectId)
  const launchable = !!s && canLaunch(s, displayStatus(s, liveSet(live.data ?? [])))
  const rows = scopeSessions(s?.scope, peers)
  return (
    <>
      {launchable || busy ? (
        <PendingButton className="sd-launch" icon={I_PLUS} pending={busy} label="Lancer une session sur ce sujet" busy="Lancement…" onClick={() => launch(subjectId)} />
      ) : null}
      <section aria-labelledby="sd-ls-h">
        <h3 className="lp-h3" id="sd-ls-h">
          Sessions en cours
        </h3>
        {rows.length ? (
          <ul className="srows pk-list" aria-labelledby="sd-ls-h">
            {rows.map((p) => (
              <PickRow key={p.session_id} p={p} now={now} onChoose={onChoose} />
            ))}
          </ul>
        ) : (
          <p className="sd-note">{launchable || busy ? 'Aucune session en cours. Lance-en une sur ce sujet avec le bouton ci-dessus.' : 'Aucune session en cours.'}</p>
        )}
      </section>
    </>
  )
}
