import { useMemo, type ReactNode } from 'react'
import { usePeersAll } from '@/api/sessions'
import type { Peer, Subject, Theme } from '@/api/types'
import { GROUP_LABEL, sessionMatches, sessionsModel, visible, type SessionsModel } from '@/domain/sessions'
import { Skeleton } from '@/list/EmptyStates'
import { Icon, Kbd, nf } from '@/list/StatusIcon'
import { cn } from '@/lib/utils'
import { setUi, toggleScope, toggleSide, useUi } from '@/state/ui'
import { SessionRow } from './SessionRow'

/** The Sessions filter field's id, focused by « / » in the view. */
export const SESS_SEARCH_ID = 'sessions-search'

/** The scopes the pills offer: the backlog's, plus any a session carries; the active ones are those not in the shared scopesOff. */
export function sessionScopes(base: readonly string[], peers: readonly Peer[]): string[] {
  const out = [...base]
  for (const p of peers) if (p.scope && !out.includes(p.scope)) out.push(p.scope)
  return out
}

/** The sessions the filters keep: the shared scope pills (a session with no scope always shows) and the text after « / ». */
export function filterSessions(peers: readonly Peer[], off: readonly string[], query: string, subjects: readonly Subject[], themes: readonly Theme[]): Peer[] {
  return peers.filter((p) => !(p.scope && off.includes(p.scope)) && sessionMatches(p, query, subjects, themes))
}

/** The model of the view as shown: every visible session, filtered, grouped. */
export function useSessionsModel(subjects: readonly Subject[], themes: readonly Theme[], enabled: boolean) {
  const q = usePeersAll({ enabled })
  const off = useUi((s) => s.scopesOff)
  const query = useUi((s) => s.sessQuery)
  const all = q.data
  const model = useMemo(
    () => (all ? sessionsModel(filterSessions(all, off, query, subjects, themes), subjects, themes) : null),
    [all, off, query, subjects, themes],
  )
  return { q, model, all }
}

function Head({ live, scopes, counts, docked, wide, banner }: { live: number | null; scopes: string[]; counts: Record<string, number>; docked: boolean; wide: boolean; banner: ReactNode }) {
  const off = useUi((s) => s.scopesOff)
  const query = useUi((s) => s.sessQuery)
  const active = scopes.filter((s) => !off.includes(s))
  return (
    <header className="d-head s-head">
      {docked ? null : (
        <button type="button" className="ghost sm" aria-label="Navigation" aria-keyshortcuts="Meta+B" title="Navigation (⌘B)" onClick={() => toggleSide(false, wide)}>
          <Icon name="menu" />
          <Kbd k="⌘B" />
        </button>
      )}
      <h1>Sessions</h1>
      {live !== null ? (
        <span className="fcount" data-testid="sess-live">
          {nf(live)} vivantes
        </span>
      ) : null}
      {scopes.length > 1 ? (
        <div className="pills" role="group" aria-label="Scopes affichés">
          {scopes.map((sc) => {
            const on = active.includes(sc)
            const locked = on && active.length <= 1
            return (
              <button
                key={sc}
                type="button"
                className="pill"
                aria-pressed={on}
                aria-disabled={locked ? true : undefined}
                title={locked ? 'Au moins un scope reste affiché' : undefined}
                onClick={() => toggleScope(sc, scopes)}
              >
                {on ? (
                  <span className="pill-c">
                    <Icon name="check" />
                  </span>
                ) : null}
                {sc}
                <span className="pill-n">{nf(counts[sc] ?? 0)}</span>
              </button>
            )
          })}
        </div>
      ) : null}
      <span className="d-tools">
        <label className="fsearch">
          <Icon name="search" />
          <input
            id={SESS_SEARCH_ID}
            type="search"
            placeholder="Filtrer"
            aria-label="Filtrer les sessions"
            value={query}
            onChange={(e) => setUi({ sessQuery: e.target.value })}
            onKeyDown={(e) => {
              // Esc clears the text; Esc on an empty field gives the focus back to the list (the view and the panel stay)
              if (e.key === 'Escape') {
                e.stopPropagation()
                if (query) setUi({ sessQuery: '' })
                else {
                  e.currentTarget.blur()
                  setUi((s) => ({ focusTick: s.focusTick + 1 }))
                }
              }
            }}
          />
          <Kbd k="/" />
        </label>
      </span>
      {banner}
    </header>
  )
}

