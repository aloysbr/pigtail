import { useId, useLayoutEffect, useRef, useState } from 'react'
import type { SdkSession } from '@/api/types'
import { PendingButton } from '@/ui/PendingButton'
import { TextField } from '@/ui/TextField'
import { isTerminal, useSdkActions, useSdkEvents, useSdkSessions } from './api'
import { useGuarded } from './guarded'
import { SdkRequestCard } from './SdkRequestCard'
import { eventText, stateLabel } from './state'

/** « Lancer ici » (D17), beside « Lancer ». Absent when the option is off, when the launch is closed (`launch_enabled` false) or while a session of the subject is live. */
export function LaunchHereButton({ subjectId, title, scope }: { subjectId: string; title: string; scope?: string }) {
  const { off, sessions, launchEnabled } = useSdkSessions(subjectId)
  const { launch } = useSdkActions()
  const { busy, run } = useGuarded(title, scope)
  if (off || !launchEnabled || sessions.some((s) => s.live)) return null
  return <PendingButton className="sdk-launch" pending={busy} label="Lancer ici" busy="Lancement…" onClick={() => void run(() => launch(subjectId), 'launch')} />
}

/** What the block shows: every session still going, plus the newest one that is over (older ones are not listed). */
function shown(all: SdkSession[]): SdkSession[] {
  const byNewest = [...all].sort((a, b) => b.started_at.localeCompare(a.started_at))
  const going = byNewest.filter((s) => !isTerminal(s.state))
  const over = byNewest.find((s) => isTerminal(s.state))
  return over ? [...going, over] : going
}

/** The session block of the subject sheet (CA12): state, thread, request card, reply field, « Terminer la session », resume command. Nothing when the option is off or there is no session. */
export function SdkSessionBlock({ subjectId, title, scope, now }: { subjectId: string; title: string; scope?: string; now: number }) {
  const { off, error, sessions } = useSdkSessions(subjectId)
  const hid = useId()
  const list = off ? [] : shown(sessions)
  if (error) {
    return (
      <section className="lp-sec sdk-blk" aria-labelledby={hid}>
        <h3 className="lp-h3" id={hid}>
          Session pilotée
        </h3>
        <p className="sdk-why" role="alert">
          Impossible de lire les sessions pilotées pour l’instant. Réessaie dans un instant.
        </p>
      </section>
    )
  }
  if (list.length === 0) return null
  return (
    <section className="lp-sec sdk-blk" aria-labelledby={hid}>
      <h3 className="lp-h3" id={hid}>
        Session pilotée
      </h3>
      {list.map((s) => (
        <SessionItem key={s.id} session={s} title={title} scope={scope} now={now} />
      ))}
    </section>
  )
}

function SessionItem({ session: s, title, scope, now }: { session: SdkSession; title: string; scope?: string; now: number }) {
  const { events, ready } = useSdkEvents(s.id, s.state)
  const { say, end } = useSdkActions()
  const { busy, run } = useGuarded(title, scope)
  const [text, setText] = useState('')
  const log = useRef<HTMLOListElement>(null)
  // the most recent events sit at the bottom: the thread follows them as they arrive
  useLayoutEffect(() => {
    const el = log.current
    if (el) el.scrollTop = el.scrollHeight
  }, [events.length])
  const send = async (v: string) => {
    if (await run(() => say(s.id, v.trim()))) setText('')
  }
  return (
    <article className="sdk-s" data-session={s.id}>
      <div className="sdk-h">
        <span className="sdk-st" data-state={s.state}>
          {stateLabel(s.state)}
        </span>
      </div>
      {/* the reason of a failed or lost session, when the host gave one */}
      {(s.state === 'failed' || s.state === 'lost') && s.reason ? <p className="sdk-why">{s.reason}</p> : null}
      <ol className="sdk-log" role="log" aria-label="Fil de la session" aria-busy={!ready || undefined} tabIndex={0} ref={log}>
        {ready
          ? events.map((e) => (
              <li key={e.seq} data-seq={e.seq} data-kind={e.kind}>
                <time dateTime={e.at}>{new Date(e.at).toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</time>
                <span className="sdk-ev">{eventText(e)}</span>
              </li>
            ))
          : [62, 45, 71].map((w) => <li key={w} className="sdk-sk" aria-hidden="true" style={{ '--w': `${w}%` } as React.CSSProperties} />)}
      </ol>
      {s.pending.map((r) => (
        <SdkRequestCard key={r.rid} sessionId={s.id} request={r} now={now} title={title} scope={scope} />
      ))}
      {s.state === 'idle' ? (
        <div className="sdk-reply">
          <TextField label="Ta réponse" value={text} onChange={setText} onSubmit={(v) => void send(v)} disabled={busy} />
          <div className="sdk-acts">
            <button type="button" className="btnl" disabled={busy || text.trim() === ''} onClick={() => void send(text)}>
              Envoyer
            </button>
            <button type="button" className="btnl" disabled={busy} onClick={() => void run(() => end(s.id))}>
              Terminer la session
            </button>
          </div>
        </div>
      ) : null}
      {s.resume_command && !s.live ? (
        <div className="sdk-resume" role="group" aria-labelledby={`${s.id}-resume`}>
          <h4 className="sdk-h4" id={`${s.id}-resume`}>Reprendre dans un terminal</h4>
          <pre className="sdk-cmd">
            <code>{s.resume_command}</code>
          </pre>
        </div>
      ) : null}
    </article>
  )
}
