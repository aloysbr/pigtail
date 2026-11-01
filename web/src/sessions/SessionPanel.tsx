import type { MouseEvent } from 'react'
import type { Peer, Subject, Theme } from '@/api/types'
import { GROUP_LABEL, groupOf, lifeLabel, lifeOf, sinceOf, subjectOf, waitOf } from '@/domain/sessions'
import { Icon, Kbd } from '@/list/StatusIcon'
import { closeSessionPanel } from '@/state/ui'
import { WaitCard } from '@/ui/WaitCard'
import { STATE_TEXT, worktreeOf } from './SessionRow'

/**
 * The session panel (t3 sessionPanel, app.js:330): trail « Sessions › <groupe> », the subject's short title in serif,
 * « Session <cellule> · <projet> », the read-only wait card when it waits, then Cellule, Worktree and Statut, each only
 * when /peers carries it. No model, context, account, workflow step, PR, servers, last activity nor gesture: /peers has none.
 */
export function SessionPanel({
  p,
  subjects,
  themes,
  now,
  page = false,
  onStep,
  stepper = true,
  canPrev = true,
  canNext = true,
  onOpenSubject,
}: {
  p: Peer
  subjects: readonly Subject[]
  themes: readonly Theme[]
  now: number
  page?: boolean
  onStep: (by: 1 | -1) => void
  /** False when the session has no place in the shown order (a deep link to a filtered-out one): no previous / next. */
  stepper?: boolean
  canPrev?: boolean
  canNext?: boolean
  /** « Ouvrir le sujet »: the subject sheet in the backlog (the session panel closes). */
  onOpenSubject: (id: string) => void
}) {
  const g = groupOf(p)
  const w = g === 'wait' ? waitOf(p) : null
  const subject = subjectOf(p, subjects, themes)
  const wt = worktreeOf(p)
  const sub = [p.cell_id, subject.project].filter(Boolean).join(' · ')
  const open = (e: MouseEvent<HTMLAnchorElement>) => {
    if (!subject.id || e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return
    e.preventDefault()
    onOpenSubject(subject.id)
  }
  return (
    <section className="lpanel spanel" aria-labelledby="sp-title" data-sid={p.session_id}>
      <div className="lp-h">
        {page ? (
          <button type="button" className="ghost sm" aria-label="Retour aux sessions" onClick={closeSessionPanel}>
            <Icon name="back" />
            Sessions
            <Kbd k="esc" />
          </button>
        ) : (
          <button type="button" className="ib x-l" aria-label="Fermer le panneau" aria-keyshortcuts="Escape" onClick={closeSessionPanel}>
            <Icon name="x" />
          </button>
        )}
        {page ? null : (
          <span className="lp-trail">
            Sessions › <b>{GROUP_LABEL[g]}</b>
          </span>
        )}
        <span className="c-sp" />
        {stepper ? (
          <>
            <button type="button" className="ib" aria-label="Session précédente" aria-keyshortcuts="k" aria-disabled={!canPrev} onClick={() => canPrev && onStep(-1)}>
              <Icon name="up" />
            </button>
            <button type="button" className="ib" aria-label="Session suivante" aria-keyshortcuts="j" aria-disabled={!canNext} onClick={() => canNext && onStep(1)}>
              <Icon name="chev" />
            </button>
          </>
        ) : null}
      </div>
      <div className="lp-ttl">
        <span className={`sdot-w sg-${g}${g === 'wait' ? ' sess-wait' : ''}`} aria-hidden="true">
          <span className="dot" />
        </span>
        <h2 className="lp-t" id="sp-title">
          {subject.title}
        </h2>
      </div>
      {sub ? (
        <p className="lp-sub">
          Session {p.cell_id ? <b>{p.cell_id}</b> : null}
          {subject.project ? ` · ${subject.project}` : null}
        </p>
      ) : null}
      {w ? <WaitCard type={w.type} reason={w.wait?.reason} since={sinceOf(p)} now={now} /> : null}
      <dl className="lp-props">
        {p.cell_id ? (
          <>
            <dt>Cellule</dt>
            <dd>
              <span className="cell">{p.cell_id}</span>
            </dd>
          </>
        ) : null}
        {wt ? (
          <>
            <dt>Worktree</dt>
            <dd>
              <span className="path">{wt}</span>
            </dd>
          </>
        ) : null}
        <dt>Statut</dt>
        <dd>
          {STATE_TEXT[g]} · {lifeLabel(lifeOf(p))}
        </dd>
      </dl>
      {subject.id ? (
        <p className="lp-link">
          <a className="lnk" href={`#/s/${encodeURIComponent(subject.id)}`} onClick={open}>
            Ouvrir le sujet
          </a>
        </p>
      ) : null}
    </section>
  )
}