function Groups({ model, now, sel, onOpen }: { model: SessionsModel; now: number; sel: string | null; onOpen: (id: string) => void }) {
  const off = useUi((s) => s.scopesOff)
  const query = useUi((s) => s.sessQuery)
  // a filter is on: the groups it leaves empty are hidden (all of them empty: the groups and their « Aucune session. » stay)
  const filtered = query.trim() !== '' || off.length > 0
  const some = model.groups.some((x) => x.count > 0)
  const shown = filtered && some ? model.groups.filter((x) => x.count > 0) : model.groups
  return (
    <div className="ll6 sll" role="region" aria-label="Sessions">
      {shown.map(({ g, count, blocks }) => (
        <section key={g} className={cn('sgrp', g === 'wait' && 'grp-attn')} aria-labelledby={`sg-${g}`}>
          <h2 className="gh g1 sgh" id={`sg-${g}`}>
            <span className="gname">{GROUP_LABEL[g]}</span>
            <span className={g === 'wait' ? 'cnt' : 'lg-n'} data-testid={`count-${g}`}>
              {nf(count)}
            </span>
          </h2>
          {blocks.length ? (
            blocks.map((b) => (
              <div key={b.key} className="sblk">
                <h3 className="gh g2 sgh">
                  {b.subject.project ? <span className="gsub">{b.subject.project} ›</span> : null}
                  <span className="gname">{b.subject.title}</span>
                  {b.peers.length > 1 ? <span className="lg-n">{nf(b.peers.length)} sessions</span> : null}
                </h3>
                <ul className="srows">
                  {b.peers.map((p) => (
                    <SessionRow key={p.session_id} p={p} now={now} sel={sel === p.session_id} onOpen={onOpen} />
                  ))}
                </ul>
              </div>
            ))
          ) : (
            <p className="empty-l">Aucune session.</p>
          )}
        </section>
      ))}
    </div>
  )
}

/** Clicking a row: it becomes the selection and its panel opens (T16). */
export function openSession(id: string) {
  setUi((s) => ({ sessionSel: id, sessionPanel: true, panel: false, focusTick: s.focusTick + 1 }))
}

/**
 * The Sessions view (spec § Vue Sessions, t3 « sessions »): « N vivantes » from GET /peers, the shared scope pills, a text
 * filter, three groups « Attend ta réponse » / « Travaille » / « Inactives », each by subject « projet › titre court ». Read
 * only: nothing on screen that /peers?all=1 does not carry.
 */
export function SessionsView({
  data,
  baseScopes,
  live,
  docked,
  wide,
  now,
  banner = null,
}: {
  /** useSessionsModel, called by the Shell (its order drives j/k). */
  data: ReturnType<typeof useSessionsModel>
  baseScopes: readonly string[]
  live: number | null
  docked: boolean
  wide: boolean
  now: number
  /** The daemon banner, at the bottom of the header like the backlog's. */
  banner?: ReactNode
}) {
  const { q, model, all } = data
  const sel = useUi((s) => s.sessionSel)
  const scopes = sessionScopes(baseScopes, all ?? [])
  // a pill counts the visible sessions of its scope, on or off (like the backlog's pills)
  const counts: Record<string, number> = {}
  for (const p of (all ?? []).filter(visible)) if (p.scope) counts[p.scope] = (counts[p.scope] ?? 0) + 1
  return (
    <>
      <Head live={live} scopes={scopes} counts={counts} docked={docked} wide={wide} banner={banner} />
      {model ? (
        <Groups model={model} now={now} sel={sel} onOpen={openSession} />
      ) : q.isError ? (
        <div className="empty6" role="alert">
          <p>Sessions indisponibles pour l&apos;instant.</p>
          <button type="button" className="btnl" onClick={() => void q.refetch()}>
            Réessayer
          </button>
        </div>
      ) : (
        <Skeleton label="Chargement des sessions" />
      )}
    </>
  )
}
