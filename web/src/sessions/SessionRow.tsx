import type { Peer } from '@/api/types'
import { ageOf } from '@/domain/age'
import { groupOf, lifeLabel, lifeOf, sinceOf, waitLabel, waitOf, type SessionGroup } from '@/domain/sessions'
import { cn } from '@/lib/utils'

/** The state of a session written out (R9: the dot only repeats it). */
export const STATE_TEXT: Record<SessionGroup, string> = {
  wait: 'Attend ta réponse',
  work: 'Travaille',
  idle: 'Inactive',
}

/** The worktree a session works in: its pigtail worktree, else the last one its presence marker named. */
export function worktreeOf(p: Peer): string {
  if (p.workspace_name) return p.workspace_name
  const last = p.last_worktree?.replace(/\/+$/, '') ?? ''
  return last ? last.slice(last.lastIndexOf('/') + 1) : ''
}

/**
 * One session (t3 .srow): state dot, the wait's type chip and reason when it waits, the state and life written out, the cell
 * and « depuis ». Only what GET /peers carries: no model, context, workflow step nor action button. `compact` is the subject
 * sheet's « Sessions liées » row (T17): the same facts, the subject left out.
 */
export function SessionRow({
  p,
  now,
  sel = false,
  compact = false,
  onOpen,
}: {
  p: Peer
  now: number
  sel?: boolean
  compact?: boolean
  onOpen: (id: string) => void
}) {
  const g = groupOf(p)
  const w = g === 'wait' ? waitOf(p) : null
  const life = lifeOf(p)
  const since = sinceOf(p)
  const age = ageOf(since, now)
  const wt = worktreeOf(p)
  const main = w?.wait?.reason || wt || STATE_TEXT[g]
  return (
    <li
      className={cn('srow', `sg-${g}`, g === 'wait' && 'sess-wait', sel && 'sel', compact && 'compact')}
      data-sid={p.session_id}
      tabIndex={sel || compact ? 0 : -1}
      aria-current={sel ? 'true' : undefined}
      onClick={() => onOpen(p.session_id)}
      onKeyDown={(e) => {
        if (compact && (e.key === 'Enter' || e.key === ' ')) {
          e.preventDefault()
          e.stopPropagation()
          onOpen(p.session_id)
        }
      }}
    >
      <span className="dot" aria-hidden="true" />
      <span className="s-t">
        {w ? <span className="chip-attn">{waitLabel(w.type)}</span> : null}
        <b>{main}</b>
      </span>
      <span className="s-m">
        <span className="s-st">
          {STATE_TEXT[g]} · {lifeLabel(life)}
        </span>
        {p.cell_id ? (
          <span className="cell" title={`Cellule ${p.cell_id}`}>
            {p.cell_id}
          </span>
        ) : null}
      </span>
      <span className="s-age" title={since}>
        {age ? `depuis ${age}` : null}
      </span>
    </li>
  )
}
